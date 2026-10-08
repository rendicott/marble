package session

import (
	"encoding/json"
	"strings"

	"github.com/rendicott/marble/internal/memory"
)

// TaskRef points a transcript row at the continuation or task its tool call created,
// so the UI can render a live chip (countdown, running/exited) next to it.
type TaskRef struct {
	Kind string `json:"kind"` // continuation | bg_task | agent_task
	ID   string `json:"id"`
}

// taskRefsFromResult extracts the id created by a scheduling/start tool call. Only
// creating calls count: an agent poll also returns agent_task_id, but only the
// background-start result carries the "poll" instructions object.
func taskRefsFromResult(toolName, result string) []TaskRef {
	var m map[string]interface{}
	if json.Unmarshal([]byte(strings.TrimSpace(result)), &m) != nil {
		return nil
	}
	str := func(k string) string {
		v, _ := m[k].(string)
		return strings.TrimSpace(v)
	}
	switch toolName {
	case "schedule_continuation":
		if id := str("continuation_id"); id != "" {
			return []TaskRef{{Kind: "continuation", ID: id}}
		}
	case "start_background_task":
		if id := str("task_id"); id != "" {
			return []TaskRef{{Kind: "bg_task", ID: id}}
		}
	case "call_agent_process":
		// Background start returns agent_task_id + poll; a poll (taskView) has no poll key.
		if id := str("agent_task_id"); id != "" {
			if _, isStart := m["poll"]; isStart {
				return []TaskRef{{Kind: "agent_task", ID: id}}
			}
		}
	}
	return nil
}

func transcriptRefs(in []TaskRef) []memory.TranscriptRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]memory.TranscriptRef, len(in))
	for i, r := range in {
		out[i] = memory.TranscriptRef{Kind: r.Kind, ID: r.ID}
	}
	return out
}

func uiRefsFromTranscript(in []memory.TranscriptRef) []TaskRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]TaskRef, len(in))
	for i, r := range in {
		out[i] = TaskRef{Kind: r.Kind, ID: r.ID}
	}
	return out
}

// NotifyPending tells connected UIs that this session's continuations or
// background tasks changed; they refetch /api/sessions/{id}/pending.
func (s *Session) NotifyPending() {
	if s == nil {
		return
	}
	s.publish(Event{Type: "pending"})
}
