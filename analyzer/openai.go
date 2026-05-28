package analyzer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type OpenAIProvider struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

type OpenAIOption func(*OpenAIProvider)

func WithOpenAIAPIKey(apiKey string) OpenAIOption {
	return func(o *OpenAIProvider) { o.apiKey = apiKey }
}

func WithOpenAIBaseURL(baseURL string) OpenAIOption {
	return func(o *OpenAIProvider) { o.baseURL = baseURL }
}

func WithOpenAIModel(model string) OpenAIOption {
	return func(o *OpenAIProvider) { o.model = model }
}

func WithOpenAITimeout(d time.Duration) OpenAIOption {
	return func(o *OpenAIProvider) { o.client.Timeout = d }
}

func NewOpenAIProvider(opts ...OpenAIOption) *OpenAIProvider {
	o := &OpenAIProvider{
		client: &http.Client{Timeout: 120 * time.Second},
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (o *OpenAIProvider) Chat(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	reqBody := chatRequest{
		Model: o.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := o.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request to %s failed: %w", o.baseURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if chatResp.Error != nil {
		return "", fmt.Errorf("API error: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("empty response from %s", o.baseURL)
	}

	return chatResp.Choices[0].Message.Content, nil
}
