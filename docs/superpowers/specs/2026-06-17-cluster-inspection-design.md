# 集群巡检系统设计

- 日期：2026-06-17
- 状态：已评审，待实现
- 模块：新增 `inspect/` 包

## 1. 背景与目标

storage-bot 目前通过 `Skill` 接口提供按需的运维查询能力（osd_status、nic_info、bond_status 等），由飞书聊天的意图解析触发，返回给人看的文本。

新增需求：**集群巡检**——周期性、自动地检查每套集群的健康状况与每个节点的硬件状态，产出结构化的巡检报告，发现问题主动告警。

设计目标：

1. **通用、可扩展**：巡检项以接口形式定义，新增一个巡检项（或未来新的存储类型）只需实现接口并注册一行，不改动编排/调度/渲染逻辑。
2. **集群状态巡检**：对 Ceph 集群查询关键信息判断是否合理（健康、OSD、Mon、PG、容量、慢请求、crash）。
3. **节点硬件巡检**：CPU、内存、网卡、bond、磁盘使用率、磁盘寿命（SMART）。
4. **可配置触发**：定时任务（cron）为主，另支持聊天手动触发、Web 页面触发、HTTP API。
5. **结果呈现**：飞书卡片推送 + 历史报告留存 + 可选 LLM 总结。

## 2. 架构概览

新增独立的 `inspect` 包，与现有 `skill` 包解耦，各自演进。复用现有执行器（`executor.KubeExecutor` / `executor.SSHExecutor`）作为采集底座，复用 `cluster.Manager` 做集群/节点解析，复用 `analyzer` 做 LLM 总结，复用 `card/` 构造飞书卡片。

```
inspect/
  inspector.go    # 核心接口 Inspector / 结果类型 Finding / Level / Scope / InspectContext
  registry.go     # InspectorRegistry，类比 skill.Registry
  runner.go       # 编排：跑一组 inspector，聚合成 Report（所有入口收口于此）
  scheduler.go    # cron 调度，后台 goroutine
  ceph.go         # Ceph 集群级 inspector（走 KubeExec.RunCephCommand）
  hardware.go     # 硬件节点级 inspector（走 SSHExec）
  report.go       # Report 结构 + 文本/飞书卡片渲染
  store.go        # 历史报告留存（JSON 文件）
```

**数据源可替换**：硬件采集当前为 SSH 命令，但判定逻辑与采集分离，未来接入 Prometheus 等监控源时不必改判定规则。

## 3. 核心接口与结果模型

```go
// inspect/inspector.go
package inspect

type Level int

const (
    LevelOK       Level = iota // 正常
    LevelWarn                  // 需关注，未影响服务
    LevelCritical              // 严重，需立即处理
    LevelUnknown               // 采集失败 / 无法判定
)

// Scope 决定 Runner 怎么调度这个 inspector。
type Scope int

const (
    ClusterScope Scope = iota // 整集群跑一次（如 ceph health）
    NodeScope                 // 每个节点各跑一次（如 cpu/mem/disk）
)

// Finding 是一条结构化巡检结论。
type Finding struct {
    Item    string            // 巡检项标识，如 "ceph.osd" / "hw.disk"
    Node    string            // NodeScope 时填节点名；ClusterScope 留空
    Level   Level
    Summary string            // 一句话结论："2 个 OSD down"
    Metrics map[string]string // 结构化指标：{"osd_down":"2","osd_total":"36"}
    Detail  string            // 原始命令输出（报告里可折叠）
    Advice  string            // 修复建议（可空）
}

type Inspector interface {
    Name() string
    Description() string
    Scope() Scope
    Inspect(ic *InspectContext) ([]Finding, error)
}

// InspectContext 复用现有执行器，避免重复造轮子。
type InspectContext struct {
    Ctx         context.Context
    ClusterName string
    Node        config.SSHNode        // NodeScope 时为当前节点
    Gateway     *config.SSHNode
    KubeExec    *executor.KubeExecutor
    SSHExec     *executor.SSHExecutor
    Thresholds  Thresholds            // 从 config 读，见第 6 节
}
```

`InspectContext` 提供 `RunOnNode` / `RunCeph` 等辅助方法，复刻 `skill.Context` 的 ProxyJump 等逻辑。

