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
		return NewClaudeProvider(cfg.APIKey, cfg.BaseURL, cfg.Model), nil
	case "openai":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		return NewOpenAIProvider(cfg.APIKey, baseURL, cfg.Model), nil
	case "deepseek":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "https://api.deepseek.com/v1"
		}
		return NewOpenAIProvider(cfg.APIKey, baseURL, cfg.Model), nil
	case "local", "ollama":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434/v1"
		}
		return NewOpenAIProvider(cfg.APIKey, baseURL, cfg.Model), nil
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %q (supported: claude, openai, deepseek, glm, local)", cfg.Provider)
	}
}
