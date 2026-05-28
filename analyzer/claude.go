package analyzer

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type ClaudeProvider struct {
	apiKey  string
	baseURL string
	model   string
	client  anthropic.Client
}

type ClaudeOption func(*ClaudeProvider)

func WithClaudeBaseURL(baseURL string) ClaudeOption {
	return func(c *ClaudeProvider) { c.baseURL = baseURL }
}

func WithClaudeModel(model string) ClaudeOption {
	return func(c *ClaudeProvider) { c.model = model }
}

func NewClaudeProvider(apiKey string, opts ...ClaudeOption) *ClaudeProvider {
	c := &ClaudeProvider{apiKey: apiKey}
	for _, opt := range opts {
		opt(c)
	}
	reqOpts := []option.RequestOption{option.WithAPIKey(c.apiKey)}
	if c.baseURL != "" {
		reqOpts = append(reqOpts, option.WithBaseURL(c.baseURL))
	}
	c.client = anthropic.NewClient(reqOpts...)
	return c
}

func (c *ClaudeProvider) Chat(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	msg, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userMessage)),
		},
	})
	if err != nil {
		return "", fmt.Errorf("claude API call failed: %w", err)
	}

	if len(msg.Content) == 0 {
		return "", fmt.Errorf("empty response from Claude")
	}

	var result string
	for _, block := range msg.Content {
		if block.Type == "text" {
			result += block.Text
		}
	}
	return result, nil
}
