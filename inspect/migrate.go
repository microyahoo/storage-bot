package inspect

import (
	"regexp"
	"strconv"
	"strings"
)

// oldBondWarnRe matches the pre-metric hw_bond Warn summary, e.g.
// "bond 累计 Link Failure 3 次", capturing the count.
var oldBondWarnRe = regexp.MustCompile(`Link Failure (\d+) 次`)

// backfillBondFinding reconstructs the link_failure_total metric for a single
// hw_bond finding produced before parseBond started emitting it. The count is
// recovered from the summary text:
//
//   - "bond 累计 Link Failure N 次" (Warn)  → link_failure_total = N
//   - "bond 链路正常"              (OK)    → link_failure_total = 0
//   - "无 bond 配置"               (OK)    → left as-is (no bonds → no baseline)
//   - "存在 MII Status 非 up 的 bond" (Crit) → left as-is (no count in text;
//     Critical never uses a baseline anyway)
//
// It is idempotent: a finding that already carries link_failure_total, or is not
// an hw_bond finding, is returned unchanged. Reports true when the finding was
// modified.
func backfillBondFinding(f *Finding) bool {
	if f.Item != "hw_bond" {
		return false
	}
	if _, ok := f.Metrics["link_failure_total"]; ok {
		return false // already migrated
	}
	total, ok := recoverBondTotal(f.Summary)
	if !ok {
		return false // no bonds / MII-down / unrecognized summary
	}
	if f.Metrics == nil {
		f.Metrics = map[string]string{}
	}
	f.Metrics["link_failure_total"] = strconv.Itoa(total)
	return true
}

// recoverBondTotal derives the cumulative link failure count from an old bond
// summary. Returns (0,false) when the summary carries no recoverable count.
func recoverBondTotal(summary string) (int, bool) {
	if m := oldBondWarnRe.FindStringSubmatch(summary); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, true
	}
	if strings.Contains(summary, "bond 链路正常") {
		return 0, true
	}
	return 0, false
}

// MigrateReport backfills link_failure_total on every hw_bond finding in the
// report that predates the metric. Returns the number of findings changed.
func MigrateReport(r *Report) int {
	changed := 0
	for i := range r.Findings {
		if backfillBondFinding(&r.Findings[i]) {
			changed++
		}
	}
	return changed
}

// MigrateResult records what a report migration did: the file and how many
// hw_bond findings were backfilled (or an error if it could not be processed).
type MigrateResult struct {
	Cluster string
	File    string
	Changed int
	Err     error
}

// MigrateStore walks every stored report and backfills the link_failure_total
// metric, rewriting the file in place when anything changed. Reports that
// already carry the metric are skipped (idempotent). Only changed or errored
// files are returned; a per-file error is captured and does not abort the walk.
func MigrateStore(s *Store) ([]MigrateResult, error) {
	clusters, err := s.Clusters()
	if err != nil {
		return nil, err
	}
	var out []MigrateResult
	for _, cluster := range clusters {
		names, err := s.List(cluster)
		if err != nil {
			out = append(out, MigrateResult{Cluster: cluster, Err: err})
			continue
		}
		for _, name := range names {
			r, err := s.Load(cluster, name)
			if err != nil {
				out = append(out, MigrateResult{Cluster: cluster, File: name, Err: err})
				continue
			}
			changed := MigrateReport(r)
			if changed == 0 {
				continue // already migrated or nothing recoverable
			}
			res := MigrateResult{Cluster: cluster, File: name, Changed: changed}
			if err := s.RewriteReport(cluster, name, r); err != nil {
				res.Err = err
			}
			out = append(out, res)
		}
	}
	return out, nil
}
