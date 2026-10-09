package session

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rendicott/marble/internal/agentproc"
)

func TestWrapRoutedPrompt(t *testing.T) {
	s := wrapRoutedPrompt(routedPromptOpts{SessionID: "abc", CWD: "/ws", GitTop: "/ws", UserText: "do the thing"})
	if !strings.Contains(s, "session abc") || !strings.Contains(s, "Working directory: /ws") || !strings.Contains(s, "Git repository: /ws") {
		t.Fatal(s)
	}
	if strings.Contains(s, "first turn") {
		t.Fatal(s)
	}
	if !strings.HasSuffix(strings.TrimSpace(s), "do the thing") {
		t.Fatal(s)
	}
}

func TestWrapRoutedPromptFirstTurn(t *testing.T) {
	s := wrapRoutedPrompt(routedPromptOpts{
		SessionID: "abc", CWD: "/ws/projects/app", GitTop: "/ws/projects/app",
		FirstTurn: true, UserText: "fix the bug",
	})
	for _, want := range []string{
		"first turn of this session",
		"no earlier transcript",
		"Work only in the working directory",
		"wrong repository",
		"Working directory: /ws/projects/app",
		"Git repository: /ws/projects/app",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q\n%s", want, s)
		}
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

func TestRoutedHeartbeatIsOneLiveStep(t *testing.T) {
	s := newSession("testroute1", "t")
	s.initTurnProgress(80, 65)
	s.appendStep(TurnStep{Kind: "starting", Detail: "routing → grok (grok)"})
	s.appendStep(TurnStep{Kind: "advisory", Detail: "context full+memory"})
	for i := 0; i < 500; i++ {
		s.upsertStep(TurnStep{Kind: "tool_result", Tool: "subprocess", Detail: fmt.Sprintf("subprocess streaming_text · %ds", i)})
	}
	steps := s.turn.prog.Steps
	if len(steps) != 3 {
		t.Fatalf("heartbeats should update one step in place, got %d steps", len(steps))
	}
	if steps[0].Kind != "starting" || steps[2].Detail != "subprocess streaming_text · 499s" {
		t.Fatalf("early steps evicted or heartbeat stale: %+v", steps)
	}
	// A distinct step (stuck advisory) is appended, and the next heartbeat starts a new live line.
	s.appendStep(TurnStep{Kind: "advisory", Tool: "subprocess", Detail: "stuck_hint: …"})
	s.upsertStep(TurnStep{Kind: "tool_result", Tool: "subprocess", Detail: "subprocess running · 9m0s"})
	if n := len(s.turn.prog.Steps); n != 5 {
		t.Fatalf("want 5 steps, got %d", n)
	}
}

func TestRoutedHeartbeatText(t *testing.T) {
	got := routedHeartbeat(&agentproc.Progress{ElapsedSec: 195, Phase: "streaming_text", AliveSignal: "session_state", AliveAgeSec: 2})
	if got != "subprocess · streaming_text · 3m15s · last signal session_state 2s ago" {
		t.Fatalf("%q", got)
	}
	if got := routedHeartbeat(&agentproc.Progress{ElapsedSec: 5}); got != "subprocess · running · 5s" {
		t.Fatalf("%q", got)
	}
}
