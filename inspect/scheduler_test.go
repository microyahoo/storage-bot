package inspect

import (
	"context"
	"testing"

	"github.com/microyahoo/storage-bot/config"
)

func TestShouldNotify(t *testing.T) {
	if !shouldNotify(LevelWarn, "warn") || !shouldNotify(LevelCritical, "warn") {
		t.Error("warn level should notify on warn/critical")
	}
	if shouldNotify(LevelOK, "warn") {
		t.Error("OK should not notify")
	}
	if shouldNotify(LevelWarn, "critical") {
		t.Error("warn should not notify when min=critical")
	}
	if !shouldNotify(LevelCritical, "critical") {
		t.Error("critical should notify when min=critical")
	}
}

func TestSchedulerTargetClusters(t *testing.T) {
	got := targetClusters(nil, []string{"a", "b"})
	if len(got) != 2 {
		t.Errorf("empty config should fall back to all, got %v", got)
	}
	got = targetClusters([]string{"a"}, []string{"a", "b"})
	if len(got) != 1 || got[0] != "a" {
		t.Errorf("explicit list should win, got %v", got)
	}
}

func TestSchedulerConstructs(t *testing.T) {
	s := NewScheduler(nil, config.InspectConfig{Schedule: "0 3 * * *"}, nil, nil, "")
	if s == nil {
		t.Fatal("nil scheduler")
	}
	_ = context.Background()
}
