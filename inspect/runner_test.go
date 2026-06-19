package inspect

import (
	"context"
	"testing"
	"time"

	"github.com/microyahoo/storage-bot/config"
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
