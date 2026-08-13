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

func TestSanitizeOrphanToolResults(t *testing.T) {
	// MD reload shape: tool row without a preceding assistant tool_calls block
	in := []model.Message{
		{Role: "user", Content: model.ContentFromText("ssh please")},
		{Role: "tool", Content: model.ContentFromText("found host"), Name: "memory_search", ToolCallID: "c1"},
		{Role: "user", Content: model.ContentFromText("try again")},
	}
	out := sanitizeToolCallHistory(in)
	if len(out) != 3 {
		t.Fatalf("len=%d roles=%v", len(out), roles(out))
	}
	if out[1].Role != "user" || !strings.Contains(out[1].Content.PlainText(), "memory_search") {
		t.Fatalf("orphan tool not demoted: %+v", out[1])
	}
	if out[2].Role != "user" {
		t.Fatalf("last user lost: %v", roles(out))
	}
}

func TestSanitizeFillsToolName(t *testing.T) {
	in := []model.Message{
		{Role: "user", Content: model.ContentFromText("go")},
		{Role: "assistant", ToolCalls: []model.ToolCall{
			{ID: "c1", Type: "function", Function: model.FunctionCall{Name: "memory_search", Arguments: `{"q":"x"}`},
				ExtraContent: []byte(`{"google":{"thought_signature":"SIG"}}`)},
		}},
		{Role: "tool", Content: model.ContentFromText("ok"), ToolCallID: "c1"}, // name missing
	}
	out := sanitizeToolCallHistory(in)
	if len(out) != 3 || out[2].Role != "tool" {
		t.Fatalf("roles=%v", roles(out))
	}
	if out[2].Name != "memory_search" {
		t.Fatalf("name not filled: %q", out[2].Name)
	}
}

func TestSanitizeIncompleteToolRound(t *testing.T) {
	in := []model.Message{
		{Role: "user", Content: model.ContentFromText("go")},
		{Role: "assistant", ToolCalls: []model.ToolCall{
			{ID: "c1", Type: "function", Function: model.FunctionCall{Name: "memory_search", Arguments: `{}`}},
		}},
		// no tool result — user continues
		{Role: "user", Content: model.ContentFromText("try again")},
	}
	out := sanitizeToolCallHistory(in)
	// assistant tool_calls should be collapsed so Gemini does not see dangling calls
	if len(out) != 3 {
		t.Fatalf("len=%d roles=%v", len(out), roles(out))
	}
	if len(out[1].ToolCalls) != 0 {
		t.Fatalf("expected tool_calls cleared, got %d", len(out[1].ToolCalls))
	}
	if !strings.Contains(out[1].Content.PlainText(), "memory_search") {
		t.Fatalf("want note about tools: %q", out[1].Content.PlainText())
	}
}

func roles(msgs []model.Message) []string {
	var r []string
	for _, m := range msgs {
		r = append(r, m.Role)
	}
	return r
}
