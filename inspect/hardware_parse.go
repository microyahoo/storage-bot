package inspect

import (
	"fmt"
	"path"
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
	// 始终落盘累计次数：这是下一次巡检做增量比对的基线来源，必须进报告 JSON。
	f.Metrics = map[string]string{"link_failure_total": strconv.Itoa(failTotal)}
	switch {
	case miiDown:
		f.Level, f.Summary = LevelCritical, "存在 MII Status 非 up 的 bond"
		f.Advice = "检查物理链路与交换机端口"
	case failTotal > 0:
		// Warn 初值：Runner 的 applyBondDelta 会据上一次报告改写文案或降级为 OK。
		f.Level, f.Summary = LevelWarn, fmt.Sprintf("bond 累计 Link Failure %d 次", failTotal)
	default:
		f.Level, f.Summary = LevelOK, "bond 链路正常"
	}
	return f
}

// pcieMeta identifies a device we want to check, keyed by BDF. kind/name come
// authoritatively from sysfs (bonding files + the device symlink), so the lspci
// pass only needs to fill in link speed/width — it no longer sniffs the device
// class from the description text.
type pcieMeta struct {
	kind string // "NVMe" | "NIC"
	name string // kernel name, e.g. nvme0 / eth0
}

var (
	bdfRe       = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-9a-f]$`)
	pcieSpeedRe = regexp.MustCompile(`Speed ([0-9.]+)GT/s`)
	pcieWidthRe = regexp.MustCompile(`Width x([0-9]+)`)
)

// parseBondSlaveNames returns the physical NIC names enslaved to a bond, read
// from the "Slave Interface:" lines of `grep ... /proc/net/bonding/bond*`. These
// are the only NICs worth a PCIe check — virtual interfaces (lo, cali*, tun*) and
// the bond masters themselves are never bond slaves, so they are excluded by
// construction rather than by a denylist.
func parseBondSlaveNames(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		// Line shape: "/proc/net/bonding/bond0:Slave Interface: eth0".
		_, body, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, val, ok := strings.Cut(strings.TrimSpace(body), ":")
		if !ok || strings.TrimSpace(key) != "Slave Interface" {
			continue
		}
		if name := strings.TrimSpace(val); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// bdfFromReadlink extracts the PCI BDF from `readlink -f /sys/class/<k>/<dev>/device`
// output. The resolved target's basename IS the device's BDF (e.g.
// /sys/devices/pci0000:00/0000:00:1d.0/0000:01:00.0 → 0000:01:00.0), so no
// path-pattern guessing is needed. Returns "" if the device has no PCI parent
// (virtual devices, or a missing symlink yields empty/non-BDF output).
func bdfFromReadlink(raw string) string {
	base := path.Base(strings.TrimSpace(raw))
	if bdfRe.MatchString(base) {
		return base
	}
	return ""
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

// pcieDev is one PCIe endpoint (NVMe controller or NIC) with its rated link
// capability (LnkCap) and currently negotiated link state (LnkSta).
type pcieDev struct {
	bdf      string // PCI domain:bus:device.function, e.g. 0000:01:00.0
	kind     string // "NVMe" | "NIC", from sysfs (the want set)
	name     string // kernel name, e.g. nvme0 / eth0
	model    string // lspci device description
	capSpeed float64
	capWidth int
	staSpeed float64
	staWidth int
	capRaw   string // "Speed 32GT/s, Width x4"
	staRaw   string
}

// parseLspciLinks parses `lspci -Dvvv` output into pcieDev records, keeping only
// the devices in want (BDF → kind/name, resolved from sysfs). kind/name come from
// want — authoritative — while the model description and link speed/width are read
// from the lspci block. Devices not in want (bridges, GPUs, non-bond NICs) are
// skipped.
//
// Block shape (header at column 0, capabilities indented):
//
//	0000:01:00.0 Non-Volatile memory controller: Samsung ... PM173X
//	        LnkCap: Port #0, Speed 32GT/s, Width x4, ...
//	        LnkSta: Speed 16GT/s (downgraded), Width x4, ...
//
// Only "LnkCap:"/"LnkSta:" (not the LnkCap2/LnkSta2 variants) carry the rated and
// negotiated speed/width we compare.
func parseLspciLinks(raw string, want map[string]pcieMeta) []*pcieDev {
	byBDF := map[string]*pcieDev{}
	var order []string
	var cur *pcieDev

	for _, line := range strings.Split(raw, "\n") {
		// A header line starts at column 0 with a BDF as its first field;
		// capability lines are indented.
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			cur = nil
			fields := strings.Fields(line)
			if len(fields) == 0 || !bdfRe.MatchString(fields[0]) {
				continue
			}
			bdf := fields[0]
			meta, ok := want[bdf]
			if !ok {
				continue // not a device we track
			}
			model := ""
			if _, desc, ok := strings.Cut(line, ": "); ok {
				model = strings.TrimSpace(desc)
			}
			cur = &pcieDev{bdf: bdf, kind: meta.kind, name: meta.name, model: model}
			byBDF[bdf] = cur
			order = append(order, bdf)
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

// evalPCIeLinks turns parsed devices into findings, grouping by downgrade
// signature within the node so 22 identically-downgraded NVMe drives collapse to
// one finding instead of 22.
//
//   - Width loss (a lane is physically dead) → Critical, never silenced.
//   - Speed-only downgrade → Warn, UNLESS minSpeedGTS > 0 and the negotiated speed
//     is ≥ minSpeedGTS: then it is treated as an intentional downgrade (e.g.
//     PCIe 5.0→4.0) and silenced, only noted in the OK summary.
//
// Devices with no parseable LnkCap/LnkSta are skipped (some bridges expose no
// link caps). When nothing is reportable, a single OK summary is returned (with a
// note if any drives were silenced as intentional).
func evalPCIeLinks(devs []*pcieDev, minSpeedGTS float64) []Finding {
	// sig groups devices that share the same downgrade shape, so they merge into
	// one finding per (kind, severity, speed transition, width transition).
	type sig struct {
		kind     string
		level    Level
		capSpeed float64
		staSpeed float64
		capWidth int
		staWidth int
	}
	type group struct {
		sig
		devs []*pcieDev
	}
	groups := map[sig]*group{}
	var order []sig // preserve first-seen order for deterministic output

	checked, silenced := 0, 0
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
		// Intentional speed downgrade: speed-only, still at/above the floor.
		if !widthDown && minSpeedGTS > 0 && d.staSpeed >= minSpeedGTS {
			silenced++
			continue
		}

		level := LevelWarn
		if widthDown {
			level = LevelCritical
		}
		s := sig{d.kind, level, d.capSpeed, d.staSpeed, d.capWidth, d.staWidth}
		g, ok := groups[s]
		if !ok {
			g = &group{sig: s}
			groups[s] = g
			order = append(order, s)
		}
		g.devs = append(g.devs, d)
	}

	var out []Finding
	for _, s := range order {
		out = append(out, buildPCIeFinding(groups[s].devs, s.level, s.capSpeed, s.staSpeed, s.capWidth, s.staWidth))
	}

	if len(out) == 0 {
		summary := "PCIe 链路速率/宽度正常"
		switch {
		case checked == 0:
			summary = "未发现可检测的 NVMe/网卡 PCIe 链路"
		case silenced > 0:
			summary = fmt.Sprintf("PCIe 链路正常（%d 块仅速率降级但 ≥%sGT/s，视为故意配置）",
				silenced, trimFloat(minSpeedGTS))
		}
		return []Finding{{Item: "hw_pcie_link", Level: LevelOK, Summary: summary}}
	}
	return out
}

// buildPCIeFinding renders one finding for a group of devices sharing a downgrade
// signature. A single device keeps the "nvme0 (bdf)" form; multiple collapse to
// "NVMe ×N", with the full device list in Detail so nothing is lost.
func buildPCIeFinding(devs []*pcieDev, level Level, capSpeed, staSpeed float64, capWidth, staWidth int) Finding {
	d0 := devs[0]
	ids := make([]string, len(devs))
	for i, d := range devs {
		ids[i] = d.name
		if d.name == "" {
			ids[i] = d.bdf
		} else {
			ids[i] = d.name + "(" + d.bdf + ")"
		}
	}

	// subject: "nvme0 (0000:01:00.0)" for one, "NVMe ×22" for many.
	subject := d0.kind + " " + ids[0]
	if len(devs) > 1 {
		subject = fmt.Sprintf("%s ×%d", d0.kind, len(devs))
	}

	f := Finding{
		Item:  "hw_pcie_link",
		Level: level,
		Detail: fmt.Sprintf("%s %s\nLnkCap: %s\nLnkSta: %s\n受影响设备(%d)：%s",
			d0.kind, d0.model, d0.capRaw, d0.staRaw, len(devs), strings.Join(ids, ", ")),
		Metrics: map[string]string{
			"kind": d0.kind, "count": strconv.Itoa(len(devs)),
			"names": strings.Join(ids, ", "),
			"cap":   d0.capRaw, "sta": d0.staRaw,
		},
	}
	if level == LevelCritical { // width downgrade
		f.Summary = fmt.Sprintf("%s PCIe 链路降级：宽度 x%d→x%d，速率 %sGT/s→%sGT/s",
			subject, capWidth, staWidth, trimFloat(capSpeed), trimFloat(staSpeed))
		f.Advice = "PCIe lane 数下降，检查插槽接触/插拔重插，性能会显著受损"
	} else { // speed-only
		f.Summary = fmt.Sprintf("%s PCIe 链路速率降级：%sGT/s→%sGT/s（宽度 x%d 正常）",
			subject, trimFloat(capSpeed), trimFloat(staSpeed), staWidth)
		f.Advice = "PCIe 速率未跑满，检查 BIOS PCIe 配置/信号质量，吞吐会下降"
	}
	return f
}

// trimFloat renders a PCIe speed without trailing ".0" (32.0→"32", 2.5→"2.5").
func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// sortPCIeDevs orders devices by BDF for deterministic output.
func sortPCIeDevs(devs []*pcieDev) {
	sort.Slice(devs, func(i, j int) bool { return devs[i].bdf < devs[j].bdf })
}
