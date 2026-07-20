package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/model"
	"github.com/rendicott/marble/internal/tools"
)

// Runner owns shared deps for agent turns.
type Runner struct {
	Cfg    config.Config
	Client *model.Client
	Tools  *tools.Registry
	Reg    *Registry // set after construction for DB logging
}

// PostUserMessage appends a user message and runs the agent loop in a new goroutine.
func (r *Runner) PostUserMessage(s *Session, text string) bool {
	return r.postMessage(s, text, false)
}

// PostContinuation injects a scheduled continuation prompt (same as user turn).
func (r *Runner) PostContinuation(s *Session, text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return true
	}
	if !strings.HasPrefix(text, "[scheduled continuation]") {
		text = "[scheduled continuation]\n" + text
	}
	return r.postMessage(s, text, true)
}

// PostCron injects a cron fire prompt (ADR-0015). Caller supplies [cron:id name] prefix.
func (r *Runner) PostCron(s *Session, text string) bool {
	return r.postMessage(s, text, true)
}

func (r *Runner) postMessage(s *Session, text string, continuation bool) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return true
	}
	if !s.tryBeginTurn() {
		return false
	}

	s.mu.Lock()
	if len(s.ui) == 0 && !continuation {
		s.Title = truncateTitle(text, 48)
	}
	uid := s.nextID("m")
	um := Message{
		ID:        uid,
		Role:      "user",
		Content:   text,
		CreatedAt: time.Now(),
	}
	s.appendUI(um)
	s.history = append(s.history, model.Message{Role: "user", Content: text})
	s.mu.Unlock()

	if r.Reg != nil {
		est := estimateTokens(text)
		kind := "user_message"
		if continuation {
			kind = "continuation"
		}
		r.Reg.logEvent(s, kind, "user", text, "", "", "", nil, nil, intPtr(est), nil, nil, "", "")
		r.Reg.syncSessionRow(s)
	}

	s.publish(Event{Type: "message", Message: &um})
	s.publish(Event{Type: "status", Status: "running"})

	go r.runTurn(s)
	return true
}

