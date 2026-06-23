package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
