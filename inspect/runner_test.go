package inspect

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/microyahoo/storage-bot/config"
	"github.com/microyahoo/storage-bot/executor"
)

type fakeInspector struct {
	name  string
	scope Scope
	out   []Finding
}

func (f fakeInspector) Name() string                               { return f.name }
func (f fakeInspector) Description() string                        { return f.name }
func (f fakeInspector) Scope() Scope                               { return f.scope }
func (f fakeInspector) Inspect(*InspectContext) ([]Finding, error) { return f.out, nil }

func TestRunnerAggregates(t *testing.T) {
	reg := &Registry{}
	reg.add(
		fakeInspector{"ceph_x", ClusterScope, []Finding{{Item: "ceph_x", Level: LevelCritical, Summary: "bad"}}},
		fakeInspector{"hw_y", NodeScope, []Finding{{Item: "hw_y", Level: LevelOK, Summary: "fine"}}},
	)
	r := &Runner{registry: reg}
	now := time.Date(2026, 6, 17, 3, 0, 0, 0, time.UTC)
	rep := r.runWith(context.Background(), "c1", nil, nil,
		[]config.SSHNode{{Name: "node-1"}, {Name: "node-2"}}, config.Thresholds{}, now)

	if len(rep.Findings) != 3 {
		t.Fatalf("findings = %d, want 3 (1 cluster + 2 nodes)", len(rep.Findings))
	}
	if rep.Overall != LevelCritical {
		t.Errorf("overall = %v, want Critical", rep.Overall)
	}
	if rep.StartedAt != now {
		t.Errorf("StartedAt not propagated")
	}
	var nodeNames []string
	for _, f := range rep.Findings {
		if f.Item == "hw_y" {
			nodeNames = append(nodeNames, f.Node)
		}
	}
	if len(nodeNames) != 2 {
		t.Errorf("hw_y should run per node, got nodes %v", nodeNames)
	}
}

func TestRunLoadsPrevForBondDelta(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 30)

	// 上一次报告：n1 累计 10 次。
	prev := &Report{
		Cluster:   "c1",
		StartedAt: time.Date(2026, 7, 6, 3, 0, 0, 0, time.UTC),
		Findings: []Finding{{
			Item: "hw_bond", Node: "n1", Level: LevelWarn,
			Summary: "bond 累计 Link Failure 10 次",
			Metrics: map[string]string{"link_failure_total": "10"},
		}},
	}
	if err := store.Save(prev); err != nil {
		t.Fatalf("save prev: %v", err)
	}

	// 本次：n1 累计 10 次（无新增）→ 期望被降级为 OK。
	reg := &Registry{}
	reg.add(fakeInspector{"hw_bond", NodeScope, []Finding{{
		Item: "hw_bond", Level: LevelWarn,
		Summary: "bond 累计 Link Failure 10 次",
		Metrics: map[string]string{"link_failure_total": "10"},
	}}})
	r := &Runner{registry: reg, store: store}

	prevRep := r.latestReport("c1")
	if prevRep == nil {
		t.Fatal("latestReport returned nil, want the saved prev report")
	}

	now := time.Date(2026, 7, 7, 3, 0, 0, 0, time.UTC)
	rep := r.runWith(context.Background(), "c1", nil, nil,
		[]config.SSHNode{{Name: "n1"}}, config.Thresholds{}, now)
	applyBondDelta(rep, prevRep)
	rep.Finalize()

	var got Finding
	for _, f := range rep.Findings {
		if f.Item == "hw_bond" {
			got = f
		}
	}
	if got.Level != LevelOK {
		t.Errorf("no-increase bond → %v, want OK", got.Level)
	}
}

type fakeClusterProvider struct {
	name  string
	nodes []config.SSHNode
}

func (f fakeClusterProvider) FindByPrefix(string) (string, *config.ClusterConfig, error) {
	return f.name, &config.ClusterConfig{}, nil
}
func (f fakeClusterProvider) KubeExecutor(string, *config.ClusterConfig) (*executor.KubeExecutor, error) {
	return nil, nil
}
func (f fakeClusterProvider) ResolveSSHNodes(context.Context, string, *config.ClusterConfig) ([]config.SSHNode, error) {
	return f.nodes, nil
}

func TestRunAppliesBondDeltaEndToEnd(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 30)

	// 上一次报告：n1 累计 10 次。
	prev := &Report{
		Cluster:   "c1",
		StartedAt: time.Date(2026, 7, 6, 3, 0, 0, 0, time.UTC),
		Findings: []Finding{{
			Item: "hw_bond", Node: "n1", Level: LevelWarn,
			Summary: "bond 累计 Link Failure 10 次",
			Metrics: map[string]string{"link_failure_total": "10"},
		}},
	}
	if err := store.Save(prev); err != nil {
		t.Fatalf("save prev: %v", err)
	}

	// 本次巡检 n1 仍是 10 次（无新增）→ Run 应经 applyBondDelta 把它降级为 OK。
	reg := &Registry{}
	reg.add(fakeInspector{"hw_bond", NodeScope, []Finding{{
		Item: "hw_bond", Level: LevelWarn,
		Summary: "bond 累计 Link Failure 10 次",
		Metrics: map[string]string{"link_failure_total": "10"},
	}}})
	r := &Runner{
		registry: reg,
		store:    store,
		clusters: fakeClusterProvider{name: "c1", nodes: []config.SSHNode{{Name: "n1"}}},
	}

	rep, err := r.Run(context.Background(), "c1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got Finding
	for _, f := range rep.Findings {
		if f.Item == "hw_bond" {
			got = f
		}
	}
	if got.Level != LevelOK {
		t.Errorf("Run should downgrade no-increase bond → %v, want OK", got.Level)
	}
	if rep.Overall != LevelOK {
		t.Errorf("Overall = %v, want OK after downgrade", rep.Overall)
	}
}

func TestLatestReportCorruptFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 30)

	// 写一个损坏的报告文件（非法 JSON）到集群历史目录。
	cd := filepath.Join(dir, "c1")
	if err := os.MkdirAll(cd, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cd, "20260706-030000.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	r := &Runner{store: store}
	if rep := r.latestReport("c1"); rep != nil {
		t.Errorf("corrupt latest report → %v, want nil (no baseline)", rep)
	}
}

func TestLatestReportNilStoreAndEmpty(t *testing.T) {
	// 无 store → nil。
	if rep := (&Runner{}).latestReport("c1"); rep != nil {
		t.Errorf("nil store → %v, want nil", rep)
	}
	// 有 store 但无历史 → nil（真正的首次巡检）。
	r := &Runner{store: NewStore(t.TempDir(), 30)}
	if rep := r.latestReport("c1"); rep != nil {
		t.Errorf("empty history → %v, want nil", rep)
	}
}
