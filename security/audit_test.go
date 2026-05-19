package security

import "testing"

func TestAuditLog_RingBufferBounded(t *testing.T) {
	a := NewAuditLog(3)

	for i := 0; i < 10; i++ {
		a.Record("u", "c", "a", "cmd", "ok")
	}

	if len(a.buf) != 3 {
		t.Errorf("underlying buffer should stay at capacity 3, got %d", len(a.buf))
	}
	if a.count != 3 {
		t.Errorf("count should be 3, got %d", a.count)
	}

	recent := a.Recent(10)
	if len(recent) != 3 {
		t.Errorf("Recent(10) should return 3 entries, got %d", len(recent))
	}
}

func TestAuditLog_RecentOrder(t *testing.T) {
	a := NewAuditLog(3)
	a.Record("u", "c1", "a", "cmd", "ok")
	a.Record("u", "c2", "a", "cmd", "ok")
	a.Record("u", "c3", "a", "cmd", "ok")
	a.Record("u", "c4", "a", "cmd", "ok") // overwrites c1
	a.Record("u", "c5", "a", "cmd", "ok") // overwrites c2

	recent := a.Recent(3)
	want := []string{"c3", "c4", "c5"}
	for i, e := range recent {
		if e.ClusterName != want[i] {
			t.Errorf("entry %d: got %s, want %s", i, e.ClusterName, want[i])
		}
	}
}

func TestAuditLog_PartialFill(t *testing.T) {
	a := NewAuditLog(5)
	a.Record("u", "c1", "a", "cmd", "ok")
	a.Record("u", "c2", "a", "cmd", "ok")

	recent := a.Recent(5)
	if len(recent) != 2 {
		t.Fatalf("got %d entries, want 2", len(recent))
	}
	if recent[0].ClusterName != "c1" || recent[1].ClusterName != "c2" {
		t.Errorf("ordering wrong: %s, %s", recent[0].ClusterName, recent[1].ClusterName)
	}
}
