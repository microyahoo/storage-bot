package inspect

import (
	"testing"
	"time"
)

func TestStoreSaveListLoadPrune(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, 2) // keep 2

	base := time.Date(2026, 6, 17, 3, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		r := &Report{Cluster: "c1", StartedAt: base.Add(time.Duration(i) * time.Hour)}
		r.Finalize()
		if err := s.Save(r); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	list, err := s.List("c1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("after prune keep=2, got %d entries", len(list))
	}

	r, err := s.Load("c1", list[0])
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if r.Cluster != "c1" {
		t.Errorf("loaded cluster = %q", r.Cluster)
	}
}

func TestStoreClustersAndRewrite(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, 30)

	// Empty store → no clusters.
	if cs, err := s.Clusters(); err != nil || len(cs) != 0 {
		t.Fatalf("empty Clusters() = %v, %v; want [], nil", cs, err)
	}

	base := time.Date(2026, 6, 17, 3, 0, 0, 0, time.UTC)
	for _, name := range []string{"c2", "c1"} {
		r := &Report{Cluster: name, StartedAt: base}
		if err := s.Save(r); err != nil {
			t.Fatalf("save %s: %v", name, err)
		}
	}

	cs, err := s.Clusters()
	if err != nil {
		t.Fatalf("Clusters: %v", err)
	}
	if len(cs) != 2 || cs[0] != "c1" || cs[1] != "c2" {
		t.Errorf("Clusters() = %v, want sorted [c1 c2]", cs)
	}

	// RewriteReport overwrites in place; the filename set is unchanged.
	list, _ := s.List("c1")
	name := list[0]
	r, _ := s.Load("c1", name)
	r.Findings = append(r.Findings, Finding{Item: "hw_bond", Level: LevelOK, Summary: "x"})
	if err := s.RewriteReport("c1", name, r); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	after, _ := s.List("c1")
	if len(after) != 1 || after[0] != name {
		t.Errorf("filenames changed after rewrite: %v, want [%s]", after, name)
	}
	reloaded, _ := s.Load("c1", name)
	if len(reloaded.Findings) != 1 {
		t.Errorf("rewritten report findings = %d, want 1", len(reloaded.Findings))
	}
}
