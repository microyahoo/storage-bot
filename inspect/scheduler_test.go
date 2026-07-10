package inspect

import (
	"context"
	"fmt"
	"testing"

	"github.com/microyahoo/storage-bot/config"
	"github.com/microyahoo/storage-bot/executor"
)

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

// --- test helpers for tick tests ---

type mockNotifier struct {
	reports   []*Report
	summaries []*Summary
}

func (m *mockNotifier) NotifyReport(_ context.Context, _ string, rep *Report) error {
	m.reports = append(m.reports, rep)
	return nil
}

func (m *mockNotifier) NotifySummary(_ context.Context, _ string, s *Summary) error {
	m.summaries = append(m.summaries, s)
	return nil
}

type mockLister struct{ names []string }

func (m *mockLister) List() []string { return m.names }

type multiClusterProvider struct {
	configs map[string]*config.ClusterConfig
}

func (p *multiClusterProvider) FindByPrefix(input string) (string, *config.ClusterConfig, error) {
	if cfg, ok := p.configs[input]; ok {
		return input, cfg, nil
	}
	return "", nil, fmt.Errorf("cluster %q not found", input)
}

func (p *multiClusterProvider) KubeExecutor(string, *config.ClusterConfig) (*executor.KubeExecutor, error) {
	return nil, nil
}

func (p *multiClusterProvider) ResolveSSHNodes(context.Context, string, *config.ClusterConfig) ([]config.SSHNode, error) {
	return nil, nil
}

type clusterAwareInspector struct {
	levels map[string]Level
}

func (c *clusterAwareInspector) Name() string       { return "cluster-aware" }
func (c *clusterAwareInspector) Description() string { return "test" }
func (c *clusterAwareInspector) Scope() Scope        { return ClusterScope }
func (c *clusterAwareInspector) Inspect(ic *InspectContext) ([]Finding, error) {
	lvl := c.levels[ic.ClusterName]
	return []Finding{{Item: "check", Level: lvl, Summary: "test"}}, nil
}

func TestTickAllOK(t *testing.T) {
	reg := &Registry{}
	reg.add(&clusterAwareInspector{levels: map[string]Level{
		"a": LevelOK, "b": LevelOK, "c": LevelOK,
	}})

	provider := &multiClusterProvider{configs: map[string]*config.ClusterConfig{
		"a": {}, "b": {}, "c": {},
	}}
	runner := NewRunner(reg, provider, nil, nil, config.Thresholds{}, false, nil)
	notif := &mockNotifier{}
	lister := &mockLister{names: []string{"a", "b", "c"}}

	s := NewScheduler(runner, config.InspectConfig{
		Enabled:    true,
		Schedule:   "0 3 * * *",
		NotifyChat: "chat123",
	}, lister, notif, "")

	s.tick(context.Background())

	if len(notif.reports) != 0 {
		t.Errorf("expected 0 individual reports for all-OK clusters, got %d", len(notif.reports))
	}
	if len(notif.summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(notif.summaries))
	}
	sum := notif.summaries[0]
	if sum.Total != 3 || sum.OK != 3 {
		t.Errorf("expected Total=3 OK=3, got Total=%d OK=%d", sum.Total, sum.OK)
	}
}

func TestTickMixed(t *testing.T) {
	reg := &Registry{}
	reg.add(&clusterAwareInspector{levels: map[string]Level{
		"ok1": LevelOK, "warn1": LevelWarn, "crit1": LevelCritical,
	}})

	provider := &multiClusterProvider{configs: map[string]*config.ClusterConfig{
		"ok1": {}, "warn1": {}, "crit1": {},
	}}
	runner := NewRunner(reg, provider, nil, nil, config.Thresholds{}, false, nil)
	notif := &mockNotifier{}
	lister := &mockLister{names: []string{"ok1", "warn1", "crit1"}}

	s := NewScheduler(runner, config.InspectConfig{
		Enabled:    true,
		Schedule:   "0 3 * * *",
		NotifyChat: "chat123",
	}, lister, notif, "")

	s.tick(context.Background())

	if len(notif.reports) != 2 {
		t.Errorf("expected 2 individual reports (warn+crit), got %d", len(notif.reports))
	}
	if len(notif.summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(notif.summaries))
	}
	sum := notif.summaries[0]
	if sum.Total != 3 || sum.OK != 1 || sum.Warn != 1 || sum.Critical != 1 {
		t.Errorf("summary counts: total=%d ok=%d warn=%d crit=%d", sum.Total, sum.OK, sum.Warn, sum.Critical)
	}
}
