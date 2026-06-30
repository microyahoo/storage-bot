package inspect

import (
	"fmt"
	"regexp"
	"sort"
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

// pcieDev is one PCIe endpoint (NVMe controller or NIC) with its rated link
// capability (LnkCap) and currently negotiated link state (LnkSta).
type pcieDev struct {
	bdf      string // PCI domain:bus:device.function, e.g. 0000:01:00.0
	kind     string // "NVMe" | "NIC"
	name     string // kernel name, e.g. nvme0 / eth0
	model    string // lspci device description
	capSpeed float64
	capWidth int
	staSpeed float64
	staWidth int
	capRaw   string // "Speed 32GT/s, Width x4"
	staRaw   string
}

var (
	bdfRe       = regexp.MustCompile(`[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-9a-f]`)
	pcieSpeedRe = regexp.MustCompile(`Speed ([0-9.]+)GT/s`)
	pcieWidthRe = regexp.MustCompile(`Width x([0-9]+)`)
)

// parseSysClassLinks maps PCI BDF → kernel device name from `ls -l /sys/class/<kind>/`
// output. Each symlink target embeds the device's PCI path; the LAST BDF in the
// path is the device's own function (mirrors the reference script's `tail -1`).
// Virtual devices (lo, bond*, cali*) have no BDF in their target and are skipped.
func parseSysClassLinks(raw string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		name, target, ok := strings.Cut(line, " -> ")
		if !ok {
			continue
		}
		fields := strings.Fields(name)
		if len(fields) == 0 {
			continue
		}
		dev := fields[len(fields)-1]
		if bdfs := bdfRe.FindAllString(target, -1); len(bdfs) > 0 {
			out[bdfs[len(bdfs)-1]] = dev
		}
	}
	return out
}

func parsePCIeSpeed(s string) float64 {
	if m := pcieSpeedRe.FindStringSubmatch(s); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		return v
	}
	return 0
}

func parsePCIeWidth(s string) int {
	if m := pcieWidthRe.FindStringSubmatch(s); m != nil {
		v, _ := strconv.Atoi(m[1])
		return v
	}
	return 0
}

// pcieLinkSummary extracts the "Speed …, Width x…" portion of a LnkCap/LnkSta line.
func pcieLinkSummary(s string) string {
	return strings.TrimSpace(pcieSpeedRe.FindString(s) + ", " + pcieWidthRe.FindString(s))
}

// parseLspciLinks parses `lspci -Dvvv` output into pcieDev records, keeping only
// NVMe controllers and NICs. names maps BDF → kernel device name (from sysfs); a
// device absent from names keeps an empty name and is identified by BDF + model.
//
// Block shape (header at column 0, capabilities indented):
//
//	0000:01:00.0 Non-Volatile memory controller: Samsung ... PM173X
//	        LnkCap: Port #0, Speed 32GT/s, Width x4, ...
//	        LnkSta: Speed 16GT/s (downgraded), Width x4, ...
//
// Only "LnkCap:"/"LnkSta:" (not the LnkCap2/LnkSta2 variants) carry the rated and
// negotiated speed/width we compare.
func parseLspciLinks(raw string, names map[string]string) []*pcieDev {
	byBDF := map[string]*pcieDev{}
	var order []string
	var cur *pcieDev

	for _, line := range strings.Split(raw, "\n") {
		// A header line starts at column 0 with a BDF; capability lines are indented.
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			cur = nil
			m := bdfRe.FindString(line)
			if m == "" {
				continue
			}
			kind := ""
			switch {
			case strings.Contains(line, "Non-Volatile memory controller"):
				kind = "NVMe"
			case strings.Contains(line, "Ethernet controller"), strings.Contains(line, "Network controller"):
				kind = "NIC"
			default:
				continue // not a device we track
			}
			model := ""
			if _, desc, ok := strings.Cut(line, ": "); ok {
				model = strings.TrimSpace(desc)
			}
			cur = &pcieDev{bdf: m, kind: kind, name: names[m], model: model}
			byBDF[m] = cur
			order = append(order, m)
			continue
		}
		if cur == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "LnkCap:"):
			cur.capSpeed, cur.capWidth, cur.capRaw = parsePCIeSpeed(trimmed), parsePCIeWidth(trimmed), pcieLinkSummary(trimmed)
		case strings.HasPrefix(trimmed, "LnkSta:"):
			cur.staSpeed, cur.staWidth, cur.staRaw = parsePCIeSpeed(trimmed), parsePCIeWidth(trimmed), pcieLinkSummary(trimmed)
		}
	}

	out := make([]*pcieDev, 0, len(order))
	for _, bdf := range order {
		out = append(out, byBDF[bdf])
	}
	return out
}

// evalPCIeLinks turns parsed devices into findings. A device whose negotiated
// LnkSta is below its rated LnkCap is flagged: width loss → Critical (a lane is
// physically dead, halving throughput), speed-only downgrade → Warn. Devices with
// no parseable LnkCap/LnkSta are skipped (some bridges expose no link caps). When
// nothing is degraded, a single OK summary is returned.
func evalPCIeLinks(devs []*pcieDev) []Finding {
	var out []Finding
	checked := 0
	for _, d := range devs {
		if d.capSpeed == 0 || d.capWidth == 0 || d.staSpeed == 0 || d.staWidth == 0 {
			continue
		}
		checked++
		widthDown := d.staWidth < d.capWidth
		speedDown := d.staSpeed < d.capSpeed
		if !widthDown && !speedDown {
			continue
		}

		id := d.bdf
		if d.name != "" {
			id = d.name + " (" + d.bdf + ")"
		}
		f := Finding{
			Item:   "hw_pcie_link",
			Detail: fmt.Sprintf("%s %s\nLnkCap: %s\nLnkSta: %s", d.kind, d.model, d.capRaw, d.staRaw),
			Metrics: map[string]string{
				"kind": d.kind, "bdf": d.bdf, "name": d.name,
				"cap": d.capRaw, "sta": d.staRaw,
			},
		}
		switch {
		case widthDown:
			f.Level = LevelCritical
			f.Summary = fmt.Sprintf("%s %s PCIe 链路降级：宽度 x%d→x%d，速率 %sGT/s→%sGT/s",
				d.kind, id, d.capWidth, d.staWidth, trimFloat(d.capSpeed), trimFloat(d.staSpeed))
			f.Advice = "PCIe lane 数下降，检查插槽接触/插拔重插，性能会显著受损"
		default: // speed-only
			f.Level = LevelWarn
			f.Summary = fmt.Sprintf("%s %s PCIe 链路速率降级：%sGT/s→%sGT/s（宽度 x%d 正常）",
				d.kind, id, trimFloat(d.capSpeed), trimFloat(d.staSpeed), d.staWidth)
			f.Advice = "PCIe 速率未跑满，检查 BIOS PCIe 配置/信号质量，吞吐会下降"
		}
		out = append(out, f)
	}

	if len(out) == 0 {
		summary := "PCIe 链路速率/宽度正常"
		if checked == 0 {
			summary = "未发现可检测的 NVMe/网卡 PCIe 链路"
		}
		return []Finding{{Item: "hw_pcie_link", Level: LevelOK, Summary: summary}}
	}
	return out
}

// trimFloat renders a PCIe speed without trailing ".0" (32.0→"32", 2.5→"2.5").
func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// sortPCIeDevs orders devices by BDF for deterministic output.
func sortPCIeDevs(devs []*pcieDev) {
	sort.Slice(devs, func(i, j int) bool { return devs[i].bdf < devs[j].bdf })
}
