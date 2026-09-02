package agentproc

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewestMtime(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	mt, ok := newestMtime(dir, 3, 50)
	if !ok {
		t.Fatal("expected mtime")
	}
	if time.Since(mt) > time.Minute {
		t.Fatalf("unexpected old mtime %v", mt)
	}
}

func TestBuildProgressStuck(t *testing.T) {
	dir := t.TempDir()
	// Old file only — no change after start
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Backdate so grace window doesn't see it as "after start"
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "old.txt"), past, past)

	t0 := time.Now().Add(-10 * time.Minute)
	task := &Task{
		Status:    StatusRunning,
		CWD:       dir,
		StartedAt: t0,
	}
	p := buildProgress(task, 5*time.Minute, "", "")
	if p.ElapsedSec < 500 {
		t.Fatalf("elapsed %d", p.ElapsedSec)
	}
	if p.CWDMtimeChanged {
		t.Fatal("expected no mtime change")
	}
	if !p.StuckHint {
		t.Fatalf("expected stuck_hint %+v", p)
	}
}

func TestFilterExtraNoPlan(t *testing.T) {
	out := filterExtra([]string{"--no-plan", "--effort", "low", "--evil", "x"}, grokExtraAllow)
	joined := ""
	for _, a := range out {
		joined += a + " "
	}
	if !containsAll(out, "--no-plan", "--effort", "low") {
		t.Fatalf("got %v", out)
	}
	for _, a := range out {
		if a == "--evil" || a == "x" {
			t.Fatalf("evil flag leaked: %v", out)
		}
	}
}

func TestDedupeFlagsLastWins(t *testing.T) {
	in := []string{
		"/bin/grok", "-p", "hi",
		"--output-format", "json",
		"--cwd", "/tmp",
		"--always-approve",
		"--no-plan", "--effort", "medium", "--max-turns", "40",
		"--effort", "low", "--max-turns", "25", "--no-plan",
	}
	out := dedupeFlagsLastWins(in)
	// count --max-turns
	nMax, nEff, nPlan := 0, 0, 0
	var maxVal, effVal string
	for i := 0; i < len(out); i++ {
		switch out[i] {
		case "--max-turns":
			nMax++
			if i+1 < len(out) {
				maxVal = out[i+1]
			}
		case "--effort":
			nEff++
			if i+1 < len(out) {
				effVal = out[i+1]
			}
		case "--no-plan":
			nPlan++
		}
	}
	if nMax != 1 || nEff != 1 || nPlan != 1 {
		t.Fatalf("expected single flags, got %v", out)
	}
	if maxVal != "25" || effVal != "low" {
		t.Fatalf("last-wins failed: max=%s eff=%s full=%v", maxVal, effVal, out)
	}
}

func containsAll(have []string, want ...string) bool {
	set := map[string]bool{}
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}
