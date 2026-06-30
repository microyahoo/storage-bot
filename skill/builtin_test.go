package skill

import (
	"strings"
	"testing"
)

// Representative grep output of "Slave Interface" / "MII Status" lines from
// /proc/net/bonding/bond*. The bond-level "MII Status" line precedes the first
// "Slave Interface" and must not be attributed to any slave.
const bondProbe = `/proc/net/bonding/bond0:MII Status: up
/proc/net/bonding/bond0:Slave Interface: eth0
/proc/net/bonding/bond0:MII Status: up
/proc/net/bonding/bond0:Slave Interface: eth1
/proc/net/bonding/bond0:MII Status: up
/proc/net/bonding/bond1:MII Status: up
/proc/net/bonding/bond1:Slave Interface: eth2
/proc/net/bonding/bond1:MII Status: down
/proc/net/bonding/bond1:Slave Interface: eth3
/proc/net/bonding/bond1:MII Status: up`

func TestFindBondForSlave(t *testing.T) {
	cases := []struct {
		eth        string
		wantBond   string
		wantFound  bool
		wantSlaves int
	}{
		{"eth0", "bond0", true, 2},
		{"eth1", "bond0", true, 2},
		{"eth2", "bond1", true, 2},
		{"eth9", "", false, 0},
	}
	for _, c := range cases {
		t.Run(c.eth, func(t *testing.T) {
			bond, slaves, found := findBondForSlave(bondProbe, c.eth)
			if found != c.wantFound {
				t.Fatalf("eth=%q: found=%v, want %v", c.eth, found, c.wantFound)
			}
			if bond != c.wantBond {
				t.Errorf("eth=%q: bond=%q, want %q", c.eth, bond, c.wantBond)
			}
			if len(slaves) != c.wantSlaves {
				t.Errorf("eth=%q: slaves=%d, want %d", c.eth, len(slaves), c.wantSlaves)
			}
		})
	}
}

// The bond-level MII line must not leak onto a slave: every slave in bond0 is up,
// and in bond1 exactly eth2 is down.
func TestParseBondsMIIAttribution(t *testing.T) {
	bonds := parseBonds(bondProbe)
	if len(bonds) != 2 {
		t.Fatalf("got %d bonds, want 2", len(bonds))
	}

	want := map[string]map[string]string{
		"bond0": {"eth0": "up", "eth1": "up"},
		"bond1": {"eth2": "down", "eth3": "up"},
	}
	for _, b := range bonds {
		for _, sl := range b.slaves {
			if got := want[b.name][sl.name]; got != sl.miiStatus {
				t.Errorf("bond=%s slave=%s: MII=%q, want %q", b.name, sl.name, sl.miiStatus, got)
			}
		}
	}
}

func TestEthNameRe(t *testing.T) {
	valid := []string{"eth0", "ens1f0", "enp3s0f1", "bond0", "eth0.100"}
	for _, v := range valid {
		if !ethNameRe.MatchString(v) {
			t.Errorf("ethNameRe rejected valid name %q", v)
		}
	}
	invalid := []string{"eth0; rm -rf /", "eth0 down", "$(reboot)", "eth0|nc"}
	for _, v := range invalid {
		if ethNameRe.MatchString(v) {
			t.Errorf("ethNameRe accepted invalid name %q", v)
		}
	}
}

// formatBondReport (BondStatus path) shares parseBonds with NICDown; verify it
// still surfaces mode and flags a non-zero Link Failure Count with ⚠.
func TestFormatBondReport(t *testing.T) {
	raw := `/proc/net/bonding/bond0:Bonding Mode: IEEE 802.3ad Dynamic link aggregation
/proc/net/bonding/bond0:MII Status: up
/proc/net/bonding/bond0:Slave Interface: eth0
/proc/net/bonding/bond0:MII Status: up
/proc/net/bonding/bond0:Link Failure Count: 0
/proc/net/bonding/bond0:Slave Interface: eth1
/proc/net/bonding/bond0:MII Status: up
/proc/net/bonding/bond0:Link Failure Count: 3`

	out := formatBondReport(raw)
	for _, want := range []string{"bond0", "mode: IEEE 802.3ad", "eth0", "eth1", "Link Failure Count=3", "⚠ "} {
		if !strings.Contains(out, want) {
			t.Errorf("formatBondReport output missing %q\n---\n%s", want, out)
		}
	}

	if got := formatBondReport("(no bonds configured)"); !strings.Contains(got, "no bonds configured") {
		t.Errorf("empty case: got %q", got)
	}
}

// findBondForSlave is shared by NICDown and NICUp; verify it routes both
// slaves correctly and returns the full sibling list.
func TestFindBondForSlaveAllSlaves(t *testing.T) {
	_, slaves, found := findBondForSlave(bondProbe, "eth0")
	if !found {
		t.Fatal("eth0 should be found in bond0")
	}
	if len(slaves) != 2 {
		t.Errorf("want 2 slaves for bond0, got %d", len(slaves))
	}
}
