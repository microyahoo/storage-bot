package inspect

import (
	"encoding/json"
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
