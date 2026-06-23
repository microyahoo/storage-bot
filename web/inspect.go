package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/microyahoo/storage-bot/bot"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// handleInspectAPI:
//
//	POST /api/inspect/{cluster}         → run inspection now, return Report JSON
//	GET  /api/inspect/{cluster}         → return history report filenames
//	GET  /api/inspect/{cluster}/{file}  → return one stored Report JSON
func (s *Server) handleInspectAPI(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/inspect/"), "/")
	cluster, file := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		cluster, file = rest[:i], rest[i+1:]
	}
	if cluster == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cluster name required"})
		return
	}

	// Loading a stored report only needs the store, not the runner.
	if r.Method == http.MethodGet && file != "" {
		if s.inspectStore == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "inspect history not enabled"})
			return
		}
		rep, err := s.inspectStore.Load(cluster, file)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rep)
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

// handleInspectPage renders the inspection page:
//   - /inspect            → cluster selector (when no cluster name in path)
//   - /inspect/{cluster}  → run form + history list for that cluster
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
		Cluster  string
		Clusters []bot.ClusterSummary
		History  []string
		Enabled  bool
	}{
		baseData: baseData{Title: "集群巡检", Page: "inspect"},
		Cluster:  cluster,
		Clusters: s.handler.ListClusters(),
		History:  history,
		Enabled:  s.inspectRunner != nil,
	})
}
