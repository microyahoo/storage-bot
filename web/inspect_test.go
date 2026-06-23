package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/microyahoo/storage-bot/inspect"
)

func TestInspectAPIRequiresCluster(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/inspect/", nil)
	rec := httptest.NewRecorder()
	s.handleInspectAPI(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing cluster → %d, want 400", rec.Code)
	}
}

func TestInspectAPIDisabled(t *testing.T) {
	s := &Server{} // inspectRunner nil → 功能未启用
	req := httptest.NewRequest(http.MethodPost, "/api/inspect/c1", nil)
	rec := httptest.NewRecorder()
	s.handleInspectAPI(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("disabled → %d, want 503", rec.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"] == "" {
		t.Errorf("expected error message in JSON body")
	}
}

// GET /api/inspect/{cluster}/{file} with no store configured must degrade to 503
// rather than panic — and must not require the runner (loading history ≠ running).
func TestInspectAPILoadReportNoStore(t *testing.T) {
	s := &Server{} // both runner and store nil
	req := httptest.NewRequest(http.MethodGet, "/api/inspect/c1/20260619-215500.json", nil)
	rec := httptest.NewRecorder()
	s.handleInspectAPI(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("load report w/o store → %d, want 503", rec.Code)
	}
}

// A stored report loads back through the API even when the runner is nil,
// proving the {cluster}/{file} route reads from the store, not the runner.
func TestInspectAPILoadReport(t *testing.T) {
	store := inspect.NewStore(t.TempDir(), 5)
	rep := &inspect.Report{
		Cluster:   "c1",
		StartedAt: time.Date(2026, 6, 19, 21, 55, 0, 0, time.UTC),
		Findings:  []inspect.Finding{{Item: "ceph_health", Level: inspect.LevelWarn, Summary: "HEALTH_WARN"}},
	}
	rep.Finalize()
	if err := store.Save(rep); err != nil {
		t.Fatalf("save: %v", err)
	}
	names, err := store.List("c1")
	if err != nil || len(names) != 1 {
		t.Fatalf("list: %v names=%v", err, names)
	}

	s := &Server{inspectStore: store} // runner intentionally nil
	req := httptest.NewRequest(http.MethodGet, "/api/inspect/c1/"+names[0], nil)
	rec := httptest.NewRecorder()
	s.handleInspectAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("load report → %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got inspect.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Cluster != "c1" || len(got.Findings) != 1 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}
