# 集群巡检系统 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 storage-bot 新增「集群巡检」能力：周期性检查 Ceph 集群状态与节点硬件，产出结构化报告，通过 cron/聊天/Web/API 触发，结果以飞书卡片推送并留存历史。

**Architecture:** 新增独立 `inspect` 包。核心是 `Inspector` 接口产出结构化 `Finding`。**关键设计：每个 inspector 把「采集」（执行命令，难单测）与「判定解析」（纯函数 `parseXxx(raw) []Finding`，易单测）分离**——TDD 只测纯解析函数，采集层做集成验证。所有触发入口收口于 `Runner.Run`。

**Tech Stack:** Go 1.23、`github.com/robfig/cron/v3`（调度）、标准 `testing`、复用 `executor.KubeExecutor`/`SSHExecutor`、`analyzer`、`card`。

**测试命令：** 单包 `go test ./inspect/ -run TestName -v`；全量 `go test ./...`。

---

## 设计落地约束（实现前必读）

实现中已核对现有代码，以下约束必须遵守，避免与项目脱节：

1. **`card.Card` 没有交互按钮组件**，只有 `New(emoji,title,theme)` / `Subtitle` / `Body(md)` / `Divider` / `Note(md)` / `JSON()`。spec 第 8.2 的「双按钮」**降级为 Body 里的 markdown 链接**（飞书 markdown 支持 `[文字](url)`）。`RenderCard` 返回 `*card.Card`，不返回自定义 `larkCard` 类型。
2. **`KubeExecutor.RunCephCommand(ctx, args ...string) (string, error)`**：args 是拆开的，如 `RunCephCommand(ctx, "osd", "stat")`。
3. **`SSHExecutor.Run(ctx, node, cmd) (string,error)` 与 `RunViaGateway(ctx, gateway, target, cmd)`**；`executor.HostIP(hostPort) string` 用于比较是否为网关本机。
4. **`cluster.Manager.ResolveSSHNodes(ctx, name, *config.ClusterConfig) ([]config.SSHNode, error)`** 已存在，直接复用做节点解析。
5. **`analyzer.Analyzer.Analyze(ctx, clusterName, diagnosticData string) (string, error)`** 用于 LLM 总结；`Analyzer` 可能为 nil（LLM 关闭），调用前判空。
6. **`intent.Action`** 有字段 `Type ActionType`、`ClusterName`、`RawMessage`、`Args map[string]string`。新增 `ActionInspect` 常量。
7. **`bot.Handler.replyCard(ctx, messageID string, c *card.Card) error`** 是私有方法；新增的 `HandleInspect` 在 bot 包内，可直接用。
8. Go 中 `time.Now()` 在测试里不可控——`Report.StartedAt`/`Duration` 由调用方传入，渲染/判定函数不调 `time.Now()`。

---

## 文件结构

**新增 `inspect/` 包：**

| 文件 | 职责 |
|---|---|
| `inspect/inspector.go` | `Level`/`Scope`/`Finding`/`Inspector`/`InspectContext` 类型（`Thresholds` 在 config 包，这里引用 `config.Thresholds`）+ `InspectContext` 的 `RunOnNode`/`RunCeph` 辅助方法 + `Level.Emoji()`/`Level.String()` |
| `inspect/registry.go` | `Registry`：注册/按 Scope 取 inspector |
| `inspect/ceph.go` | 7 个 ceph inspector：采集 + 调用纯解析函数 |
| `inspect/ceph_parse.go` | ceph 各项的纯解析函数 `parseCephHealth` 等（被单测覆盖） |
| `inspect/hardware.go` | 6 个硬件 inspector：采集 + 调用纯解析函数 |
| `inspect/hardware_parse.go` | 硬件各项纯解析函数 `parseMemory`/`parseSmart` 等（被单测覆盖） |
| `inspect/report.go` | `Report` 结构 + `Counts`/`Abnormal`/`RenderText`/`RenderCard` |
| `inspect/runner.go` | `Runner`：编排所有 inspector，聚合成 `Report` |
| `inspect/scheduler.go` | `Scheduler`：cron 调度 + 热重载 + 主动推送 |
| `inspect/store.go` | `Store`：JSON 历史留存 |
| `inspect/*_test.go` | 对应纯函数测试 |

**改动现有文件：** `config/config.go`、`main.go`、`intent/parser.go`、`bot/handler.go`、`web/server.go`(+模板)、`config.yaml.example`、`go.mod`。

---

## Task 1: 核心类型与 Level 渲染辅助

**Files:**
- Create: `inspect/inspector.go`
- Test: `inspect/inspector_test.go`

- [ ] **Step 1: 写失败测试**

```go
// inspect/inspector_test.go
package inspect

import "testing"

func TestLevelString(t *testing.T) {
	cases := map[Level]string{
		LevelOK: "OK", LevelWarn: "WARN", LevelCritical: "CRITICAL", LevelUnknown: "UNKNOWN",
	}
	for lvl, want := range cases {
		if got := lvl.String(); got != want {
			t.Errorf("Level(%d).String() = %q, want %q", lvl, got, want)
		}
	}
}

func TestLevelEmoji(t *testing.T) {
	if LevelCritical.Emoji() != "🔴" || LevelWarn.Emoji() != "🟡" || LevelOK.Emoji() != "🟢" {
		t.Errorf("unexpected emoji mapping")
	}
}

func TestMaxLevel(t *testing.T) {
	got := MaxLevel([]Finding{{Level: LevelOK}, {Level: LevelCritical}, {Level: LevelWarn}})
	if got != LevelCritical {
		t.Errorf("MaxLevel = %v, want Critical", got)
	}
	if MaxLevel(nil) != LevelOK {
		t.Errorf("MaxLevel(nil) should be OK")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./inspect/ -run 'TestLevel|TestMaxLevel' -v`
Expected: FAIL（编译错误：未定义类型）

- [ ] **Step 3: 写最小实现**

```go
// inspect/inspector.go
package inspect

import (
	"context"

	"github.com/microyahoo/storage-bot/config"
	"github.com/microyahoo/storage-bot/executor"
)

type Level int

const (
	LevelOK Level = iota
	LevelWarn
	LevelCritical
	LevelUnknown
)

func (l Level) String() string {
	switch l {
	case LevelOK:
		return "OK"
	case LevelWarn:
		return "WARN"
	case LevelCritical:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

func (l Level) Emoji() string {
	switch l {
	case LevelOK:
		return "🟢"
	case LevelWarn:
		return "🟡"
	case LevelCritical:
		return "🔴"
	default:
		return "⚪"
	}
}

type Scope int

const (
	ClusterScope Scope = iota
	NodeScope
)

type Finding struct {
	Item    string            `json:"item"`
	Node    string            `json:"node,omitempty"`
	Level   Level             `json:"level"`
	Summary string            `json:"summary"`
	Metrics map[string]string `json:"metrics,omitempty"`
	Detail  string            `json:"detail,omitempty"`
	Advice  string            `json:"advice,omitempty"`
}

// MaxLevel returns the highest (most severe) level among findings, treating
// LevelUnknown as below Critical but above OK is NOT assumed — Unknown ranks
// just under Warn here so a parse failure doesn't mask a healthy cluster as bad.
// Ordering for severity: OK < Unknown < Warn < Critical.
func MaxLevel(findings []Finding) Level {
	rank := map[Level]int{LevelOK: 0, LevelUnknown: 1, LevelWarn: 2, LevelCritical: 3}
	max := LevelOK
	for _, f := range findings {
		if rank[f.Level] > rank[max] {
			max = f.Level
		}
	}
	return max
}

// 注意：Thresholds 类型定义在 config 包（见 Task 8），不在这里重复定义。
// inspect 包直接引用 config.Thresholds，避免两个包各有一份导致类型不一致。

type InspectContext struct {
	Ctx         context.Context
	ClusterName string
	Node        config.SSHNode
	Gateway     *config.SSHNode
	KubeExec    *executor.KubeExecutor
	SSHExec     *executor.SSHExecutor
	Thresholds  config.Thresholds
}

// RunCeph runs `ceph <args...>` via the toolbox pod.
func (ic *InspectContext) RunCeph(args ...string) (string, error) {
	return ic.KubeExec.RunCephCommand(ic.Ctx, args...)
}

// RunOnNode runs cmd on ic.Node, using the gateway hop when needed (mirrors
// skill.Context.RunOnNode).
func (ic *InspectContext) RunOnNode(cmd string) (string, error) {
	if ic.Gateway != nil && executor.HostIP(ic.Node.Host) != executor.HostIP(ic.Gateway.Host) {
		return ic.SSHExec.RunViaGateway(ic.Ctx, *ic.Gateway, ic.Node, cmd)
	}
	return ic.SSHExec.Run(ic.Ctx, ic.Node, cmd)
}

type Inspector interface {
	Name() string
	Description() string
	Scope() Scope
	Inspect(ic *InspectContext) ([]Finding, error)
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./inspect/ -run 'TestLevel|TestMaxLevel' -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add inspect/inspector.go inspect/inspector_test.go
git commit -m "feat(inspect): core types Level/Scope/Finding/InspectContext

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: Ceph 纯解析函数（健康/OSD/容量）

**Files:**
- Create: `inspect/ceph_parse.go`
- Test: `inspect/ceph_parse_test.go`

纯函数签名：`func parseCephHealth(raw string) Finding`、`func parseOSDStat(raw string) Finding`、`func parseCephDF(raw string, warnPct, critPct int) Finding`。采集层（Task 4）只负责取 raw 再调它们。

- [ ] **Step 1: 写失败测试**

```go
// inspect/ceph_parse_test.go
package inspect

import "testing"

func TestParseCephHealth(t *testing.T) {
	if f := parseCephHealth("HEALTH_OK\n"); f.Level != LevelOK {
		t.Errorf("HEALTH_OK → %v", f.Level)
	}
	if f := parseCephHealth("HEALTH_WARN 1 daemons...\n"); f.Level != LevelWarn {
		t.Errorf("HEALTH_WARN → %v", f.Level)
	}
	if f := parseCephHealth("HEALTH_ERR 2 pgs inconsistent\n"); f.Level != LevelCritical {
		t.Errorf("HEALTH_ERR → %v", f.Level)
	}
	if f := parseCephHealth("garbage output"); f.Level != LevelUnknown {
		t.Errorf("unparseable → %v, want Unknown", f.Level)
	}
}

func TestParseOSDStat(t *testing.T) {
	// `ceph osd stat` form: "36 osds: 34 up (since ...), 35 in (since ...)"
	f := parseOSDStat("36 osds: 34 up (since 2h), 35 in (since 3h); ...")
	if f.Level != LevelCritical {
		t.Errorf("2 down 1 out → %v, want Critical", f.Level)
	}
	if f.Metrics["osd_down"] != "2" || f.Metrics["osd_total"] != "36" {
		t.Errorf("metrics = %v", f.Metrics)
	}
	healthy := parseOSDStat("36 osds: 36 up (since 2h), 36 in (since 3h)")
	if healthy.Level != LevelOK {
		t.Errorf("all up/in → %v, want OK", healthy.Level)
	}
}

