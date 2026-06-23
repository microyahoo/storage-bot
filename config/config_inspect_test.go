package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTmpConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const baseCfg = `
feishu:
  app_id: a
  app_secret: b
llm:
  provider: claude
  api_key: k
clusters:
  c1:
    kubeconfig: /tmp/kc
`

func TestInspectDefaults(t *testing.T) {
	cfg, err := Load(writeTmpConfig(t, baseCfg+`
inspect:
  enabled: true
  schedule: "0 3 * * *"
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Inspect.Thresholds.CapacityWarnPct != 80 {
		t.Errorf("CapacityWarnPct default = %d, want 80", cfg.Inspect.Thresholds.CapacityWarnPct)
	}
	if cfg.Inspect.Thresholds.LoadWarnRatio != 2.0 {
		t.Errorf("LoadWarnRatio default = %v, want 2.0", cfg.Inspect.Thresholds.LoadWarnRatio)
	}
	if cfg.Inspect.NotifyMinLevel != "warn" {
		t.Errorf("NotifyMinLevel default = %q, want warn", cfg.Inspect.NotifyMinLevel)
	}
	if cfg.Inspect.HistoryKeep != 30 {
		t.Errorf("HistoryKeep default = %d, want 30", cfg.Inspect.HistoryKeep)
	}
}

func TestInspectBadSchedule(t *testing.T) {
	_, err := Load(writeTmpConfig(t, baseCfg+`
inspect:
  enabled: true
  schedule: "not a cron"
`))
	if err == nil {
		t.Fatal("expected error for invalid cron schedule")
	}
}

func TestInspectBadNotifyLevel(t *testing.T) {
	_, err := Load(writeTmpConfig(t, baseCfg+`
inspect:
  enabled: true
  schedule: "0 3 * * *"
  notify_min_level: panic
`))
	if err == nil {
		t.Fatal("expected error for invalid notify_min_level")
	}
}
