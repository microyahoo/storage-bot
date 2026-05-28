package analyzer

import (
	"context"
	"fmt"
	"strings"

	"github.com/microyahoo/storage-bot/config"
)

type LLMProvider interface {
	Chat(ctx context.Context, systemPrompt, userMessage string) (string, error)
}

func NewProvider(cfg config.LLMConfig) (LLMProvider, error) {
	switch strings.ToLower(cfg.Provider) {
	case "claude", "anthropic":
		return NewClaudeProvider(cfg.APIKey,
			WithClaudeBaseURL(cfg.BaseURL),
			WithClaudeModel(cfg.Model),
		), nil
	case "openai":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		return newOpenAIWith(cfg.APIKey, baseURL, cfg.Model), nil
	case "deepseek":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "https://api.deepseek.com/v1"
		}
		return newOpenAIWith(cfg.APIKey, baseURL, cfg.Model), nil
	case "qwen", "dashscope", "aliyun":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
		}
		return newOpenAIWith(cfg.APIKey, baseURL, cfg.Model), nil
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %q (supported: claude, openai, deepseek, qwen)", cfg.Provider)
	}
}

func newOpenAIWith(apiKey, baseURL, model string) *OpenAIProvider {
	return NewOpenAIProvider(
		WithOpenAIAPIKey(apiKey),
		WithOpenAIBaseURL(baseURL),
		WithOpenAIModel(model),
	)
}
