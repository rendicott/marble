package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/agentproc"
)

type agentProcArgs struct {
	Format       string   `json:"format"`
	Prompt       string   `json:"prompt"`
	CWD          string   `json:"cwd"`
	Workdir      string   `json:"workdir"`
	OutputFormat string   `json:"output_format"`
	TimeoutSec   int      `json:"timeout_sec"`
	Model        string   `json:"model"`
	ExtraArgs    []string `json:"extra_args"`
	Background   bool     `json:"background"`
	TaskID       string   `json:"task_id"` // poll existing bg agent task
	Detail       bool     `json:"detail"`  // include full command/prompt on poll
	Kill         bool     `json:"kill"`    // with task_id: terminate agent process group
}

func (r *Registry) callAgentProcess(argsJSON string, tc *TurnContext) (string, error) {
	if r.Agents == nil {
		return "", fmt.Errorf("call_agent_process not configured")
	}
	var a agentProcArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}

	// Poll / kill existing background agent task
	if a.TaskID != "" {
		if a.Kill {
			if err := r.Agents.Kill(a.TaskID, true); err != nil {
				return "", err
			}
			t, ok := r.Agents.Get(a.TaskID)
			if !ok {
				return mustJSON(map[string]interface{}{
					"agent_task_id": a.TaskID,
					"status":        "killed",
					"note":          "kill requested",
				}), nil
			}
			return mustJSON(taskView(t, true)), nil
		}
		t, ok := r.Agents.Get(a.TaskID)
		if !ok {
			return "", fmt.Errorf("agent task not found")
		}
		return mustJSON(taskView(t, a.Detail)), nil
	}

	if tc != nil && tc.SessionKind == "system" && !r.Agents.SystemAgentsEnabled() {
		return "", fmt.Errorf("call_agent_process disabled for system agents (set system_agents_enabled in agent_process.json)")
	}
	sessionID := ""
	if tc != nil {
		sessionID = tc.SessionID
	}
	if sessionID == "" {
		return "", fmt.Errorf("session context required")
	}

	cwd, err := r.Agents.ResolveCWD(a.CWD, a.Workdir)
	if err != nil {
		return "", err
	}

	req := agentproc.Request{
		Format:       a.Format,
		Prompt:       a.Prompt,
		CWD:          cwd,
		OutputFormat: a.OutputFormat,
		TimeoutSec:   a.TimeoutSec,
		Model:        a.Model,
		ExtraArgs:    a.ExtraArgs,
		Background:   a.Background,
	}

	// Prefer background for multi-minute agent runs so the Marble turn is not blocked.
	if a.Background {
		t, err := r.Agents.StartBackground(sessionID, req)
		if err != nil {
			return "", err
		}
		// First response includes command once; subsequent polls omit unless detail=true
		return mustJSON(map[string]interface{}{
			"agent_task_id": t.ID,
			"status":        t.Status,
			"format":        t.Format,
			"cwd":           t.CWD,
			"pid":           t.PID,
			"started_at":    t.StartedAt.Format(time.RFC3339),
			"command":       t.Command,
			"prompt_preview": truncateStr(t.Prompt, 200),
			"poll": map[string]string{
				"task_id": t.ID,
			},
			"note": "Background agent started. Poll with {\"task_id\":\"" + t.ID + "\"} (omit full command after first poll). " +
				"Judge progress by progress.cwd_mtime_changed / stuck_hint, not by identical poll text. " +
				"Kill: {\"task_id\":\"" + t.ID + "\",\"kill\":true}. " +
				"Do not kill under ~5–8m unless stuck_hint or no writes; prefer shorter prompts + medium effort.",
		}), nil
	}

	parent := context.Background()
	if tc != nil && tc.Ctx != nil {
		parent = tc.Ctx
	}
	res, err := r.Agents.RunSync(parent, sessionID, req)
	// Always return JSON body; err is rare (setup)
	if err != nil && res.Summary == "" && res.Error == "" {
		return "", err
	}
	return mustJSON(res), nil
}

// taskView is the poll-facing shape. detail=false omits bulky command/prompt while running.
func taskView(t *agentproc.Task, detail bool) map[string]interface{} {
	if t == nil {
		return map[string]interface{}{"error": "nil task"}
	}
	out := map[string]interface{}{
		"agent_task_id": t.ID,
		"status":        t.Status,
		"format":        t.Format,
		"cwd":           t.CWD,
		"pid":           t.PID,
		"started_at":    t.StartedAt.Format(time.RFC3339),
	}
	if t.EndedAt != nil {
		out["ended_at"] = t.EndedAt.Format(time.RFC3339)
	}
	if t.ExitCode != nil {
		out["exit_code"] = *t.ExitCode
	}
	if t.Error != "" {
		out["error"] = t.Error
	}
	if t.Progress != nil {
		out["progress"] = t.Progress
		if t.Progress.StuckHint {
			out["note"] = "stuck_hint: child still running but no cwd file changes / write signals for stuck_after_sec. " +
				"Prefer kill+retry with tighter prompt/extra_args, or wait if edits are expected soon. " +
				"Kill: {\"task_id\":\"" + t.ID + "\",\"kill\":true}."
		}
	}
	if t.Result != nil {
		out["result"] = t.Result
	}
	// Full command/prompt only when detail requested or task finished (result may need command for debug)
	if detail || t.Status != agentproc.StatusRunning {
		out["command"] = t.Command
		out["prompt_preview"] = truncateStr(t.Prompt, 400)
	} else {
		out["command_preview"] = commandPreview(t.Command)
		out["prompt_preview"] = truncateStr(t.Prompt, 120)
	}
	return out
}

func commandPreview(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	// Show binary + flags, hide long -p prompt body
	var parts []string
	skipNext := false
	for i, a := range argv {
		if skipNext {
			skipNext = false
			parts = append(parts, "…")
			continue
		}
		if a == "-p" || a == "--prompt" {
			parts = append(parts, a)
			skipNext = true
			continue
		}
		if i == 0 {
			parts = append(parts, a)
			continue
		}
		if len(a) > 48 {
			parts = append(parts, a[:45]+"…")
		} else {
			parts = append(parts, a)
		}
	}
	s := strings.Join(parts, " ")
	return truncateStr(s, 160)
}

func truncateStr(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
