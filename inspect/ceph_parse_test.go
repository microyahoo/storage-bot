package inspect

import "testing"

func TestParseCephHealth(t *testing.T) {
	if f := parseCephHealth("HEALTH_OK\n"); f.Level != LevelOK {
		t.Errorf("HEALTH_OK → %v", f.Level)
	}
	if f := parseCephHealth("HEALTH_WARN 1 daemons...\n"); f.Level != LevelWarn {
		t.Errorf("HEALTH_WARN → %v", f.Level)
	}
	if f := parseCephHealth("HEALTH_ERR 2 pgs inconsistent\n"); f.Level != LevelCritical {
		t.Errorf("HEALTH_ERR → %v", f.Level)
	}
	if f := parseCephHealth("garbage output"); f.Level != LevelUnknown {
		t.Errorf("unparseable → %v, want Unknown", f.Level)
	}
}

func TestParseOSDStat(t *testing.T) {
	f := parseOSDStat("36 osds: 34 up (since 2h), 35 in (since 3h); ...")
	if f.Level != LevelCritical {
		t.Errorf("2 down 1 out → %v, want Critical", f.Level)
	}
	if f.Metrics["osd_down"] != "2" || f.Metrics["osd_total"] != "36" {
		t.Errorf("metrics = %v", f.Metrics)
	}
	healthy := parseOSDStat("36 osds: 36 up (since 2h), 36 in (since 3h)")
	if healthy.Level != LevelOK {
		t.Errorf("all up/in → %v, want OK", healthy.Level)
	}
}

func TestParseCephDF(t *testing.T) {
	raw := "--- RAW STORAGE ---\nCLASS  SIZE  AVAIL  USED  RAW USED  %RAW USED\nTOTAL  100 TiB  18 TiB  82 TiB  82 TiB  82.00\n"
	f := parseCephDF(raw, 80, 90)
	if f.Level != LevelWarn {
		t.Errorf("82%% with warn=80 → %v, want Warn", f.Level)
	}
	if f.Metrics["used_pct"] != "82.00" {
		t.Errorf("used_pct = %q", f.Metrics["used_pct"])
	}
	crit := parseCephDF("TOTAL  100 TiB  5 TiB  95 TiB  95 TiB  95.00\n", 80, 90)
	if crit.Level != LevelCritical {
		t.Errorf("95%% → %v, want Critical", crit.Level)
	}
}

func TestParseMonQuorum(t *testing.T) {
	ok := `{"quorum":[0,1,2],"monmap":{"mons":[{"name":"a"},{"name":"b"},{"name":"c"}]}}`
	if f := parseMonQuorum(ok); f.Level != LevelOK {
		t.Errorf("3/3 quorum → %v, want OK", f.Level)
	}
	bad := `{"quorum":[0],"monmap":{"mons":[{"name":"a"},{"name":"b"},{"name":"c"}]}}`
	if f := parseMonQuorum(bad); f.Level != LevelCritical {
		t.Errorf("1/3 quorum → %v, want Critical", f.Level)
	}
	if f := parseMonQuorum("not json"); f.Level != LevelUnknown {
		t.Errorf("bad json → %v, want Unknown", f.Level)
	}
}

func TestParsePGStat(t *testing.T) {
	if f := parsePGStat("100 pgs: 100 active+clean"); f.Level != LevelOK {
		t.Errorf("active+clean → %v, want OK", f.Level)
	}
	if f := parsePGStat("90 active+clean, 10 active+undersized+degraded"); f.Level != LevelWarn {
		t.Errorf("degraded → %v, want Warn", f.Level)
	}
	if f := parsePGStat("5 stale+inactive"); f.Level != LevelCritical {
		t.Errorf("inactive → %v, want Critical", f.Level)
	}
}

func TestParseSlowOps(t *testing.T) {
	if f := parseSlowOps("HEALTH_WARN 3 slow ops, oldest one blocked"); f.Level != LevelWarn {
		t.Errorf("slow ops → %v, want Warn", f.Level)
	}
	if f := parseSlowOps("HEALTH_OK"); f.Level != LevelOK {
		t.Errorf("no slow ops → %v, want OK", f.Level)
	}
}

func TestParseCrashLs(t *testing.T) {
	if f := parseCrashLs(""); f.Level != LevelOK {
		t.Errorf("no crash → %v, want OK", f.Level)
	}
	if f := parseCrashLs("2026-06-17_01:02:03.456_abcd\n2026-06-17_02:03:04.789_efgh\n"); f.Level != LevelWarn {
		t.Errorf("crashes → %v, want Warn", f.Level)
	}
}
