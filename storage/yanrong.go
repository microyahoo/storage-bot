package storage

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// YanrongBackend talks to the Yanrong cloud filesystem REST API.
//
// Auth flow (mirrors examples/yrfs.py):
//  1. password → md5 → md5 again → interleave the two hex digests char-by-char
//     into a 64-char "token cipher" string.
//  2. POST /api/auth/tokens with {"username","password":<cipher>} → token in
//     data.token of the JSON response.
//  3. Subsequent calls send the token via the "x-auth-token" header.
//
// The token is cached and refreshed lazily on the first request and on any
// 401 response.
type YanrongBackend struct {
	name     string
	baseURL  string
	username string
	password string
	client   *http.Client

	// User-path prefixes used by ResolveUserPath to turn a user name into
	// a full quota path. Either may be empty to disable that scope.
	publicUserPrefix  string
	privateUserPrefix string

	mu    sync.Mutex
	token string
}

// YanrongOption tunes optional fields on a YanrongBackend. Use the
// With* helpers below — keeps the constructor signature stable as we
// add knobs (user prefixes today, maybe per-host timeout / TLS later).
type YanrongOption func(*YanrongBackend)

// WithUserPrefixes sets the public/private user-path prefixes used by
// ResolveUserPath. Either may be empty to disable that scope.
func WithUserPrefixes(public, private string) YanrongOption {
	return func(y *YanrongBackend) {
		y.publicUserPrefix = public
		y.privateUserPrefix = private
	}
}

// NewYanrongBackend creates a Yanrong backend. baseURL should be the host root,
// e.g. "https://192.168.73.25". TLS verification is disabled because Yanrong
// installs typically use self-signed certs.
func NewYanrongBackend(name, baseURL, username, password string, opts ...YanrongOption) *YanrongBackend {
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	y := &YanrongBackend{
		name:     name,
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		client:   &http.Client{Timeout: 60 * time.Second, Transport: tr},
	}
	for _, opt := range opts {
		opt(y)
	}
	return y
}

// ResolveUserPath turns a user name (e.g. "liangzheng") into a full quota path
// (e.g. "/drtraining/user/liangzheng") based on scope ("public" or "private").
func (y *YanrongBackend) ResolveUserPath(user, scope string) (string, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return "", fmt.Errorf("user is required")
	}
	var prefix string
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "public":
		prefix = y.publicUserPrefix
	case "private", "":
		prefix = y.privateUserPrefix
	default:
		return "", fmt.Errorf("unknown scope %q (want public or private)", scope)
	}
	if prefix == "" {
		return "", fmt.Errorf("no %s_user_prefix configured for storage %q", strings.ToLower(scope), y.name)
	}
	return filepath.Join(prefix, user), nil
}

// DirUsageForUser resolves a user name through ResolveUserPath and
// returns the quota for the resulting path.
func (y *YanrongBackend) DirUsageForUser(ctx context.Context, user, scope string) (string, error) {
	path, err := y.ResolveUserPath(user, scope)
	if err != nil {
		return "", err
	}
	return y.DirUsage(ctx, path)
}

func (y *YanrongBackend) Type() string { return "yanrong" }

// ClusterInfo returns a curated summary of /api/v3/overview: product info,
// redundancy / EC model, and capacity numbers. /overview is huge (mostly
// dashboard time-series); we extract only what an operator cares about.
func (y *YanrongBackend) ClusterInfo(ctx context.Context) (string, error) {
	return y.overviewSummary(ctx)
}

// HealthCheck returns the same curated /api/v3/overview summary as ClusterInfo.
// Yanrong does not expose a separate cluster-status endpoint that's stable
// across versions, but /overview always includes a `health` subobject.
func (y *YanrongBackend) HealthCheck(ctx context.Context) (string, error) {
	return y.overviewSummary(ctx)
}

func (y *YanrongBackend) overviewSummary(ctx context.Context) (string, error) {
	raw, err := y.authedGet(ctx, "/api/v3/overview", url.Values{"lang": {"zh"}})
	if err != nil {
		return "", err
	}
	summary, ok := formatOverview([]byte(raw))
	if !ok {
		// Parsing failed — fall back to the raw JSON so the operator can debug.
		return raw, nil
	}
	return summary, nil
}

// ListRecycles paginates through /api/v3/recycles and returns a pretty-printed
// summary of every recycle-bin entry in the cluster.
func (y *YanrongBackend) ListRecycles(ctx context.Context) (string, error) {
	recycles, err := y.listAllRecycles(ctx)
	if err != nil {
		return "", err
	}
	return formatRecycles(recycles), nil
}

