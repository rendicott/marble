package session

import (
	"testing"
	"time"

	"github.com/rendicott/marble/internal/memory"
)

func TestTaskRefsFromResult(t *testing.T) {
	cases := []struct {
		tool, result string
		want         []TaskRef
	}{
		{"schedule_continuation", `{"continuation_id":"c1","fire_at":"2026-10-08T10:00:00Z","prompt":"p"}`, []TaskRef{{"continuation", "c1"}}},
		{"start_background_task", `{"command":"make","task_id":"b1","status":"running"}`, []TaskRef{{"bg_task", "b1"}}},
		{"call_agent_process", `{"agent_task_id":"a1","status":"running","poll":{"task_id":"a1"}}`, []TaskRef{{"agent_task", "a1"}}},
		// A poll result repeats agent_task_id but did not create anything.
		{"call_agent_process", `{"agent_task_id":"a1","status":"running","progress":{}}`, nil},
		{"schedule_continuation", `error: continuations not configured`, nil},
		{"shell_execute", `{"task_id":"x"}`, nil},
	}
	for _, c := range cases {
		got := taskRefsFromResult(c.tool, c.result)
		if len(got) != len(c.want) || (len(got) == 1 && got[0] != c.want[0]) {
			t.Errorf("%s %s: got %v want %v", c.tool, c.result, got, c.want)
		}
	}
}

func TestRefsSurviveMarkdownRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	doc := &memory.SessionDoc{
		SessionMeta: memory.SessionMeta{ID: "refs01abc", Title: "r", CreatedAt: now, UpdatedAt: now, Status: "active"},
		Messages: []memory.TranscriptMessage{{
			ID: "t1", Role: "tool", ToolName: "schedule_continuation", ToolCallID: "call1", CreatedAt: now,
			Content: "schedule_continuation → {…}",
			Refs:    []memory.TranscriptRef{{Kind: "continuation", ID: "c1"}},
		}},
	}
	back, err := memory.DecodeSession(memory.EncodeSession(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Messages) != 1 || len(back.Messages[0].Refs) != 1 || back.Messages[0].Refs[0].ID != "c1" {
		t.Fatalf("refs lost on reload: %+v", back.Messages)
	}
}
