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
	raw, err := ic.RunOnNode("nproc; uptime")
	if err != nil {
		return []Finding{{Item: "hw_cpu", Node: ic.Node.Name, Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	cores := 0
	if parts := strings.SplitN(strings.TrimSpace(raw), "\n", 2); len(parts) > 0 {
		cores, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
	}
	f := parseLoad(raw, cores, ic.Thresholds.LoadWarnRatio)
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
func (hwNIC) Description() string { return "物理网卡 UP 状态" }
func (hwNIC) Scope() Scope        { return NodeScope }
func (hwNIC) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunOnNode("ip -br link show")
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
