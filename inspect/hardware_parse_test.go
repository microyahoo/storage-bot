package inspect

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseMemory(t *testing.T) {
	raw := "              total        used        free      shared  buff/cache   available\nMem:    100         95          1           0           4           2\n"
	f := parseMemory(raw, 90, 95)
	if f.Level != LevelCritical {
		t.Errorf("95%% used → %v, want Critical", f.Level)
	}
	ok := parseMemory("Mem:  100  50  40  0  10  45\n", 90, 95)
	if ok.Level != LevelOK {
		t.Errorf("50%% → %v, want OK", ok.Level)
	}
}

func TestParseDiskUsage(t *testing.T) {
	raw := "Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda1 100 92 8 92% /\n/dev/sdb1 100 50 50 50% /data\n"
	fs := parseDiskUsage(raw, 85, 90)
	if len(fs) != 1 {
		t.Fatalf("only the 92%% mount should be abnormal, got %d findings", len(fs))
	}
	if fs[0].Level != LevelCritical || fs[0].Metrics["mount"] != "/" {
		t.Errorf("got %+v", fs[0])
	}
}

func TestParseSmartNVMe(t *testing.T) {
	raw := "SMART overall-health self-assessment test result: PASSED\nPercentage Used:  85%\n"
	f := parseSmart("/dev/nvme0n1", raw, 80, 90)
	if f.Level != LevelWarn {
		t.Errorf("85%% used, warn=80 → %v, want Warn", f.Level)
	}
	failed := parseSmart("/dev/sda", "SMART overall-health self-assessment test result: FAILED\n", 80, 90)
	if failed.Level != LevelCritical {
		t.Errorf("FAILED → %v, want Critical", failed.Level)
	}
}

func TestParseSmartMissing(t *testing.T) {
	f := parseSmart("/dev/sda", "smartctl: command not found", 80, 90)
	if f.Level != LevelUnknown {
		t.Errorf("missing smartctl → %v, want Unknown", f.Level)
	}
}

func TestParseSmartHealthStatus(t *testing.T) {
	// SCSI/RAID 盘（如系统盘 RAID1、SAS HDD）无寿命百分比，健康体现在
	// "SMART Health Status:" 行——曾被误判为"无法解析"。
	raw := "=== START OF READ SMART DATA SECTION ===\n" +
		"SMART Health Status: OK\n" +
		"Current Drive Temperature:     0 C\n"
	if f := parseSmart("/dev/sdk", raw, 80, 90); f.Level != LevelOK {
		t.Errorf("Health Status OK → %v, want OK (summary %q)", f.Level, f.Summary)
	}
	bad := parseSmart("/dev/sdk", "SMART Health Status: FAILED\n", 80, 90)
	if bad.Level != LevelCritical {
		t.Errorf("Health Status FAILED → %v, want Critical", bad.Level)
	}
}

func TestParseLoad(t *testing.T) {
	raw := " 14:02:01 up 10 days,  load average: 18.0, 5.0, 4.0\n"
	f := parseLoad(raw, 8, 2.0)
	if f.Level != LevelWarn {
		t.Errorf("ratio 2.25 → %v, want Warn", f.Level)
	}
	ok := parseLoad(" load average: 4.0, 3.0, 2.0\n", 8, 2.0)
	if ok.Level != LevelOK {
		t.Errorf("ratio 0.5 → %v, want OK", ok.Level)
	}
}

func TestParseNIC(t *testing.T) {
	// hw_nic checks only bond member ports via the bonding file's per-slave
	// "MII Status". A slave with non-up MII → Warn; no bonds → OK.
	noBonds := "(no bonds)"
	if parseNIC(noBonds).Level != LevelOK {
		t.Errorf("no bonds → %v, want OK", parseNIC(noBonds).Level)
	}

	allUp := "/proc/net/bonding/bond0:Slave Interface: eth0\n" +
		"/proc/net/bonding/bond0:MII Status: up\n" +
		"/proc/net/bonding/bond0:Slave Interface: eth1\n" +
		"/proc/net/bonding/bond0:MII Status: up\n"
	if f := parseNIC(allUp); f.Level != LevelOK {
		t.Errorf("all slaves up → %v, want OK", f.Level)
	}

	oneDown := "/proc/net/bonding/bond0:Slave Interface: eth0\n" +
		"/proc/net/bonding/bond0:MII Status: up\n" +
		"/proc/net/bonding/bond0:Slave Interface: eth1\n" +
		"/proc/net/bonding/bond0:MII Status: down\n"
	f := parseNIC(oneDown)
	if f.Level != LevelWarn {
		t.Errorf("one slave down → %v, want Warn", f.Level)
	}
	if !strings.Contains(f.Summary, "eth1") {
		t.Errorf("summary should name the down slave eth1, got %q", f.Summary)
	}
}

