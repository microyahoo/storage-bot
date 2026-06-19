package intent

import "testing"

func TestParseInspectIntent(t *testing.T) {
	clusters := []string{"prod-ceph-01"}
	for _, msg := range []string{"巡检 prod-ceph-01", "检查一下 prod-ceph-01 集群", "体检 prod-ceph-01"} {
		a := ParseWithAll(msg, clusters, nil, nil)
		if a.Type != ActionInspect {
			t.Errorf("%q → %v, want ActionInspect", msg, a.Type)
		}
		if a.ClusterName != "prod-ceph-01" {
			t.Errorf("%q → cluster %q", msg, a.ClusterName)
		}
	}
}

func TestParseInspectAll(t *testing.T) {
	a := ParseWithAll("巡检所有集群", []string{"a", "b"}, nil, nil)
	if a.Type != ActionInspect {
		t.Fatalf("want ActionInspect, got %v", a.Type)
	}
	if a.ClusterName != "" {
		t.Errorf("巡检所有集群 should leave ClusterName empty (means all), got %q", a.ClusterName)
	}
}
