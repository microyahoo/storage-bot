package inspect

import (
	"fmt"
	"strconv"
)

// applyBondDelta 把 hw_bond 的 link failure 判定从「绝对值告警」修正为「与最近
// 两次巡检比对，有增长才告警」。parseBond 对 failTotal>0 给出的是 Warn 初值。
//
// 时间序：prev2(T-2) → prev1(T-1) → 本次(T)。Link Failure Count 单调累计，所以
// 正常情况 base2 ≤ base1 ≤ cur（重启会归零打破此序）。按节点用两个独立条件判定：
//
//   - recentInc = cur > base1（较上次有涨）
//   - windowInc = cur > base2（较上上次有涨——能翻出被漏看的 T-1 那次增长）
//
// 任一成立即保持 Warn。只跟上一次比会漏掉「T-1 涨了但没看、T 跟 T-1 持平」的情况，
// windowInc 把它重新暴露一个周期。双条件也顺带处理重启：base2 因重启而偏大时，
// recentInc 仍能报出最近的真实增长，不被 stale 的 base2 掩盖。
//
// 级别与文案：
//   - 无任何基线（首次/新节点/两次 metric 都缺）→ 保持 Warn，标注首次基线。
//   - 都不涨 → 降级 OK。
//   - recentInc → Warn，写 link_failure_delta；若较上上次涨更多再补 link_failure_delta2。
//   - 仅 windowInc（较上次=0、较上上次>0）→ Warn，写 link_failure_delta2，提示可能漏看上次。
//
// MII down 的 Critical 与历史无关，不在处理范围内。必须在 rep.Finalize() 之前、
// summarize() 之前调用，否则 Overall 与 LLM 摘要会用到未修正的 level。
func applyBondDelta(rep *Report, prev1, prev2 *Report) {
	base1 := bondBaseline(prev1)
	base2 := bondBaseline(prev2)
	for i := range rep.Findings {
		f := &rep.Findings[i]
		if f.Item != "hw_bond" || f.Level != LevelWarn {
			continue
		}
		cur, ok := bondTotal(*f)
		if !ok {
			continue // 本次 metric 异常，保底不动
		}
		b1, has1 := base1[f.Node]
		b2, has2 := base2[f.Node]
		if !has1 && !has2 {
			f.Summary = fmt.Sprintf("bond 累计 Link Failure %d 次（首次记录，作为基线）", cur)
			continue
		}
		applyNodeDelta(f, cur, b1, has1, b2, has2)
	}
}

// applyNodeDelta 对单个已有至少一个基线的 hw_bond finding 应用两窗口判定，改写
// level/summary/metrics。
func applyNodeDelta(f *Finding, cur int, b1 int, has1 bool, b2 int, has2 bool) {
	d1 := cur - b1 // 仅在 has1 时有意义
	d2 := cur - b2 // 仅在 has2 时有意义
	recentInc := has1 && d1 > 0
	windowInc := has2 && d2 > 0

	if !recentInc && !windowInc {
		f.Level = LevelOK
		f.Summary = fmt.Sprintf("bond Link Failure 累计 %d 次，最近无新增", cur)
		return
	}

	if recentInc {
		f.Metrics["link_failure_delta"] = strconv.Itoa(d1)
		// 较上上次涨得更多，说明中间那次也有增长，一并标注。
		if has2 && d2 > d1 {
			f.Metrics["link_failure_delta2"] = strconv.Itoa(d2)
			f.Summary = fmt.Sprintf("bond Link Failure 较上次新增 %d 次、较上上次累计新增 %d 次（累计 %d 次）", d1, d2, cur)
			return
		}
		f.Summary = fmt.Sprintf("bond Link Failure 较上次新增 %d 次（累计 %d 次）", d1, cur)
		return
	}

	// 仅 windowInc：较上次持平，但较上上次有涨 → 可能漏看了上次报告。
	f.Metrics["link_failure_delta2"] = strconv.Itoa(d2)
	f.Summary = fmt.Sprintf("bond Link Failure 较上次无新增，但较上上次新增 %d 次（可能漏看上次报告，累计 %d 次）", d2, cur)
}

// bondBaseline 从一份报告提取每个节点的 link_failure_total 基线。report 为 nil、
// metric 缺失或非数字的节点不进 map（视为无基线）。
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
