package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Feishu   FeishuConfig              `yaml:"feishu"`
	LLM      LLMConfig                 `yaml:"llm"`
	Clusters map[string]*ClusterConfig `yaml:"clusters"`
}

type FeishuConfig struct {
	AppID     string `yaml:"app_id"`
	AppSecret string `yaml:"app_secret"`
}

type LLMConfig struct {
	Provider string `yaml:"provider"`
	APIKey   string `yaml:"api_key"`
	Model    string `yaml:"model"`
	BaseURL  string `yaml:"base_url"`
}

type ClusterConfig struct {
	Kubeconfig string    `yaml:"kubeconfig"`
	Namespace  string    `yaml:"namespace"`
	ToolboxPod string    `yaml:"toolbox_pod"`
	SSHNodes   []SSHNode `yaml:"ssh_nodes"`
}

type SSHNode struct {
	Name    string `yaml:"name"`
	Host    string `yaml:"host"`
	User    string `yaml:"user"`
	KeyFile string `yaml:"key_file"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	if env := os.Getenv("LLM_API_KEY"); env != "" {
		cfg.LLM.APIKey = env
	}
	if env := os.Getenv("LLM_BASE_URL"); env != "" {
		cfg.LLM.BaseURL = env
	}
	if env := os.Getenv("ANTHROPIC_API_KEY"); env != "" && cfg.LLM.APIKey == "" {
		cfg.LLM.APIKey = env
	}
	if env := os.Getenv("FEISHU_APP_ID"); env != "" {
		cfg.Feishu.AppID = env
	}
	if env := os.Getenv("FEISHU_APP_SECRET"); env != "" {
		cfg.Feishu.AppSecret = env
	}

	if cfg.LLM.Provider == "" {
		cfg.LLM.Provider = "claude"
	}
	if cfg.LLM.Model == "" {
		cfg.LLM.Model = defaultModel(cfg.LLM.Provider)
	}

	for name, cluster := range cfg.Clusters {
		if cluster.Namespace == "" {
			cluster.Namespace = "rook-ceph"
		}
		if cluster.ToolboxPod == "" {
			cluster.ToolboxPod = "rook-ceph-tools"
		}
		if cluster.Kubeconfig == "" {
			return nil, fmt.Errorf("cluster %q: kubeconfig is required", name)
		}
	}

	if cfg.Feishu.AppID == "" || cfg.Feishu.AppSecret == "" {
		return nil, fmt.Errorf("feishu app_id and app_secret are required")
	}
	if cfg.LLM.APIKey == "" && cfg.LLM.Provider != "local" && cfg.LLM.Provider != "ollama" {
		return nil, fmt.Errorf("llm api_key is required (config or LLM_API_KEY env)")
	}

	return &cfg, nil
}

func defaultModel(provider string) string {
	switch provider {
	case "claude", "anthropic":
		return "claude-sonnet-4-6-20250514"
	case "openai":
		return "gpt-4o"
	case "deepseek":
		return "deepseek-chat"
	case "local", "ollama":
		return "qwen2.5:14b"
	default:
		return ""
	}
}
