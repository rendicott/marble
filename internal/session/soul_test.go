package session

import (
	"testing"

	"github.com/rendicott/marble/internal/memory"
	"github.com/rendicott/marble/internal/model"
)

func TestInjectSoul(t *testing.T) {
	root := t.TempDir()
	store, err := memory.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSoul("HOUSE RULES"); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(nil, store, nil, "/ws", "m")
	r := &Runner{Reg: reg}

	user := newSession("siduser0001", "u")
	user.Kind = "user"
	prompt := []model.Message{
		{Role: "system", Content: SystemPrompt()},
		{Role: "user", Content: "hi"},
	}
	out := r.injectSoul(user, prompt)
	if len(out) != 3 {
		t.Fatalf("len %d", len(out))
	}
	if out[0].Role != "system" || out[1].Role != "system" || out[1].Content != "HOUSE RULES" {
		t.Fatalf("%+v", out)
	}
	if out[2].Role != "user" {
		t.Fatal(out[2])
	}

	// system agent: no inject
	sys := newSession("sidsys00001", "sys")
	sys.Kind = "system"
	out2 := r.injectSoul(sys, prompt)
	if len(out2) != 2 {
		t.Fatalf("system agent got soul: %+v", out2)
	}

	// blank soul omitted
	_ = store.WriteSoul("")
	out3 := r.injectSoul(user, prompt)
	if len(out3) != 2 {
		t.Fatalf("blank soul: %+v", out3)
	}
}

func TestSystemPromptNonEmpty(t *testing.T) {
	if SystemPrompt() == "" || len(SystemPrompt()) < 50 {
		t.Fatal("expected system prompt")
	}
}
