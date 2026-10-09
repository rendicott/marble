package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/db"
)

func TestNormalizeAgentCWD(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"", "", false},
		{"  .  ", "", false},
		{"projects/app/", "projects/app", false},
		{"projects//app", "projects/app", false},
		{`projects\app`, "projects/app", false},
		{"/Users/rini", "", true},
		{"~/projects/app", "", true},
		{"../outside", "", true},
		{"projects/../../etc", "", true},
	}
	for _, c := range cases {
		got, err := NormalizeAgentCWD(c.in)
		if c.err {
			if err == nil {
				t.Errorf("NormalizeAgentCWD(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("NormalizeAgentCWD(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestResolveRoutedDirRefusesNonGitWorkspace(t *testing.T) {
	dir := t.TempDir()
	if _, ok := gitToplevel(dir); ok {
		t.Fatal("temp dir is inside a git work tree; refusal cannot be tested")
	}
	abs, gitTop, refusal, err := resolveRoutedDir(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if gitTop != "" || abs != dir || !strings.Contains(refusal, "not inside a git work tree") || !strings.Contains(refusal, dir) || !strings.Contains(refusal, "was not started") {
		t.Fatalf("abs=%q top=%q refusal=%q", abs, gitTop, refusal)
	}
}

func TestResolveRoutedDirUsesGitWorkspaceAndSubdir(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	sub := filepath.Join(repo, "projects", "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	abs, gitTop, refusal, err := resolveRoutedDir(repo, "")
	if err != nil || refusal != "" {
		t.Fatalf("root: abs=%q top=%q refusal=%q err=%v", abs, gitTop, refusal, err)
	}
	if abs != repo || gitTop == "" {
		t.Fatalf("root abs=%q top=%q", abs, gitTop)
	}

	abs, gitTop, refusal, err = resolveRoutedDir(repo, "projects/app")
	if err != nil || refusal != "" {
		t.Fatalf("sub: abs=%q top=%q refusal=%q err=%v", abs, gitTop, refusal, err)
	}
	if abs != sub {
		t.Fatalf("subdir cwd %q want %q", abs, sub)
	}
	if gitTop == "" {
		t.Fatal("subdir of a repo should report a git toplevel")
	}

	_, _, _, err = resolveRoutedDir(repo, "missing/dir")
	if err == nil {
		t.Fatal("missing directory should error")
	}
}

func TestRunRoutedTurnDoesNotStartOutsideGit(t *testing.T) {
	dir := t.TempDir()
	s := newSession("sidcwd1", "t")
	s.appendUI(Message{Role: "user", Content: "fix the bug"})
	s.initTurnProgress(80, 65)
	r := &Runner{Cfg: config.Config{Workspace: dir}}
	r.runRoutedTurn(s, context.Background(), &db.AgentPresetRow{ID: "grok", Driver: "grok"}, TurnOpts{})
	msgs := s.UIMessages()
	if len(msgs) != 2 || msgs[1].Role != "assistant" {
		t.Fatalf("msgs %+v", msgs)
	}
	body := msgs[1].Content
	if !strings.Contains(body, "[subprocess: grok · error]") || !strings.Contains(body, "not inside a git work tree") || !strings.Contains(body, "was not started") {
		t.Fatal(body)
	}
	if strings.Contains(body, "agent process manager") {
		t.Fatal("agent was considered for launch: " + body)
	}
}

func TestRunRoutedTurnUsesProjectDir(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	sub := filepath.Join(repo, "projects", "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	s := newSession("sidcwd2", "t")
	s.AgentCWD = "projects/app"
	s.appendUI(Message{Role: "user", Content: "fix the bug"})
	s.initTurnProgress(80, 65)
	r := &Runner{Cfg: config.Config{Workspace: repo}}
	r.runRoutedTurn(s, context.Background(), &db.AgentPresetRow{ID: "grok", Driver: "grok"}, TurnOpts{})
	body := s.UIMessages()[1].Content
	if !strings.Contains(body, "agent process manager unavailable") {
		t.Fatal(body)
	}
	if strings.Contains(body, "not inside a git work tree") {
		t.Fatal(body)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, step := range s.turn.prog.Steps {
		if strings.Contains(step.Detail, "cwd "+sub) {
			found = true
		}
	}
	if !found {
		t.Fatalf("cwd step missing: %+v", s.turn.prog.Steps)
	}
}

func TestAgentCWDReloads(t *testing.T) {
	s := newSession("sidcwd3", "t")
	s.AgentCWD = "projects/app"
	out, raw := reloadRoundTrip(t, s)
	if out.AgentCWD != "projects/app" || out.Summary().AgentCWD != "projects/app" {
		t.Fatalf("cwd %q summary %q\n%s", out.AgentCWD, out.Summary().AgentCWD, raw)
	}
	if !strings.Contains(raw, `agent_cwd: "projects/app"`) {
		t.Fatal(raw)
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}
