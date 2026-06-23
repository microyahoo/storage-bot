package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// handleInspectAPI:
//
//	POST /api/inspect/{cluster}  → run inspection now, return Report JSON
//	GET  /api/inspect/{cluster}  → return history report filenames
func (s *Server) handleInspectAPI(w http.ResponseWriter, r *http.Request) {
	cluster := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/inspect/"), "/")
	if cluster == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cluster name required"})
		return
	}
	if s.inspectRunner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "inspect not enabled"})
		return
	}
	switch r.Method {
	case http.MethodPost:
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		rep, err := s.inspectRunner.Run(ctx, cluster)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rep)
	case http.MethodGet:
		if s.inspectStore == nil {
			writeJSON(w, http.StatusOK, []string{})
			return
		}
		list, err := s.inspectStore.List(cluster)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, list)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleInspectPage renders the inspection page: a run form + history list for
// the cluster named in the path (/inspect/{cluster}).
func (s *Server) handleInspectPage(w http.ResponseWriter, r *http.Request) {
	cluster := strings.Trim(strings.TrimPrefix(r.URL.Path, "/inspect/"), "/")
	var history []string
	if cluster != "" && s.inspectStore != nil {
		if list, err := s.inspectStore.List(cluster); err == nil {
			history = list
		}
	}
	s.render(w, "inspect.html", struct {
		baseData
		Cluster string
		History []string
		Enabled bool
	}{
		baseData: baseData{Title: "集群巡检", Page: "inspect"},
		Cluster:  cluster,
		History:  history,
		Enabled:  s.inspectRunner != nil,
	})
}
