# bond Link Failure 增量告警设计

日期：2026-07-07
状态：已评审，待写实现计划

## 背景与问题

`inspect` 包的 `hw_bond` 巡检项目前的判定逻辑（`inspect/hardware_parse.go` 的 `parseBond`）：

- 汇总节点上所有 bond 的 `Link Failure Count` 得到 `failTotal`；
- MII Status 有非 `up` → Critical；
- 否则 `failTotal > 0` → Warn，摘要「bond 累计 Link Failure N 次」；
- `failTotal == 0` → OK。

问题在于：当前使用的操作系统无法在不重启节点的情况下重置 `Link Failure Count`。这个计数器是**单调累计**的，历史上任何一次链路抖动都会永久留在计数里。结果是几乎每次巡检、几乎所有节点都会因为 `failTotal > 0` 报 Warn，告警噪音极大，掩盖了真正的新增故障。

## 目标

把 `hw_bond` 的 link failure 判定从「绝对值 > 0 就告警」改为「**与上一次巡检比对，有增长才告警**」，并在告警文案里显示较上次新增了多少次。历史巡检报告已持久化在 `Store` 里，可据此比对。

MII Status 非 up 表示链路真的断了，与历史无关，仍然 Critical，不受本设计影响。

## 关键决策（已与用户确认）

1. **首次/无基线**：首次巡检，或历史里没有可比对的上一次记录（含新加入的节点、老报告缺该 metric），`failTotal > 0` 时**仍告警一次**，文案标注「首次记录基线」。之后自愈为增量比对。
2. **比对粒度**：**按节点汇总**。延续现有 `parseBond` 把该节点所有 bond 的 `Link Failure Count` 求和的做法，告警只说该节点总共新增多少。
3. **告警阈值**：增量 `> 0` 即告警，不设额外阈值。
4. **计数器归零**（`delta < 0`，疑似重启后重来）：视为无新增，降级 OK，不额外提示。

## 方案选择

**方案 A — Runner 后处理增量（采用）**

`parseBond` 保持纯解析函数：永远在 `Metrics["link_failure_total"]` 写出节点累计失败次数，并按 `failTotal > 0` 给出 Warn 初值（MII down 仍 Critical）。Runner 在收集完所有 finding 后、`Finalize` 前，跑一个 `applyBondDelta` 步骤：加载上一次报告，按节点比对 `link_failure_total`，据增量把 Warn 保持/降级并改写文案。

- 优点：`parseBond` 仍是纯函数、易测；Store 访问集中在 Runner 一处，不被每节点并发放大；历史比对逻辑独立可单测。
- 权衡：severity 分两处决定（解析给初值、Runner 调整），用注释说清。

**方案 B — Inspector 内直接比对**（否决）：`hwBond.Inspect` 自己读 store 比对。破坏 Inspector 不碰 store 的现有约定；每节点并发都读历史文件放大 IO；`parseBond` 要吃历史入参，难测。

**方案 C — 通用 counter baseline 框架**（否决）：抽象「单调计数器指标」通用机制。当前只有一个场景，YAGNI。

## 详细设计

### 1. parseBond 职责调整（`inspect/hardware_parse.go`）

- 无 bond（`(no bonds)`）→ OK，不变。
- 汇总所有 bond 的 `Link Failure Count` 得 `failTotal`，**无论最终级别**都写入 `Metrics["link_failure_total"] = strconv.Itoa(failTotal)`。这是下一次比对的基线来源，必须落进报告 JSON。
- MII 有非 up → Critical，摘要「存在 MII Status 非 up 的 bond」，不变。
- 否则 `failTotal > 0` → Warn 初值，摘要暂为「bond 累计 Link Failure N 次」（Runner 会据历史改写或降级）。
- `failTotal == 0` → OK，摘要「bond 链路正常」。

`parseBond` 仍是纯函数、无历史上下文，只是多产出一个 metric。

