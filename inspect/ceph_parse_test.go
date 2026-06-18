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
