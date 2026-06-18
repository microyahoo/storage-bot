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

// dfTotalRe matches a TOTAL line and captures the last numeric value on the line
// (the %RAW USED column), e.g.: "TOTAL  100 TiB  18 TiB  82 TiB  82 TiB  82.00"
var dfTotalRe = regexp.MustCompile(`(?m)^TOTAL\b.*\s([\d.]+)\s*$`)

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
