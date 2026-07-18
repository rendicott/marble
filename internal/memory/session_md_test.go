package memory

import (
	"strings"
	"testing"
	"time"
)

func TestEncodeDecodeSession(t *testing.T) {
	now := time.Date(2026, 7, 16, 20, 1, 2, 0, time.FixedZone("EDT", -4*3600))
	doc := &SessionDoc{
		SessionMeta: SessionMeta{
			ID:        "k7m2q9a3f2",
			Title:     "test session",
			CreatedAt: now,
			UpdatedAt: now.Add(time.Minute),
			Status:    "active",
			Workspace: "/tmp/ws",
			Model:     "Qwen/test",
		},
		Messages: []TranscriptMessage{
			{ID: "m1", Role: "user", Content: "hello", CreatedAt: now},
			{ID: "t1", Role: "tool", ToolName: "list_files", ToolCallID: "c1", Content: "file\ta\t1", CreatedAt: now.Add(time.Second)},
			{ID: "m2", Role: "assistant", Content: "hi there", CreatedAt: now.Add(2 * time.Second)},
		},
	}
	raw := EncodeSession(doc)
	got, err := DecodeSession(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != doc.ID || got.Title != doc.Title {
		t.Fatalf("meta: %+v", got.SessionMeta)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("msgs %d: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Role != "user" || got.Messages[0].Content != "hello" {
		t.Fatalf("user: %+v", got.Messages[0])
	}
	if got.Messages[1].ToolName != "list_files" {
		t.Fatalf("tool: %+v", got.Messages[1])
	}
	if got.Messages[2].Content != "hi there" {
		t.Fatalf("asst: %+v", got.Messages[2])
	}
}

func TestNewSessionID(t *testing.T) {
	id := NewSessionID()
	if len(id) != 10 {
		t.Fatalf("len=%d id=%s", len(id), id)
	}
	for _, c := range id {
		if !strings.ContainsRune(crockford, c) {
			t.Fatalf("bad char %c in %s", c, id)
		}
	}
}

func TestEncodeDaily(t *testing.T) {
	day := time.Date(2026, 7, 16, 12, 0, 0, 0, time.Local)
	doc := &SessionDoc{
		SessionMeta: SessionMeta{ID: "abc123xyz0", Title: "t", Status: "active", UpdatedAt: day},
		Messages: []TranscriptMessage{
			{Role: "user", Content: "do stuff"},
			{Role: "assistant", Content: "done"},
		},
	}
	raw := EncodeDaily(day, []*SessionDoc{doc})
	if !strings.Contains(raw, "date: 2026-07-16") {
		t.Fatal(raw)
	}
	if !strings.Contains(raw, "abc123xyz0") {
		t.Fatal(raw)
	}
}
