package inspect

import (
	"strconv"
	"strings"
)

type hwMemory struct{}

func (hwMemory) Name() string        { return "hw_memory" }
func (hwMemory) Description() string { return "内存使用率" }
func (hwMemory) Scope() Scope        { return NodeScope }
func (hwMemory) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunOnNode("free -b")
	if err != nil {
		return []Finding{{Item: "hw_memory", Node: ic.Node.Name, Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	f := parseMemory(raw, ic.Thresholds.MemWarnPct, ic.Thresholds.MemCritPct)
	f.Node = ic.Node.Name
	return []Finding{f}, nil
}

type hwCPU struct{}

func (hwCPU) Name() string        { return "hw_cpu" }
func (hwCPU) Description() string { return "CPU load" }
func (hwCPU) Scope() Scope        { return NodeScope }
func (hwCPU) Inspect(ic *InspectContext) ([]Finding, error) {
	// Two single commands instead of "nproc; uptime": the SSH validator rejects
	// shell metacharacters (the ";") unless the leading token is allow-listed.
	coresOut, err := ic.RunOnNode("nproc")
	if err != nil {
		return []Finding{{Item: "hw_cpu", Node: ic.Node.Name, Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	loadOut, err := ic.RunOnNode("uptime")
	if err != nil {
		return []Finding{{Item: "hw_cpu", Node: ic.Node.Name, Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	cores, _ := strconv.Atoi(strings.TrimSpace(coresOut))
	f := parseLoad(loadOut, cores, ic.Thresholds.LoadWarnRatio)
	f.Node = ic.Node.Name
	return []Finding{f}, nil
}

type hwDiskUsage struct{}

func (hwDiskUsage) Name() string        { return "hw_disk_usage" }
func (hwDiskUsage) Description() string { return "文件系统使用率" }
func (hwDiskUsage) Scope() Scope        { return NodeScope }
func (hwDiskUsage) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunOnNode("df -B1 -x tmpfs -x devtmpfs")
	if err != nil {
		return []Finding{{Item: "hw_disk_usage", Node: ic.Node.Name, Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	fs := parseDiskUsage(raw, ic.Thresholds.FsWarnPct, ic.Thresholds.FsCritPct)
	if len(fs) == 0 {
		fs = []Finding{{Item: "hw_disk_usage", Level: LevelOK, Summary: "文件系统使用率正常"}}
	}
	for i := range fs {
		fs[i].Node = ic.Node.Name
	}
	return fs, nil
}

type hwDiskSmart struct{}

func (hwDiskSmart) Name() string        { return "hw_disk_smart" }
func (hwDiskSmart) Description() string { return "磁盘 SMART 寿命/自检" }
func (hwDiskSmart) Scope() Scope        { return NodeScope }
func (hwDiskSmart) Inspect(ic *InspectContext) ([]Finding, error) {
	list, err := ic.RunOnNode("lsblk -dno NAME,TYPE | awk '$2==\"disk\"{print $1}'")
	if err != nil {
		return []Finding{{Item: "hw_disk_smart", Node: ic.Node.Name, Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	var out []Finding
	for _, name := range strings.Fields(list) {
		dev := "/dev/" + name
		// smartctl 对正常磁盘也常以非零退出码退出（退出码是状态位掩码），
		// 但 SSHExecutor 在非零退出时仍返回有效 output，所以这里保留 raw、
		// 忽略 err；只有当 output 为空时才用 err 文本兜底。
		raw, e := ic.RunOnNode("smartctl -A -H " + dev + " 2>&1")
		if e != nil && raw == "" {
			raw = e.Error()
		}
		f := parseSmart(dev, raw, ic.Thresholds.DiskLifeWarnPct, ic.Thresholds.DiskLifeCritPct)
		f.Node = ic.Node.Name
		out = append(out, f)
	}
	if len(out) == 0 {
		out = []Finding{{Item: "hw_disk_smart", Node: ic.Node.Name, Level: LevelUnknown, Summary: "未发现物理磁盘"}}
	}
	return out, nil
}

type hwNIC struct{}

func (hwNIC) Name() string        { return "hw_nic" }
func (hwNIC) Description() string { return "bond 成员口 UP 状态" }
func (hwNIC) Scope() Scope        { return NodeScope }
func (hwNIC) Inspect(ic *InspectContext) ([]Finding, error) {
	// Only bond member ports matter; read the bonding files' Slave Interface +
	// per-slave MII Status. Idle standalone NICs are intentionally not checked.
	cmd := "grep -H -E '^(Slave Interface|MII Status)' /proc/net/bonding/bond* 2>/dev/null || echo '(no bonds)'"
	raw, err := ic.RunOnNode(cmd)
	if err != nil {
		return []Finding{{Item: "hw_nic", Node: ic.Node.Name, Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	f := parseNIC(raw)
	f.Node = ic.Node.Name
	return []Finding{f}, nil
}

type hwBond struct{}

func (hwBond) Name() string        { return "hw_bond" }
func (hwBond) Description() string { return "bond 链路状态" }
func (hwBond) Scope() Scope        { return NodeScope }
func (hwBond) Inspect(ic *InspectContext) ([]Finding, error) {
	cmd := "grep -H -E '^(Slave Interface|Link Failure Count|MII Status)' /proc/net/bonding/bond* 2>/dev/null || echo '(no bonds)'"
	raw, err := ic.RunOnNode(cmd)
	if err != nil {
		return []Finding{{Item: "hw_bond", Node: ic.Node.Name, Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	f := parseBond(raw)
	f.Node = ic.Node.Name
	return []Finding{f}, nil
}

// hwPCIeLink checks that NVMe drives and NICs negotiated their full rated PCIe
// link (LnkCap vs LnkSta). A downgrade — fewer lanes or a lower speed — silently
// caps disk/network throughput, so it is worth surfacing in inspection.
type hwPCIeLink struct{}

func (hwPCIeLink) Name() string        { return "hw_pcie_link" }
func (hwPCIeLink) Description() string { return "NVMe/网卡 PCIe 链路速率与宽度" }
func (hwPCIeLink) Scope() Scope        { return NodeScope }
func (hwPCIeLink) Inspect(ic *InspectContext) ([]Finding, error) {
	// Build the want set: NVMe drives (all of /sys/class/nvme/*) + the physical
	// NICs enslaved to a bond (never virtual lo/cali*/bond masters). Each device's
	// BDF is the basename of its `device` symlink — readlink resolves it directly,
	// no path-pattern guessing, and virtual devices (no symlink) drop out.
	want := map[string]pcieMeta{}

	nvmeList, _ := ic.RunOnNode("ls /sys/class/nvme/ 2>/dev/null || true")
	for _, dev := range strings.Fields(nvmeList) {
		if bdf := resolveDeviceBDF(ic, "/sys/class/nvme/"+dev+"/device"); bdf != "" {
			want[bdf] = pcieMeta{kind: "NVMe", name: dev}
		}
	}

	bondRaw, _ := ic.RunOnNode("grep -H '^Slave Interface' /proc/net/bonding/bond* 2>/dev/null || true")
	for _, nic := range parseBondSlaveNames(bondRaw) {
		if bdf := resolveDeviceBDF(ic, "/sys/class/net/"+nic+"/device"); bdf != "" {
			want[bdf] = pcieMeta{kind: "NIC", name: nic}
		}
	}

	if len(want) == 0 {
		return []Finding{{Item: "hw_pcie_link", Node: ic.Node.Name, Level: LevelOK,
			Summary: "未发现可检测的 NVMe/bond 网卡 PCIe 链路"}}, nil
	}

	// Single lspci pass for all devices. CombinedOutput captures "command not
	// found" into raw, so detect a missing binary the way hwDiskSmart does.
	raw, e := ic.RunOnNode("lspci -Dvvv 2>&1")
	if e != nil && raw == "" {
		raw = e.Error()
	}
	if strings.Contains(raw, "command not found") || strings.Contains(raw, "not installed") {
		return []Finding{{Item: "hw_pcie_link", Node: ic.Node.Name, Level: LevelUnknown,
			Summary: "lspci 不可用", Advice: "节点未安装 pciutils，建议 `yum install pciutils`"}}, nil
	}

	devs := parseLspciLinks(raw, want)
	sortPCIeDevs(devs)
	out := evalPCIeLinks(devs, ic.Thresholds.PcieMinSpeedGTS)
	for i := range out {
		out[i].Node = ic.Node.Name
	}
	return out, nil
}

// resolveDeviceBDF reads a device's PCI BDF from its sysfs `device` symlink. The
// resolved target's basename is the BDF; an absent symlink (virtual device) or
// non-PCI parent yields "".
func resolveDeviceBDF(ic *InspectContext, devicePath string) string {
	out, err := ic.RunOnNode("readlink -f " + devicePath)
	if err != nil && out == "" {
		return ""
	}
	return bdfFromReadlink(out)
}