判定规则写在每个 inspector 的 `Inspect` 里，阈值取自 `ic.Thresholds`。解析失败时降级为 `LevelUnknown` 并把原始输出放进 `Detail`，不让解析脆弱性拖垮整个巡检。

## 4. 巡检项清单与判定规则

### 4.1 Ceph 集群级（ClusterScope，走 `KubeExec.RunCephCommand`）

| Inspector | 采集命令 | 判定规则 |
|---|---|---|
| `ceph_health` | `health detail` | `HEALTH_OK`→OK；`HEALTH_WARN`→Warn；`HEALTH_ERR`→Critical |
| `ceph_osd` | `osd stat`、`osd tree` | down>0 或 out>0 → Critical；否则 OK。Metrics: osd_up/in/down/total |
| `ceph_mon` | `mon stat`、`quorum_status` | quorum 缺失/不足半数 → Critical；OK |
| `ceph_pg` | `pg stat` | 有 inactive/incomplete/stale → Critical；有 degraded/undersized → Warn；OK |
| `ceph_capacity` | `df` | 全局或任一 OSD 使用率 ≥ `CapacityCritPct`(默认 90)→ Critical；≥ `CapacityWarnPct`(默认 80)→ Warn |
| `ceph_slow_ops` | `health detail` 过滤 SLOW_OPS | 有 slow ops → Warn |
| `ceph_crash` | `crash ls-new` | 有未确认 crash → Warn，列出最近一条 |

### 4.2 硬件节点级（NodeScope，走 SSH，每节点一次）

| Inspector | 采集命令 | 判定规则 |
|---|---|---|
| `hw_cpu` | `lscpu`；`uptime`(load) | load1/核数 ≥ `LoadWarnRatio`(默认 2.0)→ Warn。Metrics: cores, load1/5/15 |
| `hw_memory` | `free -b` | used% ≥ `MemCritPct`(默认 95)→ Critical；≥ `MemWarnPct`(默认 90)→ Warn |
| `hw_disk_smart` | 枚举盘 `lsblk -dno NAME,TYPE`，逐盘 `smartctl -A -H /dev/X` | 健康自检 FAILED → Critical；SSD/NVMe `Percentage_Used ≥ DiskLifeCritPct`(默认 90)→ Critical，≥ `DiskLifeWarnPct`(默认 80)→ Warn；HDD 看 Reallocated/Pending sectors > 0 → Warn |
| `hw_disk_usage` | `df -B1 -x tmpfs -x devtmpfs` | 任一挂载点 ≥ `FsCritPct`(默认 90)→ Critical；≥ `FsWarnPct`(默认 85)→ Warn |
| `hw_nic` | `ip -br link show` | 物理网卡（排除 lo/cali/tun/ipvs 及 bond 的 slave 从属口）状态非 UP/UNKNOWN → Warn |
| `hw_bond` | `grep ... /proc/net/bonding/bond*` | Link Failure Count > 0 → Warn；MII Status 非 up → Critical |
| `hw_pcie_link` | `ls -l /sys/class/{nvme,net}/`(BDF→内核名映射)，`lspci -Dvvv` | 逐 NVMe/网卡比对 `LnkCap`(额定) 与 `LnkSta`(协商)：宽度降级(lane 减少)→ Critical；仅速率降级 → Warn。全部匹配产出单条 OK |

**采集前置**：硬件 SMART 检查需节点安装 `smartmontools`。inspector 检测到 `smartctl` 缺失时产出 `LevelUnknown` + Advice「节点未安装 smartmontools」，而不是报错。PCIe 链路检查需 `pciutils`(`lspci`)，缺失时同样产出 `LevelUnknown` + Advice。

### 4.3 复用策略

- **Ceph 项**：直接 `ic.KubeExec.RunCephCommand(...)`，与 skill 包 `runCephCommands` 同源（同一个导出方法），无需改动 skill 包。
- **硬件项**：SSH 采集与现有 `NICInfo`/`BondStatus` 同模式。`SSHExecutor.Run` 自身不做命令白名单（白名单在 intent/bot 上层；巡检走后台不经该链路），可直接执行采集命令。纯解析函数（如 bond 解析）在 inspect 包内实现，不强行与 skill 合并。

### 4.4 扩展性

未来加新存储类型（如 MinIO）或新硬件项，只需实现 `Inspector` 接口并在 registry 注册一行。Ceph 专属项按集群类型路由（集群为 ceph 才注册 ceph 系列）。

