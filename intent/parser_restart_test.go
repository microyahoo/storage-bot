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

// Regression: "restart mon cdn" (no id) must not consume the cluster token
// "cdn" as the daemon id. id stays empty (→ skill lists candidates). When the
// cluster name is exactly "cdn" it also resolves as the cluster.
func TestRestartMonNoIDKeepsCluster(t *testing.T) {
	// Exact name "cdn": both cluster resolved and id empty.
	a := ParseWithAll("restart mon cdn", []string{"cdn"}, nil, nil)
	if a.SkillName != "restart_mon" || a.ClusterName != "cdn" || a.Args["id"] != "" {
		t.Errorf("restart mon cdn [cdn] → skill=%q cluster=%q id=%q, want restart_mon/cdn/empty",
			a.SkillName, a.ClusterName, a.Args["id"])
	}
	// Shorthand "cdn" for "cdn-01": cluster may not resolve (whole-word limit),
	// but "cdn" must NOT be taken as the daemon id.
	for _, clusters := range [][]string{{"cdn-01"}, {"cdn", "cdn-01"}} {
		a := ParseWithAll("restart mon cdn", clusters, nil, nil)
		if a.Args["id"] != "" {
			t.Errorf("clusters=%v: id=%q, want empty (cdn is a cluster fragment, not an id)", clusters, a.Args["id"])
		}
	}
}

func TestRestartMonNotHijackedByMonStatus(t *testing.T) {
	// Ensure restart_* is matched before mon_status (alias "mon").
	a := ParseWithAll("重启 mon a cluster-01", []string{"cluster-01"}, nil, nil)
	if a.SkillName == "mon_status" {
		t.Errorf("重启 mon a should not be mon_status")
	}
}

// Regression: "restart mon a cdn" must resolve cluster=cdn, id=a — the daemon
// id "a" must NOT be picked up as a cluster prefix ("a*").
func TestRestartMonIDNotMistakenForCluster(t *testing.T) {
	a := ParseWithAll("restart mon a cdn", []string{"cdn"}, nil, nil)
	if a.SkillName != "restart_mon" {
		t.Fatalf("skill = %q, want restart_mon", a.SkillName)
	}
	if a.Args["id"] != "a" {
		t.Errorf("id = %q, want a", a.Args["id"])
	}
	if a.ClusterName != "cdn" {
		t.Errorf("cluster = %q, want cdn (id 'a' leaked into cluster match)", a.ClusterName)
	}
}

// Same with multiple cdn-prefixed clusters present: id must still not broadcast.
func TestRestartMonIDNotBroadcast(t *testing.T) {
	a := ParseWithAll("restart mon a cdn-01", []string{"cdn-01", "cdn-02"}, nil, nil)
	if a.ClusterName != "cdn-01" {
		t.Errorf("cluster = %q, want cdn-01", a.ClusterName)
	}
	if a.Args["id"] != "a" {
		t.Errorf("id = %q, want a", a.Args["id"])
	}
}
