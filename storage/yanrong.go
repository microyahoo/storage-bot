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
		client:   &http.Client{Timeout: 30 * time.Second, Transport: tr},
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

// listAllQuotas paginates /api/v3/quotas until pagination.total entries have
// been collected. Pages are pulled in order; we don't try to parallelize since
// the API returns the total count up front and 100/page keeps it cheap.
func (y *YanrongBackend) listAllQuotas(ctx context.Context) ([]quotaEntry, error) {
	const perPage = 100
	page := 1
	var all []quotaEntry
	for {
		q := url.Values{
			"page":      {strconv.Itoa(page)},
			"page_size": {strconv.Itoa(perPage)},
			"lang":      {"zh"},
		}
		raw, err := y.authedGet(ctx, "/api/v3/quotas", q)
		if err != nil {
			return nil, fmt.Errorf("list quotas page %d: %w", page, err)
		}
		var resp struct {
			Data struct {
				List       []quotaEntry `json:"list"`
				Pagination struct {
					Total        int `json:"total"`
					CurrentPage  int `json:"current_page"`
					PerPageCount int `json:"per_page_count"`
				} `json:"pagination"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &resp); err != nil {
			return nil, fmt.Errorf("parse quotas page %d: %w", page, err)
		}
		all = append(all, resp.Data.List...)

		// Stop conditions: server returned fewer than asked, or we've reached the total.
		if resp.Data.Pagination.Total == 0 || len(all) >= resp.Data.Pagination.Total {
			break
		}
		if len(resp.Data.List) == 0 {
			break // defensive: avoid infinite loop if the server lies about total
		}
		page++
	}
	return all, nil
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

// listAllRecycles paginates /api/v3/recycles. Same shape as listAllQuotas
// (data.recycles[] + pagination); kept separate so each can evolve independently.
func (y *YanrongBackend) listAllRecycles(ctx context.Context) ([]recycleEntry, error) {
	const perPage = 100
	page := 1
	var all []recycleEntry
	for {
		q := url.Values{
			"page":      {strconv.Itoa(page)},
			"page_size": {strconv.Itoa(perPage)},
			"lang":      {"zh"},
		}
		raw, err := y.authedGet(ctx, "/api/v2/recycle", q)
		if err != nil {
			return nil, fmt.Errorf("list recycles page %d: %w", page, err)
		}
		var resp struct {
			Data struct {
				Recycles   []recycleEntry `json:"recycles"`
				Pagination struct {
					Total        int `json:"total"`
					CurrentPage  int `json:"current_page"`
					PerPageCount int `json:"per_page_count"`
				} `json:"pagination"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &resp); err != nil {
			return nil, fmt.Errorf("parse recycles page %d: %w", page, err)
		}
		all = append(all, resp.Data.Recycles...)

		if resp.Data.Pagination.Total == 0 || len(all) >= resp.Data.Pagination.Total {
			break
		}
		if len(resp.Data.Recycles) == 0 {
			break // defensive
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

func normalizeQuotaPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return "/"
	}
	return strings.TrimRight(p, "/")
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
	fmt.Fprintf(&b, "%-50s %12s %12s %10s\n", "PATH", "USED", "LIMIT", "USED%")
	for _, q := range quotas {
		limit := "unlimited"
		pct := "-"
		if q.SpaceLimit > 0 {
			limit = humanBytes(float64(q.SpaceLimit))
			pct = fmt.Sprintf("%.1f%%", float64(q.SpaceUsed)/float64(q.SpaceLimit)*100)
		}
		fmt.Fprintf(&b, "%-50s %12s %12s %10s\n",
			truncate(q.Path, 50), humanBytes(float64(q.SpaceUsed)), limit, pct)
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
	body, status, err := y.doGet(ctx, path, query)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized {
		// Token expired or first call. Re-login and retry once.
		y.mu.Lock()
		y.token = ""
		y.mu.Unlock()
		body, status, err = y.doGet(ctx, path, query)
		if err != nil {
			return "", err
		}
	}
	if status >= 400 {
		return "", fmt.Errorf("GET %s returned %d: %s", path, status, string(body))
	}
	return prettyJSON(body), nil
}

func (y *YanrongBackend) doGet(ctx context.Context, path string, query url.Values) ([]byte, int, error) {
	token, err := y.ensureToken(ctx)
	if err != nil {
		return nil, 0, err
	}

	u := y.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("x-auth-token", token)
	req.Header.Set("Accept", "application/json")

	resp, err := y.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("GET %s: %w", u, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	return body, resp.StatusCode, nil
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
