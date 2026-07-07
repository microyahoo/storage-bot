# bond Link Failure 增量告警 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 `hw_bond` 巡检项从「Link Failure Count 绝对值 > 0 就告警」改为「与上一次巡检比对，有增长才告警」，并显示较上次新增的次数。

**Architecture:** `parseBond` 保持纯解析，永远落盘 `link_failure_total` metric 并按 `failTotal>0` 给 Warn 初值（MII down 仍 Critical）。Runner 在收集完 finding 后、`Finalize` 前跑纯函数 `applyBondDelta(rep, prev)`，按节点比对上一次报告的 `link_failure_total`，据增量把 Warn 保持/降级并改写文案。

**Tech Stack:** Go，标准库 `testing`。包 `github.com/microyahoo/storage-bot/inspect`。

**Spec:** `docs/superpowers/specs/2026-07-07-bond-link-failure-delta-design.md`

---

## File Structure

- `inspect/hardware_parse.go` — 修改 `parseBond`：始终写 `link_failure_total` metric；摘要文案微调。
- `inspect/hardware_parse_test.go` — 扩充 `TestParseBond`：断言 metric 存在且正确。
- `inspect/bond_delta.go`（新建）— `applyBondDelta(rep, prev *Report)` 纯函数 + 内部 helper。
- `inspect/bond_delta_test.go`（新建）— `applyBondDelta` 全场景单测。
- `inspect/runner.go` — 修改 `Run`：加载上一次报告（`latestReport`），在 `Finalize` 前调用 `applyBondDelta`。

---

## Task 1: parseBond 始终落盘 link_failure_total metric

**Files:**
- Modify: `inspect/hardware_parse.go:198-234` (`parseBond`)
- Test: `inspect/hardware_parse_test.go:107-119` (`TestParseBond`)

- [ ] **Step 1: 扩充 TestParseBond 断言 metric**

替换 `inspect/hardware_parse_test.go` 的 `TestParseBond`（107-119 行）为：

```go
func TestParseBond(t *testing.T) {
	if f := parseBond("(no bonds)"); f.Level != LevelOK {
		t.Errorf("no bonds → %v, want OK", f.Level)
	}

	warn := "/proc/net/bonding/bond0:Link Failure Count: 3\n/proc/net/bonding/bond0:MII Status: up\n"
	f := parseBond(warn)
	if f.Level != LevelWarn {
		t.Errorf("link failure → %v, want Warn", f.Level)
	}
	if f.Metrics["link_failure_total"] != "3" {
		t.Errorf("link_failure_total = %q, want \"3\"", f.Metrics["link_failure_total"])
	}

	// 多个 bond 求和后写入 total。
	sum := "/proc/net/bonding/bond0:Link Failure Count: 3\n" +
		"/proc/net/bonding/bond0:MII Status: up\n" +
		"/proc/net/bonding/bond1:Link Failure Count: 5\n" +
		"/proc/net/bonding/bond1:MII Status: up\n"
	if f := parseBond(sum); f.Metrics["link_failure_total"] != "8" {
		t.Errorf("summed link_failure_total = %q, want \"8\"", f.Metrics["link_failure_total"])
	}

	// failTotal==0 仍写 total=0，级别 OK。
	zero := "/proc/net/bonding/bond0:Link Failure Count: 0\n/proc/net/bonding/bond0:MII Status: up\n"
	if f := parseBond(zero); f.Level != LevelOK || f.Metrics["link_failure_total"] != "0" {
		t.Errorf("zero failures → level %v total %q, want OK \"0\"", f.Level, f.Metrics["link_failure_total"])
	}

	crit := "/proc/net/bonding/bond0:Link Failure Count: 3\n/proc/net/bonding/bond0:MII Status: down\n"
	f = parseBond(crit)
	if f.Level != LevelCritical {
		t.Errorf("MII down → %v, want Critical", f.Level)
	}
	if f.Metrics["link_failure_total"] != "3" {
		t.Errorf("critical still records total, got %q", f.Metrics["link_failure_total"])
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./inspect/ -run TestParseBond -v`
Expected: FAIL — 当前 `parseBond` 不写 `Metrics`，`f.Metrics["link_failure_total"]` 为空串（且 `f.Metrics` 为 nil）。

- [ ] **Step 3: 修改 parseBond 写入 metric**

替换 `inspect/hardware_parse.go` 的 `parseBond`（198-234 行）为：

