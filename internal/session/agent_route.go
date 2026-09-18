package session

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/agentproc"
	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/model"
)

const agentPresetPrefix = "agent:"

// ResolveAgentPresetID returns the preset to route this turn to, or "" to use the Marble model.
// Fall-through (missing/disabled/not detected) returns "" plus an advisory (ADR-0030 Q11).
func (r *Runner) ResolveAgentPresetID(s *Session, opts TurnOpts) (presetID, advisory string) {
	if s == nil {
		return "", ""
	}
	if s.Kind == "system" {
		if r.Tools == nil || r.Tools.Agents == nil || !r.Tools.Agents.SystemAgentsEnabled() {
			return "", ""
		}
	}
	id := strings.TrimSpace(opts.AgentPresetID)
	if id == "" {
		s.mu.Lock()
		id = strings.TrimSpace(s.AgentPresetID)
		s.mu.Unlock()
	}
	id = strings.TrimPrefix(id, agentPresetPrefix)
	if id == "" {
		return "", ""
	}
	if opts.CronModelID != "" && strings.TrimSpace(opts.AgentPresetID) == "" {
		return "", ""
	}
	if r.Reg == nil || r.Reg.sqldb == nil || !r.Reg.sqldb.Writable() {
		return "", fmt.Sprintf("[harness] agent preset %q unavailable (no catalog); using Marble model", id)
	}
	row, err := r.Reg.sqldb.GetAgentPreset(id)
	if err != nil || row == nil {
		return "", fmt.Sprintf("[harness] agent preset %q not found; using Marble model", id)
	}
	if !row.Enabled {
		return "", fmt.Sprintf("[harness] agent preset %q is disabled; using Marble model", id)
	}
	probe := agentproc.ProbeCommand(row.Command)
	if !probe.Detected {
		return "", fmt.Sprintf("[harness] agent preset %q command %q not found on PATH; using Marble model", id, row.Command)
	}
	return row.ID, ""
}

func wrapRoutedPrompt(sessionID, workspace, userText string) string {
	var b strings.Builder
	b.WriteString("You are operating in Marble session ")
	b.WriteString(sessionID)
	b.WriteString(" as an external coding agent.\n")
	if workspace != "" {
		b.WriteString("Workspace (cwd): ")
		b.WriteString(workspace)
		b.WriteString("\n")
	}
	b.WriteString("Reply with your result. Do not ask the Marble operator to paste this back.\n\n")
	b.WriteString(userText)
	return b.String()
}

func formatRoutedResult(presetID string, res agentproc.Result) string {
	st := "ok"
	if !res.OK {
		st = "error"
	}
	sec := res.DurationMs / 1000
	header := fmt.Sprintf("[subprocess: %s · %s · %ds · cwd=%s]", presetID, st, sec, res.CWD)
	body := strings.TrimSpace(res.Summary)
	if body == "" {
		body = strings.TrimSpace(fmt.Sprint(res.Raw))
	}
	if res.Error != "" {
		if body != "" {
			body += "\n\n"
		}
		body += res.Error
	}
	if body == "" {
		body = "(no output)"
	}
	return header + "\n" + body
}

func lastUserText(s *Session) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.ui) - 1; i >= 0; i-- {
		if s.ui[i].Role == "user" {
			return s.ui[i].Content
		}
	}
	return ""
}

func (r *Runner) runRoutedTurn(s *Session, ctx context.Context, preset *db.AgentPresetRow, opts TurnOpts) {
	userText := lastUserText(s)
	if strings.HasPrefix(strings.TrimSpace(userText), "[scheduled continuation]") {
		r.advisory(s, "[harness] continuation not routed to subprocess")
		return
	}
	ws := r.Cfg.Workspace
	prompt := wrapRoutedPrompt(s.ID, ws, userText)
	s.setPhase("running_tool")
	s.appendStep(TurnStep{Kind: "starting", Detail: "routing → " + preset.ID + " (" + preset.Driver + ")"})
	s.publishTurnProgress()

	if r.Tools == nil || r.Tools.Agents == nil {
		r.emitRoutedAssistant(s, "[subprocess: "+preset.ID+" · error]\nagent process manager unavailable")
		return
	}
	m := r.Tools.Agents
	timeout := preset.TimeoutSec
	block := r.buildSubprocessBlock(s, opts, preset, userText, nil)
	if block.Marker != "" && block.Marker != "none" {
		s.appendStep(TurnStep{Kind: "advisory", Detail: "context " + block.Marker})
		s.publishTurnProgress()
	}
	req := agentproc.Request{
		Format:              preset.Driver,
		Prompt:              prompt,
		CWD:                 ws,
		Model:               preset.Model,
		TimeoutSec:          timeout,
		OutputFormat:        "json",
		CommandOverride:     preset.Command,
		ArgsOverride:        preset.DefaultArgs,
		IgnoreDriverEnabled: true,
		ContextBlock:        block.Text,
		ContextMarker:       block.Marker,
	}
	t, err := m.StartBackground(s.ID, req)
	if err != nil {
		r.emitRoutedAssistant(s, formatRoutedResult(preset.ID, agentproc.Result{OK: false, Error: err.Error(), CWD: ws}))
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = m.Kill(t.ID, true)
			r.emitRoutedAssistant(s, fmt.Sprintf("[subprocess: %s · stopped]\n%s", preset.ID, ctx.Err()))
			s.finalizeTurnProgress("complete", "subprocess stopped")
			return
		case <-ticker.C:
			task, ok := m.Get(t.ID)
			if !ok {
				r.emitRoutedAssistant(s, "[subprocess: "+preset.ID+" · error]\ntask lost")
				s.finalizeTurnProgress("error", "task lost")
				return
			}
			if task.Progress != nil {
				detail := "subprocess running"
				if task.Progress.StuckHint {
					detail = "stuck_hint: " + task.Progress.StuckReason
				} else if task.Progress.CWDMtimeChanged {
					detail = "cwd mtime changed"
				}
				s.appendStep(TurnStep{Kind: "tool_result", Tool: "subprocess", Detail: detail})
				s.publishTurnProgress()
			}
			if task.Status == agentproc.StatusRunning {
				continue
			}
			res := agentproc.Result{OK: false, CWD: ws, Error: task.Error}
			if task.Result != nil {
				res = *task.Result
			}
			if task.Status == agentproc.StatusKilled {
				res.OK = false
				if res.Error == "" {
					res.Error = "killed"
				}
			}
			out := formatRoutedResult(preset.ID, res)
			r.emitRoutedAssistant(s, out)
			phase := "complete"
			if !res.OK {
				phase = "error"
			}
			s.finalizeTurnProgress(phase, "subprocess "+string(task.Status))
			return
		}
	}
}

func (r *Runner) emitRoutedAssistant(s *Session, content string) {
	s.mu.Lock()
	aid := s.nextID("m")
	am := Message{
		ID:        aid,
		Role:      "assistant",
		Content:   content,
		CreatedAt: time.Now(),
	}
	s.appendUI(am)
	s.history = append(s.history, model.Message{Role: "assistant", Content: model.ContentFromText(content)})
	s.mu.Unlock()
	if r.Reg != nil {
		r.Reg.logEvent(s, "assistant_message", "assistant", content, "", "", "", nil, nil, nil, nil, nil, "subprocess", "")
		r.Reg.syncSessionRow(s)
	}
	s.publish(Event{Type: "message", Message: &am})
}
