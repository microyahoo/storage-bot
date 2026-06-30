package inspect

import (
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
	if f := parseBond(warn); f.Level != LevelWarn {
		t.Errorf("link failure → %v, want Warn", f.Level)
	}
	crit := "/proc/net/bonding/bond0:MII Status: down\n"
	if f := parseBond(crit); f.Level != LevelCritical {
		t.Errorf("MII down → %v, want Critical", f.Level)
	}
}

func TestParseSysClassLinks(t *testing.T) {
	// `ls -l /sys/class/nvme/` and `/sys/class/net/` style output. Virtual
	// interfaces (lo, bond0) have no PCI BDF in their target and are skipped.
	raw := "total 0\n" +
		"lrwxrwxrwx 1 root root 0 Jun 30 10:00 nvme0 -> ../../devices/pci0000:00/0000:00:1d.0/0000:01:00.0/nvme/nvme0\n" +
		"lrwxrwxrwx 1 root root 0 Jun 30 10:00 nvme1 -> ../../devices/pci0000:00/0000:00:1d.4/0000:02:00.0/nvme/nvme1\n"
	m := parseSysClassLinks(raw)
	if m["0000:01:00.0"] != "nvme0" {
		t.Errorf("nvme0 BDF mapping wrong: %v", m)
	}
	if m["0000:02:00.0"] != "nvme1" {
		t.Errorf("nvme1 BDF mapping wrong: %v", m)
	}

	netRaw := "lo -> ../../devices/virtual/net/lo\n" +
		"eth0 -> ../../devices/pci0000:00/0000:00:1c.0/0000:03:00.0/net/eth0\n" +
		"bond0 -> ../../devices/virtual/net/bond0\n"
	nm := parseSysClassLinks(netRaw)
	if nm["0000:03:00.0"] != "eth0" {
		t.Errorf("eth0 mapping wrong: %v", nm)
	}
	if len(nm) != 1 {
		t.Errorf("virtual lo/bond0 should be skipped, got %v", nm)
	}
}

// Real-world shape: an NVMe rated 32GT/s x4 negotiated down to 16GT/s x4 (the
// user's reported case) → speed-only Warn. A second drive losing lanes (x4→x1)
// → Critical.
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

	names := map[string]string{"0000:01:00.0": "nvme0", "0000:03:00.0": "eth0"}
	devs := parseLspciLinks(raw, names)
	if len(devs) != 3 {
		t.Fatalf("want 3 tracked devices (2 NVMe + 1 NIC), got %d", len(devs))
	}
	if devs[0].name != "nvme0" || devs[0].kind != "NVMe" {
		t.Errorf("device 0 = %+v, want nvme0/NVMe", devs[0])
	}

	findings := evalPCIeLinks(devs)
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
	// The healthy NIC and the ISA bridge must not produce a finding.
	if byLevel[LevelOK] != 0 {
		t.Errorf("healthy devices should be silent when others are degraded, got %d OK", byLevel[LevelOK])
	}
}

func TestEvalPCIeLinksAllOK(t *testing.T) {
	devs := []*pcieDev{{
		bdf: "0000:01:00.0", kind: "NVMe", name: "nvme0",
		capSpeed: 32, capWidth: 4, staSpeed: 32, staWidth: 4,
	}}
	f := evalPCIeLinks(devs)
	if len(f) != 1 || f[0].Level != LevelOK {
		t.Errorf("all matched → want single OK, got %+v", f)
	}
}

func TestEvalPCIeLinksNoDevices(t *testing.T) {
	f := evalPCIeLinks(nil)
	if len(f) != 1 || f[0].Level != LevelOK {
		t.Fatalf("no devices → want single OK, got %+v", f)
	}
	if !strings.Contains(f[0].Summary, "未发现") {
		t.Errorf("no-device summary = %q", f[0].Summary)
	}
}
