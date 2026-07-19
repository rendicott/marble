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
}

func (r *Registry) callAgentProcess(argsJSON string, tc *TurnContext) (string, error) {
	if r.Agents == nil {
		return "", fmt.Errorf("call_agent_process not configured")
	}
	var a agentProcArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}

	// Poll existing background agent task
	if a.TaskID != "" {
		t, ok := r.Agents.Get(a.TaskID)
		if !ok {
			return "", fmt.Errorf("agent task not found")
		}
		return mustJSON(taskView(t)), nil
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

	// Default to background for long agent runs when not specified? ADR: nudge BG, default false for short probes.
	// Prefer background when timeout is high or explicitly set.
	if a.Background {
		t, err := r.Agents.StartBackground(sessionID, req)
		if err != nil {
			return "", err
		}
		return mustJSON(map[string]interface{}{
			"agent_task_id": t.ID,
			"status":        t.Status,
			"format":        t.Format,
			"cwd":           t.CWD,
			"pid":           t.PID,
			"started_at":    t.StartedAt.Format(time.RFC3339),
			"note":          "Long-running external agent. Poll with call_agent_process {\"task_id\":\"" + t.ID + "\"} or wait; use kill_background_task is N/A — use call_agent_process is for agents. Kill via session stop or list agent tasks.",
			"poll":          map[string]string{"task_id": t.ID},
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

func taskView(t *agentproc.Task) map[string]interface{} {
	out := map[string]interface{}{
		"agent_task_id": t.ID,
		"status":        t.Status,
		"format":        t.Format,
		"cwd":           t.CWD,
		"pid":           t.PID,
		"started_at":    t.StartedAt.Format(time.RFC3339),
		"command":       t.Command,
		"prompt":        truncateStr(t.Prompt, 200),
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
	if t.Result != nil {
		out["result"] = t.Result
	}
	return out
}

func truncateStr(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
