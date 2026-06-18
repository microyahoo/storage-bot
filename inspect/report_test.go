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
		},
	}
}

func TestReportCountsAndOverall(t *testing.T) {
	r := sampleReport()
	r.Finalize()
	if r.Overall != LevelCritical {
		t.Errorf("Overall = %v, want Critical", r.Overall)
	}
	ok, warn, crit := r.Counts()
	if ok != 2 || warn != 1 || crit != 1 {
		t.Errorf("counts = %d/%d/%d, want 2/1/1", ok, warn, crit)
	}
	if len(r.Abnormal()) != 2 {
		t.Errorf("abnormal = %d, want 2", len(r.Abnormal()))
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
}
