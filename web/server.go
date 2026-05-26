// Package web hosts the storage-bot admin UI: cluster list, node list, ceph
// health, and an in-browser skill runner. All actions reuse the same bot.Handler
// (kubeCache, audit log, dev flags) so behavior matches what Feishu users see.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/microyahoo/storage-bot/bot"
	"github.com/microyahoo/storage-bot/config"
)

//go:embed templates/*.html
var templatesFS embed.FS

// LLMState lets the web layer read the bot's runtime LLM flag.
// Implemented by *bot.Handler via a tiny accessor.
type LLMState interface {
	LLMEnabled() bool
}

type Server struct {
	cfg       config.WebConfig
	handler   *bot.Handler
	llmState  LLMState
	templates map[string]*template.Template

	// inFlight protects against accidental double-submit of the run form.
	inFlight atomic.Int32
}

func NewServer(cfg config.WebConfig, h *bot.Handler, llmState LLMState) (*Server, error) {
	// Parse one template set per page so each page's {{define "content"}} only
	// affects its own set. Putting layout + every page in one set causes their
	// "content" definitions to clobber each other (last one wins).
	pages := []string{"home.html", "skills.html", "cluster.html", "nodes.html", "health.html", "run.html"}
	templates := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		tpl, err := template.ParseFS(templatesFS, "templates/layout.html", "templates/"+page)
		if err != nil {
			return nil, fmt.Errorf("parse templates for %s: %w", page, err)
		}
		templates[page] = tpl
	}
	return &Server{cfg: cfg, handler: h, llmState: llmState, templates: templates}, nil
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.basicAuth(s.handleHome))
	mux.HandleFunc("/skills", s.basicAuth(s.handleSkills))
	mux.HandleFunc("/run", s.basicAuth(s.handleRun))
	mux.HandleFunc("/clusters/", s.basicAuth(s.handleClusterRoutes))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })

	srv := &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("web server listening", "addr", s.cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func (s *Server) basicAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Username == "" {
			next(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.Username)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="storage-bot"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

type baseData struct {
	Title string
	Page  string
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tpl, ok := s.templates[name]
	if !ok {
		slog.Error("template not found", "name", name)
		http.Error(w, "template not found: "+name, http.StatusInternalServerError)
		return
	}
	if err := tpl.ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("template render failed", "name", name, "error", err)
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	clusters := s.handler.ListClusters()
	skills := s.handler.ListSkills()
	s.render(w, "home.html", struct {
		baseData
		Clusters   []bot.ClusterSummary
		SkillCount int
		LLMEnabled bool
	}{
		baseData:   baseData{Title: "首页", Page: "home"},
		Clusters:   clusters,
		SkillCount: len(skills),
		LLMEnabled: s.llmState.LLMEnabled(),
	})
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	s.render(w, "skills.html", struct {
		baseData
		Skills []bot.SkillInfo
	}{
		baseData: baseData{Title: "Skills", Page: "skills"},
		Skills:   s.handler.ListSkills(),
	})
}

// handleClusterRoutes dispatches /clusters/<name>, /clusters/<name>/nodes,
// /clusters/<name>/health
func (s *Server) handleClusterRoutes(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path[len("/clusters/"):]
	var name, action string
	if i := indexByte(path, '/'); i < 0 {
		name = path
	} else {
		name, action = path[:i], path[i+1:]
	}
	if name == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	// Verify the cluster exists.
	clusters := s.handler.ListClusters()
	var cs *bot.ClusterSummary
	for i := range clusters {
		if clusters[i].Name == name {
			cs = &clusters[i]
			break
		}
	}
	if cs == nil {
		http.Error(w, "cluster not found: "+name, http.StatusNotFound)
		return
	}

	switch action {
	case "":
		s.render(w, "cluster.html", struct {
			baseData
			Cluster bot.ClusterSummary
		}{
			baseData: baseData{Title: name, Page: "home"},
			Cluster:  *cs,
		})
	case "nodes":
		s.renderNodes(w, r, name)
	case "health":
		s.renderHealth(w, r, name)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) renderNodes(w http.ResponseWriter, r *http.Request, name string) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	nodes, err := s.handler.GetClusterNodes(ctx, name)
	data := struct {
		baseData
		ClusterName string
		Nodes       []bot.NodeInfo
		Error       string
	}{
		baseData:    baseData{Title: name + " 节点", Page: "home"},
		ClusterName: name,
		Nodes:       nodes,
	}
	if err != nil {
		data.Error = err.Error()
	}
	s.render(w, "nodes.html", data)
}

func (s *Server) renderHealth(w http.ResponseWriter, r *http.Request, name string) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	output, err := s.handler.GetClusterHealth(ctx, name)
	data := struct {
		baseData
		ClusterName string
		Output      string
		Error       string
	}{
		baseData:    baseData{Title: name + " 健康", Page: "home"},
		ClusterName: name,
		Output:      output,
	}
	if err != nil {
		data.Error = err.Error()
	}
	s.render(w, "health.html", data)
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	skills := s.handler.ListSkills()
	clusters := s.handler.ListClusters()

	data := struct {
		baseData
		Skills          []bot.SkillInfo
		Clusters        []bot.ClusterSummary
		SelectedSkill   string
		SelectedCluster string
		SelectedNode    string
		SelectedMax     string
		Executed        bool
		Output          string
		Error           string
	}{
		baseData:        baseData{Title: "执行 Skill", Page: "run"},
		Skills:          skills,
		Clusters:        clusters,
		SelectedSkill:   r.URL.Query().Get("skill"),
		SelectedCluster: r.URL.Query().Get("cluster"),
	}

	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			data.Error = "parse form: " + err.Error()
			s.render(w, "run.html", data)
			return
		}
		data.SelectedSkill = r.PostForm.Get("skill")
		data.SelectedCluster = r.PostForm.Get("cluster")
		data.SelectedNode = r.PostForm.Get("node")
		data.SelectedMax = r.PostForm.Get("max")

		if data.SelectedSkill == "" || data.SelectedCluster == "" {
			data.Error = "请同时选择 skill 和 cluster"
			s.render(w, "run.html", data)
			return
		}

		// Reject concurrent runs to avoid burying the toolbox pod under parallel
		// long-running ceph operations triggered by an impatient browser refresh.
		if !s.inFlight.CompareAndSwap(0, 1) {
			data.Error = "另一个 skill 正在执行中，请稍后再试"
			s.render(w, "run.html", data)
			return
		}
		defer s.inFlight.Store(0)

		args := map[string]string{}
		if data.SelectedMax != "" {
			args["max"] = data.SelectedMax
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		output, err := s.handler.RunSkillForWeb(ctx, data.SelectedSkill, data.SelectedCluster, data.SelectedNode, args)
		data.Executed = true
		data.Output = output
		if err != nil {
			data.Error = err.Error()
		}
	}

	s.render(w, "run.html", data)
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
