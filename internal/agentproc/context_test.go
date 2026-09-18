package agentproc

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeNoneAndFullWinsCompact(t *testing.T) {
	if got := NormalizeSources([]string{"none"}, ""); got != nil {
		t.Fatalf("%v", got)
	}
	got := NormalizeSources([]string{"full", "compact", "memory"}, "")
	if strings.Join(got, ",") != "full,memory" {
		t.Fatalf("%v", got)
	}
}

func TestAssembleNoneEmpty(t *testing.T) {
	b := Assemble(ContextSpec{Sources: []string{"none"}, Set: true}, ContextInputs{SessionID: "s1", Prompt: "hi"})
	if b.Text != "" || b.Marker != "none" {
		t.Fatalf("%+v", b)
	}
}

func TestAssembleDefaultFullMemory(t *testing.T) {
	in := ContextInputs{
		SessionID: "0wdabc1234",
		Turns: []TurnText{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi there"},
		},
		MemoryHits: []string{"knowledge/foo.md: …bar…"},
		Prompt:     "continue",
	}
	b := Assemble(DefaultContextSpec(), in)
	if !strings.Contains(b.Text, "source: full") || !strings.Contains(b.Text, "source: memory") {
		t.Fatalf("%s", b.Text)
	}
	if !strings.Contains(b.Text, "[user] hello") || !strings.Contains(b.Text, "knowledge/foo.md") {
		t.Fatalf("%s", b.Text)
	}
	if !strings.Contains(b.Marker, "full+memory") {
		t.Fatalf("marker %s", b.Marker)
	}
}

func TestAssembleDropOrder(t *testing.T) {
	in := ContextInputs{
		SessionID:  "s",
		Turns:      []TurnText{{Role: "user", Content: strings.Repeat("u", 200)}},
		MemoryHits: []string{strings.Repeat("m", 200)},
		ReadPaths:  []string{"a.txt", "b.txt"},
		Prompt:     "x",
	}
	b := Assemble(ContextSpec{Sources: []string{"full", "memory", "read_paths"}, MaxChars: 80, Set: true}, in)
	if strings.Contains(b.Text, "source: read_paths") {
		t.Fatalf("read_paths should drop first: %s", b.Text)
	}
	if utf8.RuneCountInString(b.Text) > 80 {
		t.Fatalf("len %d", utf8.RuneCountInString(b.Text))
	}
}

func TestAssembleRedactsSecrets(t *testing.T) {
	in := ContextInputs{
		SessionID: "s",
		Turns:     []TurnText{{Role: "user", Content: "key orb_ak_supersecret and more"}},
		Prompt:    "x",
	}
	b := Assemble(ContextSpec{Sources: []string{"full"}, MaxChars: 2000, Set: true}, in)
	if strings.Contains(b.Text, "orb_ak_supersecret") {
		t.Fatalf("secret leaked: %s", b.Text)
	}
	if !strings.Contains(b.Text, "[redacted]") {
		t.Fatalf("%s", b.Text)
	}
}

func TestResolveCallWins(t *testing.T) {
	call := ContextSpec{Sources: []string{"none"}, Set: true}
	sess := ContextSpec{Sources: []string{"full"}, Set: true}
	fb := DefaultContextSpec()
	got := ResolveContextSpecWithPrompt(call, sess, fb, "hello")
	if len(got.Sources) != 0 {
		t.Fatalf("%v", got.Sources)
	}
}

func TestResolveSessionThenDefault(t *testing.T) {
	sess := ContextSpec{Sources: []string{"read_paths"}, Set: true, MaxChars: 1000}
	got := ResolveContextSpecWithPrompt(ContextSpec{}, sess, DefaultContextSpec(), "hi")
	if strings.Join(got.Sources, ",") != "read_paths" || got.MaxChars != 1000 {
		t.Fatalf("%+v", got)
	}
	got = ResolveContextSpecWithPrompt(ContextSpec{}, ContextSpec{}, DefaultContextSpec(), "hi")
	if strings.Join(got.Sources, ",") != "full,memory" {
		t.Fatalf("%v", got.Sources)
	}
}

func TestAutoHeuristic(t *testing.T) {
	if !autoWantsFull("please continue this session") {
		t.Fatal("expected full")
	}
	if autoWantsFull("fix the typo in main.go") {
		t.Fatal("expected compact path")
	}
	got := NormalizeSources([]string{"auto"}, "as we discussed earlier")
	if strings.Join(got, ",") != "full,memory" {
		t.Fatalf("%v", got)
	}
}

func TestInjectAndMarker(t *testing.T) {
	block := ContextBlock{Text: "[CONTEXT — Marble session s · source: full]\nhi", Marker: "full · 40 chars"}
	p := InjectContext(block, "do the thing")
	if !strings.HasSuffix(p, "do the thing") || !strings.Contains(p, "source: full") {
		t.Fatal(p)
	}
	if InjectContext(ContextBlock{}, "x") != "x" {
		t.Fatal("empty")
	}
}
