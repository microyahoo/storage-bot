package inspect

import (
	"testing"
	"time"
)

func TestBackfillBondFinding(t *testing.T) {
	cases := []struct {
		name      string
		in        Finding
		wantTotal string // "" means metric should be absent
		wantChg   bool
	}{
		{
			name:      "warn recovers count from summary",
			in:        Finding{Item: "hw_bond", Level: LevelWarn, Summary: "bond 累计 Link Failure 3 次"},
			wantTotal: "3",
			wantChg:   true,
		},
		{
			name:      "ok normal → zero",
			in:        Finding{Item: "hw_bond", Level: LevelOK, Summary: "bond 链路正常"},
			wantTotal: "0",
			wantChg:   true,
		},
		{
			name:      "no bonds → left as-is",
			in:        Finding{Item: "hw_bond", Level: LevelOK, Summary: "无 bond 配置"},
			wantTotal: "",
			wantChg:   false,
		},
		{
			name:      "MII down critical → left as-is (no count in text)",
			in:        Finding{Item: "hw_bond", Level: LevelCritical, Summary: "存在 MII Status 非 up 的 bond"},
			wantTotal: "",
			wantChg:   false,
		},
		{
			name:      "already has metric → idempotent",
			in:        Finding{Item: "hw_bond", Level: LevelWarn, Summary: "bond 累计 Link Failure 3 次", Metrics: map[string]string{"link_failure_total": "3"}},
			wantTotal: "3",
			wantChg:   false,
		},
		{
			name:      "non-bond finding untouched",
			in:        Finding{Item: "hw_memory", Level: LevelWarn, Summary: "内存使用率 90%"},
			wantTotal: "",
			wantChg:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.in
			chg := backfillBondFinding(&f)
			if chg != tc.wantChg {
				t.Errorf("changed = %v, want %v", chg, tc.wantChg)
			}
			if got := f.Metrics["link_failure_total"]; got != tc.wantTotal {
				t.Errorf("link_failure_total = %q, want %q", got, tc.wantTotal)
			}
		})
	}
}

func TestMigrateReport(t *testing.T) {
	r := &Report{Findings: []Finding{
		{Item: "hw_bond", Node: "n1", Level: LevelWarn, Summary: "bond 累计 Link Failure 5 次"},
		{Item: "hw_bond", Node: "n2", Level: LevelOK, Summary: "bond 链路正常"},
		{Item: "hw_bond", Node: "n3", Level: LevelCritical, Summary: "存在 MII Status 非 up 的 bond"},
		{Item: "hw_memory", Node: "n1", Level: LevelOK, Summary: "内存使用率 40%"},
	}}
	changed := MigrateReport(r)
	if changed != 2 {
		t.Fatalf("changed = %d, want 2 (n1 warn + n2 ok)", changed)
	}
	if r.Findings[0].Metrics["link_failure_total"] != "5" {
		t.Errorf("n1 total = %q, want 5", r.Findings[0].Metrics["link_failure_total"])
	}
	if r.Findings[1].Metrics["link_failure_total"] != "0" {
		t.Errorf("n2 total = %q, want 0", r.Findings[1].Metrics["link_failure_total"])
	}
	if _, ok := r.Findings[2].Metrics["link_failure_total"]; ok {
		t.Errorf("n3 (critical) should not get a metric")
	}

	// Idempotent: a second pass changes nothing.
	if again := MigrateReport(r); again != 0 {
		t.Errorf("second migrate changed = %d, want 0", again)
	}
}

func TestMigrateStore(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, 30)

	// Old-format report (no link_failure_total metric anywhere).
	old := &Report{
		Cluster:   "c1",
		StartedAt: time.Date(2026, 6, 17, 3, 0, 0, 0, time.UTC),
		Findings: []Finding{
			{Item: "hw_bond", Node: "n1", Level: LevelWarn, Summary: "bond 累计 Link Failure 7 次"},
			{Item: "hw_bond", Node: "n2", Level: LevelOK, Summary: "bond 链路正常"},
		},
	}
	if err := s.Save(old); err != nil {
		t.Fatalf("save: %v", err)
	}
	list, _ := s.List("c1")
	name := list[0]

	// Migrate: rewrites the file in place.
	res, err := MigrateStore(s)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(res) != 1 || res[0].Changed != 2 || res[0].Err != nil {
		t.Fatalf("result = %+v, want 1 file / 2 changed / no err", res)
	}
	reloaded, _ := s.Load("c1", name)
	if reloaded.Findings[0].Metrics["link_failure_total"] != "7" {
		t.Errorf("n1 total = %q, want 7", reloaded.Findings[0].Metrics["link_failure_total"])
	}
	if reloaded.Findings[1].Metrics["link_failure_total"] != "0" {
		t.Errorf("n2 total = %q, want 0", reloaded.Findings[1].Metrics["link_failure_total"])
	}

	// Second run is a no-op (idempotent) → no files reported.
	res, err = MigrateStore(s)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("second migrate result = %+v, want empty (already migrated)", res)
	}
}

func TestMigrateStoreEmpty(t *testing.T) {
	// No history dir at all → no results, no error.
	s := NewStore(t.TempDir(), 30)
	res, err := MigrateStore(s)
	if err != nil || len(res) != 0 {
		t.Errorf("empty store = %v, %v; want [], nil", res, err)
	}
}
