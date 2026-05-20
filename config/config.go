package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Feishu       FeishuConfig                  `yaml:"feishu"`
	LLM          LLMConfig                     `yaml:"llm"`
	Dev          DevConfig                     `yaml:"dev"`
	Clusters     map[string]*ClusterConfig     `yaml:"clusters"`
	RESTStorages map[string]*RESTStorageConfig `yaml:"rest_storages"`
}

type DevConfig struct {
	// DisableLLM turns off LLM intent fallback and AI analysis.
	// Replies show raw diagnostics + which action would have been taken.
	DisableLLM bool `yaml:"disable_llm"`
	// DryRun stops the bot from running any ceph/SSH command.
	// Replies show the command that would have been executed.
	DryRun bool `yaml:"dry_run"`
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
	Kubeconfig string `yaml:"kubeconfig"`
	Namespace  string `yaml:"namespace"`
	ToolboxPod string `yaml:"toolbox_pod"`
	// ServerOverride replaces the apiserver URL embedded in the kubeconfig.
	ServerOverride        string `yaml:"server_override"`
	InsecureSkipTLSVerify bool   `yaml:"insecure_skip_tls_verify"`
	// GatewayNode is the one node that is configured in yaml.
	// All other nodes are auto-discovered via kubectl get nodes and accessed
	// over SSH using this node's credentials (passwordless root SSH assumed).
	// If ssh_nodes is also set, those override the auto-discovered list.
	GatewayNode *SSHNode  `yaml:"gateway_node"`
	SSHNodes    []SSHNode `yaml:"ssh_nodes"`
}

type SSHNode struct {
	Name    string `yaml:"name"`
	Host    string `yaml:"host"`
	User    string `yaml:"user"`
	KeyFile string `yaml:"key_file"`
	// GatewayKeyFile is the path of the private key ON the gateway itself,
	// used for the second hop (gateway → target).
	// Leave empty to auto-detect (~/.ssh/id_rsa, id_ed25519, id_ecdsa).
	GatewayKeyFile string `yaml:"gateway_key_file"`
}

// RESTStorageConfig represents a non-Ceph storage system accessible via REST API.
type RESTStorageConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	// Endpoints configures which API paths to call.
	Endpoints RESTEndpointsConfig `yaml:"endpoints"`
}

type RESTEndpointsConfig struct {
	ClusterInfo string `yaml:"cluster_info"`
	// DirUsage may contain %s for the path argument, e.g. "/api/usage?path=%s"
	DirUsage    string `yaml:"dir_usage"`
	HealthCheck string `yaml:"health_check"`
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
	if cfg.LLM.APIKey == "" && cfg.LLM.Provider != "ollama" && !cfg.Dev.DisableLLM {
		return nil, fmt.Errorf("llm api_key is required (config or LLM_API_KEY env), or set dev.disable_llm: true")
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
	case "qwen", "dashscope", "aliyun":
		return "qwen-plus"
	default:
		return ""
	}
}
