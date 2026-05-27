package storage

import (
	"os"
	"strings"
	"testing"
)

func TestFormatOverviewWithExample(t *testing.T) {
	body, err := os.ReadFile("../examples/overview.json")
	if err != nil {
		t.Skipf("example file not available: %v", err)
	}
	out, ok := formatOverview(body)
	if !ok {
		t.Fatalf("formatOverview returned !ok for examples/overview.json")
	}

	mustContain := []string{
		"F9000X",          // product_name
		"YANRONG.IO",      // manufacturer
		"EC",              // redundancy
		"N=8 M=2",         // EC model
		"health",          // health.health value
		"yrfs_capacity_total",
		"yrfs_capacity_used",
		"yrfs_capacity_available",
		"PiB", // yrfs_capacity_total is ~21 PB → expect PiB unit
	}
	mustNotContain := []string{
		"yrfs_bare_capacity_total", // filtered out: doesn't start with yrfs_capacity_
		"yrfs_meta_capacity_total",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q\n---\n%s", s, out)
		}
	}
	for _, s := range mustNotContain {
		if strings.Contains(out, s) {
			t.Errorf("output unexpectedly contained %q\n---\n%s", s, out)
		}
	}

	t.Logf("rendered summary:\n%s", out)
}
