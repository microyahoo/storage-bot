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
			continue
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