func (r *Runner) runTurn(s *Session) {
	defer func() {
		s.endTurn()
		if r.Reg != nil {
			r.Reg.syncSessionRow(s)
		}
	}()

	hard := r.Cfg.MaxToolIters
	soft := r.Cfg.ToolRoundSoft
	if soft <= 0 {
		soft = 65
	}
	if hard <= 0 {
		hard = 80
	}

	s.initTurnProgress(hard, soft)
	s.appendStep(TurnStep{Kind: "starting", Detail: "turn started"})
	s.publishTurnProgress()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	s.setTurnCancel(cancel)

	toolSpecs := r.Tools.Specs()
	toolEst := estimateTools(toolSpecs)
	budget := r.Cfg.Budget()

	readPaths := map[string]bool{}
	highUsageStreak := 0
	turnStart := time.Now()
	toolRounds := 0
	var lastReportedIn, lastReportedOut *int
	stopNote := ""

	tc := &tools.TurnContext{
		SessionID:   s.ID,
		SessionKind: s.Kind,
		ReadPaths:   readPaths,
		Ctx:         ctx,
		GetUsage: func() map[string]interface{} {
			return r.usageSnapshot(s, toolEst, lastReportedIn, lastReportedOut)
		},
		Compact: func(style string, keepLast int) (string, error) {
			return r.compactSession(ctx, s, style, keepLast)
		},
		OnAttachment: func(a tools.Attachment) {
			s.publish(Event{
				Type: "attachment",
				Attachment: &AttachmentInfo{
					Path: a.Path, Name: a.Name, Inline: a.Inline,
					Mime: a.Mime, Size: a.Size, Preview: a.Preview,
				},
			})
		},
	}

	for iter := 0; iter < hard; iter++ {
		if err := ctx.Err(); err != nil {
			stopNote = stopMessage(err, s)
			s.finalizeTurnProgress(phaseForErr(err, s), stopNote)
			if errors.Is(err, context.Canceled) || s.Progress().StopRequested {
				s.publish(Event{Type: "harness", Status: stopNote})
			} else {
				s.publish(Event{Type: "error", Error: stopNote})
			}
			if r.Reg != nil {
				r.Reg.logEvent(s, "error", "", stopNote, "", "", "", nil, nil, nil, nil, nil, "", stopNote)
			}
			return
		}

		s.setIter(iter)
		// Soft wall advisory
		if toolRounds > 0 && time.Since(turnStart) >= r.Cfg.SoftWall && r.Cfg.SoftWall > 0 {
			r.advisory(s, fmt.Sprintf(
				"[harness] soft wall %s of continuous tool rounds. Consider schedule_continuation or a final user reply.",
				r.Cfg.SoftWall,
			))
		}

		s.mu.Lock()
		hist := make([]model.Message, len(s.history))
		copy(hist, s.history)
		s.mu.Unlock()

		// Ephemeral advisories injected into a copy for this call only
		prompt := trimHistory(hist, budget, toolEst)
		// ADR-0013: every-turn soul as second system message (user sessions only)
		prompt = r.injectSoul(s, prompt)
		estIn := estimateAll(prompt) + toolEst
		ratio := r.Cfg.UsageRatio(estIn)
		s.setContextUsage(ratio)

		var advisories []string
		if ratio >= r.Cfg.ContextWarnRatio {
			advisories = append(advisories, fmt.Sprintf(
				"[harness] context_usage=%.0f%% (soft warn ≥%.0f%%). Prefer get_context_usage / session_compact if history is large.",
				ratio*100, r.Cfg.ContextWarnRatio*100,
			))
		}
		if toolRounds >= soft {
			advisories = append(advisories, fmt.Sprintf(
				"[harness] tool rounds=%d soft cap %d. Consider schedule_continuation, compact, or final user update.",
				toolRounds, soft,
			))
		}
		if len(advisories) > 0 {
			note := strings.Join(advisories, "\n")
			if toolRounds == soft || (ratio >= r.Cfg.ContextWarnRatio && toolRounds%10 == 0) {
				r.advisory(s, note)
			}
			prompt = append([]model.Message{{Role: "system", Content: note}}, prompt...)
		}

		// Auto-compact path
		if ratio >= r.Cfg.ContextAutoCompactRatio {
			highUsageStreak++
		} else {
			highUsageStreak = 0
		}
		if highUsageStreak >= r.Cfg.ContextAutoCompactRounds {
			r.advisory(s, fmt.Sprintf("[harness] auto-compact: usage ≥%.0f%% for %d rounds", r.Cfg.ContextAutoCompactRatio*100, highUsageStreak))
			if _, err := r.compactSession(ctx, s, "auto", 12); err != nil {
				r.advisory(s, "[harness] auto-compact failed: "+err.Error())
			} else {
				highUsageStreak = 0
				s.mu.Lock()
				hist = make([]model.Message, len(s.history))
				copy(hist, s.history)
				s.mu.Unlock()
				prompt = trimHistory(hist, budget, toolEst)
				prompt = r.injectSoul(s, prompt)
				estIn = estimateAll(prompt) + toolEst
				s.setContextUsage(r.Cfg.UsageRatio(estIn))
			}
		}

		s.setPhase("calling_model")
		s.publish(Event{Type: "status", Status: "calling_model"})
		s.publishTurnProgress()

		result, err := r.Client.Chat(ctx, prompt, toolSpecs)
		if err != nil {
			stopNote = stopMessage(err, s)
			phase := phaseForErr(err, s)
			s.finalizeTurnProgress(phase, stopNote)
			if phase == "stopping" || errors.Is(err, context.Canceled) {
				s.publish(Event{Type: "harness", Status: stopNote})
			} else {
				s.publish(Event{Type: "error", Error: stopNote})
			}
			if r.Reg != nil {
				r.Reg.logEvent(s, "error", "", stopNote, "", "", "", nil, nil, nil, nil, nil, "", stopNote)
			}
			return
		}
		msg := result.Message
		finish := result.FinishReason
		var tin, tout *int
		if result.Usage != nil {
			tin = intPtr(result.Usage.PromptTokens)
			tout = intPtr(result.Usage.CompletionTokens)
			lastReportedIn, lastReportedOut = tin, tout
		}
		estOut := estimateTokens(msg.Content)
		lat := result.LatencyMs
		s.setLastModelLatency(lat)
		s.appendStep(TurnStep{
			Kind:    "model_call",
			Iter:    iter,
			Detail:  fmt.Sprintf("finish=%s", finish),
			Latency: &lat,
		})
		s.publishTurnProgress()

		if r.Reg != nil {
			r.Reg.logEvent(s, "model_call", "assistant", msg.Content, "", "", "", tin, tout, intPtr(estIn), intPtr(estOut), &lat, finish, "")
		}

		if len(msg.ToolCalls) > 0 {
			toolRounds++
			s.setToolRounds(toolRounds)
			s.mu.Lock()
			s.history = append(s.history, model.Message{
				Role:      "assistant",
				Content:   msg.Content,
				ToolCalls: msg.ToolCalls,
			})
			s.mu.Unlock()

			for _, call := range msg.ToolCalls {
				if err := ctx.Err(); err != nil {
					stopNote = stopMessage(err, s)
					s.finalizeTurnProgress(phaseForErr(err, s), stopNote)
					s.publish(Event{Type: "harness", Status: stopNote})
					if r.Reg != nil {
						r.Reg.logEvent(s, "error", "", stopNote, "", "", "", nil, nil, nil, nil, nil, "", stopNote)
					}
					return
				}

				name := call.Function.Name
				args := call.Function.Arguments
				s.setPhase("running_tool")
				s.setCurrentTool(call.ID, name, args, "start")
				s.appendStep(TurnStep{
					Kind:   "tool_start",
					Iter:   iter,
					Tool:   name,
					Detail: truncateOneLine(args, argsPreviewMax),
				})
				s.publish(Event{
					Type: "tool",
					Tool: &ToolInfo{ID: call.ID, Name: name, Args: args, Phase: "start"},
				})
				s.publish(Event{Type: "status", Status: "running"})
				s.publishTurnProgress()

				if r.Reg != nil {
					r.Reg.logEvent(s, "tool_call", "assistant", "", name, call.ID, args, nil, nil, nil, nil, nil, "", "")
				}

				toolResult := r.Tools.Execute(name, args, tc)

				s.finishCurrentTool(toolResult)
				s.appendStep(TurnStep{
					Kind:   "tool_result",
					Iter:   iter,
					Tool:   name,
					Detail: truncateOneLine(toolResult, resultTailMax),
				})
				s.publish(Event{
					Type: "tool",
					Tool: &ToolInfo{ID: call.ID, Name: name, Args: args, Result: toolResult, Phase: "result"},
				})
				s.publishTurnProgress()

				if r.Reg != nil {
					r.Reg.logEvent(s, "tool_result", "tool", toolResult, name, call.ID, "", nil, nil, nil, nil, nil, "", "")
				}

				s.mu.Lock()
				tid := s.nextID("t")
				tm := Message{
					ID:         tid,
					Role:       "tool",
					Content:    fmt.Sprintf("%s → %s", name, compact(toolResult, 400)),
					ToolName:   name,
					ToolCallID: call.ID,
					CreatedAt:  time.Now(),
				}
				s.appendUI(tm)
				s.history = append(s.history, model.Message{
					Role:       "tool",
					Content:    toolResult,
					ToolCallID: call.ID,
					Name:       name,
				})
				s.mu.Unlock()
				s.publish(Event{Type: "message", Message: &tm})
			}
			continue
		}

		content := strings.TrimSpace(msg.Content)
		if content == "" {
			content = "(empty model response)"
		}
		s.setPhase("finishing")
		s.publishTurnProgress()

		s.mu.Lock()
		aid := s.nextID("m")
		am := Message{
			ID:        aid,
			Role:      "assistant",
			Content:   content,
			CreatedAt: time.Now(),
		}
		s.appendUI(am)
		s.history = append(s.history, model.Message{Role: "assistant", Content: content})
		s.mu.Unlock()
		if r.Reg != nil {
			r.Reg.logEvent(s, "assistant_message", "assistant", content, "", "", "", tin, tout, nil, intPtr(estOut), &lat, finish, "")
			r.Reg.syncSessionRow(s)
		}
		s.publish(Event{Type: "message", Message: &am})
		// endTurn defer finalizes as complete
		return
	}

	msg := fmt.Sprintf("stopped: exceeded hard max tool iterations (%d soft=%d)", hard, soft)
	s.finalizeTurnProgress("error", msg)
	s.publish(Event{Type: "error", Error: msg})
	if r.Reg != nil {
		r.Reg.logEvent(s, "error", "", msg, "", "", "", nil, nil, nil, nil, nil, "", msg)
	}
}

