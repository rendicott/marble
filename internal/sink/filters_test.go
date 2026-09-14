package sink

import "testing"

func TestFiltersMatch(t *testing.T) {
	ev := TurnEvent{SessionID: "abc", SessionTitle: "hello", Kind: "complete", Message: "hi there"}
	f := Filters{}
	f.normalize()
	if ok, _ := f.Match(ev); !ok {
		t.Fatal("empty filters should match")
	}

	skip := true
	f = Filters{SkipEmpty: &skip}
	f.normalize()
	if ok, reason := f.Match(TurnEvent{Kind: "complete", Message: "  "}); ok || reason != "empty" {
		t.Fatalf("skip empty: ok=%v reason=%s", ok, reason)
	}

	f = Filters{Kinds: []string{"error"}}
	f.normalize()
	if ok, _ := f.Match(ev); ok {
		t.Fatal("kind allow-list should drop complete")
	}
	ev.Kind = "error"
	if ok, _ := f.Match(ev); !ok {
		t.Fatal("error should pass")
	}

	f = Filters{SkipCron: true}
	f.normalize()
	if ok, reason := f.Match(TurnEvent{Kind: "cron", SessionTitle: "x", Message: "m"}); ok || reason != "cron" {
		t.Fatalf("skip cron kind: %v %s", ok, reason)
	}
	if ok, reason := f.Match(TurnEvent{Kind: "complete", SessionTitle: "cron: morning", Message: "m"}); ok || reason != "cron" {
		t.Fatalf("skip cron title: %v %s", ok, reason)
	}

	f = Filters{MinChars: 10}
	f.normalize()
	if ok, reason := f.Match(TurnEvent{Kind: "complete", Message: "short"}); ok || reason != "min_chars" {
		t.Fatalf("min_chars: %v %s", ok, reason)
	}

	f = Filters{OnlySessions: []string{"^abc$"}}
	f.normalize()
	if ok, _ := f.Match(TurnEvent{SessionID: "abc", Kind: "complete", Message: "m"}); !ok {
		t.Fatal("only_sessions should match id")
	}
	if ok, reason := f.Match(TurnEvent{SessionID: "zzz", Kind: "complete", Message: "m"}); ok || reason != "only_sessions" {
		t.Fatalf("only_sessions miss: %v %s", ok, reason)
	}

	f = Filters{SkipSessions: []string{"secret"}}
	f.normalize()
	if ok, reason := f.Match(TurnEvent{SessionTitle: "secret-ops", Kind: "complete", Message: "m"}); ok || reason != "skip_sessions" {
		t.Fatalf("skip_sessions: %v %s", ok, reason)
	}
}

func TestSkipEmptyDefaultTrue(t *testing.T) {
	f := Filters{}
	if !f.skipEmpty() {
		t.Fatal("nil skip_empty should default true")
	}
	off := false
	f.SkipEmpty = &off
	if f.skipEmpty() {
		t.Fatal("explicit false")
	}
}