// ListRecycleFiles lists deleted files under `path` from the matching recycle
// bin. A file is matched to the recycle with the longest path prefix that
// contains it — e.g. for recycles "/public-data/user" and "/public-data/user/alice",
// a query under "/public-data/user/alice/x" picks the latter.
//
// size caps the number of rows returned by the server in a single call. The
// /api/v2/recycle/file endpoint does NOT support page iteration, so the result
// is partial.
func (y *YanrongBackend) ListRecycleFiles(ctx context.Context, queryPath string, size int) (string, error) {
	if strings.TrimSpace(queryPath) == "" {
		return "", fmt.Errorf("path is required")
	}
	if size <= 0 {
		size = 100
	}
	cleaned := normalizeQuotaPath(queryPath)

	recycles, err := y.listAllRecycles(ctx)
	if err != nil {
		return "", err
	}
	match, ok := pickRecycleForPath(recycles, cleaned)
	if !ok {
		return "", fmt.Errorf("no recycle bin covers path %q (scanned %d entries)", queryPath, len(recycles))
	}

	// The API requires a trailing "/" on the `path` query value to be treated
	// as a directory listing. Add it only in the url.Values — leave the
	// caller's queryPath untouched.
	apiPath := queryPath
	if !strings.HasSuffix(apiPath, "/") {
		apiPath += "/"
	}
	q := url.Values{
		"recycle_id": {strconv.FormatInt(match.ID, 10)},
		"size":       {strconv.Itoa(size)},
		"path":       {apiPath},
		"list_mode":  {"LAST_PAGE"}, // FIXME: only list last page
	}
	raw, err := y.authedGet(ctx, "/api/v2/recycle/file", q)
	if err != nil {
		return "", fmt.Errorf("list recycle files: %w", err)
	}
	var resp struct {
		Data struct {
			RecycleID    int64              `json:"recycle_id"`
			RecycleFiles []recycleFileEntry `json:"recycle_files"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return "", fmt.Errorf("parse recycle files: %w", err)
	}
	return formatRecycleFiles(match, resp.Data.RecycleFiles), nil
}

// ClearRecycleFiles permanently deletes all files under `queryPath` from the
// recycle bin that owns it (chosen via the same longest-prefix rule as
// ListRecycleFiles). The action endpoint is POST /api/v2/recycle/<id>/action
// with a JSON body {"action":"delete","path":"<path>/"}.
//
// Safety: when dryRun is false, queryPath MUST live strictly under a configured
// user-prefix (public_user_prefix or private_user_prefix) — i.e. the path is
// `<prefix>/<user>` or deeper. This prevents accidental wipes of shared roots
// like `/public-data/` or arbitrary cluster directories. Dry-run skips the
// guard so operators can preview what would happen against any path.
//
// Returns a short status string on success.
func (y *YanrongBackend) ClearRecycleFiles(ctx context.Context, queryPath string, dryRun bool) (string, error) {
	if strings.TrimSpace(queryPath) == "" {
		return "", fmt.Errorf("path is required")
	}
	cleaned := normalizeQuotaPath(queryPath)

	recycles, err := y.listAllRecycles(ctx)
	if err != nil {
		return "", err
	}
	match, ok := pickRecycleForPath(recycles, cleaned)
	if !ok {
		return "", fmt.Errorf("no recycle bin covers path %q (scanned %d entries)", queryPath, len(recycles))
	}

	if !dryRun {
		if err := y.checkUserDirPath(cleaned); err != nil {
			return "", err
		}
	}

	// Same trailing-slash rule as ListRecycleFiles: append "/" only in the API
	// value, leave the caller's queryPath untouched.
	apiPath := queryPath
	if !strings.HasSuffix(apiPath, "/") {
		apiPath += "/"
	}

	endpoint := fmt.Sprintf("/api/v2/recycle/%d/action", match.ID)
	body := map[string]string{
		"action": "delete",
		"path":   apiPath,
	}

	if dryRun {
		return fmt.Sprintf(
			"🧪 dry-run · 将清空 recycle #%d (path=%s) 下的 %s\n   POST %s\n   body=%v\n   (实际未执行)",
			match.ID, match.Path, apiPath, endpoint, body,
		), nil
	}

	raw, err := y.authedPost(ctx, endpoint, url.Values{"lang": {"zh"}}, body)
	if err != nil {
		return "", fmt.Errorf("clear recycle files: %w", err)
	}
	return fmt.Sprintf("🧹 已清空 recycle #%d (path=%s) 下的 %s\n%s",
		match.ID, match.Path, apiPath, raw), nil
}

// checkUserDirPath enforces the safety rule for destructive recycle operations:
// the (already-normalized) target path must be strictly under a configured
// user-prefix. "Strictly under" means at least one component deeper than the
// prefix itself — so `<prefix>` alone is rejected, but `<prefix>/<user>` and
// `<prefix>/<user>/anything` are allowed. If no prefixes are configured the
// call is refused outright.
func (y *YanrongBackend) checkUserDirPath(cleaned string) error {
	pubPrefix := normalizeQuotaPath(y.publicUserPrefix)
	privPrefix := normalizeQuotaPath(y.privateUserPrefix)
	hasPub := y.publicUserPrefix != ""
	hasPriv := y.privateUserPrefix != ""
	if !hasPub && !hasPriv {
		return fmt.Errorf("refusing destructive op: no public/private user_prefix configured on storage %q", y.name)
	}

	if hasPriv && pathStrictlyUnder(cleaned, privPrefix) {
		return nil
	}
	if hasPub && pathStrictlyUnder(cleaned, pubPrefix) {
		return nil
	}

	var allowed []string
	if hasPriv {
		allowed = append(allowed, fmt.Sprintf("private=%s/<user>/...", privPrefix))
	}
	if hasPub {
		allowed = append(allowed, fmt.Sprintf("public=%s/<user>/...", pubPrefix))
	}
	return fmt.Errorf("refusing to clear %q: path must live under a user directory (%s)", cleaned, strings.Join(allowed, " or "))
}

// pathStrictlyUnder reports whether `p` is at least one component deeper than
// `prefix`. Equal-to-prefix returns false (that's the prefix itself, not a
// user dir).
func pathStrictlyUnder(p, prefix string) bool {
	if !pathHasPrefix(p, prefix) {
		return false
	}
	return normalizeQuotaPath(p) != normalizeQuotaPath(prefix)
}

// pickRecycleForPath returns the recycle whose Path is the longest prefix of
// queryPath. Comparison is done on path components so "/a/b" does NOT match
// "/a/bc"; the longest-prefix rule resolves nested recycles correctly.
func pickRecycleForPath(recycles []recycleEntry, queryPath string) (recycleEntry, bool) {
	q := normalizeQuotaPath(queryPath)
	var best recycleEntry
	bestLen := -1
	for _, r := range recycles {
		rp := normalizeQuotaPath(r.Path)
		if !pathHasPrefix(q, rp) {
			continue
		}
		if len(rp) > bestLen {
			best = r
			bestLen = len(rp)
		}
	}
	return best, bestLen >= 0
}

// pathHasPrefix reports whether `p` equals `prefix` or lives beneath it,
// matching on path components (so "/a/b" does NOT match "/a/bc"). Both inputs
// are run through normalizeQuotaPath here so callers can pass raw API or
// user-supplied strings — `..`, `//`, missing leading slash, etc. are all
// canonicalized before comparison. "/" is a universal prefix.
//
// We use filepath.Rel instead of a string-prefix check because Rel handles
// component boundaries by construction: if `p` is outside `prefix`, the
// returned relative path is exactly ".." or starts with "../".
func pathHasPrefix(p, prefix string) bool {
	p = normalizeQuotaPath(p)
	prefix = normalizeQuotaPath(prefix)
	rel, err := filepath.Rel(prefix, p)
	if err != nil {
		return false
	}
	sep := string(filepath.Separator)
	return rel != ".." && !strings.HasPrefix(rel, ".."+sep)
}

// recycleFileEntry mirrors data.recycle_files[] in /api/v2/recycle/file.
// `size` is a server-rendered "314.00B" string; directories return "".
type recycleFileEntry struct {
	Path         string `json:"path"`
	EID          string `json:"eid"`
	Key          string `json:"key"`
	Size         string `json:"size"`
	Expiration   string `json:"expiration"`
	RecycledTime string `json:"recycled_time"`
	OwnerMDSID   int64  `json:"owner_mds_id"`
}

// formatRecycleFiles renders a recycle-bin file listing.
func formatRecycleFiles(r recycleEntry, files []recycleFileEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🗑 Recycle #%d  path=%s  (matched %d files)\n", r.ID, r.Path, len(files))
	if len(files) == 0 {
		b.WriteString("🚫 (no files)")
		return b.String()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	fmt.Fprintf(&b, "\n⚠ server 端不支持分页，结果可能不完整\n")
	fmt.Fprintf(&b, "%-50s %15s %-20s %-20s %-20s %s\n", "PATH", "SIZE", "RECYCLED", "EXPIRES", "EID", "MDS-ID")
	for _, f := range files {
		size := f.Size
		if size == "" {
			size = "-"
		}
		fmt.Fprintf(&b, "%-50s %15s %-20s %-20s %-20s %d\n",
			truncate(f.Path, 50), size, f.RecycledTime, f.Expiration, f.EID, f.OwnerMDSID)
	}
	return strings.TrimRight(b.String(), "\n")
}

// ListQuotas paginates through /api/v3/quotas and returns a pretty-printed
// summary of every quota entry in the cluster.
func (y *YanrongBackend) ListQuotas(ctx context.Context) (string, error) {
	quotas, err := y.listAllQuotas(ctx)
	if err != nil {
		return "", err
	}
	return formatQuotas(quotas), nil
}

// DirUsage looks up the quota for an exact path. Yanrong's quota API does not
// support filtering by key=<path> reliably, so we list every quota (paginated)
// and find the one whose `path` equals the requested path. Trailing slashes
// are normalized so "/foo" and "/foo/" match the same entry.
func (y *YanrongBackend) DirUsage(ctx context.Context, path string) (string, error) {
	if path == "" {
		path = "/"
	}
	want := normalizeQuotaPath(path)

	quotas, err := y.listAllQuotas(ctx)
	if err != nil {
		return "", err
	}
	for _, q := range quotas {
		if normalizeQuotaPath(q.Path) == want {
			return formatQuotaEntry(q), nil
		}
	}
	return "", fmt.Errorf("no quota found for path %q (scanned %d entries)", path, len(quotas))
}

// listAllQuotas paginates /api/v3/quotas. See listPaginated for the loop body —
// quotas differ only in the endpoint path and the JSON list-field name ("list").
func (y *YanrongBackend) listAllQuotas(ctx context.Context) ([]quotaEntry, error) {
	return listPaginated[quotaEntry](ctx, y, pageSpec{
		endpoint: "/api/v3/quotas",
		listKey:  "list",
		label:    "quotas",
	})
}

type quotaEntry struct {
	QuotaID    int64  `json:"quota_id"`
	Path       string `json:"path"`
	SpaceUsed  int64  `json:"space_used"`
	SpaceLimit int64  `json:"space_limit"`
	InodeUsed  int64  `json:"inode_used"`
	InodeLimit int64  `json:"inode_limit"`
	DirUsed    int64  `json:"dir_used"`
	FileUsed   int64  `json:"file_used"`
	OpStatus   string `json:"op_status"`
	Recursive  bool   `json:"recursive"`
	EntryID    string `json:"entry_id"`
}

// listAllRecycles paginates /api/v2/recycle. See listPaginated.
func (y *YanrongBackend) listAllRecycles(ctx context.Context) ([]recycleEntry, error) {
	return listPaginated[recycleEntry](ctx, y, pageSpec{
		endpoint: "/api/v2/recycle",
		listKey:  "recycles",
		label:    "recycles",
	})
}

// pageSpec describes a single paginated Yanrong list endpoint: where to GET,
// which JSON key inside data.* holds the slice, and a short label for errors.
type pageSpec struct {
	endpoint string // e.g. "/api/v3/quotas"
	listKey  string // JSON field name inside `data`, e.g. "list" or "recycles"
	label    string // human label for error messages, e.g. "quotas"
}

// listPaginated walks `page=1,2,...` until pagination.total entries have been
// collected. The shape `data.{listKey}` + `data.pagination` is consistent across
// Yanrong list endpoints; only the list key differs (e.g. "list" vs "recycles").
// We decode `data` as a raw map so the same loop works for any T.
func listPaginated[T any](ctx context.Context, y *YanrongBackend, spec pageSpec) ([]T, error) {
	const perPage = 100
	page := 1
	var all []T
	for {
		q := url.Values{
			"page":      {strconv.Itoa(page)},
			"page_size": {strconv.Itoa(perPage)},
			"lang":      {"zh"},
		}
		raw, err := y.authedGet(ctx, spec.endpoint, q)
		if err != nil {
			return nil, fmt.Errorf("list %s page %d: %w", spec.label, page, err)
		}
		var env struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			return nil, fmt.Errorf("parse %s page %d: %w", spec.label, page, err)
		}
		rawList, ok := env.Data[spec.listKey]
		if !ok {
			return nil, fmt.Errorf("parse %s page %d: missing data.%s in response", spec.label, page, spec.listKey)
		}
		var batch []T
		if err := json.Unmarshal(rawList, &batch); err != nil {
			return nil, fmt.Errorf("parse %s page %d: decode list: %w", spec.label, page, err)
		}
		all = append(all, batch...)

		var pag struct {
			Total int `json:"total"`
		}
		if rawPag, ok := env.Data["pagination"]; ok {
			_ = json.Unmarshal(rawPag, &pag)
		}

		// Stop conditions: server returned fewer than asked, or we've reached the total.
		if pag.Total == 0 || len(all) >= pag.Total {
			break
		}
		if len(batch) == 0 {
			break // defensive: avoid infinite loop if the server lies about total
		}
		page++
	}
	return all, nil
}

// recycleEntry mirrors the fields in /api/v3/recycles (see examples/recycles.json).
// `expiration` is days; `usage` is a server-rendered "files/size" string we pass
// through verbatim since the server already formats it.
type recycleEntry struct {
	ID         int64  `json:"id"`
	Path       string `json:"path"`
	Expiration int    `json:"expiration"`
	Status     string `json:"status"`
	Usage      string `json:"usage"`
}

func formatRecycles(recycles []recycleEntry) string {
	if len(recycles) == 0 {
		return "🚫 (no recycles)"
	}
	sort.Slice(recycles, func(i, j int) bool { return recycles[i].Path < recycles[j].Path })

	var b strings.Builder
	fmt.Fprintf(&b, "🗑  Recycles (%d total)\n", len(recycles))
	fmt.Fprintf(&b, "%-6s %-40s %-8s %-10s %s\n", "ID", "PATH", "EXPIRE", "STATUS", "USAGE")
	for _, r := range recycles {
		fmt.Fprintf(&b, "%-6d %-40s %-8s %-10s %s\n",
			r.ID, truncate(r.Path, 40), fmt.Sprintf("%dd", r.Expiration), r.Status, r.Usage)
	}
	return strings.TrimRight(b.String(), "\n")
}

// normalizeQuotaPath canonicalizes a user-supplied path for prefix matching
// and quota lookups: trims whitespace, collapses `//`, resolves `.` / `..`,
// strips any trailing slash, and rebases relative inputs to "/" so an input
// like "foo//bar/.." round-trips to "/foo". Uses path.Clean (not filepath)
// because Yanrong paths are always forward-slash and OS-independent.
func normalizeQuotaPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	cleaned := path.Clean(p)
	if cleaned == "." {
		return "/"
	}
	return cleaned
}

func formatQuotaEntry(q quotaEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📁 path        : %s\n", q.Path)
	fmt.Fprintf(&b, "🆔 quota_id    : %d\n", q.QuotaID)
	fmt.Fprintf(&b, "🔁 recursive   : %t\n", q.Recursive)
	fmt.Fprintf(&b, "📌 op_status   : %s\n", q.OpStatus)
	fmt.Fprintf(&b, "💾 space_used  : %s  (raw=%d bytes)\n", humanBytes(float64(q.SpaceUsed)), q.SpaceUsed)
	if q.SpaceLimit > 0 {
		pct := float64(q.SpaceUsed) / float64(q.SpaceLimit) * 100
		fmt.Fprintf(&b, "📦 space_limit : %s  (raw=%d bytes, used %.1f%%)\n", humanBytes(float64(q.SpaceLimit)), q.SpaceLimit, pct)
	} else {
		fmt.Fprintf(&b, "📦 space_limit : unlimited\n")
	}
	fmt.Fprintf(&b, "🧮 inode_used  : %d\n", q.InodeUsed)
	if q.InodeLimit > 0 {
		fmt.Fprintf(&b, "🧮 inode_limit : %d\n", q.InodeLimit)
	} else {
		fmt.Fprintf(&b, "🧮 inode_limit : unlimited\n")
	}
	fmt.Fprintf(&b, "🗂 dir_used    : %d\n", q.DirUsed)
	fmt.Fprintf(&b, "📄 file_used   : %d\n", q.FileUsed)
	return strings.TrimRight(b.String(), "\n")
}

func formatQuotas(quotas []quotaEntry) string {
	if len(quotas) == 0 {
		return "🚫 (no quotas)"
	}
	// Sort by path for stable output.
	sort.Slice(quotas, func(i, j int) bool { return quotas[i].Path < quotas[j].Path })

	var b strings.Builder
	fmt.Fprintf(&b, "📊 Quotas (%d total)\n", len(quotas))
	fmt.Fprintf(&b, "%-65s %12s %12s %10s\n", "PATH", "USED", "LIMIT", "USED%")
	for _, q := range quotas {
		limit := "unlimited"
		pct := "-"
		if q.SpaceLimit > 0 {
			limit = humanBytes(float64(q.SpaceLimit))
			pct = fmt.Sprintf("%.1f%%", float64(q.SpaceUsed)/float64(q.SpaceLimit)*100)
		}
		fmt.Fprintf(&b, "%-65s %12s %12s %10s\n",
			truncate(q.Path, 65), humanBytes(float64(q.SpaceUsed)), limit, pct)
	}
	return strings.TrimRight(b.String(), "\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 10 {
		return s[:n]
	}
	return s[:n-10] + "..."
}

// authedGet runs a GET with the cached token, refreshing once on 401.
func (y *YanrongBackend) authedGet(ctx context.Context, path string, query url.Values) (string, error) {
	body, status, err := y.doRequest(ctx, "GET", path, query, nil)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized {
		// Token expired or first call. Re-login and retry once.
		y.mu.Lock()
		y.token = ""
		y.mu.Unlock()
		body, status, err = y.doRequest(ctx, "GET", path, query, nil)
		if err != nil {
			return "", err
		}
	}
	if status >= 400 {
		return "", fmt.Errorf("GET %s returned %d: %s", path, status, string(body))
	}
	return prettyJSON(body), nil
}

// authedPost runs a POST with a JSON body and the cached token, refreshing
// once on 401. Mirrors authedGet for write endpoints (e.g. recycle clear).
func (y *YanrongBackend) authedPost(ctx context.Context, path string, query url.Values, payload any) (string, error) {
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return "", fmt.Errorf("marshal payload: %w", err)
		}
	}
	resp, status, err := y.doRequest(ctx, "POST", path, query, body)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized {
		y.mu.Lock()
		y.token = ""
		y.mu.Unlock()
		resp, status, err = y.doRequest(ctx, "POST", path, query, body)
		if err != nil {
			return "", err
		}
	}
	if status >= 400 {
		return "", fmt.Errorf("POST %s returned %d: %s", path, status, string(resp))
	}
	return prettyJSON(resp), nil
}

