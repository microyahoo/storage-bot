package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/microyahoo/storage-bot/intent"
	"github.com/microyahoo/storage-bot/skill"
)

// When inspection is not enabled (no runner injected), handleInspect should
// return a clear hint rather than panicking.
func TestHandleInspectDisabled(t *testing.T) {
	h := &Handler{} // inspectRunner is nil
	reply, _, err := h.handleInspect(context.Background(), intent.Action{Type: intent.ActionInspect})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(reply, "未启用") {
		t.Errorf("expected disabled hint, got %q", reply)
	}
}

// listSkills hand-writes one example line per skill; this guards against adding
// a skill to the registry but forgetting its example (the bug that left
// restart_mon / restart_mgr undocumented). Every registered skill's Name() must
// appear in the EXAMPLE block — not just the dynamic listing above it, which
// trivially prints every name and would mask a missing example.
func TestListSkillsCoversEverySkill(t *testing.T) {
	h := &Handler{skills: skill.NewRegistry()}
	out := h.listSkills()

	// The examples follow the "💡 示例" marker; the dynamic name listing precedes
	// it. Only the example block proves an example line was actually written.
	idx := strings.Index(out, "示例")
	if idx < 0 {
		t.Fatal("listSkills() output has no 示例 block")
	}
	examples := out[idx:]
	for _, s := range h.skills.List() {
		if !strings.Contains(examples, s.Name()) {
			t.Errorf("listSkills() example block is missing skill %q (add an example line for it)", s.Name())
		}
	}
}