## 5. 触发入口（统一收口于 Runner）

```go
// inspect/runner.go
func (r *Runner) Run(ctx context.Context, clusterName string) (*Report, error)
```

`Runner.Run` 职责：
- 解析集群 → 取 `KubeExec` + SSH 节点列表（复用 `cluster.Manager.ResolveSSHNodes`）
- 按集群类型选择 inspector 集合（ceph 系列 + 硬件系列）
- ClusterScope 跑一次；NodeScope 对每节点跑，**节点间并发**（带上限），单节点失败不影响其他
- 聚合所有 `Finding` → `Report`（Overall = 最高级别）

| 入口 | 接法 | 输出 |
|---|---|---|
| **cron** | `Scheduler` 定时 → `Runner.Run` → 按 `NotifyMinLevel` 决定是否 `RenderCard` 推 `NotifyChat` | 飞书卡片 + 落盘 |
| **chat** | `intent/parser.go` 加巡检意图（触发词：`巡检`/`检查`/`体检` + 集群名，另支持「巡检所有集群」批量）→ `Runner.Run` → `RenderCard` 飞书卡片回复 | 飞书卡片 + 落盘 |
| **web** | `web/` 加巡检页：集群列表 + 「立即巡检」按钮 + 历史报告列表 → 调 `Runner.Run` / `store.List` | HTML 页 |
| **HTTP API** | `POST /api/inspect/{cluster}` → `Runner.Run` → JSON；`GET /api/inspect/{cluster}/history`。复用现有 Web Basic Auth 鉴权 | JSON |

**批量巡检**：聊天「巡检所有集群」或定时 `Clusters` 为多个时，逐集群调 `Runner.Run`，**每个集群产出独立的一张卡片**（而非合并成一张），便于分别查看与告警归因。

## 6. 配置结构

`config/config.go` 顶层 `Config` 新增 `Inspect` 字段：

```go
type InspectConfig struct {
    Enabled        bool       `yaml:"enabled"`          // 总开关，默认 false
    Schedule       string     `yaml:"schedule"`         // cron 表达式，如 "0 3 * * *"
    Clusters       []string   `yaml:"clusters"`         // 定时巡检哪些集群；空=全部
    NotifyChat     string     `yaml:"notify_chat"`      // 飞书 chat_id；有发现才推送
    NotifyMinLevel string     `yaml:"notify_min_level"` // "warn"(默认) 或 "critical"
    LLMSummary     bool       `yaml:"llm_summary"`      // 是否调 LLM 总结，跟随 dev.disable_llm
    HistoryDir     string     `yaml:"history_dir"`      // 历史目录，默认 "./inspect-reports"
    HistoryKeep    int        `yaml:"history_keep"`     // 每集群保留份数，默认 30
    Thresholds     Thresholds `yaml:"thresholds"`       // 阈值覆盖，留空用默认
}

type Thresholds struct {
    CapacityWarnPct int     `yaml:"capacity_warn_pct"`  // 默认 80
    CapacityCritPct int     `yaml:"capacity_crit_pct"`  // 默认 90
    MemWarnPct      int     `yaml:"mem_warn_pct"`       // 默认 90
    MemCritPct      int     `yaml:"mem_crit_pct"`       // 默认 95
    FsWarnPct       int     `yaml:"fs_warn_pct"`        // 默认 85
    FsCritPct       int     `yaml:"fs_crit_pct"`        // 默认 90
    DiskLifeWarnPct int     `yaml:"disk_life_warn_pct"` // 默认 80（Percentage_Used）
    DiskLifeCritPct int     `yaml:"disk_life_crit_pct"` // 默认 90
    LoadWarnRatio   float64 `yaml:"load_warn_ratio"`    // 默认 2.0（load1/核数）
}
```

`config.Load` 给 `Thresholds` 填默认值（零值视为未设置），校验 cron 表达式合法、`NotifyMinLevel` 取值合法。阈值经 `InspectContext.Thresholds` 传给每个 inspector。

**热重载**：`watcher.OnReload` 中增加 `scheduler.Reload(newCfg.Inspect)`，改 cron/阈值/集群列表无需重启。

## 7. 调度器

cron 解析用 `github.com/robfig/cron/v3`。

