package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repeatRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	dir := t.TempDir()
	return &Registry{Workspace: dir, MaxResultChars: 100000}, dir
}

func newTC(visible bool) *TurnContext {
	return &TurnContext{
		ReadPaths:     map[string]bool{},
		ResultVisible: func(string) bool { return visible },
	}
}

// Mode 1: a large unchanged re-read is stubbed while the earlier copy is still in context.
func TestRepeatStubsUnchangedLargeRead(t *testing.T) {
	r, dir := repeatRegistry(t)
	body := strings.Repeat("knowledge line\n", 200)
	if err := os.WriteFile(filepath.Join(dir, "topic.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := newTC(true)
	args := `{"path":"topic.md"}`
	if out := r.Execute("file_read", args, tc); out != body {
		t.Fatalf("first read must be full: %q", out[:40])
	}
	out := r.Execute("file_read", args, tc)
	if !strings.HasPrefix(out, "[marble] unchanged") || !strings.Contains(out, "bytes omitted") || len(out) > 400 {
		t.Fatalf("expected stub, got %d bytes: %.120q", len(out), out)
	}
	if !strings.Contains(out, "identical call #2") || !strings.Contains(out, "file_read") {
		t.Fatalf("stub should name the call: %s", out)
	}
}

// If the earlier copy fell out of the prompt (trimHistory / compaction), never stub.
func TestRepeatNoStubWhenEarlierResultNotVisible(t *testing.T) {
	r, dir := repeatRegistry(t)
	body := strings.Repeat("x", 4000)
	_ = os.WriteFile(filepath.Join(dir, "big.txt"), []byte(body), 0o644)
	tc := newTC(false)
	args := `{"path":"big.txt"}`
	for i := 1; i <= 3; i++ {
		out := r.Execute("file_read", args, tc)
		if !strings.HasPrefix(out, body) {
			t.Fatalf("call %d: full content required when earlier copy is not visible", i)
		}
		if i == 3 && !strings.Contains(out, "[marble] identical call #3") {
			t.Fatalf("call 3 should still carry the repeat note: %.80q", out[len(body):])
		}
	}

	// Nil callback (no session loop) also disables stubs.
	tc = &TurnContext{ReadPaths: map[string]bool{}}
	r.Execute("file_read", args, tc)
	if out := r.Execute("file_read", args, tc); !strings.HasPrefix(out, body) {
		t.Fatal("nil ResultVisible must not stub")
	}
}

// Small results: no stub, but a note from the 3rd identical call; interleaving (A,B,A,B,A)
// is still counted, which the consecutive-only ADR-0022 check missed.
func TestRepeatNoteInterleaved(t *testing.T) {
	r, dir := repeatRegistry(t)
	_ = os.WriteFile(filepath.Join(dir, "a.md"), []byte("alpha"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.md"), []byte("beta"), 0o644)
	tc := newTC(true)
	a, b := `{"path":"a.md"}`, `{"path":"b.md"}`
	seq := []string{a, b, a, b}
	for _, args := range seq {
		if out := r.Execute("file_read", args, tc); strings.Contains(out, "[marble]") {
			t.Fatalf("no note before the 3rd identical call: %q", out)
		}
	}
	out := r.Execute("file_read", a, tc)
	if !strings.HasPrefix(out, "alpha") || !strings.Contains(out, "[marble] identical call #3") {
		t.Fatalf("3rd interleaved read: %q", out)
	}
	if sum := tc.RepeatSummary(3, 4); !strings.Contains(sum, `file_read {"path":"a.md"} ×3 unchanged`) {
		t.Fatalf("summary: %q", sum)
	}
	if tc.RepeatSummary(3, 4) == "" || strings.Contains(tc.RepeatSummary(3, 4), "b.md") {
		t.Fatalf("b.md was only read twice: %q", tc.RepeatSummary(3, 4))
	}
}

// Polling a value that changes is progress, not thrash: never annotated.
func TestRepeatChangedResultIsNotNoted(t *testing.T) {
	r, dir := repeatRegistry(t)
	p := filepath.Join(dir, "progress.log")
	tc := newTC(true)
	for i := 0; i < 6; i++ {
		_ = os.WriteFile(p, []byte(strings.Repeat("step\n", i+1)), 0o644)
		if out := r.Execute("file_read", `{"path":"progress.log"}`, tc); strings.Contains(out, "[marble]") {
			t.Fatalf("poll %d annotated though the result changed: %q", i, out)
		}
	}
	if tc.RepeatSummary(2, 4) != "" {
		t.Fatalf("summary should be empty: %q", tc.RepeatSummary(2, 4))
	}
}

// Mode 3: an identical failure is flagged from the 2nd attempt, and policy blocks say
// they are terminal.
func TestRepeatFailingCallAndTerminalPolicyBlock(t *testing.T) {
	r, _ := repeatRegistry(t)
	tc := newTC(true)
	args := `{"path":"missing.txt"}`
	if out := r.Execute("file_read", args, tc); strings.Contains(out, "[marble]") {
		t.Fatalf("first failure should be plain: %q", out)
	}
	out := r.Execute("file_read", args, tc)
	if !strings.HasPrefix(out, "error:") || !strings.Contains(out, "identical failing call #2") {
		t.Fatalf("2nd identical failure: %q", out)
	}
}

// Mode 2: identical shell output gets the note (shell is not read-only, so never stubbed).
func TestRepeatShellIdenticalOutput(t *testing.T) {
	r, _ := repeatRegistry(t)
	r.Policy = nil
	tc := newTC(true)
	// noteRepeat directly: shell execution needs a policy; the bookkeeping is tool-agnostic.
	args := `{"command":"git status --short"}`
	out := strings.Repeat("M internal/x.go\n", 100)
	for i := 1; i <= 3; i++ {
		got := r.noteRepeat("shell_execute", args, out, tc)
		if i < 3 && got != out {
			t.Fatalf("call %d altered", i)
		}
		if i == 3 && (!strings.HasPrefix(got, out) || !strings.Contains(got, "identical call #3")) {
			t.Fatalf("shell repeat must keep output and add a note")
		}
	}
	// Whitespace-only command differences are the same call.
	if got := r.noteRepeat("shell_execute", `{"command":"git  status   --short"}`, out, tc); !strings.Contains(got, "identical call #4") {
		t.Fatalf("collapsed-whitespace command should count: %.60q", got[len(out):])
	}
}

func TestRepeatPollExempt(t *testing.T) {
	r, _ := repeatRegistry(t)
	tc := newTC(true)
	args := `{"task_id":"abc"}`
	for i := 0; i < 5; i++ {
		if got := r.noteRepeat("call_agent_process", args, `{"status":"running"}`, tc); got != `{"status":"running"}` {
			t.Fatalf("agent polls must never be annotated: %q", got)
		}
		if got := r.noteRepeat("check_background_task", args, "running", tc); got != "running" {
			t.Fatalf("bg polls must never be annotated: %q", got)
		}
	}
}