func stopMessage(err error, s *Session) string {
	if s != nil {
		p := s.Progress()
		if p.StopRequested || errors.Is(err, context.Canceled) {
			return fmt.Sprintf("Turn stopped by operator after iter %d (tool rounds %d).", p.Iter, p.ToolRounds)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "turn timeout (15m wall)"
	}
	if err != nil {
		return err.Error()
	}
	return "turn stopped"
}

func phaseForErr(err error, s *Session) string {
	if s != nil && s.Progress().StopRequested {
		return "stopping"
	}
	if errors.Is(err, context.Canceled) {
		return "stopping"
	}
	return "error"
}

// injectSoul inserts $MEMORY/soul.md as a second system message after the first system
// message when non-empty. Skips system-agent sessions and blank soul (ADR-0013).
func (r *Runner) injectSoul(s *Session, prompt []model.Message) []model.Message {
	if s == nil || s.Kind == "system" {
		return prompt
	}
	if r.Reg == nil || r.Reg.Store() == nil {
		return prompt
	}
	soul, err := r.Reg.Store().ReadSoul()
	if err != nil || strings.TrimSpace(soul) == "" {
		return prompt
	}
	soul = strings.TrimSpace(soul)
	out := make([]model.Message, 0, len(prompt)+1)
	inserted := false
	for i, m := range prompt {
		out = append(out, m)
		if !inserted && i == 0 && m.Role == "system" {
			out = append(out, model.Message{Role: "system", Content: soul})
			inserted = true
		}
	}
	if !inserted {
		// No leading system (unusual after trim) — still place soul at front as system
		out = append([]model.Message{{Role: "system", Content: soul}}, prompt...)
	}
	return out
}

// advisory emits UI harness chip + DB event; never writes to session MD transcript body intentionally.
func (r *Runner) advisory(s *Session, note string) {
	s.publish(Event{Type: "harness", Status: note})
	s.appendStep(TurnStep{Kind: "advisory", Detail: truncateOneLine(note, 200)})
	s.publishTurnProgress()
	if r.Reg != nil {
		r.Reg.logEvent(s, "harness_advisory", "system", note, "", "", "", nil, nil, nil, nil, nil, "", "")
	}
}

func (r *Runner) usageSnapshot(s *Session, toolEst int, tin, tout *int) map[string]interface{} {
	s.mu.Lock()
	hist := make([]model.Message, len(s.history))
	copy(hist, s.history)
	n := len(s.ui)
	s.mu.Unlock()
	est := estimateAll(hist) + toolEst
	ratio := r.Cfg.UsageRatio(est)
	out := map[string]interface{}{
		"context_limit":           r.Cfg.ContextLimit,
		"max_output":              r.Cfg.MaxOutput,
		"context_reserve":         r.Cfg.ContextReserve,
		"budget":                  r.Cfg.Budget(),
		"estimated_prompt_tokens": est,
		"usage_ratio":             ratio,
		"message_count":           n,
		"soft_warn":               ratio >= r.Cfg.ContextWarnRatio,
		"recommend_compact":       ratio >= r.Cfg.ContextAutoCompactRatio,
	}
	if tin != nil {
		out["reported_in"] = *tin
	}
	if tout != nil {
		out["reported_out"] = *tout
	}
	out["est_in"] = est
	return out
}

// compactSession runs LLM summary via a system agent session and replaces middle history.
func (r *Runner) compactSession(ctx context.Context, target *Session, style string, keepLast int) (string, error) {
	if keepLast < 2 {
		keepLast = 2
	}
	target.mu.Lock()
	hist := make([]model.Message, len(target.history))
	copy(hist, target.history)
	targetID := target.ID
	target.mu.Unlock()

	var system []model.Message
	rest := hist
	if len(hist) > 0 && hist[0].Role == "system" {
		system = hist[:1]
		rest = hist[1:]
	}
	if len(rest) <= keepLast+2 {
		return "nothing to compact (history short)", nil
	}
	old := rest[:len(rest)-keepLast]
	keep := rest[len(rest)-keepLast:]

	var b strings.Builder
	for _, m := range old {
		fmt.Fprintf(&b, "[%s] %s\n", m.Role, truncateTitle(m.Content, 2000))
	}
	prompt := fmt.Sprintf(`You are a system compaction agent for Marble.
Summarize the following conversation history for style=%q.
Preserve: goals, decisions, file paths, commands, errors, open threads.
Be dense but complete. Output markdown summary only.\n\n%s`, style, b.String())

	var sysSess *Session
	if r.Reg != nil {
		sysSess = r.Reg.CreateSystem(fmt.Sprintf("compact · %s", targetID), targetID)
		sysSess.mu.Lock()
		sysSess.history = append(sysSess.history, model.Message{Role: "user", Content: prompt})
		sysSess.appendUI(Message{ID: sysSess.nextID("m"), Role: "user", Content: prompt, CreatedAt: time.Now()})
		sysSess.mu.Unlock()
	}

	summaryMsgs := []model.Message{
		{Role: "system", Content: "You compress agent transcripts into durable summaries."},
		{Role: "user", Content: prompt},
	}
	result, err := r.Client.Chat(ctx, summaryMsgs, nil)
	if err != nil {
		return "", err
	}
	summary := strings.TrimSpace(result.Message.Content)
	if summary == "" {
		summary = strings.TrimSpace(result.Message.Reasoning)
	}
	if summary == "" {
		return "", fmt.Errorf("empty compaction summary")
	}

	if sysSess != nil {
		sysSess.mu.Lock()
		sysSess.history = append(sysSess.history, model.Message{Role: "assistant", Content: summary})
		sysSess.appendUI(Message{ID: sysSess.nextID("m"), Role: "assistant", Content: summary, CreatedAt: time.Now()})
		sysSess.mu.Unlock()
		if r.Reg != nil {
			r.Reg.syncSessionRow(sysSess)
			_ = r.Reg.PersistSession(sysSess)
		}
	}

	block := model.Message{
		Role:    "system",
		Content: "[compacted history]\n" + summary,
	}
	newHist := make([]model.Message, 0, len(system)+1+len(keep))
	newHist = append(newHist, system...)
	newHist = append(newHist, block)
	newHist = append(newHist, keep...)

	target.mu.Lock()
	target.history = newHist
	target.dirty = true
	target.UpdatedAt = time.Now()
	target.mu.Unlock()

	if r.Reg != nil {
		r.Reg.logEvent(target, "compact", "system", summary, "", "", "", nil, nil, nil, nil, nil, "", "")
		r.Reg.syncSessionRow(target)
	}
	r.advisory(target, "[harness] session_compact applied via system agent")
	return fmt.Sprintf("compacted: kept last %d messages; summary_chars=%d", keepLast, len(summary)), nil
}

func truncateTitle(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func compact(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
