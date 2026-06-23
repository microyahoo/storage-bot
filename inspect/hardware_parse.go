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

// SCSI/SAS 盘及 RAID 卡后面的虚拟盘（如系统盘 RAID1）走 SCSI 输出格式，
// 没有 ATA 的 "self-assessment test result" 行，也读不到 Percentage Used
// 寿命，健康状态体现在 "SMART Health Status:" 这一行。
var smartHealthRe = regexp.MustCompile(`SMART Health Status:\s*(\S+)`)

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
	// SCSI/SAS/RAID 盘的健康行；HDD 与 RAID1 系统盘多走这里，没有寿命百分比。
	if m := smartHealthRe.FindStringSubmatch(raw); m != nil {
		status := m[1]
		f.Metrics["health_status"] = status
		if status == "OK" {
			f.Level = LevelOK
			f.Summary = dev + "：SMART OK"
		} else {
			f.Level = LevelCritical
			f.Summary = dev + "：SMART Health Status " + status
			f.Advice = "磁盘健康状态异常，尽快排查更换"
		}
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

// parseNIC checks bond member ports only. Input is the bonding files' grepped
// lines (Slave Interface + MII Status). A member whose own MII Status is not
// "up" → Warn, naming the port. Independent/idle physical ports are
// intentionally ignored; overall bond health is covered by parseBond. No
// bonds → OK.
func parseNIC(raw string) Finding {
	f := Finding{Item: "hw_nic", Detail: raw}
	if strings.Contains(raw, "(no bonds)") {
		f.Level, f.Summary = LevelOK, "无 bond 成员口"
		return f
	}
	var down []string
	curSlave := ""
	for _, line := range strings.Split(raw, "\n") {
		_, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		switch {
		case strings.HasPrefix(rest, "Slave Interface"):
			if _, v, ok2 := strings.Cut(rest, ":"); ok2 {
				curSlave = strings.TrimSpace(v)
			}
		case strings.HasPrefix(rest, "MII Status"):
			// Only the MII line that follows a Slave Interface line describes a
			// member port; the bond's own global MII line has curSlave == "".
			if _, v, ok2 := strings.Cut(rest, ":"); ok2 && curSlave != "" && strings.TrimSpace(v) != "up" {
				down = append(down, curSlave)
			}
			curSlave = "" // consume; next MII must be preceded by a new slave line
		}
	}
	if len(down) > 0 {
		f.Level = LevelWarn
		f.Summary = "bond 成员口未 up：" + strings.Join(down, ", ")
		f.Advice = "检查对应物理网口连线与交换机端口"
	} else {
		f.Level = LevelOK
		f.Summary = "bond 成员口状态正常"
	}
	return f
}

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
