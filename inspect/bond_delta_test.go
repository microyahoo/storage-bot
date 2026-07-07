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

// bondReport wraps a single hw_bond finding in a report, for use as a baseline.
func bondReport(node string, total int) *Report {
	return &Report{Findings: []Finding{bondFinding(node, total, LevelWarn)}}
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
	applyBondDelta(rep, nil, nil)
	f := findBond(rep, "n1")
	if f.Level != LevelWarn {
		t.Errorf("no prev → %v, want Warn", f.Level)
	}
	if !strings.Contains(f.Summary, "首次") {
		t.Errorf("summary should mark baseline, got %q", f.Summary)
	}
}

func TestApplyBondDelta_Increase(t *testing.T) {
	prev1 := bondReport("n1", 12)
	rep := &Report{Findings: []Finding{bondFinding("n1", 15, LevelWarn)}}
	applyBondDelta(rep, prev1, nil)
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
	prev1 := bondReport("n1", 12)
	rep := &Report{Findings: []Finding{bondFinding("n1", 12, LevelWarn)}}
	applyBondDelta(rep, prev1, nil)
	if f := findBond(rep, "n1"); f.Level != LevelOK {
		t.Errorf("delta==0 → %v, want OK (downgraded)", f.Level)
	}
}

func TestApplyBondDelta_CounterReset(t *testing.T) {
	prev1 := bondReport("n1", 20)
	rep := &Report{Findings: []Finding{bondFinding("n1", 2, LevelWarn)}} // 重启后归零重来
	applyBondDelta(rep, prev1, nil)
	if f := findBond(rep, "n1"); f.Level != LevelOK {
		t.Errorf("delta<0 → %v, want OK (downgraded)", f.Level)
	}
}

func TestApplyBondDelta_NewNode(t *testing.T) {
	prev1 := bondReport("n1", 12)
	rep := &Report{Findings: []Finding{bondFinding("n2", 5, LevelWarn)}} // n2 上次不存在
	applyBondDelta(rep, prev1, nil)
	f := findBond(rep, "n2")
	if f.Level != LevelWarn || !strings.Contains(f.Summary, "首次") {
		t.Errorf("new node → level %v summary %q, want Warn + 首次", f.Level, f.Summary)
	}
}

func TestApplyBondDelta_CriticalUntouched(t *testing.T) {
	prev1 := bondReport("n1", 3)
	rep := &Report{Findings: []Finding{{
		Item: "hw_bond", Node: "n1", Level: LevelCritical,
		Summary: "存在 MII Status 非 up 的 bond",
		Metrics: map[string]string{"link_failure_total": "3"},
	}}}
	applyBondDelta(rep, prev1, nil)
	if f := findBond(rep, "n1"); f.Level != LevelCritical {
		t.Errorf("critical must stay Critical, got %v", f.Level)
	}
}

func TestApplyBondDelta_PrevMetricMissing(t *testing.T) {
	// 老报告没有 link_failure_total metric → 视为无基线，保持 Warn + 首次文案。
	prev1 := &Report{Findings: []Finding{{Item: "hw_bond", Node: "n1", Level: LevelWarn}}}
	rep := &Report{Findings: []Finding{bondFinding("n1", 7, LevelWarn)}}
	applyBondDelta(rep, prev1, nil)
	f := findBond(rep, "n1")
	if f.Level != LevelWarn || !strings.Contains(f.Summary, "首次") {
		t.Errorf("prev metric missing → level %v summary %q, want Warn + 首次", f.Level, f.Summary)
	}
}

func TestApplyBondDelta_CurMetricMissing(t *testing.T) {
	// 本次 finding 缺 link_failure_total → bondTotal 返回 false → 保底不动，保持 Warn。
	prev1 := bondReport("n1", 5)
	rep := &Report{Findings: []Finding{{Item: "hw_bond", Node: "n1", Level: LevelWarn,
		Summary: "bond 累计 Link Failure N 次", Metrics: map[string]string{}}}}
	applyBondDelta(rep, prev1, nil)
	if f := findBond(rep, "n1"); f.Level != LevelWarn {
		t.Errorf("cur metric missing → %v, want Warn (untouched)", f.Level)
	}
}

// --- 两窗口比对新增用例 ---

