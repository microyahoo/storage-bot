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
	if !names(node)["hw_pcie_link"] {
		t.Errorf("hw_pcie_link not in node scope")
	}
	if len(r.All()) != 14 {
		t.Errorf("total inspectors = %d, want 14", len(r.All()))
	}
	if len(cluster) != 7 {
		t.Errorf("cluster-scope inspectors = %d, want 7", len(cluster))
	}
	if len(node) != 7 {
		t.Errorf("node-scope inspectors = %d, want 7", len(node))
	}
}