func TestParseBond(t *testing.T) {
	if f := parseBond("(no bonds)"); f.Level != LevelOK {
		t.Errorf("no bonds → %v, want OK", f.Level)
	}

	warn := "/proc/net/bonding/bond0:Link Failure Count: 3\n/proc/net/bonding/bond0:MII Status: up\n"
	f := parseBond(warn)
	if f.Level != LevelWarn {
		t.Errorf("link failure → %v, want Warn", f.Level)
	}
	if f.Metrics["link_failure_total"] != "3" {
		t.Errorf("link_failure_total = %q, want \"3\"", f.Metrics["link_failure_total"])
	}

	// 多个 bond 求和后写入 total。
	sum := "/proc/net/bonding/bond0:Link Failure Count: 3\n" +
		"/proc/net/bonding/bond0:MII Status: up\n" +
		"/proc/net/bonding/bond1:Link Failure Count: 5\n" +
		"/proc/net/bonding/bond1:MII Status: up\n"
	if f := parseBond(sum); f.Metrics["link_failure_total"] != "8" {
		t.Errorf("summed link_failure_total = %q, want \"8\"", f.Metrics["link_failure_total"])
	}

	// failTotal==0 仍写 total=0，级别 OK。
	zero := "/proc/net/bonding/bond0:Link Failure Count: 0\n/proc/net/bonding/bond0:MII Status: up\n"
	if f := parseBond(zero); f.Level != LevelOK || f.Metrics["link_failure_total"] != "0" {
		t.Errorf("zero failures → level %v total %q, want OK \"0\"", f.Level, f.Metrics["link_failure_total"])
	}

	crit := "/proc/net/bonding/bond0:Link Failure Count: 3\n/proc/net/bonding/bond0:MII Status: down\n"
	f = parseBond(crit)
	if f.Level != LevelCritical {
		t.Errorf("MII down → %v, want Critical", f.Level)
	}
	if f.Metrics["link_failure_total"] != "3" {
		t.Errorf("critical still records total, got %q", f.Metrics["link_failure_total"])
	}
}

