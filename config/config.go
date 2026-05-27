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
	Web          WebConfig                     `yaml:"web"`
	Clusters     map[string]*ClusterConfig     `yaml:"clusters"`
	RESTStorages map[string]*RESTStorageConfig `yaml:"rest_storages"`
}

// WebConfig configures the admin web UI. If Listen is empty, the web server is disabled.
type WebConfig struct {
	Listen   string `yaml:"listen"`   // e.g. ":8080" or "127.0.0.1:8080"
	Username string `yaml:"username"` // Basic Auth username; empty disables auth (not recommended)
	Password string `yaml:"password"` // Basic Auth password
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

// RESTStorageConfig represents a Yanrong cloud filesystem accessible via its
// REST API. Auth is via username/password (twice-MD5 hashed by the backend).
type RESTStorageConfig struct {
	BaseURL  string `yaml:"base_url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
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

	// Reject overlap between clusters and rest_storages: the intent parser routes
	// by name, and a duplicate would silently shadow one or the other.
	for name, rs := range cfg.RESTStorages {
		if _, dup := cfg.Clusters[name]; dup {
			return nil, fmt.Errorf("name %q is used by both clusters and rest_storages; rename one", name)
		}
		if rs.BaseURL == "" {
			return nil, fmt.Errorf("rest_storages %q: base_url is required", name)
		}
		if rs.Username == "" || rs.Password == "" {
			return nil, fmt.Errorf("rest_storages %q: username and password are required", name)
		}
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
