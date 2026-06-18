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
