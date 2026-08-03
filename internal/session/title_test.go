package session

import (
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
