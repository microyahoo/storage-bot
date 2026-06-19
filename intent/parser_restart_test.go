package intent

import "testing"

func TestParseRestartMon(t *testing.T) {
	clusters := []string{"cluster-01"}
	a := ParseWithAll("重启 mon a cluster-01", clusters, nil, nil)
	if a.Type != ActionSkill || a.SkillName != "restart_mon" {
		t.Fatalf("→ type=%v skill=%q, want ActionSkill/restart_mon", a.Type, a.SkillName)
	}
	if a.Args["id"] != "a" {
		t.Errorf("id = %q, want a", a.Args["id"])
	}
	if a.Args["yes"] == "true" {
		t.Errorf("no --yes → yes should not be set")
	}
	if a.ClusterName != "cluster-01" {
		t.Errorf("cluster = %q", a.ClusterName)
	}
}

func TestParseRestartMgrWithYes(t *testing.T) {
	a := ParseWithAll("restart mgr b cluster-01 --yes", []string{"cluster-01"}, nil, nil)
	if a.SkillName != "restart_mgr" {
		t.Fatalf("skill = %q, want restart_mgr", a.SkillName)
	}
	if a.Args["id"] != "b" {
		t.Errorf("id = %q, want b", a.Args["id"])
	}
	if a.Args["yes"] != "true" {
		t.Errorf("--yes should set yes=true, got %q", a.Args["yes"])
	}
}

func TestParseRestartMonNoID(t *testing.T) {
	// "重启 mon cluster-01" — no id; should still route to restart_mon with empty id
	a := ParseWithAll("重启 mon cluster-01", []string{"cluster-01"}, nil, nil)
	if a.SkillName != "restart_mon" {
		t.Fatalf("skill = %q, want restart_mon", a.SkillName)
	}
	if a.Args["id"] != "" {
		t.Errorf("no id given, got id=%q", a.Args["id"])
	}
}

func TestRestartMonNotHijackedByMonStatus(t *testing.T) {
	// Ensure restart_* is matched before mon_status (alias "mon").
	a := ParseWithAll("重启 mon a cluster-01", []string{"cluster-01"}, nil, nil)
	if a.SkillName == "mon_status" {
		t.Errorf("重启 mon a should not be mon_status")
	}
}
