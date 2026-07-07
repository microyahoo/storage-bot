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