```go
// inspect/scheduler.go
type Scheduler struct {
    cron    *cron.Cron
    runner  *Runner
    cfg     InspectConfig
    mu      sync.Mutex
    entryID cron.EntryID
}

func (s *Scheduler) Start(ctx context.Context)             // 注册 cron job，阻塞到 ctx.Done
func (s *Scheduler) Reload(cfg InspectConfig)              // 热重载：重建 cron entry
func (s *Scheduler) RunOnce(ctx, clusterName) (*Report, error) // 手动触发，供 chat/web/API 复用
```

挂在 `main.go`，与现有 `watcher`/`web`/`wsClient` 同为后台 goroutine：

```go
if cfg.Inspect.Enabled {
    scheduler := inspect.NewScheduler(runner, cfg.Inspect)
    go scheduler.Start(ctx)
    // watcher.OnReload 内加 scheduler.Reload(newCfg.Inspect)
}
```

## 8. 结果呈现

### 8.1 Report 结构与渲染

```go
// inspect/report.go
type Report struct {
    Cluster    string
    StartedAt  time.Time     // 由调用方传入（不在纯函数里用 time.Now，便于测试）
    Duration   time.Duration
    Overall    Level         // = 所有 Finding 的最高级别
    Findings   []Finding
    LLMSummary string        // 可空
}

func (r *Report) Counts() (ok, warn, crit int)
func (r *Report) Abnormal() []Finding   // Warn 及以上，表格用
func (r *Report) RenderText() string    // API/降级用的 Markdown 文本
func (r *Report) RenderCard() *larkCard // 飞书卡片（chat 触发与 cron 推送共用）
```

### 8.2 飞书卡片布局（已评审确认）

「A 摘要头部 + C 表格」融合：

- **头部摘要**：按总体级别着色的标题栏（红/黄/绿 = Critical/Warn/OK）+「总体：CRITICAL」+ 时间 + 一行统计（🔴 严重 N · 🟡 警告 N · 🟢 正常 N）。
- **异常项表格**：三列 `级别 / 巡检项·节点 / 结论`，只列 Warn 及以上。硬件项每节点一条（如 `hw_disk · node-3`）。
- **正常项折叠**：一行「🟢 其余 N 项正常：…」，不刷屏。
- **底部双按钮**：「📄 完整报告」跳 Web 页（含每项原始 Detail）；「🤖 AI 总结」按需触发 LLM。

飞书卡片复用现有 `card/` 模块构造。

### 8.3 LLM 总结

`Runner.Run` 得到 `Report` 后，若 `LLMSummary` 开启且 LLM 可用，将**异常项的结构化数据**（非全部原始输出，省 token）喂给 `analyzer`，产出中文总结/处置建议填入 `Report.LLMSummary`。LLM 不可用或 `dev.disable_llm` 时跳过，报告照常产出（降级不报错）。定时巡检的卡片里 AI 总结走按钮按需触发，避免每次烧 token。

### 8.4 历史留存

- 每次巡检把 `Report` 序列化为 JSON 写到 `HistoryDir/<cluster>/<timestamp>.json`。
- 每集群保留最近 `HistoryKeep` 份（默认 30），超出删最旧。
- `store.List(cluster)` / `store.Load(cluster, ts)` 供 Web 页查历史、做趋势对比。
- 文件存储，不引数据库（符合项目现有无 DB 风格）。

## 9. 改动清单

**新增依赖**：`github.com/robfig/cron/v3`（仅调度用）。

**新增文件**：`inspect/` 包（inspector.go / registry.go / runner.go / scheduler.go / report.go / store.go / ceph.go / hardware.go + 对应 `_test.go`）。

**改动现有文件**：

- `config/config.go` —— 加 `InspectConfig` / `Thresholds` + 默认值 + 校验
- `main.go` —— 起 `Scheduler` goroutine、`watcher.OnReload` 加 `scheduler.Reload`
- `intent/parser.go` —— 加巡检意图
- `web/` —— 加巡检页与 API 路由（复用 Basic Auth）
- `config.yaml.example` —— 加 `inspect:` 配置样例

## 10. 非目标（YAGNI）

- 不引入时序数据库；趋势对比基于留存的 JSON 报告做简单对比即可。
- 不在节点上部署 agent；硬件采集走 SSH。
- 不做全配置化的判定规则引擎；判定规则内置，仅常用阈值可经 config 覆盖。
- Prometheus 等监控数据源为未来扩展点，本期不实现，但接口预留可替换空间。