### 2. Runner 增量比对（`inspect/runner.go`）

新增纯函数 `applyBondDelta(rep, prev *Report)`（不依赖网络，便于单测）：

- **基线构建**：`prev == nil` → 无任何基线。否则遍历 `prev.Findings`，对 `Item == "hw_bond"` 的按 `Node` 建 `map[string]int`，值取 `Metrics["link_failure_total"]` 并 `strconv.Atoi`；缺失或非数字的节点不进 map（视为无基线）。
- **调整**：遍历 `rep.Findings`，只处理 `Item == "hw_bond"` 且当前 `Level == LevelWarn` 的（Critical 的 MII down 不动、OK 的不动）：
  - `cur := Metrics["link_failure_total"]`（Atoi）。
  - 该节点**无基线** → 保持 Warn，摘要「bond 累计 Link Failure %d 次（首次记录，作为基线）」。
  - **有基线** `base`，`delta := cur - base`：
    - `delta > 0` → 保持 Warn，摘要「bond Link Failure 较上次新增 %d 次（累计 %d 次）」，写 `Metrics["link_failure_delta"] = delta`。
    - `delta <= 0` → 降级 `LevelOK`，摘要「bond Link Failure 累计 %d 次，较上次无新增」。

`Run` 的接线：

- 跑 inspector 前（或 `runWith` 后、`Finalize` 前）取上一次报告：新增小helper `latestReport(cluster)` —— `store == nil` → nil；`store.List` 取最新一条（此刻本次报告尚未 `Save`，所以 `List[0]` 就是上一次），`Load` 失败 → nil。
- 顺序严格为：`runWith` → `applyBondDelta(rep, prev)` → `rep.Finalize()`（Overall 依赖调整后的 level）→ `summarize()`（降级为 OK 的不该喂 LLM）→ `store.Save`。

### 3. 展示与落盘

- 文案变化通过 `Summary`/`Level` 自动生效，`RenderText`/`RenderCard` **无需改动**（它们只按 Level 分组、读 Summary）。
- 落盘 metric：`link_failure_total`（始终写，供下次比对与 web 详情）、`link_failure_delta`（仅有基线且 >0 时写）。

### 展示文案汇总

| 场景 | Level | Summary |
|------|-------|---------|
| 无 bond | OK | 无 bond 配置（不变） |
| failTotal==0 | OK | bond 链路正常 |
| MII 非 up | Critical | 存在 MII Status 非 up 的 bond |
| 首次/新节点，failTotal>0 | Warn | bond 累计 Link Failure N 次（首次记录，作为基线） |
| 有基线，delta>0 | Warn | bond Link Failure 较上次新增 D 次（累计 M 次） |
| 有基线，delta<=0 | OK | bond Link Failure 累计 M 次，较上次无新增 |

## 测试

- `hardware_parse_test.go`：`parseBond` 现在总写出 `link_failure_total` metric —— OK / Warn / Critical 各一例断言 metric 存在且数值正确；MII down 仍 Critical。
- 新增 `bond_delta_test.go`（或并入 `runner_test.go`）覆盖 `applyBondDelta`：
  1. `prev == nil`（首次）→ 保持 Warn + 基线文案。
  2. prev 有该节点、`delta > 0` → Warn + 新增文案 + `link_failure_delta` metric。
  3. `delta == 0` → 降级 OK。
  4. `delta < 0`（重启归零）→ 降级 OK。
  5. prev 无该节点（新节点）→ 保持 Warn。
  6. Critical（MII down）不受 delta 影响。
  7. prev 该节点 metric 缺失/非数字 → 当无基线，保持 Warn。
- 回归：降级为 OK 的 bond 不出现在 `Abnormal()`、不进 `summarize()`。

## 兼容性

老报告 JSON 无 `link_failure_total` metric → 视为无基线，首次比对告警一次，之后自愈。无需数据迁移。
