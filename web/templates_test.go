package web

import (
	"html/template"
	"testing"
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
	}
	for _, p := range pages {
		if _, err := template.ParseFS(templatesFS, "templates/layout.html", "templates/"+p); err != nil {
			t.Errorf("parse %s: %v", p, err)
		}
	}
}