```go
func parseBond(raw string) Finding {
	f := Finding{Item: "hw_bond", Detail: raw}
	if strings.Contains(raw, "(no bonds)") {
		f.Level, f.Summary = LevelOK, "无 bond 配置"
		return f
	}
	var failTotal int
	miiDown := false
	for _, line := range strings.Split(raw, "\n") {
		_, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		switch {
		case strings.HasPrefix(rest, "Link Failure Count"):
			if _, v, ok2 := strings.Cut(rest, ":"); ok2 {
				n, _ := strconv.Atoi(strings.TrimSpace(v))
				failTotal += n
			}
		case strings.HasPrefix(rest, "MII Status"):
			if _, v, ok2 := strings.Cut(rest, ":"); ok2 && strings.TrimSpace(v) != "up" {
				miiDown = true
			}
		}
	}
	// 始终落盘累计次数：这是下一次巡检做增量比对的基线来源，必须进报告 JSON。
	f.Metrics = map[string]string{"link_failure_total": strconv.Itoa(failTotal)}
	switch {
	case miiDown:
		f.Level, f.Summary = LevelCritical, "存在 MII Status 非 up 的 bond"
		f.Advice = "检查物理链路与交换机端口"
	case failTotal > 0:
		// Warn 初值：Runner 的 applyBondDelta 会据上一次报告改写文案或降级为 OK。
		f.Level, f.Summary = LevelWarn, fmt.Sprintf("bond 累计 Link Failure %d 次", failTotal)
	default:
		f.Level, f.Summary = LevelOK, "bond 链路正常"
	}
	return f
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./inspect/ -run TestParseBond -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add inspect/hardware_parse.go inspect/hardware_parse_test.go
git commit -m "$(cat <<'EOF'
feat(inspect): parseBond always records link_failure_total metric

Baseline data for the upcoming delta comparison; the count must land in
the report JSON regardless of severity.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: applyBondDelta 增量比对纯函数

**Files:**
- Create: `inspect/bond_delta.go`
- Test: `inspect/bond_delta_test.go`

- [ ] **Step 1: 写失败测试**

创建 `inspect/bond_delta_test.go`：

```go
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
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./inspect/ -run TestApplyBondDelta -v`
Expected: FAIL — `applyBondDelta` 未定义（编译错误）。

- [ ] **Step 3: 实现 applyBondDelta**

创建 `inspect/bond_delta.go`：

```go
package inspect

import (
	"fmt"
	"strconv"
)

// applyBondDelta 把 hw_bond 的 link failure 判定从「绝对值告警」修正为「与上一
// 次巡检比对，有增长才告警」。parseBond 对 failTotal>0 给出的是 Warn 初值；这里
// 按节点比对 prev 的 link_failure_total：
//
//   - 无基线（prev 为 nil / 该节点上次不存在 / 上次 metric 缺失或非数字）
//     → 保持 Warn，标注这是首次记录的基线。
//   - delta>0 → 保持 Warn，写 link_failure_delta，摘要显示新增与累计次数。
//   - delta<=0（含重启后计数器归零）→ 降级 OK，视为无新增。
//
// MII down 的 Critical 与历史无关，不在处理范围内。必须在 rep.Finalize() 之前、
// summarize() 之前调用，否则 Overall 与 LLM 摘要会用到未修正的 level。
func applyBondDelta(rep *Report, prev *Report) {
	baseline := bondBaseline(prev)
	for i := range rep.Findings {
		f := &rep.Findings[i]
		if f.Item != "hw_bond" || f.Level != LevelWarn {
			continue
		}
		cur, ok := bondTotal(*f)
		if !ok {
			continue // 本次 metric 异常，保底不动
		}
		base, hasBase := baseline[f.Node]
		if !hasBase {
			f.Summary = fmt.Sprintf("bond 累计 Link Failure %d 次（首次记录，作为基线）", cur)
			continue
		}
		delta := cur - base
		if delta > 0 {
			f.Metrics["link_failure_delta"] = strconv.Itoa(delta)
			f.Summary = fmt.Sprintf("bond Link Failure 较上次新增 %d 次（累计 %d 次）", delta, cur)
			continue
		}
		f.Level = LevelOK
		f.Summary = fmt.Sprintf("bond Link Failure 累计 %d 次，较上次无新增", cur)
	}
}

// bondBaseline 从上一次报告提取每个节点的 link_failure_total 基线。metric 缺失或
// 非数字的节点不进 map（视为无基线）。
func bondBaseline(prev *Report) map[string]int {
	m := map[string]int{}
	if prev == nil {
		return m
	}
	for _, f := range prev.Findings {
		if f.Item != "hw_bond" {
			continue
		}
		if n, ok := bondTotal(f); ok {
			m[f.Node] = n
		}
	}
	return m
}