func (y *YanrongBackend) doRequest(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, int, error) {
	token, err := y.ensureToken(ctx)
	if err != nil {
		return nil, 0, err
	}

	u := y.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reqBody io.Reader
	if len(body) > 0 {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("x-auth-token", token)
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := y.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%s %s: %w", method, u, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	return respBody, resp.StatusCode, nil
}

func (y *YanrongBackend) ensureToken(ctx context.Context) (string, error) {
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.token != "" {
		return y.token, nil
	}

	cipher := yanrongPasswordCipher(y.password)
	payload, err := json.Marshal(map[string]string{
		"username": y.username,
		"password": cipher,
	})
	if err != nil {
		return "", fmt.Errorf("marshal login: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", y.baseURL+"/api/auth/tokens", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := y.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("yanrong login: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read login response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("yanrong login %d: %s", resp.StatusCode, string(body))
	}

	var parsed struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parse login response: %w (body=%s)", err, string(body))
	}
	if parsed.Data.Token == "" {
		return "", fmt.Errorf("yanrong login returned empty token: %s", string(body))
	}
	y.token = parsed.Data.Token
	return y.token, nil
}

// yanrongPasswordCipher reproduces the calculate_md5_twice transform from
// examples/yrfs.py: md5(pw) → md5(md5(pw)) → interleave the two hex strings
// character by character into a 64-char string.
func yanrongPasswordCipher(password string) string {
	first := md5.Sum([]byte(password))
	firstHex := hex.EncodeToString(first[:])
	second := md5.Sum([]byte(firstHex))
	secondHex := hex.EncodeToString(second[:])

	var b strings.Builder
	b.Grow(len(firstHex) * 2)
	for i := 0; i < len(firstHex); i++ {
		b.WriteByte(firstHex[i])
		b.WriteByte(secondHex[i])
	}
	return b.String()
}

func prettyJSON(body []byte) string {
	var out bytes.Buffer
	if json.Indent(&out, body, "", "  ") == nil {
		return out.String()
	}
	return string(body)
}
