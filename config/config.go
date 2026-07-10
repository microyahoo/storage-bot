package config

import (
	"fmt"
	"os"

	cron "github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Feishu       FeishuConfig                  `yaml:"feishu"`
	LLM          LLMConfig                     `yaml:"llm"`
	Dev          DevConfig                     `yaml:"dev"`
	Web          WebConfig                     `yaml:"web"`
	Clusters     map[string]*ClusterConfig     `yaml:"clusters"`
	RESTStorages map[string]*RESTStorageConfig `yaml:"rest_storages"`
	Inspect      InspectConfig                 `yaml:"inspect"`
}

type InspectConfig struct {
	Enabled        bool       `yaml:"enabled"`
	Schedule       string     `yaml:"schedule"`
	Clusters       []string   `yaml:"clusters"`
	NotifyChat     string     `yaml:"notify_chat"`
	LLMSummary     bool       `yaml:"llm_summary"`
	HistoryDir     string     `yaml:"history_dir"`
	HistoryKeep    int        `yaml:"history_keep"`
	Thresholds     Thresholds `yaml:"thresholds"`
}

type Thresholds struct {
	CapacityWarnPct int     `yaml:"capacity_warn_pct"`
	CapacityCritPct int     `yaml:"capacity_crit_pct"`
	MemWarnPct      int     `yaml:"mem_warn_pct"`
	MemCritPct      int     `yaml:"mem_crit_pct"`
	FsWarnPct       int     `yaml:"fs_warn_pct"`
	FsCritPct       int     `yaml:"fs_crit_pct"`
	DiskLifeWarnPct int     `yaml:"disk_life_warn_pct"`
	DiskLifeCritPct int     `yaml:"disk_life_crit_pct"`
	LoadWarnRatio   float64 `yaml:"load_warn_ratio"`
	// PcieMinSpeedGTS: 仅速率降级(宽度未降)时，若协商速率 ≥ 此值(GT/s)则视为
	// 故意降级(如 PCIe 5.0→4.0)而静默，不告警。0=禁用，全部速率降级都告警。
	// 宽度降级(掉 lane)永远告警，不受此项影响。
	PcieMinSpeedGTS float64 `yaml:"pcie_min_speed_gts"`
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
//
// PublicUserPrefix / PrivateUserPrefix let skills resolve a short user name
// (e.g. "liangzheng") into a full quota path (e.g. "/drtraining/user/liangzheng") without
// the operator having to type the prefix every time. Leave empty to disable.
type RESTStorageConfig struct {
	BaseURL  string `yaml:"base_url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`

	PublicUserPrefix  string `yaml:"public_user_prefix"`
	PrivateUserPrefix string `yaml:"private_user_prefix"`
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

	if cfg.Inspect.Enabled {
		applyInspectDefaults(&cfg.Inspect)
		if err := validateInspect(&cfg.Inspect); err != nil {
			return nil, err
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

func applyInspectDefaults(c *InspectConfig) {
	t := &c.Thresholds
	if t.CapacityWarnPct == 0 {
		t.CapacityWarnPct = 80
	}
	if t.CapacityCritPct == 0 {
		t.CapacityCritPct = 90
	}
	if t.MemWarnPct == 0 {
		t.MemWarnPct = 90
	}
	if t.MemCritPct == 0 {
		t.MemCritPct = 95
	}
	if t.FsWarnPct == 0 {
		t.FsWarnPct = 85
	}
	if t.FsCritPct == 0 {
		t.FsCritPct = 90
	}
	if t.DiskLifeWarnPct == 0 {
		t.DiskLifeWarnPct = 80
	}
	if t.DiskLifeCritPct == 0 {
		t.DiskLifeCritPct = 90
	}
	if t.LoadWarnRatio == 0 {
		t.LoadWarnRatio = 2.0
	}
	if c.HistoryDir == "" {
		c.HistoryDir = "./inspect-reports"
	}
	if c.HistoryKeep == 0 {
		c.HistoryKeep = 30
	}
}

func validateInspect(c *InspectConfig) error {
	if c.Schedule == "" {
		return fmt.Errorf("inspect.schedule is required when inspect.enabled")
	}
	if _, err := cron.ParseStandard(c.Schedule); err != nil {
		return fmt.Errorf("inspect.schedule invalid cron %q: %w", c.Schedule, err)
	}
	return nil
}
