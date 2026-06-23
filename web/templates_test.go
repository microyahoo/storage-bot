package web

import (
	"bytes"
	"html/template"
	"testing"

	"github.com/microyahoo/storage-bot/bot"
)

// TestTemplatesParse confirms every page template (including storage.html which
// gained the recycle form) parses cleanly against the layout. Without this,
// template typos only surface at server startup.
func TestTemplatesParse(t *testing.T) {
	pages := []string{
		"home.html",
		"skills.html",
		"cluster.html",
		"nodes.html",
		"health.html",
		"run.html",
		"storage.html",
		"storage_output.html",
		"inspect.html",
	}
	for _, p := range pages {
		if _, err := template.ParseFS(templatesFS, "templates/layout.html", "templates/"+p); err != nil {
			t.Errorf("parse %s: %v", p, err)
		}
	}
}

// TestInspectTemplateRenders exercises both branches of inspect.html (cluster
// selector and per-cluster page) so the JS block — which embeds .Cluster into a
// <script> — fails the test at build time rather than at request time.
func TestInspectTemplateRenders(t *testing.T) {
	tpl, err := template.ParseFS(templatesFS, "templates/layout.html", "templates/inspect.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cases := []struct {
		name    string
		cluster string
	}{
		{"selector", ""},
		{"per-cluster", "prod-ceph-01"},
	}
	for _, c := range cases {
		data := struct {
			baseData
			Cluster  string
			Clusters []bot.ClusterSummary
			History  []string
			Enabled  bool
		}{
			baseData: baseData{Title: "集群巡检", Page: "inspect"},
			Cluster:  c.cluster,
			Clusters: []bot.ClusterSummary{{Name: "prod-ceph-01"}},
			History:  []string{"20260619-215500.json"},
			Enabled:  true,
		}
		var buf bytes.Buffer
		if err := tpl.ExecuteTemplate(&buf, "layout", data); err != nil {
			t.Errorf("render %s: %v", c.name, err)
		}
	}
}
