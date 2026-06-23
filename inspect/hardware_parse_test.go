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
