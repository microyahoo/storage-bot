package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

// loadQuotaExample reads examples/quota.json and returns its list slice so
// tests can pretend the server returns paginated chunks of it.
func loadQuotaExample(t *testing.T) []quotaEntry {
	t.Helper()
	raw, err := os.ReadFile("../examples/quota.json")
	if err != nil {
		t.Skipf("example file not available: %v", err)
	}
	var resp struct {
		Data struct {
			List []quotaEntry `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("parse example: %v", err)
	}
	return resp.Data.List
}

// newQuotaTestServer returns a server that paginates `entries` according to the
// `page` and `page_size` query params, mirroring Yanrong's response shape.
func newQuotaTestServer(t *testing.T, entries []quotaEntry, total int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/tokens":
			_, _ = fmt.Fprint(w, `{"code":"0","data":{"token":"t"}}`)
			return
		case "/api/v3/quotas":
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			perPage, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
			if page < 1 {
				page = 1
			}
			if perPage < 1 {
				perPage = 10
			}
			start := (page - 1) * perPage
			end := start + perPage
			if start > len(entries) {
				start = len(entries)
			}
			if end > len(entries) {
				end = len(entries)
			}
			resp := map[string]any{
				"code": "0",
				"data": map[string]any{
					"list": entries[start:end],
					"pagination": map[string]any{
						"total":          total,
						"current_page":   page,
						"per_page_count": perPage,
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
}

func TestListAllQuotasPaginates(t *testing.T) {
	entries := loadQuotaExample(t)
	if len(entries) == 0 {
		t.Skip("example has no entries")
	}

	// Pretend the server has 4x the example entries so pagination has to loop.
	bloated := make([]quotaEntry, 0, len(entries)*4)
	for i := 0; i < 4; i++ {
		for _, e := range entries {
			e.Path = fmt.Sprintf("/page%d%s", i, e.Path)
			bloated = append(bloated, e)
		}
	}

	srv := newQuotaTestServer(t, bloated, len(bloated))
	defer srv.Close()

	b := NewYanrongBackend("test", srv.URL, "u", "p")
	got, err := b.listAllQuotas(context.Background())
	if err != nil {
		t.Fatalf("listAllQuotas: %v", err)
	}
	if len(got) != len(bloated) {
		t.Fatalf("got %d entries, want %d", len(got), len(bloated))
	}
}

func TestDirUsageExactMatch(t *testing.T) {
	entries := loadQuotaExample(t)
	if len(entries) < 2 {
		t.Skip("example needs at least 2 entries")
	}
	target := entries[1].Path // e.g. "/drtraining/user/anrongzhang"

	srv := newQuotaTestServer(t, entries, len(entries))
	defer srv.Close()

	b := NewYanrongBackend("test", srv.URL, "u", "p")

	out, err := b.DirUsage(context.Background(), target)
	if err != nil {
		t.Fatalf("DirUsage(%q): %v", target, err)
	}
	if !strings.Contains(out, target) {
		t.Errorf("output does not mention %q:\n%s", target, out)
	}
	if !strings.Contains(out, "space_used") {
		t.Errorf("output missing space_used field:\n%s", out)
	}

	// Trailing slash should still hit.
	if _, err := b.DirUsage(context.Background(), target+"/"); err != nil {
		t.Errorf("DirUsage with trailing slash should match: %v", err)
	}

	// Non-existent path must error.
	if _, err := b.DirUsage(context.Background(), "/definitely/not/a/quota"); err == nil {
		t.Errorf("expected error for unknown path, got nil")
	}
}

func TestFormatQuotasTable(t *testing.T) {
	entries := loadQuotaExample(t)
	if len(entries) == 0 {
		t.Skip("example has no entries")
	}
	out := formatQuotas(entries)
	if !strings.Contains(out, "PATH") || !strings.Contains(out, "USED") || !strings.Contains(out, "LIMIT") {
		t.Errorf("missing table header:\n%s", out)
	}
	if !strings.Contains(out, fmt.Sprintf("Quotas (%d total)", len(entries))) {
		t.Errorf("missing total count line:\n%s", out)
	}
	t.Logf("rendered:\n%s", out)
}
