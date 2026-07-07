package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microyahoo/storage-bot/inspect"
)

func TestRunBackfillsOldReport(t *testing.T) {
	dir := t.TempDir()
	// Old-format report: two recoverable bond findings, no metric.
	report := `{
  "cluster": "c1",
  "started_at": "2026-06-17T03:00:00Z",
  "findings": [
    {"item": "hw_bond", "node": "n1", "level": 1, "summary": "bond 累计 Link Failure 3 次"},
    {"item": "hw_bond", "node": "n2", "level": 0, "summary": "bond 链路正常"}
  ]
}`
	if err := os.MkdirAll(filepath.Join(dir, "c1"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "c1", "20260617-030000.json")
	if err := os.WriteFile(file, []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := run(dir); err != nil {
		t.Fatalf("run: %v", err)
	}

	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), `"link_failure_total": "3"`) ||
		!strings.Contains(string(data), `"link_failure_total": "0"`) {
		t.Errorf("file not backfilled:\n%s", data)
	}

	// The store now loads the migrated metric.
	r, err := inspect.NewStore(dir, 0).Load("c1", "20260617-030000.json")
	if err != nil {
		t.Fatalf("load migrated: %v", err)
	}
	if r.Findings[0].Metrics["link_failure_total"] != "3" {
		t.Errorf("migrated metric = %q, want 3", r.Findings[0].Metrics["link_failure_total"])
	}

	// Idempotent: a second run leaves the file valid and unchanged in meaning.
	if err := run(dir); err != nil {
		t.Fatalf("second run: %v", err)
	}
}
