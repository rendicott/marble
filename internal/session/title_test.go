package session

import (
	"strings"
	"testing"
)

func TestShouldAutoTitle(t *testing.T) {
	s := newSession("abc", "New session")
	if !shouldAutoTitleLocked(s) {
		t.Fatal("new user session should auto-title")
	}
	s.TitleCustom = true
	if shouldAutoTitleLocked(s) {
		t.Fatal("custom title should not auto")
	}
	s.TitleCustom = false
	s.Title = "cron: morning"
	if shouldAutoTitleLocked(s) {
		t.Fatal("cron title should not auto")
	}
	s.Title = "hello"
	s.Kind = "system"
	if shouldAutoTitleLocked(s) {
		t.Fatal("system kind should not auto")
	}
}

func TestCreateSystemPinsTitle(t *testing.T) {
	reg := NewRegistry(nil, nil, nil, "/tmp", "m")
	s := reg.CreateSystem("compact · parent", "parentid")
	if !s.TitleCustom {
		t.Fatal("system session should pin title")
	}
	if s.Title != "compact · parent" {
		t.Fatalf("title %q", s.Title)
	}
}

func TestSetSessionTitlePins(t *testing.T) {
	reg := NewRegistry(nil, nil, nil, "/tmp", "m")
	s := reg.Create("New session")
	if s.TitleCustom {
		t.Fatal("new session should not be custom")
	}
	out, err := reg.SetSessionTitle(s.ID, "  My Project  ")
	if err != nil {
		t.Fatal(err)
	}
	if out.Title != "My Project" || !out.TitleCustom {
		t.Fatalf("got title=%q custom=%v", out.Title, out.TitleCustom)
	}
	if shouldAutoTitleLocked(out) {
		t.Fatal("after rename should not auto")
	}
}

func TestStripWonderstandEnvelope(t *testing.T) {
	raw := "[Wonderstand visual mode]\nPrefer answering with a concrete visual\n---\nFind REI stores near SFO"
	got := stripWonderstandEnvelope(raw)
	if got != "Find REI stores near SFO" {
		t.Fatalf("got %q", got)
	}
	if stripWonderstandEnvelope("plain ask") != "plain ask" {
		t.Fatal("plain passthrough")
	}
}

func TestDeriveAutoTitleWonderstand(t *testing.T) {
	env := "[Wonderstand visual mode]\nstuff\n---\nREI stores on SFO to Yosemite route please"
	got := deriveAutoTitle("ws: Wonderstand", env, "wonderstand")
	if !strings.HasPrefix(got, "ws: ") {
		t.Fatalf("want ws: prefix, got %q", got)
	}
	if strings.Contains(got, "Wonderstand visual") {
		t.Fatalf("envelope leaked: %q", got)
	}
	if !strings.Contains(got, "REI") {
		t.Fatalf("missing user text: %q", got)
	}
	// non-ws session, non-wonderstand client
	got2 := deriveAutoTitle("New session", "hello world research", "web")
	if got2 != "hello world research" {
		t.Fatalf("got %q", got2)
	}
}
