package inspect

import (
	"strings"
	"testing"
	"time"
)

func sampleReport() *Report {
	return &Report{
		Cluster:   "prod-ceph-01",
		StartedAt: time.Date(2026, 6, 17, 3, 0, 0, 0, time.UTC),
		Duration:  28 * time.Second,
		Findings: []Finding{
			{Item: "ceph_osd", Level: LevelCritical, Summary: "2 个 OSD down"},
			{Item: "hw_disk_smart", Node: "node-3", Level: LevelWarn, Summary: "寿命 85%"},
			{Item: "ceph_health", Level: LevelOK, Summary: "HEALTH_OK"},
			{Item: "ceph_mon", Level: LevelOK, Summary: "quorum 正常"},
			{Item: "hw_x", Level: LevelUnknown, Summary: "采集失败"},
		},
	}
}

func TestReportCountsAndOverall(t *testing.T) {
	r := sampleReport()
	r.Finalize()
	if r.Overall != LevelCritical {
		t.Errorf("Overall = %v, want Critical", r.Overall)
	}
	ok, warn, crit, unknown := r.Counts()
	if ok != 2 || warn != 1 || crit != 1 || unknown != 1 {
		t.Errorf("counts = %d/%d/%d/%d, want 2/1/1/1", ok, warn, crit, unknown)
	}
	ab := r.Abnormal()
	if len(ab) != 3 {
		t.Fatalf("abnormal = %d, want 3", len(ab))
	}
	if ab[0].Level != LevelCritical || ab[1].Level != LevelWarn || ab[2].Level != LevelUnknown {
		t.Errorf("abnormal order wrong: %v %v %v", ab[0].Level, ab[1].Level, ab[2].Level)
	}
}

func TestRenderText(t *testing.T) {
	r := sampleReport()
	r.Finalize()
	txt := r.RenderText()
	if !strings.Contains(txt, "ceph_osd") || !strings.Contains(txt, "2 个 OSD down") {
		t.Errorf("text missing abnormal item:\n%s", txt)
	}
	if !strings.Contains(txt, "其余") {
		t.Errorf("text missing collapsed-normal line:\n%s", txt)
	}
}

// Abnormal findings must group by node, cluster-scope first, with a divider
// between groups so dense multi-node output is separable.
func TestRenderTextGroupsByNode(t *testing.T) {
	r := &Report{
		Cluster:   "c1",
		StartedAt: time.Date(2026, 6, 30, 3, 0, 0, 0, time.UTC),
		Findings: []Finding{
			{Item: "ceph_osd", Level: LevelCritical, Summary: "OSD down"},
			{Item: "hw_pcie_link", Node: "node-b", NodeIP: "10.0.0.2", Level: LevelWarn, Summary: "PCIe 降速"},
			{Item: "hw_disk_smart", Node: "node-a", NodeIP: "10.0.0.1", Level: LevelWarn, Summary: "寿命 85%"},
			{Item: "hw_bond", Node: "node-a", NodeIP: "10.0.0.1", Level: LevelCritical, Summary: "MII down"},
		},
	}
	r.Finalize()
	txt := r.RenderText()

	// Divider present (multiple groups).
	if !strings.Contains(txt, "---") {
		t.Errorf("expected divider between node groups:\n%s", txt)
	}
	// Node header with IP.
	if !strings.Contains(txt, "node-a") || !strings.Contains(txt, "10.0.0.1") {
		t.Errorf("expected node-a header with IP:\n%s", txt)
	}
	// Cluster-scope group label appears and precedes node groups.
	ci := strings.Index(txt, "集群级")
	ai := strings.Index(txt, "node-a")
	if ci < 0 || ai < 0 || ci > ai {
		t.Errorf("cluster-scope group should come before node groups:\n%s", txt)
	}
	// node-a's two findings sit under one header (only one "node-a" occurrence).
	if n := strings.Count(txt, "node-a"); n != 1 {
		t.Errorf("node-a should head one group, appeared %d times:\n%s", n, txt)
	}
}

func TestAbnormalByNode(t *testing.T) {
	r := &Report{Findings: []Finding{
		{Item: "ceph_osd", Level: LevelCritical, Summary: "x"},
		{Item: "hw_a", Node: "n2", Level: LevelWarn, Summary: "x"},
		{Item: "hw_b", Node: "n1", Level: LevelWarn, Summary: "x"},
		{Item: "hw_c", Node: "n1", Level: LevelCritical, Summary: "x"},
	}}
	groups := r.AbnormalByNode()
	if len(groups) != 3 {
		t.Fatalf("want 3 groups (cluster, n1, n2), got %d", len(groups))
	}
	if groups[0].node != "" {
		t.Errorf("group[0] should be cluster-scope, got %q", groups[0].node)
	}
	if groups[1].node != "n1" || groups[2].node != "n2" {
		t.Errorf("node groups should sort by name: %q, %q", groups[1].node, groups[2].node)
	}
	// Within n1, Critical sorts before Warn (Abnormal order preserved).
	if groups[1].findings[0].Level != LevelCritical {
		t.Errorf("n1 first finding should be Critical, got %v", groups[1].findings[0].Level)
	}
}

func TestRenderCard(t *testing.T) {
	r := sampleReport()
	r.Finalize()
	c := r.RenderCard("http://bot.local")
	js, err := c.JSON()
	if err != nil {
		t.Fatalf("card JSON error: %v", err)
	}
	if !strings.Contains(js, "prod-ceph-01") {
		t.Errorf("card missing cluster name:\n%s", js)
	}
	if !strings.Contains(js, "http://bot.local") {
		t.Errorf("card missing report link")
	}

	c2 := r.RenderCard("")
	js2, _ := c2.JSON()
	if strings.Contains(js2, "/inspect/") {
		t.Errorf("empty webBaseURL should not render report link")
	}
}