func TestParseCephDF(t *testing.T) {
	// `ceph df` global line: "TOTAL  100 TiB  18 TiB  82 TiB  82.00  ..."
	raw := "--- RAW STORAGE ---\nCLASS  SIZE  AVAIL  USED  RAW USED  %RAW USED\nTOTAL  100 TiB  18 TiB  82 TiB  82 TiB  82.00\n"
	f := parseCephDF(raw, 80, 90)
	if f.Level != LevelWarn {
		t.Errorf("82%% with warn=80 → %v, want Warn", f.Level)
	}
	if f.Metrics["used_pct"] != "82.00" {
		t.Errorf("used_pct = %q", f.Metrics["used_pct"])
	}
	crit := parseCephDF("TOTAL  100 TiB  5 TiB  95 TiB  95 TiB  95.00\n", 80, 90)
	if crit.Level != LevelCritical {
		t.Errorf("95%% → %v, want Critical", crit.Level)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./inspect/ -run 'TestParseCeph|TestParseOSD' -v`
Expected: FAIL（未定义函数）

- [ ] **Step 3: 写最小实现**

```go
// inspect/ceph_parse.go
package inspect

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func parseCephHealth(raw string) Finding {
	f := Finding{Item: "ceph_health", Detail: raw}
	switch {
	case strings.Contains(raw, "HEALTH_OK"):
		f.Level, f.Summary = LevelOK, "集群健康 HEALTH_OK"
	case strings.Contains(raw, "HEALTH_WARN"):
		f.Level, f.Summary = LevelWarn, "集群 HEALTH_WARN"
	case strings.Contains(raw, "HEALTH_ERR"):
		f.Level, f.Summary = LevelCritical, "集群 HEALTH_ERR"
	default:
		f.Level, f.Summary = LevelUnknown, "无法解析 health 输出"
	}
	return f
}

var osdStatRe = regexp.MustCompile(`(\d+) osds:\s*(\d+) up[^,]*,\s*(\d+) in`)

func parseOSDStat(raw string) Finding {
	f := Finding{Item: "ceph_osd", Detail: raw, Metrics: map[string]string{}}
	m := osdStatRe.FindStringSubmatch(raw)
	if m == nil {
		f.Level, f.Summary = LevelUnknown, "无法解析 osd stat"
		return f
	}
	total, _ := strconv.Atoi(m[1])
	up, _ := strconv.Atoi(m[2])
	in, _ := strconv.Atoi(m[3])
	down, out := total-up, total-in
	f.Metrics["osd_total"] = strconv.Itoa(total)
	f.Metrics["osd_up"] = strconv.Itoa(up)
	f.Metrics["osd_in"] = strconv.Itoa(in)
	f.Metrics["osd_down"] = strconv.Itoa(down)
	f.Metrics["osd_out"] = strconv.Itoa(out)
	if down > 0 || out > 0 {
		f.Level = LevelCritical
		f.Summary = fmt.Sprintf("%d 个 OSD down, %d 个 out", down, out)
		f.Advice = "检查 down 的 OSD 进程与磁盘；必要时 `ceph osd in <id>`"
	} else {
		f.Level = LevelOK
		f.Summary = fmt.Sprintf("全部 %d 个 OSD up/in", total)
	}
	return f
}

// %RAW USED 是 ceph df 全局 TOTAL 行的最后一个数字。
var dfTotalRe = regexp.MustCompile(`(?m)^TOTAL\b.*?([\d.]+)\s*$`)

func parseCephDF(raw string, warnPct, critPct int) Finding {
	f := Finding{Item: "ceph_capacity", Detail: raw, Metrics: map[string]string{}}
	m := dfTotalRe.FindStringSubmatch(raw)
	if m == nil {
		f.Level, f.Summary = LevelUnknown, "无法解析 ceph df"
		return f
	}
	pct, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		f.Level, f.Summary = LevelUnknown, "无法解析使用率数值"
		return f
	}
	f.Metrics["used_pct"] = m[1]
	switch {
	case pct >= float64(critPct):
		f.Level = LevelCritical
	case pct >= float64(warnPct):
		f.Level = LevelWarn
	default:
		f.Level = LevelOK
	}
	f.Summary = fmt.Sprintf("全局使用率 %.2f%%（warn=%d crit=%d）", pct, warnPct, critPct)
	return f
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./inspect/ -run 'TestParseCeph|TestParseOSD' -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add inspect/ceph_parse.go inspect/ceph_parse_test.go
git commit -m "feat(inspect): ceph health/osd/df parsers

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: 硬件纯解析函数（内存/磁盘使用/SMART/load）

**Files:**
- Create: `inspect/hardware_parse.go`
- Test: `inspect/hardware_parse_test.go`

纯函数：`parseMemory(raw string, warnPct, critPct int) Finding`、`parseDiskUsage(raw string, warnPct, critPct int) []Finding`、`parseSmart(dev, raw string, warnPct, critPct int) Finding`、`parseLoad(raw string, cores int, warnRatio float64) Finding`。

- [ ] **Step 1: 写失败测试**

```go
// inspect/hardware_parse_test.go
package inspect

import "testing"

func TestParseMemory(t *testing.T) {
	// `free -b` line: "Mem:  total used free shared buff/cache available"
	raw := "              total        used        free      shared  buff/cache   available\nMem:    100         95          1           0           4           2\n"
	f := parseMemory(raw, 90, 95)
	if f.Level != LevelCritical {
		t.Errorf("95%% used → %v, want Critical", f.Level)
	}
	ok := parseMemory("Mem:  100  50  40  0  10  45\n", 90, 95)
	if ok.Level != LevelOK {
		t.Errorf("50%% → %v, want OK", ok.Level)
	}
}

func TestParseDiskUsage(t *testing.T) {
	// `df -B1` columns: Filesystem 1B-blocks Used Available Use% Mounted
	raw := "Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda1 100 92 8 92% /\n/dev/sdb1 100 50 50 50% /data\n"
	fs := parseDiskUsage(raw, 85, 90)
	if len(fs) != 1 {
		t.Fatalf("only the 92%% mount should be abnormal, got %d findings", len(fs))
	}
	if fs[0].Level != LevelCritical || fs[0].Metrics["mount"] != "/" {
		t.Errorf("got %+v", fs[0])
	}
}

func TestParseSmartNVMe(t *testing.T) {
	raw := "SMART overall-health self-assessment test result: PASSED\nPercentage Used:  85%\n"
	f := parseSmart("/dev/nvme0n1", raw, 80, 90)
	if f.Level != LevelWarn {
		t.Errorf("85%% used, warn=80 → %v, want Warn", f.Level)
	}
	failed := parseSmart("/dev/sda", "SMART overall-health self-assessment test result: FAILED\n", 80, 90)
	if failed.Level != LevelCritical {
		t.Errorf("FAILED → %v, want Critical", failed.Level)
	}
}

func TestParseSmartMissing(t *testing.T) {
	f := parseSmart("/dev/sda", "smartctl: command not found", 80, 90)
	if f.Level != LevelUnknown {
		t.Errorf("missing smartctl → %v, want Unknown", f.Level)
	}
}

func TestParseLoad(t *testing.T) {
	raw := " 14:02:01 up 10 days,  load average: 18.0, 5.0, 4.0\n"
	f := parseLoad(raw, 8, 2.0) // load1=18, cores=8 → ratio 2.25 ≥ 2.0
	if f.Level != LevelWarn {
		t.Errorf("ratio 2.25 → %v, want Warn", f.Level)
	}
	ok := parseLoad(" load average: 4.0, 3.0, 2.0\n", 8, 2.0)
	if ok.Level != LevelOK {
		t.Errorf("ratio 0.5 → %v, want OK", ok.Level)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./inspect/ -run 'TestParseMemory|TestParseDiskUsage|TestParseSmart|TestParseLoad' -v`
Expected: FAIL（未定义函数）

- [ ] **Step 3: 写最小实现**

```go
// inspect/hardware_parse.go
package inspect

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func parseMemory(raw string, warnPct, critPct int) Finding {
	f := Finding{Item: "hw_memory", Detail: raw, Metrics: map[string]string{}}
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && strings.HasPrefix(fields[0], "Mem") {
			total, err1 := strconv.ParseFloat(fields[1], 64)
			used, err2 := strconv.ParseFloat(fields[2], 64)
			if err1 != nil || err2 != nil || total == 0 {
				break
			}
			pct := used / total * 100
			f.Metrics["used_pct"] = fmt.Sprintf("%.1f", pct)
			switch {
			case pct >= float64(critPct):
				f.Level = LevelCritical
			case pct >= float64(warnPct):
				f.Level = LevelWarn
			default:
				f.Level = LevelOK
			}
			f.Summary = fmt.Sprintf("内存使用率 %.1f%%", pct)
			return f
		}
	}
	f.Level, f.Summary = LevelUnknown, "无法解析 free 输出"
	return f
}

func parseDiskUsage(raw string, warnPct, critPct int) []Finding {
	var out []Finding
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] == "Filesystem" {
			continue
		}
		useStr := strings.TrimSuffix(fields[4], "%")
		pct, err := strconv.Atoi(useStr)
		if err != nil {
			continue
		}
		mount := fields[5]
		var lvl Level
		switch {
		case pct >= critPct:
			lvl = LevelCritical
		case pct >= warnPct:
			lvl = LevelWarn
		default:
			continue // 正常的挂载点不产出 finding，避免刷屏
		}
		out = append(out, Finding{
			Item:    "hw_disk_usage",
			Level:   lvl,
			Summary: fmt.Sprintf("%s 使用率 %d%%", mount, pct),
			Metrics: map[string]string{"mount": mount, "used_pct": useStr},
		})
	}
	return out
}

var smartPctRe = regexp.MustCompile(`Percentage Used:\s*(\d+)%`)

func parseSmart(dev, raw string, warnPct, critPct int) Finding {
	f := Finding{Item: "hw_disk_smart", Detail: raw, Metrics: map[string]string{"device": dev}}
	if strings.Contains(raw, "command not found") || strings.Contains(raw, "not installed") {
		f.Level = LevelUnknown
		f.Summary = dev + "：smartctl 不可用"
		f.Advice = "节点未安装 smartmontools，建议 `yum install smartmontools`"
		return f
	}
	if strings.Contains(raw, "self-assessment test result: FAILED") {
		f.Level = LevelCritical
		f.Summary = dev + "：SMART 自检 FAILED"
		f.Advice = "磁盘自检失败，尽快更换"
		return f
	}
	if m := smartPctRe.FindStringSubmatch(raw); m != nil {
		used, _ := strconv.Atoi(m[1])
		f.Metrics["percentage_used"] = m[1]
		switch {
		case used >= critPct:
			f.Level = LevelCritical
		case used >= warnPct:
			f.Level = LevelWarn
		default:
			f.Level = LevelOK
		}
		f.Summary = fmt.Sprintf("%s：寿命已用 %d%%", dev, used)
		return f
	}
	if strings.Contains(raw, "self-assessment test result: PASSED") {
		f.Level = LevelOK
		f.Summary = dev + "：SMART PASSED"
		return f
	}
	f.Level, f.Summary = LevelUnknown, dev+"：无法解析 smartctl 输出"
	return f
}

var loadRe = regexp.MustCompile(`load average:\s*([\d.]+),\s*([\d.]+),\s*([\d.]+)`)

func parseLoad(raw string, cores int, warnRatio float64) Finding {
	f := Finding{Item: "hw_cpu", Detail: raw, Metrics: map[string]string{"cores": strconv.Itoa(cores)}}
	m := loadRe.FindStringSubmatch(raw)
	if m == nil || cores <= 0 {
		f.Level, f.Summary = LevelUnknown, "无法解析 load average"
		return f
	}
	load1, _ := strconv.ParseFloat(m[1], 64)
	f.Metrics["load1"] = m[1]
	f.Metrics["load5"] = m[2]
	f.Metrics["load15"] = m[3]
	ratio := load1 / float64(cores)
	if ratio >= warnRatio {
		f.Level = LevelWarn
		f.Summary = fmt.Sprintf("load1=%.1f / %d 核 = %.2f ≥ %.1f", load1, cores, ratio, warnRatio)
	} else {
		f.Level = LevelOK
		f.Summary = fmt.Sprintf("load1=%.1f / %d 核 正常", load1, cores)
	}
	return f
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./inspect/ -run 'TestParseMemory|TestParseDiskUsage|TestParseSmart|TestParseLoad' -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add inspect/hardware_parse.go inspect/hardware_parse_test.go
git commit -m "feat(inspect): hardware parsers memory/disk/smart/load

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 4: Report 结构与渲染（含飞书卡片）

**Files:**
- Create: `inspect/report.go`
- Test: `inspect/report_test.go`

`RenderCard` 返回 `*card.Card`（项目现有类型，无按钮组件，按钮降级为 markdown 链接）。`webBaseURL` 用于生成「完整报告」链接，由调用方传入（可空，为空则不渲染链接）。

- [ ] **Step 1: 写失败测试**

```go
// inspect/report_test.go
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
	if !strings.Contains(txt, "其余") { // 正常项折叠提示
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
	if !strings.Contains(js, "http://bot.local") { // 「完整报告」链接
		t.Errorf("card missing report link")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./inspect/ -run 'TestReport|TestRender' -v`
Expected: FAIL（未定义 Report）

- [ ] **Step 3: 写最小实现**

```go
// inspect/report.go
package inspect

import (
	"fmt"
	"strings"
	"time"

	"github.com/microyahoo/storage-bot/card"
)

type Report struct {
	Cluster    string    `json:"cluster"`
	StartedAt  time.Time `json:"started_at"`
	Duration   time.Duration `json:"duration"`
	Overall    Level     `json:"overall"`
	Findings   []Finding `json:"findings"`
	LLMSummary string    `json:"llm_summary,omitempty"`
}

// Finalize sets Overall from the findings. Call after collecting all findings.
func (r *Report) Finalize() { r.Overall = MaxLevel(r.Findings) }

func (r *Report) Counts() (ok, warn, crit int) {
	for _, f := range r.Findings {
		switch f.Level {
		case LevelOK:
			ok++
		case LevelWarn:
			warn++
		case LevelCritical:
			crit++
		}
	}
	return
}

// Abnormal returns findings at Warn or above (Critical first, then Warn).
func (r *Report) Abnormal() []Finding {
	var crit, warn []Finding
	for _, f := range r.Findings {
		switch f.Level {
		case LevelCritical:
			crit = append(crit, f)
		case LevelWarn:
			warn = append(warn, f)
		}
	}
	return append(crit, warn...)
}

func itemLabel(f Finding) string {
	if f.Node != "" {
		return f.Item + " · " + f.Node
	}
	return f.Item
}

func (r *Report) RenderText() string {
	var b strings.Builder
	ok, warn, crit := r.Counts()
	fmt.Fprintf(&b, "**集群巡检 · %s**\n总体：%s · 🔴%d 🟡%d 🟢%d · %s\n\n",
		r.Cluster, r.Overall.String(), crit, warn, ok, r.StartedAt.Format("2006-01-02 15:04"))
	ab := r.Abnormal()
	if len(ab) == 0 {
		b.WriteString("✅ 全部正常\n")
	} else {
		for _, f := range ab {
			fmt.Fprintf(&b, "%s `%s` — %s\n", f.Level.Emoji(), itemLabel(f), f.Summary)
			if f.Advice != "" {
				fmt.Fprintf(&b, "    建议：%s\n", f.Advice)
			}
		}
		if ok > 0 {
			fmt.Fprintf(&b, "\n🟢 其余 %d 项正常\n", ok)
		}
	}
	if r.LLMSummary != "" {
		fmt.Fprintf(&b, "\n🤖 %s\n", r.LLMSummary)
	}
	return b.String()
}

func themeFor(l Level) card.Theme {
	switch l {
	case LevelCritical:
		return card.ThemeRed
	case LevelWarn:
		return card.ThemeOrange
	default:
		return card.ThemeGreen
	}
}

// RenderCard builds the reviewed layout: colored header + stats + abnormal
// table (markdown) + collapsed-normal line + report link. webBaseURL may be ""
// to omit the link.
func (r *Report) RenderCard(webBaseURL string) *card.Card {
	ok, warn, crit := r.Counts()
	c := card.New(r.Overall.Emoji(), "集群巡检报告 · "+r.Cluster, themeFor(r.Overall)).
		Subtitle(fmt.Sprintf("总体：%s · %s", r.Overall.String(), r.StartedAt.Format("2006-01-02 15:04")))

	c.Body(fmt.Sprintf("🔴 严重 %d · 🟡 警告 %d · 🟢 正常 %d", crit, warn, ok))
	c.Divider()

	ab := r.Abnormal()
	if len(ab) == 0 {
		c.Body("✅ 全部正常")
	} else {
		var t strings.Builder
		t.WriteString("**级别 | 巡检项 | 结论**\n")
		for _, f := range ab {
			fmt.Fprintf(&t, "%s | `%s` | %s\n", f.Level.Emoji(), itemLabel(f), f.Summary)
		}
		c.Body(t.String())
		if ok > 0 {
			c.Body(fmt.Sprintf("🟢 其余 %d 项正常", ok))
		}
	}

	if r.LLMSummary != "" {
		c.Divider()
		c.Body("🤖 " + r.LLMSummary)
	}

	if webBaseURL != "" {
		c.Divider()
		c.Note(fmt.Sprintf("[📄 查看完整报告](%s/inspect/%s) · 耗时 %s",
			strings.TrimRight(webBaseURL, "/"), r.Cluster, r.Duration.Round(time.Second)))
	} else {
		c.Note(fmt.Sprintf("耗时 %s", r.Duration.Round(time.Second)))
	}
	return c
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./inspect/ -run 'TestReport|TestRender' -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add inspect/report.go inspect/report_test.go
git commit -m "feat(inspect): Report struct + text/card rendering

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 5: 历史报告留存（JSON 文件 Store）

**Files:**
- Create: `inspect/store.go`
- Test: `inspect/store_test.go`

`Store.Save` 用传入的时间戳命名（不调 `time.Now`，便于测试）。

- [ ] **Step 1: 写失败测试**

```go
// inspect/store_test.go
package inspect

import (
	"testing"
	"time"
)

func TestStoreSaveListLoadPrune(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, 2) // keep 2

	base := time.Date(2026, 6, 17, 3, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		r := &Report{Cluster: "c1", StartedAt: base.Add(time.Duration(i) * time.Hour)}
		r.Finalize()
		if err := s.Save(r); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	list, err := s.List("c1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("after prune keep=2, got %d entries", len(list))
	}

	// 最新的能 Load 回来
	r, err := s.Load("c1", list[0])
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if r.Cluster != "c1" {
		t.Errorf("loaded cluster = %q", r.Cluster)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./inspect/ -run TestStore -v`
Expected: FAIL（未定义 NewStore）

- [ ] **Step 3: 写最小实现**

```go
// inspect/store.go
package inspect

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type Store struct {
	dir  string
	keep int
}

func NewStore(dir string, keep int) *Store {
	if keep <= 0 {
		keep = 30
	}
	return &Store{dir: dir, keep: keep}
}

func (s *Store) clusterDir(cluster string) string {
	return filepath.Join(s.dir, cluster)
}

// Save writes the report as <dir>/<cluster>/<timestamp>.json then prunes old ones.
func (s *Store) Save(r *Report) error {
	cd := s.clusterDir(r.Cluster)
	if err := os.MkdirAll(cd, 0o755); err != nil {
		return fmt.Errorf("mkdir history: %w", err)
	}
	name := r.StartedAt.UTC().Format("20060102-150405") + ".json"
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cd, name), data, 0o644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return s.prune(r.Cluster)
}

// List returns report filenames for a cluster, newest first.
func (s *Store) List(cluster string) ([]string, error) {
	entries, err := os.ReadDir(s.clusterDir(cluster))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // 文件名按时间排序，逆序=最新在前
	return names, nil
}

func (s *Store) Load(cluster, name string) (*Report, error) {
	data, err := os.ReadFile(filepath.Join(s.clusterDir(cluster), name))
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("unmarshal report: %w", err)
	}
	return &r, nil
}

func (s *Store) prune(cluster string) error {
	names, err := s.List(cluster)
	if err != nil {
		return err
	}
	for _, old := range names[min(len(names), s.keep):] {
		_ = os.Remove(filepath.Join(s.clusterDir(cluster), old))
	}
	return nil
}
```

注：`min` 是 Go 1.21+ 内建，项目用 1.23，可直接用。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./inspect/ -run TestStore -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add inspect/store.go inspect/store_test.go
git commit -m "feat(inspect): JSON history store with prune

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 6: Inspector 采集层 + Registry

**Files:**
- Create: `inspect/ceph.go`, `inspect/hardware.go`, `inspect/registry.go`
- Test: `inspect/registry_test.go`

采集层是薄壳：取 raw → 调 Task 2/3 的纯解析函数。这里只对 Registry 做单测（采集本身依赖真实集群，不单测）。本任务展示全部 13 个 inspector 中的代表实现；其余同模式。

- [ ] **Step 1: 写失败测试**

```go
// inspect/registry_test.go
package inspect

import "testing"

func TestRegistryScopes(t *testing.T) {
	r := NewRegistry()
	cluster := r.ByScope(ClusterScope)
	node := r.ByScope(NodeScope)
	if len(cluster) == 0 || len(node) == 0 {
		t.Fatalf("registry empty: cluster=%d node=%d", len(cluster), len(node))
	}
	// ceph_health 必须是 ClusterScope，hw_memory 必须是 NodeScope
	names := func(in []Inspector) map[string]bool {
		m := map[string]bool{}
		for _, i := range in {
			m[i.Name()] = true
		}
		return m
	}
	if !names(cluster)["ceph_health"] {
		t.Errorf("ceph_health not in cluster scope")
	}
	if !names(node)["hw_memory"] {
		t.Errorf("hw_memory not in node scope")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./inspect/ -run TestRegistry -v`
Expected: FAIL（未定义 NewRegistry）

- [ ] **Step 3: 写最小实现**

```go
// inspect/registry.go
package inspect

type Registry struct {
	inspectors []Inspector
}

func NewRegistry() *Registry {
	r := &Registry{}
	// Ceph 集群级
	r.add(&cephHealth{}, &cephOSD{}, &cephMon{}, &cephPG{}, &cephCapacity{}, &cephSlowOps{}, &cephCrash{})
	// 硬件节点级
	r.add(&hwCPU{}, &hwMemory{}, &hwDiskSmart{}, &hwDiskUsage{}, &hwNIC{}, &hwBond{})
	return r
}

func (r *Registry) add(in ...Inspector) { r.inspectors = append(r.inspectors, in...) }

func (r *Registry) ByScope(s Scope) []Inspector {
	var out []Inspector
	for _, in := range r.inspectors {
		if in.Scope() == s {
			out = append(out, in)
		}
	}
	return out
}

func (r *Registry) All() []Inspector { return r.inspectors }
```

```go
// inspect/ceph.go
package inspect

type cephHealth struct{}

func (cephHealth) Name() string        { return "ceph_health" }
func (cephHealth) Description() string { return "Ceph 集群健康状态" }
func (cephHealth) Scope() Scope        { return ClusterScope }
func (cephHealth) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunCeph("health", "detail")
	if err != nil {
		return []Finding{{Item: "ceph_health", Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	return []Finding{parseCephHealth(raw)}, nil
}

type cephOSD struct{}

func (cephOSD) Name() string        { return "ceph_osd" }
func (cephOSD) Description() string { return "OSD up/in 状态" }
func (cephOSD) Scope() Scope        { return ClusterScope }
func (cephOSD) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunCeph("osd", "stat")
	if err != nil {
		return []Finding{{Item: "ceph_osd", Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	return []Finding{parseOSDStat(raw)}, nil
}

type cephCapacity struct{}

func (cephCapacity) Name() string        { return "ceph_capacity" }
func (cephCapacity) Description() string { return "集群容量使用率" }
func (cephCapacity) Scope() Scope        { return ClusterScope }
func (cephCapacity) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunCeph("df")
	if err != nil {
		return []Finding{{Item: "ceph_capacity", Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	return []Finding{parseCephDF(raw, ic.Thresholds.CapacityWarnPct, ic.Thresholds.CapacityCritPct)}, nil
}

// cephMon/cephPG/cephSlowOps/cephCrash 同模式：RunCeph 取 raw → 解析函数。
// 解析函数 parseMonStat/parsePGStat/parseSlowOps/parseCrashLs 按 Task 2 的风格
// 补充到 ceph_parse.go（各配套单测）。骨架如下：

type cephMon struct{}

func (cephMon) Name() string        { return "ceph_mon" }
func (cephMon) Description() string { return "Monitor quorum 状态" }
func (cephMon) Scope() Scope        { return ClusterScope }
func (cephMon) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunCeph("quorum_status")
	if err != nil {
		return []Finding{{Item: "ceph_mon", Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	return []Finding{parseMonQuorum(raw)}, nil
}

type cephPG struct{}

func (cephPG) Name() string        { return "ceph_pg" }
func (cephPG) Description() string { return "PG 状态" }
func (cephPG) Scope() Scope        { return ClusterScope }
func (cephPG) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunCeph("pg", "stat")
	if err != nil {
		return []Finding{{Item: "ceph_pg", Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	return []Finding{parsePGStat(raw)}, nil
}

type cephSlowOps struct{}

func (cephSlowOps) Name() string        { return "ceph_slow_ops" }
func (cephSlowOps) Description() string { return "慢请求" }
func (cephSlowOps) Scope() Scope        { return ClusterScope }
func (cephSlowOps) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunCeph("health", "detail")
	if err != nil {
		return []Finding{{Item: "ceph_slow_ops", Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	return []Finding{parseSlowOps(raw)}, nil
}

type cephCrash struct{}

func (cephCrash) Name() string        { return "ceph_crash" }
func (cephCrash) Description() string { return "未确认 crash" }
func (cephCrash) Scope() Scope        { return ClusterScope }
func (cephCrash) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunCeph("crash", "ls-new")
	if err != nil {
		return []Finding{{Item: "ceph_crash", Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	return []Finding{parseCrashLs(raw)}, nil
}
```

补充到 `inspect/ceph_parse.go` 的解析函数（含各自单测，与 Task 2 同风格）：

```go
// parseMonQuorum：quorum_status 是 JSON，含 "quorum" 数组与 "monmap.mons" 数组。
// quorum 数 < mons 总数的半数+1 → Critical；缺失/解析失败 → Unknown；否则 OK。
func parseMonQuorum(raw string) Finding {
	f := Finding{Item: "ceph_mon", Detail: raw}
	var qs struct {
		Quorum []int `json:"quorum"`
		Monmap struct {
			Mons []struct {
				Name string `json:"name"`
			} `json:"mons"`
		} `json:"monmap"`
	}
	if err := json.Unmarshal([]byte(raw), &qs); err != nil {
		f.Level, f.Summary = LevelUnknown, "无法解析 quorum_status"
		return f
	}
	total := len(qs.Monmap.Mons)
	inQuorum := len(qs.Quorum)
	if total == 0 {
		f.Level, f.Summary = LevelUnknown, "monmap 为空"
		return f
	}
	if inQuorum < total/2+1 {
		f.Level = LevelCritical
		f.Summary = fmt.Sprintf("quorum %d/%d，不足多数", inQuorum, total)
		f.Advice = "检查失联的 mon 进程与网络"
	} else {
		f.Level = LevelOK
		f.Summary = fmt.Sprintf("quorum %d/%d 正常", inQuorum, total)
	}
	return f
}

// parsePGStat：`pg stat` 输出含 "X pgs: ... active+clean" 等状态词。
func parsePGStat(raw string) Finding {
	f := Finding{Item: "ceph_pg", Detail: raw}
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "inactive"), strings.Contains(lower, "incomplete"), strings.Contains(lower, "stale"):
		f.Level, f.Summary = LevelCritical, "存在 inactive/incomplete/stale PG"
		f.Advice = "`ceph pg dump_stuck` 排查"
	case strings.Contains(lower, "degraded"), strings.Contains(lower, "undersized"):
		f.Level, f.Summary = LevelWarn, "存在 degraded/undersized PG"
	case strings.Contains(lower, "active+clean"):
		f.Level, f.Summary = LevelOK, "PG 全部 active+clean"
	default:
		f.Level, f.Summary = LevelUnknown, "无法判定 PG 状态"
	}
	return f
}

// parseSlowOps：health detail 含 "SLOW_OPS" 或 "slow ops" 即 Warn。
func parseSlowOps(raw string) Finding {
	f := Finding{Item: "ceph_slow_ops", Detail: raw}
	if strings.Contains(raw, "SLOW_OPS") || strings.Contains(strings.ToLower(raw), "slow ops") {
		f.Level, f.Summary = LevelWarn, "存在慢请求 slow ops"
		f.Advice = "`ceph daemon osd.N dump_ops_in_flight` 定位"
	} else {
		f.Level, f.Summary = LevelOK, "无慢请求"
	}
	return f
}

// parseCrashLs：`crash ls-new` 每行一条 crash id；非空表示有未确认 crash。
func parseCrashLs(raw string) Finding {
	f := Finding{Item: "ceph_crash", Detail: raw}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(raw), "\n") {
		if s := strings.TrimSpace(l); s != "" && !strings.HasPrefix(s, "ID") {
			lines = append(lines, s)
		}
	}
	if len(lines) == 0 {
		f.Level, f.Summary = LevelOK, "无未确认 crash"
	} else {
		f.Level = LevelWarn
		f.Summary = fmt.Sprintf("%d 条未确认 crash，最近：%s", len(lines), lines[len(lines)-1])
		f.Advice = "`ceph crash info <id>` 查看；确认后 `ceph crash archive-all`"
	}
	return f
}
```

> 上面这些解析函数需要 `encoding/json`，把它加进 `ceph_parse.go` 的 import。每个都要补一个表驱动单测（与 Task 2 同风格），断言 OK/Warn/Critical/Unknown 四种输入的分级。

```go
// inspect/hardware.go
package inspect

import (
	"fmt"
	"strconv"
	"strings"
)

type hwMemory struct{}

func (hwMemory) Name() string        { return "hw_memory" }
func (hwMemory) Description() string { return "内存使用率" }
func (hwMemory) Scope() Scope        { return NodeScope }
func (hwMemory) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunOnNode("free -b")
	if err != nil {
		return []Finding{{Item: "hw_memory", Node: ic.Node.Name, Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	f := parseMemory(raw, ic.Thresholds.MemWarnPct, ic.Thresholds.MemCritPct)
	f.Node = ic.Node.Name
	return []Finding{f}, nil
}

type hwCPU struct{}

func (hwCPU) Name() string        { return "hw_cpu" }
func (hwCPU) Description() string { return "CPU load" }
func (hwCPU) Scope() Scope        { return NodeScope }
func (hwCPU) Inspect(ic *InspectContext) ([]Finding, error) {
	// 一次取核数与 load：`nproc; uptime`
	raw, err := ic.RunOnNode("nproc; uptime")
	if err != nil {
		return []Finding{{Item: "hw_cpu", Node: ic.Node.Name, Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	cores := 0
	if first := strings.SplitN(strings.TrimSpace(raw), "\n", 2); len(first) > 0 {
		cores, _ = strconv.Atoi(strings.TrimSpace(first[0]))
	}
	f := parseLoad(raw, cores, ic.Thresholds.LoadWarnRatio)
	f.Node = ic.Node.Name
	return []Finding{f}, nil
}

type hwDiskUsage struct{}

func (hwDiskUsage) Name() string        { return "hw_disk_usage" }
func (hwDiskUsage) Description() string { return "文件系统使用率" }
func (hwDiskUsage) Scope() Scope        { return NodeScope }
func (hwDiskUsage) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunOnNode("df -B1 -x tmpfs -x devtmpfs")
	if err != nil {
		return []Finding{{Item: "hw_disk_usage", Node: ic.Node.Name, Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	fs := parseDiskUsage(raw, ic.Thresholds.FsWarnPct, ic.Thresholds.FsCritPct)
	if len(fs) == 0 {
		fs = []Finding{{Item: "hw_disk_usage", Level: LevelOK, Summary: "文件系统使用率正常"}}
	}
	for i := range fs {
		fs[i].Node = ic.Node.Name
	}
	return fs, nil
}

type hwDiskSmart struct{}

func (hwDiskSmart) Name() string        { return "hw_disk_smart" }
func (hwDiskSmart) Description() string { return "磁盘 SMART 寿命/自检" }
func (hwDiskSmart) Scope() Scope        { return NodeScope }
func (hwDiskSmart) Inspect(ic *InspectContext) ([]Finding, error) {
	// 枚举物理盘（type=disk），逐盘 smartctl。
	list, err := ic.RunOnNode("lsblk -dno NAME,TYPE | awk '$2==\"disk\"{print $1}'")
	if err != nil {
		return []Finding{{Item: "hw_disk_smart", Node: ic.Node.Name, Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	var out []Finding
	for _, name := range strings.Fields(list) {
		dev := "/dev/" + name
		raw, e := ic.RunOnNode("smartctl -A -H " + dev + " 2>&1")
		if e != nil {
			raw = e.Error()
		}
		f := parseSmart(dev, raw, ic.Thresholds.DiskLifeWarnPct, ic.Thresholds.DiskLifeCritPct)
		f.Node = ic.Node.Name
		out = append(out, f)
	}
	if len(out) == 0 {
		out = []Finding{{Item: "hw_disk_smart", Node: ic.Node.Name, Level: LevelUnknown, Summary: "未发现物理磁盘"}}
	}
	return out, nil
}

type hwNIC struct{}

func (hwNIC) Name() string        { return "hw_nic" }
func (hwNIC) Description() string { return "物理网卡 UP 状态" }
func (hwNIC) Scope() Scope        { return NodeScope }
func (hwNIC) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunOnNode("ip -br link show")
	if err != nil {
		return []Finding{{Item: "hw_nic", Node: ic.Node.Name, Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	f := parseNIC(raw)
	f.Node = ic.Node.Name
	return []Finding{f}, nil
}

type hwBond struct{}

func (hwBond) Name() string        { return "hw_bond" }
func (hwBond) Description() string { return "bond 链路状态" }
func (hwBond) Scope() Scope        { return NodeScope }
func (hwBond) Inspect(ic *InspectContext) ([]Finding, error) {
	cmd := "grep -H -E '^(Slave Interface|Link Failure Count|MII Status)' /proc/net/bonding/bond* 2>/dev/null || echo '(no bonds)'"
	raw, err := ic.RunOnNode(cmd)
	if err != nil {
		return []Finding{{Item: "hw_bond", Node: ic.Node.Name, Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	f := parseBond(raw)
	f.Node = ic.Node.Name
	return []Finding{f}, nil
}
```

补充到 `inspect/hardware_parse.go` 的 `parseNIC`/`parseBond`（含单测，与 Task 3 同风格）：

```go
// parseNIC：ip -br link show 每行 "name state ..."；排除 lo/cali/tun/ipvs/veth/docker
// 与 bond slave（名字带 @ 或 master）的从属口；物理口 state 非 UP/UNKNOWN → Warn。
func parseNIC(raw string) Finding {
	f := Finding{Item: "hw_nic", Detail: raw}
	skip := func(name string) bool {
		for _, p := range []string{"lo", "cali", "tun", "ipvs", "veth", "docker", "kube"} {
			if strings.HasPrefix(name, p) {
				return true
			}
		}
		return false
	}
	var down []string
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name, state := fields[0], fields[1]
		name = strings.SplitN(name, "@", 2)[0] // 去掉 "eth0@if3" 的从属标记
		if skip(name) {
			continue
		}
		if state != "UP" && state != "UNKNOWN" {
			down = append(down, name+"="+state)
		}
	}
	if len(down) > 0 {
		f.Level = LevelWarn
		f.Summary = "网卡未 UP：" + strings.Join(down, ", ")
	} else {
		f.Level = LevelOK
		f.Summary = "物理网卡状态正常"
	}
	return f
}

// parseBond：grep 输出 "/proc/net/bonding/bond0:Link Failure Count: 3" 等。
// 任一 Link Failure Count > 0 → Warn；任一 MII Status 非 up → Critical。
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
			if _, v, ok := strings.Cut(rest, ":"); ok {
				n, _ := strconv.Atoi(strings.TrimSpace(v))
				failTotal += n
			}
		case strings.HasPrefix(rest, "MII Status"):
			if _, v, ok := strings.Cut(rest, ":"); ok && strings.TrimSpace(v) != "up" {
				miiDown = true
			}
		}
	}
	switch {
	case miiDown:
		f.Level, f.Summary = LevelCritical, "存在 MII Status 非 up 的 bond"
		f.Advice = "检查物理链路与交换机端口"
	case failTotal > 0:
		f.Level, f.Summary = LevelWarn, fmt.Sprintf("bond 累计 Link Failure %d 次", failTotal)
	default:
		f.Level, f.Summary = LevelOK, "bond 链路正常"
	}
	return f
}
```

> `parseBond`/`parseNIC` 需要 `fmt`/`strconv`/`strings`，已在 `hardware_parse.go` import。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./inspect/ -v`（含本任务补充的所有解析函数单测）
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add inspect/ceph.go inspect/hardware.go inspect/registry.go inspect/ceph_parse.go inspect/hardware_parse.go inspect/*_test.go
git commit -m "feat(inspect): collectors + registry + mon/pg/slowops/crash/nic/bond parsers

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 7: cluster.Manager 暴露 KubeExecutor（供 Runner 复用连接）

**Files:**
- Modify: `cluster/manager.go`
- Test: `cluster/manager_test.go`

**为什么**：`Runner` 需要 `*executor.KubeExecutor`，但现有获取逻辑藏在 `Manager.kubeExecFn`（私有）和 `Handler.getKubeExecutor`（私有）里。让 `Runner` 依赖 `cluster.Manager`（而非 `bot.Handler`）可避免包依赖环（bot 依赖 inspect 不行——inspect 要被 bot 引用）。在 Manager 上加导出方法 + 缓存。

- [ ] **Step 1: 写失败测试**

```go
// cluster/manager_test.go
package cluster

import "testing"

func TestKubeExecutorCaches(t *testing.T) {
	calls := 0
	m := NewManager(map[string]*config.ClusterConfig{
		"c1": {Kubeconfig: "x", Namespace: "rook-ceph"},
	})
	// 用测试钩子替换真实连接，计数调用次数
	m.SetKubeExecFnForTest(func(cfg *config.ClusterConfig) (*executor.KubeExecutor, error) {
		calls++
		return &executor.KubeExecutor{}, nil
	})
	cfg, _ := m.Get("c1")
	if _, err := m.KubeExecutor("c1", cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := m.KubeExecutor("c1", cfg); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("kubeExecFn called %d times, want 1 (cached)", calls)
	}
}
```

需在测试文件顶部 import `"github.com/microyahoo/storage-bot/config"` 和 `"github.com/microyahoo/storage-bot/executor"`。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./cluster/ -run TestKubeExecutorCaches -v`
Expected: FAIL（未定义 KubeExecutor / SetKubeExecFnForTest）

- [ ] **Step 3: 写最小实现**

在 `cluster/manager.go` 的 `Manager` 结构加缓存字段（与现有 `nodeCache` 并列）：

```go
type Manager struct {
	mu         sync.RWMutex
	clusters   map[string]*config.ClusterConfig
	nodeCache  map[string][]config.SSHNode
	kubeCache  map[string]*executor.KubeExecutor // 新增
	kubeExecFn func(cfg *config.ClusterConfig) (*executor.KubeExecutor, error)
}
```

在 `NewManager` 里初始化 `kubeCache: make(map[string]*executor.KubeExecutor)`。在 `Reload` 里重置 `m.kubeCache = make(...)`（连接随配置失效）。新增方法：

```go
// KubeExecutor returns a cached KubeExecutor for the cluster, creating it on
// first use. Safe for concurrent callers.
func (m *Manager) KubeExecutor(name string, cfg *config.ClusterConfig) (*executor.KubeExecutor, error) {
	m.mu.RLock()
	if ke, ok := m.kubeCache[name]; ok {
		m.mu.RUnlock()
		return ke, nil
	}
	m.mu.RUnlock()

	ke, err := m.kubeExecFn(cfg)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.kubeCache[name] = ke
	m.mu.Unlock()
	return ke, nil
}

// SetKubeExecFnForTest swaps the executor factory (test seam).
func (m *Manager) SetKubeExecFnForTest(fn func(cfg *config.ClusterConfig) (*executor.KubeExecutor, error)) {
	m.kubeExecFn = fn
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./cluster/ -run TestKubeExecutorCaches -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add cluster/manager.go cluster/manager_test.go
git commit -m "feat(cluster): expose cached KubeExecutor for inspect Runner

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 8: config 新增 InspectConfig + 默认值 + 校验

**Files:**
- Modify: `config/config.go`
- Test: `config/config_inspect_test.go`

`Thresholds` 是巡检阈值，天然属于配置，**唯一定义在 `config` 包**（避免包依赖环：`config` 不依赖 `inspect`，而 `inspect` 已依赖 `config`）。Task 1 的 `InspectContext.Thresholds` 字段已写成 `config.Thresholds`，本任务在 config 包补上该类型定义即可，无需回头改 inspect 包。Task 2/3 解析函数签名用 `warnPct int` 等基本类型，不依赖该结构。

- [ ] **Step 1: 写失败测试**

```go
// config/config_inspect_test.go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTmpConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const baseCfg = `
feishu:
  app_id: a
  app_secret: b
llm:
  provider: claude
  api_key: k
clusters:
  c1:
    kubeconfig: /tmp/kc
`

func TestInspectDefaults(t *testing.T) {
	cfg, err := Load(writeTmpConfig(t, baseCfg+`
inspect:
  enabled: true
  schedule: "0 3 * * *"
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Inspect.Thresholds.CapacityWarnPct != 80 {
		t.Errorf("CapacityWarnPct default = %d, want 80", cfg.Inspect.Thresholds.CapacityWarnPct)
	}
	if cfg.Inspect.Thresholds.LoadWarnRatio != 2.0 {
		t.Errorf("LoadWarnRatio default = %v, want 2.0", cfg.Inspect.Thresholds.LoadWarnRatio)
	}
	if cfg.Inspect.NotifyMinLevel != "warn" {
		t.Errorf("NotifyMinLevel default = %q, want warn", cfg.Inspect.NotifyMinLevel)
	}
	if cfg.Inspect.HistoryKeep != 30 {
		t.Errorf("HistoryKeep default = %d, want 30", cfg.Inspect.HistoryKeep)
	}
}

func TestInspectBadSchedule(t *testing.T) {
	_, err := Load(writeTmpConfig(t, baseCfg+`
inspect:
  enabled: true
  schedule: "not a cron"
`))
	if err == nil {
		t.Fatal("expected error for invalid cron schedule")
	}
}

func TestInspectBadNotifyLevel(t *testing.T) {
	_, err := Load(writeTmpConfig(t, baseCfg+`
inspect:
  enabled: true
  schedule: "0 3 * * *"
  notify_min_level: panic
`))
	if err == nil {
		t.Fatal("expected error for invalid notify_min_level")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./config/ -run TestInspect -v`
Expected: FAIL（未定义 Inspect 字段）

- [ ] **Step 3: 写最小实现**

在 `config/config.go` 加类型并接进 `Config`：

```go
type Config struct {
	// ... 现有字段
	Inspect InspectConfig `yaml:"inspect"`
}

type InspectConfig struct {
	Enabled        bool       `yaml:"enabled"`
	Schedule       string     `yaml:"schedule"`
	Clusters       []string   `yaml:"clusters"`
	NotifyChat     string     `yaml:"notify_chat"`
	NotifyMinLevel string     `yaml:"notify_min_level"`
	LLMSummary     bool       `yaml:"llm_summary"`
	HistoryDir     string     `yaml:"history_dir"`
	HistoryKeep    int        `yaml:"history_keep"`
	Thresholds     Thresholds `yaml:"thresholds"`
}

type Thresholds struct {
	CapacityWarnPct int     `yaml:"capacity_warn_pct"`
	CapacityCritPct int     `yaml:"capacity_crit_pct"`
	MemWarnPct      int     `yaml:"mem_warn_pct"`
	MemCritPct      int     `yaml:"mem_crit_pct"`
	FsWarnPct       int     `yaml:"fs_warn_pct"`
	FsCritPct       int     `yaml:"fs_crit_pct"`
	DiskLifeWarnPct int     `yaml:"disk_life_warn_pct"`
	DiskLifeCritPct int     `yaml:"disk_life_crit_pct"`
	LoadWarnRatio   float64 `yaml:"load_warn_ratio"`
}
```

在 `Load` 函数 return 前加默认值与校验（cron 校验用 robfig/cron 的 parser，Task 9 会引入依赖；此处先用标准解析器接口）：

```go
	// inspect defaults + validation
	if cfg.Inspect.Enabled {
		applyInspectDefaults(&cfg.Inspect)
		if err := validateInspect(&cfg.Inspect); err != nil {
			return nil, err
		}
	}
```

新增函数（放 config.go 末尾）：

```go
func applyInspectDefaults(c *InspectConfig) {
	t := &c.Thresholds
	if t.CapacityWarnPct == 0 {
		t.CapacityWarnPct = 80
	}
	if t.CapacityCritPct == 0 {
		t.CapacityCritPct = 90
	}
	if t.MemWarnPct == 0 {
		t.MemWarnPct = 90
	}
	if t.MemCritPct == 0 {
		t.MemCritPct = 95
	}
	if t.FsWarnPct == 0 {
		t.FsWarnPct = 85
	}
	if t.FsCritPct == 0 {
		t.FsCritPct = 90
	}
	if t.DiskLifeWarnPct == 0 {
		t.DiskLifeWarnPct = 80
	}
	if t.DiskLifeCritPct == 0 {
		t.DiskLifeCritPct = 90
	}
	if t.LoadWarnRatio == 0 {
		t.LoadWarnRatio = 2.0
	}
	if c.NotifyMinLevel == "" {
		c.NotifyMinLevel = "warn"
	}
	if c.HistoryDir == "" {
		c.HistoryDir = "./inspect-reports"
	}
	if c.HistoryKeep == 0 {
		c.HistoryKeep = 30
	}
}

func validateInspect(c *InspectConfig) error {
	if c.Schedule == "" {
		return fmt.Errorf("inspect.schedule is required when inspect.enabled")
	}
	// 标准 5 字段 cron 校验：用 robfig/cron 的 standard parser
	if _, err := cron.ParseStandard(c.Schedule); err != nil {
		return fmt.Errorf("inspect.schedule invalid cron %q: %w", c.Schedule, err)
	}
	switch c.NotifyMinLevel {
	case "warn", "critical":
	default:
		return fmt.Errorf("inspect.notify_min_level must be warn or critical, got %q", c.NotifyMinLevel)
	}
	return nil
}
```

import 增加 `"github.com/robfig/cron/v3"` 别名 `cron`（go.mod 在 Task 9 添加；本任务先 `go get`）：

```bash
go get github.com/robfig/cron/v3@v3.0.1
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./config/ -run TestInspect -v && go test ./inspect/ -v`
Expected: PASS（inspect 包因 Thresholds 迁移到 config 后仍编译通过）

- [ ] **Step 5: 提交**

```bash
git add config/config.go config/config_inspect_test.go inspect/inspector.go go.mod go.sum
git commit -m "feat(config): InspectConfig + Thresholds with defaults and validation

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 9: Runner — 编排所有 inspector

**Files:**
- Create: `inspect/runner.go`
- Test: `inspect/runner_test.go`

`Runner` 依赖一个最小接口而非具体 `cluster.Manager`，便于测试注入 fake。

- [ ] **Step 1: 写失败测试**

```go
// inspect/runner_test.go
package inspect

import (
	"context"
	"testing"
	"time"

	"github.com/microyahoo/storage-bot/config"
)

// fakeInspector 不触网，直接返回预置 finding。
type fakeInspector struct {
	name  string
	scope Scope
	out   []Finding
}

func (f fakeInspector) Name() string        { return f.name }
func (f fakeInspector) Description() string  { return f.name }
func (f fakeInspector) Scope() Scope         { return f.scope }
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

	// ClusterScope 跑 1 次 → 1 finding；NodeScope × 2 节点 → 2 findings
	if len(rep.Findings) != 3 {
		t.Fatalf("findings = %d, want 3", len(rep.Findings))
	}
	if rep.Overall != LevelCritical {
		t.Errorf("overall = %v, want Critical", rep.Overall)
	}
	if rep.StartedAt != now {
		t.Errorf("StartedAt not propagated")
	}
	// NodeScope 的 finding 必须带节点名
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
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./inspect/ -run TestRunnerAggregates -v`
Expected: FAIL（未定义 Runner）

- [ ] **Step 3: 写最小实现**

```go
// inspect/runner.go
package inspect

import (
	"context"
	"sync"
	"time"

	"github.com/microyahoo/storage-bot/analyzer"
	"github.com/microyahoo/storage-bot/config"
	"github.com/microyahoo/storage-bot/executor"
)

// ClusterProvider is the slice of cluster.Manager that Runner needs.
type ClusterProvider interface {
	FindByPrefix(input string) (string, *config.ClusterConfig, error)
	KubeExecutor(name string, cfg *config.ClusterConfig) (*executor.KubeExecutor, error)
	ResolveSSHNodes(ctx context.Context, name string, cfg *config.ClusterConfig) ([]config.SSHNode, error)
}

type Runner struct {
	registry   *Registry
	clusters   ClusterProvider
	sshExec    *executor.SSHExecutor
	analyzer   *analyzer.Analyzer // 可空
	thresholds config.Thresholds
	llmSummary bool
	store      *Store // 可空
	maxNodePar int    // 节点并发上限，0 → 8
}

func NewRunner(reg *Registry, clusters ClusterProvider, ssh *executor.SSHExecutor,
	az *analyzer.Analyzer, thresholds config.Thresholds, llmSummary bool, store *Store) *Runner {
	return &Runner{
		registry: reg, clusters: clusters, sshExec: ssh, analyzer: az,
		thresholds: thresholds, llmSummary: llmSummary, store: store, maxNodePar: 8,
	}
}

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
	start := time.Now()
	rep := r.runWith(ctx, name, ke, cfg.GatewayNode, nodes, r.thresholds, start)
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

// runWith is the testable core: no network resolution, time injected.
func (r *Runner) runWith(ctx context.Context, name string, ke *executor.KubeExecutor,
	gateway *config.SSHNode, nodes []config.SSHNode, th config.Thresholds, start time.Time) *Report {
	rep := &Report{Cluster: name, StartedAt: start}

	// ClusterScope：串行跑一次
	for _, in := range r.registry.ByScope(ClusterScope) {
		ic := &InspectContext{Ctx: ctx, ClusterName: name, KubeExec: ke, SSHExec: r.sshExec, Thresholds: th}
		fs, err := in.Inspect(ic)
		if err == nil {
			rep.Findings = append(rep.Findings, fs...)
		}
	}

	// NodeScope：每节点并发，带上限
	nodeInspectors := r.registry.ByScope(NodeScope)
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, max(1, r.maxNodePar))
	)
	for _, node := range nodes {
		wg.Add(1)
		go func(node config.SSHNode) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var local []Finding
			for _, in := range nodeInspectors {
				ic := &InspectContext{Ctx: ctx, ClusterName: name, Node: node,
					Gateway: gateway, KubeExec: ke, SSHExec: r.sshExec, Thresholds: th}
				if fs, err := in.Inspect(ic); err == nil {
					local = append(local, fs...)
				}
			}
			mu.Lock()
			rep.Findings = append(rep.Findings, local...)
			mu.Unlock()
		}(node)
	}
	wg.Wait()

	rep.Finalize()
	return rep
}

func (r *Runner) summarize(ctx context.Context, rep *Report) (string, error) {
	var b []byte
	for _, f := range rep.Abnormal() {
		b = append(b, []byte(f.Level.String()+" "+itemLabel(f)+": "+f.Summary+"\n")...)
	}
	if len(b) == 0 {
		return "", nil
	}
	return r.analyzer.Analyze(ctx, rep.Cluster, string(b))
}
```

注：`max` 是 Go 1.21+ 内建。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./inspect/ -run TestRunnerAggregates -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add inspect/runner.go inspect/runner_test.go
git commit -m "feat(inspect): Runner orchestrates inspectors with per-node concurrency

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 10: Scheduler — cron 调度 + 热重载 + 主动推送

**Files:**
- Create: `inspect/scheduler.go`
- Test: `inspect/scheduler_test.go`

**Notifier 接口**：调度器发现问题时需主动发飞书卡片。定义最小 `Notifier` 接口，真实实现在 bot 包（Task 11）。

- [ ] **Step 1: 写失败测试**

```go
// inspect/scheduler_test.go
package inspect

import (
	"context"
	"testing"
)

func TestShouldNotify(t *testing.T) {
	// notify_min_level=warn：Warn/Critical 推送，OK 不推
	if !shouldNotify(LevelWarn, "warn") || !shouldNotify(LevelCritical, "warn") {
		t.Error("warn level should notify on warn/critical")
	}
	if shouldNotify(LevelOK, "warn") {
		t.Error("OK should not notify")
	}
	// notify_min_level=critical：仅 Critical
	if shouldNotify(LevelWarn, "critical") {
		t.Error("warn should not notify when min=critical")
	}
	if !shouldNotify(LevelCritical, "critical") {
		t.Error("critical should notify when min=critical")
	}
}

func TestSchedulerTargetClusters(t *testing.T) {
	// Clusters 为空 → 用 allClusters；非空 → 原样返回
	got := targetClusters(nil, []string{"a", "b"})
	if len(got) != 2 {
		t.Errorf("empty config should fall back to all, got %v", got)
	}
	got = targetClusters([]string{"a"}, []string{"a", "b"})
	if len(got) != 1 || got[0] != "a" {
		t.Errorf("explicit list should win, got %v", got)
	}
}

// 确保 Scheduler 满足接口、能构造（不真正启动 cron）。
func TestSchedulerConstructs(t *testing.T) {
	s := NewScheduler(nil, config.InspectConfig{Schedule: "0 3 * * *"}, nil, nil, "")
	if s == nil {
		t.Fatal("nil scheduler")
	}
	_ = context.Background()
}
```

测试文件 import `"github.com/microyahoo/storage-bot/config"`。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./inspect/ -run 'TestShouldNotify|TestSchedulerTargetClusters|TestSchedulerConstructs' -v`
Expected: FAIL（未定义）

- [ ] **Step 3: 写最小实现**

```go
// inspect/scheduler.go
package inspect

import (
	"context"
	"log/slog"
	"sync"

	"github.com/microyahoo/storage-bot/config"
	"github.com/robfig/cron/v3"
)

// Notifier sends a finished report somewhere (e.g. a Feishu chat). chatID may
// be empty if the scheduler is configured without a notify target.
type Notifier interface {
	NotifyReport(ctx context.Context, chatID string, rep *Report) error
}

// ClusterLister is the slice of cluster.Manager the scheduler needs to expand
// "all clusters".
type ClusterLister interface {
	List() []string
}

type Scheduler struct {
	runner   *Runner
	cfg      config.InspectConfig
	lister   ClusterLister
	notifier Notifier
	webBase  string

	mu      sync.Mutex
	cron    *cron.Cron
	entryID cron.EntryID
}

func NewScheduler(runner *Runner, cfg config.InspectConfig, lister ClusterLister, notifier Notifier, webBase string) *Scheduler {
	return &Scheduler{runner: runner, cfg: cfg, lister: lister, notifier: notifier, webBase: webBase}
}

func shouldNotify(overall Level, minLevel string) bool {
	switch minLevel {
	case "critical":
		return overall == LevelCritical
	default: // "warn"
		return overall == LevelWarn || overall == LevelCritical
	}
}

func targetClusters(configured, all []string) []string {
	if len(configured) > 0 {
		return configured
	}
	return all
}

// Start registers the cron job and blocks until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	s.install(s.cfg)
	<-ctx.Done()
	s.mu.Lock()
	if s.cron != nil {
		s.cron.Stop()
	}
	s.mu.Unlock()
}

// Reload rebuilds the cron entry from a new config (called on hot-reload).
func (s *Scheduler) Reload(cfg config.InspectConfig) {
	s.cfg = cfg
	s.install(cfg)
}

func (s *Scheduler) install(cfg config.InspectConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		s.cron.Stop()
	}
	if !cfg.Enabled {
		s.cron = nil
		return
	}
	c := cron.New()
	id, err := c.AddFunc(cfg.Schedule, func() { s.tick(context.Background()) })
	if err != nil {
		slog.Error("inspect scheduler: bad schedule", "schedule", cfg.Schedule, "error", err)
		return
	}
	s.entryID = id
	s.cron = c
	c.Start()
	slog.Info("inspect scheduler installed", "schedule", cfg.Schedule)
}

// tick runs inspection for all target clusters, one report (and one card) each.
func (s *Scheduler) tick(ctx context.Context) {
	clusters := targetClusters(s.cfg.Clusters, s.lister.List())
	for _, name := range clusters {
		rep, err := s.runner.Run(ctx, name)
		if err != nil {
			slog.Error("inspect run failed", "cluster", name, "error", err)
			continue
		}
		if s.notifier != nil && s.cfg.NotifyChat != "" && shouldNotify(rep.Overall, s.cfg.NotifyMinLevel) {
			if err := s.notifier.NotifyReport(ctx, s.cfg.NotifyChat, rep); err != nil {
				slog.Error("inspect notify failed", "cluster", name, "error", err)
			}
		}
	}
}

// RunOnce runs a single cluster on demand (chat/web/API reuse this).
func (s *Scheduler) RunOnce(ctx context.Context, cluster string) (*Report, error) {
	return s.runner.Run(ctx, cluster)
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./inspect/ -run 'TestShouldNotify|TestSchedulerTargetClusters|TestSchedulerConstructs' -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add inspect/scheduler.go inspect/scheduler_test.go
git commit -m "feat(inspect): cron Scheduler with hot-reload and notify gating

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 11: bot 集成 — Notifier 实现 + 聊天巡检意图

**Files:**
- Modify: `intent/parser.go`（加 `ActionInspect` + 识别触发词）
- Modify: `bot/handler.go`（加 `handleInspect` + `NotifyReport`）
- Test: `intent/parser_inspect_test.go`、`bot/handler_inspect_test.go`

- [ ] **Step 1: 写失败测试**

```go
// intent/parser_inspect_test.go
package intent

import "testing"

func TestParseInspectIntent(t *testing.T) {
	clusters := []string{"prod-ceph-01"}
	for _, msg := range []string{"巡检 prod-ceph-01", "检查一下 prod-ceph-01 集群", "体检 prod-ceph-01"} {
		a := ParseWithAll(msg, clusters, nil, nil)
		if a.Type != ActionInspect {
			t.Errorf("%q → %v, want ActionInspect", msg, a.Type)
		}
		if a.ClusterName != "prod-ceph-01" {
			t.Errorf("%q → cluster %q", msg, a.ClusterName)
		}
	}
}

func TestParseInspectAll(t *testing.T) {
	a := ParseWithAll("巡检所有集群", []string{"a", "b"}, nil, nil)
	if a.Type != ActionInspect {
		t.Fatalf("want ActionInspect, got %v", a.Type)
	}
	if a.ClusterName != "" {
		t.Errorf("巡检所有集群 should leave ClusterName empty (means all), got %q", a.ClusterName)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./intent/ -run TestParseInspect -v`
Expected: FAIL（未定义 ActionInspect）

- [ ] **Step 3: 写最小实现**

在 `intent/parser.go` 的 `ActionType` 常量块末尾加：

```go
	ActionInspect // run cluster inspection
```

在 `ActionType.String()` 的 switch 加：

```go
	case ActionInspect:
		return "inspect"
```

在 `ParseWithAll`（主解析函数）的早期分支里，**在 health 等技能匹配之前**加入巡检识别（巡检触发词优先级高于普通技能，避免 "检查" 落到别的分支）。复用现有的 `extractClusterName(lower, knownClusters) string` 辅助函数（已在 parser.go 定义，行 276）：

```go
	// 巡检意图：触发词 巡检/检查/体检（+ 可选集群名 或 "所有集群"）
	if strings.Contains(lower, "巡检") || strings.Contains(lower, "体检") ||
		(strings.Contains(lower, "检查") && strings.Contains(lower, "集群")) {
		a := Action{Type: ActionInspect, RawMessage: message}
		if !strings.Contains(lower, "所有") && !strings.Contains(lower, "全部") {
			a.ClusterName = extractClusterName(lower, knownClusters)
		}
		return a
	}
```

在 `bot/handler.go`：先在 `HandleMessage` 的 action 分发 switch（约 174-192 行）加一条 `case intent.ActionInspect:`，再实现方法。**依赖注入**：`Handler` 需要持有 `*inspect.Scheduler`（或 `*inspect.Runner`）。在 `Handler` 结构加字段 `inspectRunner *inspect.Runner` 与 `webBase string`，并加 option `WithInspect(r *inspect.Runner, webBase string)`（仿现有 `WithAnalyzer` 等 option 风格）。

```go
// bot/handler.go —— 分发
	case intent.ActionInspect:
		reply, err = h.handleInspect(ctx, action)
```

```go
// handleInspect 运行巡检并以卡片回复。ClusterName 为空表示「所有集群」。
func (h *Handler) handleInspect(ctx context.Context, action intent.Action) (string, error) {
	if h.inspectRunner == nil {
		return "巡检功能未启用（config inspect.enabled）", nil
	}
	var targets []string
	if action.ClusterName != "" {
		targets = []string{action.ClusterName}
	} else {
		targets = h.clusterMgr.List()
	}
	// 多集群：逐个跑、各回一张卡片（返回空串，卡片走 replyCard 直接发）
	for _, name := range targets {
		rep, err := h.inspectRunner.Run(ctx, name)
		if err != nil {
			// 单集群失败不阻断其余
			continue
		}
		_ = h.replyCardToChat(ctx, action, rep.RenderCard(h.webBase))
	}
	return "", nil // 卡片已直接发送
}
```

> `handleInspect` 走「直接发卡片」而非返回文本，因为巡检结果用卡片更合适，且多集群要发多张。需要一个能在「回复当前消息」语境下发卡片的辅助。现有 `replyCard(ctx, messageID, *card.Card)` 需要 messageID——把它从 `HandleMessage` 透传进 action 处理链，或在 `handleInspect` 内用 `action` 里已带的消息上下文。实现时复用 `HandleMessage` 末尾构造卡片+`replyCard` 的同一路径：最简做法是让 `handleInspect` 返回 `rep.RenderText()` 文本（单集群时），多集群时返回汇总文本——**与现有 reply string 流程一致，零侵入**。

**采用零侵入版**（推荐，替换上面的 handleInspect）：

```go
func (h *Handler) handleInspect(ctx context.Context, action intent.Action) (string, error) {
	if h.inspectRunner == nil {
		return "巡检功能未启用（设置 config inspect.enabled: true）", nil
	}
	var targets []string
	if action.ClusterName != "" {
		targets = []string{action.ClusterName}
	} else {
		targets = h.clusterMgr.List()
	}
	var b strings.Builder
	for _, name := range targets {
		rep, err := h.inspectRunner.Run(ctx, name)
		if err != nil {
			fmt.Fprintf(&b, "❌ %s 巡检失败：%v\n\n", name, err)
			continue
		}
		b.WriteString(rep.RenderText())
		b.WriteString("\n")
	}
	return b.String(), nil
}
```

并在 `cardForAction`（约 717 行）加 case，给巡检结果一个主题卡片：

```go
	case intent.ActionInspect:
		return card.New("🔍", titleWithCluster("集群巡检", action.ClusterName), card.ThemeTurquoise).Body(body)
```

**Notifier 实现**（供 Scheduler 主动推送）：在 `bot/handler.go` 加方法，满足 `inspect.Notifier`：

```go
// NotifyReport sends a report card to a specific chat (used by the scheduler).
func (h *Handler) NotifyReport(ctx context.Context, chatID string, rep *inspect.Report) error {
	c := rep.RenderCard(h.webBase)
	js, err := c.JSON()
	if err != nil {
		return err
	}
	_, err = h.feishu.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).MsgType("interactive").Content(js).Build()).
		Build())
	return err
}
```

> `h.feishu` 字段名以 handler 实际持有的 lark client 字段为准（`grep -n "lark.Client" bot/handler.go`）。`replyCard` 已有同款发送代码，照搬其 client 调用。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./intent/ -run TestParseInspect -v && go build ./...`
Expected: PASS + 编译通过

- [ ] **Step 5: 提交**

```bash
git add intent/parser.go intent/parser_inspect_test.go bot/handler.go
git commit -m "feat(bot): chat inspect intent + scheduler Notifier

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 12: web — 巡检页 + HTTP API

**Files:**
- Modify: `web/server.go`（加路由）
- Create: `web/inspect.go`（handler）、`web/templates/inspect.html`
- Test: `web/inspect_test.go`

`Server` 需要持有 `*inspect.Runner` 与 `*inspect.Store`。在 `NewServer` 增加参数或加 setter `SetInspect(runner *inspect.Runner, store *inspect.Store)`（用 setter 避免改动现有 main.go 调用签名）。

- [ ] **Step 1: 写失败测试**

```go
// web/inspect_test.go
package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInspectAPIRequiresCluster(t *testing.T) {
	s := &Server{} // runner 为 nil
	req := httptest.NewRequest(http.MethodPost, "/api/inspect/", nil)
	rec := httptest.NewRecorder()
	s.handleInspectAPI(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing cluster → %d, want 400", rec.Code)
	}
}

func TestInspectAPIDisabled(t *testing.T) {
	s := &Server{} // inspectRunner nil → 功能未启用
	req := httptest.NewRequest(http.MethodPost, "/api/inspect/c1", nil)
	rec := httptest.NewRecorder()
	s.handleInspectAPI(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("disabled → %d, want 503", rec.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"] == "" {
		t.Errorf("expected error message in JSON body")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./web/ -run TestInspectAPI -v`
Expected: FAIL（未定义 handleInspectAPI）

- [ ] **Step 3: 写最小实现**

在 `web/server.go` 的 `Server` 结构加字段：

```go
	inspectRunner *inspect.Runner
	inspectStore  *inspect.Store
```

加 setter 与路由（在 `Start` 的 mux 注册区）：

```go
func (s *Server) SetInspect(r *inspect.Runner, store *inspect.Store) {
	s.inspectRunner = r
	s.inspectStore = store
}
```

```go
	mux.HandleFunc("/inspect/", s.basicAuth(s.handleInspectPage))   // 浏览器页（含历史）
	mux.HandleFunc("/api/inspect/", s.basicAuth(s.handleInspectAPI)) // POST 触发，GET 历史
```

新建 `web/inspect.go`：

```go
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// POST /api/inspect/{cluster}      → 立即巡检，返回 Report JSON
// GET  /api/inspect/{cluster}      → 返回历史文件名列表
func (s *Server) handleInspectAPI(w http.ResponseWriter, r *http.Request) {
	cluster := strings.TrimPrefix(r.URL.Path, "/api/inspect/")
	cluster = strings.Trim(cluster, "/")
	if cluster == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cluster name required"})
		return
	}
	if s.inspectRunner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "inspect not enabled"})
		return
	}
	switch r.Method {
	case http.MethodPost:
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		rep, err := s.inspectRunner.Run(ctx, cluster)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rep)
	case http.MethodGet:
		if s.inspectStore == nil {
			writeJSON(w, http.StatusOK, []string{})
			return
		}
		list, err := s.inspectStore.List(cluster)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, list)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleInspectPage renders a simple page: cluster list + run button + history.
func (s *Server) handleInspectPage(w http.ResponseWriter, r *http.Request) {
	tpl := s.templates["inspect.html"]
	if tpl == nil {
		http.Error(w, "template missing", http.StatusInternalServerError)
		return
	}
	cluster := strings.Trim(strings.TrimPrefix(r.URL.Path, "/inspect/"), "/")
	data := map[string]any{"Cluster": cluster}
	if cluster != "" && s.inspectStore != nil {
		if list, err := s.inspectStore.List(cluster); err == nil {
			data["History"] = list
		}
	}
	if err := tpl.ExecuteTemplate(w, "layout.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
```

新建 `web/templates/inspect.html`（仿现有页面的 `{{define "content"}}` 风格；参照 `web/templates/health.html` 的结构写表格与按钮）：

```html
{{define "content"}}
<h1>集群巡检</h1>
<form method="post" action="/api/inspect/{{.Cluster}}">
  <button type="submit">立即巡检 {{.Cluster}}</button>
</form>
<h2>历史报告</h2>
<ul>
  {{range .History}}<li>{{.}}</li>{{else}}<li>暂无历史</li>{{end}}
</ul>
{{end}}
```

并把 `"inspect.html"` 加入 `NewServer` 里的 `pages` 列表。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./web/ -run TestInspectAPI -v && go build ./...`
Expected: PASS + 编译通过

- [ ] **Step 5: 提交**

```bash
git add web/server.go web/inspect.go web/templates/inspect.html web/inspect_test.go
git commit -m "feat(web): inspect page + HTTP API (POST run, GET history)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 13: main.go 接线 + 配置样例 + 全量验证

**Files:**
- Modify: `main.go`
- Modify: `config.yaml.example`
- Modify: `README.md`（可选：加巡检章节）

- [ ] **Step 1: main.go 接线（无独立单测，靠编译 + 全量 test 验证）**

在 `main.go` 构造完 `clusterMgr` / `sshExec` / `az` / `handler` 之后，加：

```go
	// 集群巡检
	var inspectScheduler *inspect.Scheduler
	if cfg.Inspect.Enabled {
		inspectReg := inspect.NewRegistry()
		inspectStore := inspect.NewStore(cfg.Inspect.HistoryDir, cfg.Inspect.HistoryKeep)
		inspectRunner := inspect.NewRunner(inspectReg, clusterMgr, sshExec, az,
			cfg.Inspect.Thresholds, cfg.Inspect.LLMSummary, inspectStore)
		handler.WithInspectRunner(inspectRunner, cfg.Web.Listen) // 注入给 chat 入口

		webBase := "" // 若 web 启用，可用其外部地址；留空则卡片不带链接
		inspectScheduler = inspect.NewScheduler(inspectRunner, cfg.Inspect, clusterMgr, handler, webBase)
		go inspectScheduler.Start(ctx)
		slog.Info("cluster inspection enabled", "schedule", cfg.Inspect.Schedule)

		// web 注入
		// （在下方 web 启动处调用 webSrv.SetInspect(inspectRunner, inspectStore)）
	}
```

> `handler.WithInspectRunner(...)` 是 Task 11 加的注入方法（option 风格的 setter；若 `WithInspect` 已用作 functional option，则改名为 setter `SetInspectRunner`，保持与 Task 11 的字段名一致）。`handler` 满足 `inspect.Notifier`（Task 11 的 `NotifyReport`）。`clusterMgr` 满足 `inspect.ClusterProvider`（Task 7/9：有 `FindByPrefix`/`KubeExecutor`/`ResolveSSHNodes`）与 `inspect.ClusterLister`（有 `List`）。

在 `watcher.OnReload` 回调里加：

```go
		if inspectScheduler != nil {
			inspectScheduler.Reload(newCfg.Inspect)
		}
```

在 web 启动块（`if cfg.Web.Listen != ""`）的 `NewServer` 之后、`Start` 之前加：

```go
		if cfg.Inspect.Enabled {
			webSrv.SetInspect(inspectRunner, inspectStore)
		}
```

> 注意作用域：`inspectRunner`/`inspectStore` 在 `if cfg.Inspect.Enabled` 块内声明，web 块要用需提到外层作用域。实现时把 `var inspectRunner *inspect.Runner; var inspectStore *inspect.Store` 提到两个 if 块之前声明。

- [ ] **Step 2: 配置样例**

在 `config.yaml.example` 末尾加：

```yaml
# 集群巡检（可选）。enabled: false 时整套巡检不启动。
inspect:
  enabled: false
  schedule: "0 3 * * *"        # 标准 cron，每天 03:00
  clusters: []                  # 空 = 全部集群；或 ["prod-ceph-01"]
  notify_chat: ""               # 飞书 chat_id；有发现才推送
  notify_min_level: warn        # warn | critical
  llm_summary: false            # 是否调 LLM 生成总结
  history_dir: ./inspect-reports
  history_keep: 30
  thresholds:                   # 留空用默认值
    capacity_warn_pct: 80
    capacity_crit_pct: 90
    mem_warn_pct: 90
    mem_crit_pct: 95
    fs_warn_pct: 85
    fs_crit_pct: 90
    disk_life_warn_pct: 80
    disk_life_crit_pct: 90
    load_warn_ratio: 2.0
```

- [ ] **Step 3: 全量编译与测试**

Run:
```bash
go build ./... && go test ./...
```
Expected: 全部 PASS，无编译错误。

- [ ] **Step 4: 手动冒烟（可选，需真实集群）**

```bash
# 在 config.yaml 设 inspect.enabled: true、给一个真实集群
go run main.go -config config.yaml
# 飞书发「巡检 <集群名>」应收到巡检卡片
# curl -u user:pass -XPOST http://127.0.0.1:8080/api/inspect/<集群> 应返回 JSON
```

- [ ] **Step 5: 提交**

```bash
git add main.go config.yaml.example README.md
git commit -m "feat: wire cluster inspection into main (scheduler/chat/web)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 完成标准

- `go build ./...` 与 `go test ./...` 全绿。
- 飞书发「巡检 <集群>」「巡检所有集群」返回巡检卡片。
- `inspect.enabled: true` + 配置 cron 后，定时巡检在发现 ≥ notify_min_level 时推送到 notify_chat。
- `POST /api/inspect/{cluster}` 返回 Report JSON；`GET` 返回历史列表；Web 页可手动触发。
- 历史报告落盘到 `history_dir`，每集群保留 `history_keep` 份。
- 新增巡检项只需实现 `Inspector` 接口 + `Registry.add` 一行。
