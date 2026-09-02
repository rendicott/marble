package model

import "testing"

func TestThoughtText(t *testing.T) {
	m := Message{
		Reasoning: "I should list files first",
		Content:   ContentFromText("using list_files"),
		ToolCalls: []ToolCall{{ID: "1", Type: "function", Function: FunctionCall{Name: "list_files", Arguments: "{}"}}},
	}
	got := ThoughtText(m)
	if got == "" || !contains(got, "list files") {
		t.Fatalf("got %q", got)
	}
	// tool-only, no prose
	m2 := Message{ToolCalls: []ToolCall{{ID: "1", Type: "function", Function: FunctionCall{Name: "x", Arguments: "{}"}}}}
	if ThoughtText(m2) != "" {
		t.Fatal("expected empty")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