func TestParseBondSlaveNames(t *testing.T) {
	// `grep -H '^Slave Interface' /proc/net/bonding/bond*` shape. Only physical
	// bond members are returned; virtual interfaces never appear here.
	raw := "/proc/net/bonding/bond0:Slave Interface: eth0\n" +
		"/proc/net/bonding/bond0:Slave Interface: eth1\n" +
		"/proc/net/bonding/bond1:Slave Interface: ens1f0\n"
	got := parseBondSlaveNames(raw)
	want := []string{"eth0", "eth1", "ens1f0"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("slave[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if n := parseBondSlaveNames(""); len(n) != 0 {
		t.Errorf("no bonds → want empty, got %v", n)
	}
}

func TestBDFFromReadlink(t *testing.T) {
	// `readlink -f /sys/class/nvme/nvme0/device` resolves to the PCI dir; its
	// basename is the BDF.
	cases := map[string]string{
		"/sys/devices/pci0000:00/0000:00:1d.0/0000:01:00.0\n": "0000:01:00.0",
		"/sys/devices/pci0000:00/0000:00:1c.0/0000:03:00.0":   "0000:03:00.0",
		"":                          "", // missing symlink
		"/sys/devices/virtual/foo": "", // virtual device, basename not a BDF
	}
	for raw, want := range cases {
		if got := bdfFromReadlink(raw); got != want {
			t.Errorf("bdfFromReadlink(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Real-world shape: an NVMe rated 32GT/s x4 negotiated down to 16GT/s x4 (the
// user's reported case) → speed-only Warn. A second drive losing lanes (x4→x1)
// → Critical. kind/name come from the want set (sysfs), not the lspci text.
func TestParseAndEvalPCIeLinks(t *testing.T) {
	raw := "0000:01:00.0 Non-Volatile memory controller: Samsung Electronics Co Ltd NVMe SSD PM173X\n" +
		"\tLnkCap:\tPort #0, Speed 32GT/s, Width x4, ASPM not supported\n" +
		"\tLnkSta:\tSpeed 16GT/s (downgraded), Width x4 (ok)\n" +
		"0000:02:00.0 Non-Volatile memory controller: Samsung Electronics Co Ltd NVMe SSD PM173X\n" +
		"\tLnkCap:\tPort #0, Speed 32GT/s, Width x4\n" +
		"\tLnkSta:\tSpeed 32GT/s, Width x1 (downgraded)\n" +
		"0000:03:00.0 Ethernet controller: Mellanox Technologies MT2910 ConnectX-7\n" +
		"\tLnkCap:\tPort #0, Speed 16GT/s, Width x8\n" +
		"\tLnkSta:\tSpeed 16GT/s, Width x8\n" +
		"0000:00:1f.0 ISA bridge: Intel Corporation C620\n"

	want := map[string]pcieMeta{
		"0000:01:00.0": {kind: "NVMe", name: "nvme0"},
		"0000:02:00.0": {kind: "NVMe", name: "nvme1"},
		"0000:03:00.0": {kind: "NIC", name: "eth0"},
		// 0000:00:1f.0 (ISA bridge) intentionally absent → must be skipped.
	}
	devs := parseLspciLinks(raw, want)
	if len(devs) != 3 {
		t.Fatalf("want 3 tracked devices, got %d", len(devs))
	}
	if devs[0].name != "nvme0" || devs[0].kind != "NVMe" {
		t.Errorf("device 0 = %+v, want nvme0/NVMe", devs[0])
	}

	findings := evalPCIeLinks(devs, 0)
	byLevel := map[Level]int{}
	for _, f := range findings {
		byLevel[f.Level]++
	}
	if byLevel[LevelWarn] != 1 {
		t.Errorf("want 1 Warn (speed downgrade), got %d (findings=%+v)", byLevel[LevelWarn], findings)
	}
	if byLevel[LevelCritical] != 1 {
		t.Errorf("want 1 Critical (width downgrade), got %d", byLevel[LevelCritical])
	}
	// The healthy NIC and the skipped ISA bridge must not produce a finding.
	if byLevel[LevelOK] != 0 {
		t.Errorf("healthy devices should be silent when others are degraded, got %d OK", byLevel[LevelOK])
	}
}

func TestEvalPCIeLinksAllOK(t *testing.T) {
	devs := []*pcieDev{{
		bdf: "0000:01:00.0", kind: "NVMe", name: "nvme0",
		capSpeed: 32, capWidth: 4, staSpeed: 32, staWidth: 4,
	}}
	f := evalPCIeLinks(devs, 0)
	if len(f) != 1 || f[0].Level != LevelOK {
		t.Errorf("all matched → want single OK, got %+v", f)
	}
}

func TestEvalPCIeLinksNoDevices(t *testing.T) {
	f := evalPCIeLinks(nil, 0)
	if len(f) != 1 || f[0].Level != LevelOK {
		t.Fatalf("no devices → want single OK, got %+v", f)
	}
	if !strings.Contains(f[0].Summary, "未发现") {
		t.Errorf("no-device summary = %q", f[0].Summary)
	}
}

// 22 NVMe drives all downgraded 32→16GT/s x4 must collapse to ONE finding with
// "×22" in the summary and every device listed in Detail.
func TestEvalPCIeLinksMergesBySignature(t *testing.T) {
	var devs []*pcieDev
	for i := 0; i < 22; i++ {
		devs = append(devs, &pcieDev{
			bdf: fmt.Sprintf("0000:%02d:00.0", i+1), kind: "NVMe", name: fmt.Sprintf("nvme%d", i),
			capSpeed: 32, capWidth: 4, staSpeed: 16, staWidth: 4,
			capRaw: "Speed 32GT/s, Width x4", staRaw: "Speed 16GT/s, Width x4",
		})
	}
	f := evalPCIeLinks(devs, 0)
	if len(f) != 1 {
		t.Fatalf("22 identical downgrades → want 1 merged finding, got %d", len(f))
	}
	if f[0].Level != LevelWarn {
		t.Errorf("speed-only → want Warn, got %v", f[0].Level)
	}
	if !strings.Contains(f[0].Summary, "×22") {
		t.Errorf("summary should show ×22, got %q", f[0].Summary)
	}
	if f[0].Metrics["count"] != "22" {
		t.Errorf("count metric = %q, want 22", f[0].Metrics["count"])
	}
	if !strings.Contains(f[0].Detail, "nvme0") || !strings.Contains(f[0].Detail, "nvme21") {
		t.Errorf("Detail should list all devices, got %q", f[0].Detail)
	}
}

// Mixed signatures within a node split into separate findings; different speed
// transitions don't merge.
func TestEvalPCIeLinksDistinctSignatures(t *testing.T) {
	devs := []*pcieDev{
		{bdf: "0000:01:00.0", kind: "NVMe", name: "nvme0", capSpeed: 32, capWidth: 4, staSpeed: 16, staWidth: 4},
		{bdf: "0000:02:00.0", kind: "NVMe", name: "nvme1", capSpeed: 32, capWidth: 4, staSpeed: 8, staWidth: 4},  // different speed
		{bdf: "0000:03:00.0", kind: "NVMe", name: "nvme2", capSpeed: 32, capWidth: 4, staSpeed: 32, staWidth: 1}, // width loss
	}
	f := evalPCIeLinks(devs, 0)
	if len(f) != 3 {
		t.Fatalf("3 distinct signatures → want 3 findings, got %d", len(f))
	}
}

// pcie_min_speed_gts silences intentional speed downgrades (≥ floor) but never
// width downgrades.
func TestEvalPCIeLinksSilenceThreshold(t *testing.T) {
	devs := []*pcieDev{
		// speed-only 32→16, ≥16 floor → silenced
		{bdf: "0000:01:00.0", kind: "NVMe", name: "nvme0", capSpeed: 32, capWidth: 4, staSpeed: 16, staWidth: 4},
		// speed-only 32→8, below 16 floor → still Warn
		{bdf: "0000:02:00.0", kind: "NVMe", name: "nvme1", capSpeed: 32, capWidth: 4, staSpeed: 8, staWidth: 4},
		// width loss → Critical regardless of floor
		{bdf: "0000:03:00.0", kind: "NVMe", name: "nvme2", capSpeed: 16, capWidth: 4, staSpeed: 16, staWidth: 2},
	}
	f := evalPCIeLinks(devs, 16)
	byLevel := map[Level]int{}
	for _, x := range f {
		byLevel[x.Level]++
	}
	if byLevel[LevelWarn] != 1 {
		t.Errorf("want 1 Warn (32→8 below floor), got %d", byLevel[LevelWarn])
	}
	if byLevel[LevelCritical] != 1 {
		t.Errorf("want 1 Critical (width loss, floor doesn't apply), got %d", byLevel[LevelCritical])
	}

	// All silenced → single OK that mentions the intentional downgrade.
	allSilenced := []*pcieDev{
		{bdf: "0000:01:00.0", kind: "NVMe", name: "nvme0", capSpeed: 32, capWidth: 4, staSpeed: 16, staWidth: 4},
		{bdf: "0000:02:00.0", kind: "NVMe", name: "nvme1", capSpeed: 32, capWidth: 4, staSpeed: 16, staWidth: 4},
	}
	ok := evalPCIeLinks(allSilenced, 16)
	if len(ok) != 1 || ok[0].Level != LevelOK {
		t.Fatalf("all silenced → want single OK, got %+v", ok)
	}
	if !strings.Contains(ok[0].Summary, "故意配置") {
		t.Errorf("silenced OK summary should note intentional downgrade, got %q", ok[0].Summary)
	}
}
