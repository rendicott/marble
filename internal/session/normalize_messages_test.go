package session

import (
	"strings"
	"testing"

	"github.com/rendicott/marble/internal/model"
)

func TestNormalizeOutboundMergesLeadingSystems(t *testing.T) {
	in := []model.Message{
		{Role: "system", Content: model.ContentFromText("base prompt")},
		{Role: "system", Content: model.ContentFromText("soul rules")},
		{Role: "system", Content: model.ContentFromText("[compacted history]\nsummary")},
		{Role: "user", Content: model.ContentFromText("hi")},
		{Role: "assistant", Content: model.ContentFromText("hello")},
	}
	out := normalizeOutboundChatMessages(in)
	if len(out) != 3 {
		t.Fatalf("len=%d want 3: %+v", len(out), roles(out))
	}
	if out[0].Role != "system" {
		t.Fatalf("first role %s", out[0].Role)
	}
	txt := out[0].Content.PlainText()
	for _, want := range []string{"base prompt", "soul rules", "compacted history"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("merged system missing %q:\n%s", want, txt)
		}
	}
	if out[1].Role != "user" || out[2].Role != "assistant" {
		t.Fatalf("rest roles %v", roles(out))
	}
}

func TestNormalizeOutboundDemotesMidSystem(t *testing.T) {
	in := []model.Message{
		{Role: "system", Content: model.ContentFromText("base")},
		{Role: "user", Content: model.ContentFromText("u1")},
		{Role: "system", Content: model.ContentFromText("orphan system")},
		{Role: "assistant", Content: model.ContentFromText("a1")},
	}
	out := normalizeOutboundChatMessages(in)
	if out[0].Role != "system" {
		t.Fatal("want leading system")
	}
	for i := 1; i < len(out); i++ {
		if out[i].Role == "system" {
			t.Fatalf("system at index %d after non-system", i)
		}
	}
	if out[2].Role != "user" || !strings.Contains(out[2].Content.PlainText(), "orphan system") {
		t.Fatalf("orphan not demoted: %+v", out[2])
	}
}

func roles(msgs []model.Message) []string {
	var r []string
	for _, m := range msgs {
		r = append(r, m.Role)
	}
	return r
}
