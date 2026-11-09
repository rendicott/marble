package tools

import (
	"fmt"
	"strings"

	"github.com/rendicott/marble/internal/watch"
)

type watchArgs struct {
	Action      string `json:"action"`
	Command     string `json:"command"`
	Label       string `json:"label"`
	CWD         string `json:"cwd"`
	Prompt      string `json:"prompt"`
	DeadlineSec int    `json:"deadline_sec"`
	ID          string `json:"id"`
	Reason      string `json:"reason"`
}

func (r *Registry) watchTool(argsJSON string, tc *TurnContext) (string, error) {
	if r.Watch == nil {
		return "", fmt.Errorf("watch not configured")
	}
	if tc == nil || tc.SessionID == "" {
		return "", fmt.Errorf("session context required")
	}
	var a watchArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(a.Action)) {
	case "create":
		row, err := r.Watch.Create(tc.SessionID, watch.CreateArgs{
			Command: a.Command, CWD: a.CWD, Label: a.Label, Prompt: a.Prompt, DeadlineSec: a.DeadlineSec,
		})
		if err != nil {
			return "", err
		}
		return mustJSON(map[string]interface{}{
			"action":          "create",
			"watch_id":        row.ID,
			"status":          row.Status,
			"label":           row.Label,
			"task_id":         row.TaskID,
			"continuation_id": row.ContinuationID,
			"deadline_at":     row.DeadlineAt,
			"note":            "Running in the background. You will be woken when it exits; do not report this work as finished before then.",
		}), nil
	case "status":
		if strings.TrimSpace(a.ID) == "" {
			return "", fmt.Errorf("id is required")
		}
		v, err := r.Watch.Status(tc.SessionID, a.ID)
		if err != nil {
			return "", err
		}
		return mustJSON(v), nil
	case "list":
		views, err := r.Watch.List(tc.SessionID)
		if err != nil {
			return "", err
		}
		return mustJSON(map[string]interface{}{"watches": views}), nil
	case "cancel":
		if strings.TrimSpace(a.ID) == "" {
			return "", fmt.Errorf("id is required")
		}
		row, won, err := r.Watch.Cancel(tc.SessionID, a.ID, a.Reason, false)
		if err != nil {
			return "", err
		}
		out := map[string]interface{}{
			"watch_id":   row.ID,
			"status":     row.Status,
			"end_reason": row.EndReason,
			"cancelled":  won,
		}
		if !won {
			out["note"] = "The watch had already ended before this cancel; its result stands."
		}
		return mustJSON(out), nil
	default:
		return "", fmt.Errorf("unknown action %q (want create|status|list|cancel)", a.Action)
	}
}