func TestApplyBondDelta_MissedMiddleReSurfaces(t *testing.T) {
	// T-2=10, T-1=13（漏看，涨了3）, T=13（较上次持平）。跟上次比=0 会被藏住，
	// 跟上上次比=+3 应重新报 Warn 并提示可能漏看。
	prev1 := bondReport("n1", 13) // T-1
	prev2 := bondReport("n1", 10) // T-2
	rep := &Report{Findings: []Finding{bondFinding("n1", 13, LevelWarn)}}
	applyBondDelta(rep, prev1, prev2)
	f := findBond(rep, "n1")
	if f.Level != LevelWarn {
		t.Errorf("windowInc → %v, want Warn", f.Level)
	}
	if f.Metrics["link_failure_delta2"] != "3" {
		t.Errorf("delta2 = %q, want \"3\"", f.Metrics["link_failure_delta2"])
	}
	if !strings.Contains(f.Summary, "漏看") || !strings.Contains(f.Summary, "较上上次新增 3") {
		t.Errorf("summary = %q, want 漏看 + 较上上次新增 3", f.Summary)
	}
}

func TestApplyBondDelta_BothWindowsFlat_Downgrades(t *testing.T) {
	// T-2=T-1=T=10：两窗口都无新增 → 降级 OK。
	prev1 := bondReport("n1", 10)
	prev2 := bondReport("n1", 10)
	rep := &Report{Findings: []Finding{bondFinding("n1", 10, LevelWarn)}}
	applyBondDelta(rep, prev1, prev2)
	if f := findBond(rep, "n1"); f.Level != LevelOK {
		t.Errorf("both flat → %v, want OK", f.Level)
	}
}

func TestApplyBondDelta_RecentAndWindowBothGrew(t *testing.T) {
	// T-2=10, T-1=12, T=15：较上次+3、较上上次+5 → 两个 delta 都标注。
	prev1 := bondReport("n1", 12)
	prev2 := bondReport("n1", 10)
	rep := &Report{Findings: []Finding{bondFinding("n1", 15, LevelWarn)}}
	applyBondDelta(rep, prev1, prev2)
	f := findBond(rep, "n1")
	if f.Level != LevelWarn {
		t.Errorf("→ %v, want Warn", f.Level)
	}
	if f.Metrics["link_failure_delta"] != "3" || f.Metrics["link_failure_delta2"] != "5" {
		t.Errorf("deltas = %q/%q, want 3/5", f.Metrics["link_failure_delta"], f.Metrics["link_failure_delta2"])
	}
	if !strings.Contains(f.Summary, "较上次新增 3") || !strings.Contains(f.Summary, "较上上次累计新增 5") {
		t.Errorf("summary = %q, want both deltas", f.Summary)
	}
}

func TestApplyBondDelta_ResetBetweenBaselines_RecentStillWarns(t *testing.T) {
	// T-2=100（重启前）, T-1=2, T=5：base2 偏大是 stale。较上次+3 仍应报 Warn，
	// 不被 stale base2 掩盖，也不写 delta2（cur<base2）。
	prev1 := bondReport("n1", 2)
	prev2 := bondReport("n1", 100)
	rep := &Report{Findings: []Finding{bondFinding("n1", 5, LevelWarn)}}
	applyBondDelta(rep, prev1, prev2)
	f := findBond(rep, "n1")
	if f.Level != LevelWarn {
		t.Errorf("recentInc after reset → %v, want Warn", f.Level)
	}
	if f.Metrics["link_failure_delta"] != "3" {
		t.Errorf("delta = %q, want \"3\"", f.Metrics["link_failure_delta"])
	}
	if _, ok := f.Metrics["link_failure_delta2"]; ok {
		t.Errorf("stale base2 (cur<base2) must not produce delta2")
	}
}

func TestApplyBondDelta_OnlyPrev2HasNode(t *testing.T) {
	// 该节点只在 T-2 出现（T-1 那次巡检漏采/节点缺失），T=8 > base2=5 → windowInc。
	prev1 := bondReport("other", 1)
	prev2 := bondReport("n1", 5)
	rep := &Report{Findings: []Finding{bondFinding("n1", 8, LevelWarn)}}
	applyBondDelta(rep, prev1, prev2)
	f := findBond(rep, "n1")
	if f.Level != LevelWarn {
		t.Errorf("only prev2 has node, grew → %v, want Warn", f.Level)
	}
	if f.Metrics["link_failure_delta2"] != "3" {
		t.Errorf("delta2 = %q, want \"3\"", f.Metrics["link_failure_delta2"])
	}
}
