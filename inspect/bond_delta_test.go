package inspect

import (
	"strconv"
	"strings"
	"testing"
)

// bondFinding 构造一个 hw_bond finding，Warn 初值 + total metric。
func bondFinding(node string, total int, level Level) Finding {
	return Finding{
		Item:    "hw_bond",
		Node:    node,
		Level:   level,
		Summary: "bond 累计 Link Failure N 次",
		Metrics: map[string]string{"link_failure_total": strconv.Itoa(total)},
	}
}

func findBond(rep *Report, node string) Finding {
	for _, f := range rep.Findings {
		if f.Item == "hw_bond" && f.Node == node {
			return f
		}
	}
	return Finding{}
}

func TestApplyBondDelta_FirstRun_NoPrev(t *testing.T) {
	rep := &Report{Findings: []Finding{bondFinding("n1", 12, LevelWarn)}}
	applyBondDelta(rep, nil)
	f := findBond(rep, "n1")
	if f.Level != LevelWarn {
		t.Errorf("no prev → %v, want Warn", f.Level)
	}
	if !strings.Contains(f.Summary, "首次") {
		t.Errorf("summary should mark baseline, got %q", f.Summary)
	}
}

func TestApplyBondDelta_Increase(t *testing.T) {
	prev := &Report{Findings: []Finding{bondFinding("n1", 12, LevelWarn)}}
	rep := &Report{Findings: []Finding{bondFinding("n1", 15, LevelWarn)}}
	applyBondDelta(rep, prev)
	f := findBond(rep, "n1")
	if f.Level != LevelWarn {
		t.Errorf("delta>0 → %v, want Warn", f.Level)
	}
	if f.Metrics["link_failure_delta"] != "3" {
		t.Errorf("delta metric = %q, want \"3\"", f.Metrics["link_failure_delta"])
	}
	if !strings.Contains(f.Summary, "新增 3") || !strings.Contains(f.Summary, "15") {
		t.Errorf("summary = %q, want mentions of 新增 3 and 累计 15", f.Summary)
	}
}

func TestApplyBondDelta_NoIncrease(t *testing.T) {
	prev := &Report{Findings: []Finding{bondFinding("n1", 12, LevelWarn)}}
	rep := &Report{Findings: []Finding{bondFinding("n1", 12, LevelWarn)}}
	applyBondDelta(rep, prev)
	if f := findBond(rep, "n1"); f.Level != LevelOK {
		t.Errorf("delta==0 → %v, want OK (downgraded)", f.Level)
	}
}

func TestApplyBondDelta_CounterReset(t *testing.T) {
	prev := &Report{Findings: []Finding{bondFinding("n1", 20, LevelWarn)}}
	rep := &Report{Findings: []Finding{bondFinding("n1", 2, LevelWarn)}} // 重启后归零重来
	applyBondDelta(rep, prev)
	if f := findBond(rep, "n1"); f.Level != LevelOK {
		t.Errorf("delta<0 → %v, want OK (downgraded)", f.Level)
	}
}

func TestApplyBondDelta_NewNode(t *testing.T) {
	prev := &Report{Findings: []Finding{bondFinding("n1", 12, LevelWarn)}}
	rep := &Report{Findings: []Finding{bondFinding("n2", 5, LevelWarn)}} // n2 上次不存在
	applyBondDelta(rep, prev)
	f := findBond(rep, "n2")
	if f.Level != LevelWarn || !strings.Contains(f.Summary, "首次") {
		t.Errorf("new node → level %v summary %q, want Warn + 首次", f.Level, f.Summary)
	}
}

func TestApplyBondDelta_CriticalUntouched(t *testing.T) {
	prev := &Report{Findings: []Finding{bondFinding("n1", 3, LevelWarn)}}
	rep := &Report{Findings: []Finding{{
		Item: "hw_bond", Node: "n1", Level: LevelCritical,
		Summary: "存在 MII Status 非 up 的 bond",
		Metrics: map[string]string{"link_failure_total": "3"},
	}}}
	applyBondDelta(rep, prev)
	if f := findBond(rep, "n1"); f.Level != LevelCritical {
		t.Errorf("critical must stay Critical, got %v", f.Level)
	}
}

func TestApplyBondDelta_PrevMetricMissing(t *testing.T) {
	// 老报告没有 link_failure_total metric → 视为无基线，保持 Warn + 首次文案。
	prev := &Report{Findings: []Finding{{Item: "hw_bond", Node: "n1", Level: LevelWarn}}}
	rep := &Report{Findings: []Finding{bondFinding("n1", 7, LevelWarn)}}
	applyBondDelta(rep, prev)
	f := findBond(rep, "n1")
	if f.Level != LevelWarn || !strings.Contains(f.Summary, "首次") {
		t.Errorf("prev metric missing → level %v summary %q, want Warn + 首次", f.Level, f.Summary)
	}
}

func TestApplyBondDelta_CurMetricMissing(t *testing.T) {
	// 本次 finding 缺 link_failure_total → bondTotal 返回 false → 保底不动，保持 Warn。
	prev := &Report{Findings: []Finding{bondFinding("n1", 5, LevelWarn)}}
	rep := &Report{Findings: []Finding{{Item: "hw_bond", Node: "n1", Level: LevelWarn,
		Summary: "bond 累计 Link Failure N 次", Metrics: map[string]string{}}}}
	applyBondDelta(rep, prev)
	if f := findBond(rep, "n1"); f.Level != LevelWarn {
		t.Errorf("cur metric missing → %v, want Warn (untouched)", f.Level)
	}
}