// bondTotal reads link_failure_total from a finding's metrics.
func bondTotal(f Finding) (int, bool) {
	v, ok := f.Metrics["link_failure_total"]
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./inspect/ -run TestApplyBondDelta -v`
Expected: PASS（全部 7 个用例）

- [ ] **Step 5: 提交**

```bash
git add inspect/bond_delta.go inspect/bond_delta_test.go
git commit -m "$(cat <<'EOF'
feat(inspect): add applyBondDelta for link failure delta alerting

Compare hw_bond link_failure_total against the previous report per node:
keep Warn only when the count grew (showing the delta), downgrade to OK
otherwise. First run / new node keeps a one-time baseline warning.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Runner 接线 — 加载上一次报告并应用增量

**Files:**
- Modify: `inspect/runner.go:59-85` (`Run`)，新增 `latestReport` helper
- Test: `inspect/runner_test.go`（新增 `TestApplyBondDeltaWiring` 走 store 往返）

- [ ] **Step 1: 写失败测试**

在 `inspect/runner_test.go` 末尾追加（注意已 import `context`/`time`/`config`；新增需要 `path/filepath`... 不需要，用 `Store` 的公开 API）：

```go
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
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./inspect/ -run TestRunLoadsPrevForBondDelta -v`
Expected: FAIL — `r.latestReport` 未定义（编译错误）。

- [ ] **Step 3: 加 latestReport helper 并接入 Run**

在 `inspect/runner.go` 的 `Run` 方法（59-85 行）里，把 store 保存前后的逻辑改为先取 prev、再 apply。替换整个 `Run` 方法为：

```go
// Run resolves the cluster, runs all inspectors, persists and returns the report.
func (r *Runner) Run(ctx context.Context, clusterInput string) (*Report, error) {
	name, cfg, err := r.clusters.FindByPrefix(clusterInput)
	if err != nil {
		return nil, err
	}
	ke, err := r.clusters.KubeExecutor(name, cfg)
	if err != nil {
		return nil, err
	}
	nodes, err := r.clusters.ResolveSSHNodes(ctx, name, cfg)
	if err != nil {
		return nil, err
	}

	// 取上一次报告用于 bond link failure 增量比对（此刻本次尚未 Save）。
	prev := r.latestReport(name)

	start := time.Now()
	rep := r.runWith(ctx, name, ke, cfg.GatewayNode, nodes, r.thresholds, start)

	// 增量修正必须在 Finalize（Overall 依赖 level）与 summarize（降级项不喂 LLM）之前。
	applyBondDelta(rep, prev)
	rep.Finalize()
	rep.Duration = time.Since(start)

	if r.llmSummary && r.analyzer != nil {
		if s, err := r.summarize(ctx, rep); err == nil {
			rep.LLMSummary = s
		}
	}
	if r.store != nil {
		_ = r.store.Save(rep)
	}
	return rep, nil
}

// latestReport loads the most recent stored report for the cluster, or nil when
// there is no store or no history yet (first run). Used as the bond link-failure
// delta baseline.
func (r *Runner) latestReport(cluster string) *Report {
	if r.store == nil {
		return nil
	}
	names, err := r.store.List(cluster)
	if err != nil || len(names) == 0 {
		return nil
	}
	rep, err := r.store.Load(cluster, names[0])
	if err != nil {
		return nil
	}
	return rep
}
```

注意：`runWith` 内部末尾已调用 `rep.Finalize()`（runner.go:136）。因为现在要在 `applyBondDelta` 之后再 Finalize，`runWith` 里那次 Finalize 会用未修正的 level 算出一个临时 Overall，被外层重新 Finalize 覆盖，无害；但为避免误导，保留 `runWith` 的 Finalize 不动（其它测试如 `TestRunnerAggregates` 直接调 `runWith` 并断言 `rep.Overall`，依赖它）。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./inspect/ -run TestRunLoadsPrevForBondDelta -v`
Expected: PASS

- [ ] **Step 5: 跑整包回归**

Run: `go test ./inspect/ -v`
Expected: PASS（含 `TestRunnerAggregates`、`TestParseBond`、`TestApplyBondDelta*`、`TestRunLoadsPrevForBondDelta`）。

- [ ] **Step 6: 提交**

```bash
git add inspect/runner.go inspect/runner_test.go
git commit -m "$(cat <<'EOF'
feat(inspect): wire bond link failure delta into Run

Load the previous report before inspecting and apply applyBondDelta
before Finalize/summarize, so hw_bond warns only on a real increase.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: 全量构建与验证

**Files:** 无（验证任务）

- [ ] **Step 1: 全包构建**

Run: `go build ./...`
Expected: 无输出，退出码 0。

- [ ] **Step 2: 全量测试**

Run: `go test ./...`
Expected: PASS（重点确认 `inspect`、`bot`、`web` 包不受影响 —— RenderText/RenderCard 未改，仅数据变化）。

- [ ] **Step 3: go vet**

Run: `go vet ./inspect/`
Expected: 无告警。

---

## Self-Review 记录

- **Spec 覆盖**：parseBond 始终写 total（Task 1）；applyBondDelta 全场景含首次/新增/无新增/归零/新节点/Critical/metric 缺失（Task 2 七个用例，与 spec 测试清单一一对应）；Runner 接线与顺序（Task 3）；展示层不改（spec 已说明，Task 4 回归验证）；老报告兼容（Task 2 `TestApplyBondDelta_PrevMetricMissing`）。
- **占位符**：无 TBD/TODO，所有 step 含完整代码或确切命令。
- **类型一致**：`applyBondDelta(rep, prev *Report)`、`bondBaseline`、`bondTotal`、`latestReport(cluster string) *Report`、metric 键 `link_failure_total` / `link_failure_delta` 在各 Task 间一致。
- **步骤编号**：Task 2 为五步（写测试 → 确认失败 → 实现 → 确认通过 → 提交），测试文件直接 import `strconv`，无临时 helper。
