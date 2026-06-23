package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/microyahoo/storage-bot/intent"
)

// When inspection is not enabled (no runner injected), handleInspect should
// return a clear hint rather than panicking.
func TestHandleInspectDisabled(t *testing.T) {
	h := &Handler{} // inspectRunner is nil
	reply, err := h.handleInspect(context.Background(), intent.Action{Type: intent.ActionInspect})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(reply, "未启用") {
		t.Errorf("expected disabled hint, got %q", reply)
	}
}
