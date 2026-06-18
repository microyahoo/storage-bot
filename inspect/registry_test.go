package inspect

import "testing"

func TestRegistryScopes(t *testing.T) {
	r := NewRegistry()
	cluster := r.ByScope(ClusterScope)
	node := r.ByScope(NodeScope)
	if len(cluster) == 0 || len(node) == 0 {
		t.Fatalf("registry empty: cluster=%d node=%d", len(cluster), len(node))
	}
	names := func(in []Inspector) map[string]bool {
		m := map[string]bool{}
		for _, i := range in {
			m[i.Name()] = true
		}
		return m
	}
	if !names(cluster)["ceph_health"] {
		t.Errorf("ceph_health not in cluster scope")
	}
	if !names(node)["hw_memory"] {
		t.Errorf("hw_memory not in node scope")
	}
}
