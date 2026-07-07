// inspectctl migrate-reports backfills the hw_bond `link_failure_total` metric
// onto old inspection reports that predate it, recovering the count from the
// finding summary (e.g. "bond 累计 Link Failure 3 次"). Without the metric the
// delta-alerting feature treats every bond node as a first-run baseline; after
// migration the next inspection compares against real history.
//
// Usage:
//
//	inspectctl <history-dir>
//
// e.g. inspectctl ./inspect-reports
//
// The operation is idempotent: reports that already carry the metric are skipped.
package main

import (
	"fmt"
	"os"

	"github.com/microyahoo/storage-bot/inspect"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] == "-h" || os.Args[1] == "--help" {
		fmt.Fprintln(os.Stderr, "usage: inspectctl <history-dir>\n\nBackfill hw_bond link_failure_total onto old inspection reports.")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	results, err := inspect.MigrateStore(inspect.NewStore(dir, 0))
	if err != nil {
		return fmt.Errorf("migrate reports in %s: %w", dir, err)
	}

	changedFiles, changedFindings, failed := 0, 0, 0
	for _, r := range results {
		if r.Err != nil {
			failed++
			fmt.Printf("  ✗ %s/%s: %v\n", r.Cluster, r.File, r.Err)
			continue
		}
		changedFiles++
		changedFindings += r.Changed
		fmt.Printf("  • %s/%s: backfilled %d bond finding(s)\n", r.Cluster, r.File, r.Changed)
	}

	if changedFiles == 0 && failed == 0 {
		fmt.Println("Nothing to migrate — all reports already carry link_failure_total.")
	} else {
		fmt.Printf("%d file(s), %d finding(s) updated\n", changedFiles, changedFindings)
	}
	if failed > 0 {
		return fmt.Errorf("%d report(s) could not be migrated", failed)
	}
	return nil
}
