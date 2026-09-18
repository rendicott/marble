package session

import (
	"strings"
	"testing"

	"github.com/rendicott/marble/internal/agentproc"
)

func TestWrapRoutedPrompt(t *testing.T) {
	s := wrapRoutedPrompt("abc", "/ws", "do the thing")
	if !strings.Contains(s, "session abc") || !strings.Contains(s, "/ws") || !strings.HasSuffix(strings.TrimSpace(s), "do the thing") {
		t.Fatal(s)
	}
}

func TestFormatRoutedResult(t *testing.T) {
	out := formatRoutedResult("grok-fast", agentproc.Result{
		OK: true, DurationMs: 214000, CWD: "/ws", Summary: "hello",
	})
	if !strings.Contains(out, "[subprocess: grok-fast · ok · 214s") || !strings.Contains(out, "hello") {
		t.Fatal(out)
	}
}

func TestResolveAgentPresetEmpty(t *testing.T) {
	r := &Runner{}
	s := newSession("sid", "t")
	id, note := r.ResolveAgentPresetID(s, TurnOpts{})
	if id != "" || note != "" {
		t.Fatalf("%q %q", id, note)
	}
}
