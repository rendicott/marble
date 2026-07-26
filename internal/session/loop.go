package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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

	// CatalogGet optional override for tests; default uses Reg.sqldb.
	CatalogGet CatalogLookup

	clientMu    sync.Mutex
	clientCache map[string]*model.Client
}

// PostUserMessage appends a user message and runs the agent loop in a new goroutine.
// actor may be nil (open mode / anonymous).
func (r *Runner) PostUserMessage(s *Session, text string, actor *Actor) bool {
	return r.PostUserMessageWithAttachments(s, text, actor, nil)
}

// PostUserMessageWithAttachments commits optional staged attachment ids (ADR-0019).
func (r *Runner) PostUserMessageWithAttachments(s *Session, text string, actor *Actor, attachmentIDs []string) bool {
	return r.postMessage(s, text, false, actor, TurnOpts{}, attachmentIDs)
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
	return r.postMessage(s, text, true, nil, TurnOpts{}, nil)
}

// PostCron injects a cron fire prompt (ADR-0015/0018). modelID is optional catalog pin for this fire only.
func (r *Runner) PostCron(s *Session, text, modelID string) bool {
	return r.postMessage(s, text, true, nil, TurnOpts{CronModelID: strings.TrimSpace(modelID)}, nil)
}

func (r *Runner) postMessage(s *Session, text string, continuation bool, actor *Actor, opts TurnOpts, attachmentIDs []string) bool {
	text = strings.TrimSpace(text)
	if text == "" && len(attachmentIDs) == 0 {
		return true
	}
	// Build multimodal content before taking busy lock validation for attachments
	content, uis, err := r.buildUserContent(s.ID, text, attachmentIDs)
	if err != nil && len(attachmentIDs) > 0 {
		// attachment resolve errors — surface via false; caller maps
		return false
	}
	if err != nil {
		// no attachments path: empty text already handled
		content = model.ContentFromText(text)
	}
	if !s.tryBeginTurn() {
		return false
	}
	s.setTurnOpts(opts)

	display := text
	if display == "" && len(uis) > 0 {
		display = uis[0].Name
		if len(uis) > 1 {
			display = fmt.Sprintf("%d attachments", len(uis))
		}
	}

	s.mu.Lock()
	if len(s.ui) == 0 && !continuation {
		s.Title = truncateTitle(display, 48)
	}
	uid := s.nextID("m")
	um := Message{
		ID:          uid,
		Role:        "user",
		Content:     display,
		CreatedAt:   time.Now(),
		Attachments: uis,
	}
	if actor != nil {
		um.UserEmail = actor.Email
		um.UserName = actor.Name
		um.UserSub = actor.Sub
	}
	s.appendUI(um)
	// Model history: never identity (ADR-0017 Q13). Multimodal sentinels (ADR-0019).
	s.history = append(s.history, model.Message{Role: "user", Content: content})
	s.mu.Unlock()

	// Commit staged attachments
	if r.Reg != nil && r.Reg.sqldb != nil && r.Reg.sqldb.Writable() && len(attachmentIDs) > 0 {
		ids := make([]string, 0, len(uis))
		for _, u := range uis {
			ids = append(ids, u.ID)
		}
		_ = r.Reg.sqldb.CommitAttachments(s.ID, uid, ids, "user_upload")
	}

	if r.Reg != nil {
		est := estimateTokens(display)
		kind := "user_message"
		if continuation {
			kind = "continuation"
		}
		meta := ""
		if len(uis) > 0 {
			ids := make([]string, len(uis))
			for i, u := range uis {
				ids[i] = u.ID
			}
			meta = attMetaJSON(ids)
		}
		r.Reg.logEventMeta(s, kind, "user", display, "", "", "", nil, nil, intPtr(est), nil, nil, "", "", meta)
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

	opts := s.takeTurnOpts()
	em := r.resolveEffective(s, opts)
	// Persist last effective provider string (KD12)
	s.mu.Lock()
	s.ProviderModel = em.Model
	s.mu.Unlock()
	if em.Advisory != "" {
		r.advisory(s, em.Advisory)
	}

	hard := r.Cfg.MaxToolIters
	soft := r.Cfg.ToolRoundSoft
	if soft <= 0 {
		soft = 65
	}
	if hard <= 0 {
		hard = 80
	}

	s.initTurnProgress(hard, soft)
	s.appendStep(TurnStep{Kind: "starting", Detail: fmt.Sprintf("turn started model=%s source=%s", em.Model, em.Source)})
	s.publishTurnProgress()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	s.setTurnCancel(cancel)

	client := r.clientFor(em)
	var toolSpecs []model.ToolSpec
	if em.CapTools && r.Tools != nil {
		toolSpecs = r.Tools.Specs()
	} else if !em.CapTools {
		r.advisory(s, "[harness] model has cap_tools=false; tools omitted for this turn")
	}
	toolEst := estimateTools(toolSpecs)
	budget := em.Budget()

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
			return r.usageSnapshot(s, em, toolEst, lastReportedIn, lastReportedOut)
		},
		Compact: func(style string, keepLast int) (string, error) {
			return r.compactSession(ctx, s, style, keepLast, em)
		},
		OnAttachment: func(a tools.Attachment) {
			// attach_file — ephemeral SSE only (ADR-0005)
			s.publish(Event{
				Type: "attachment",
				Attachment: &AttachmentInfo{
					Path: a.Path, Name: a.Name, Inline: a.Inline,
					Mime: a.Mime, Size: a.Size, Preview: a.Preview,
				},
			})
		},
		OnChatAttachment: func(a tools.Attachment) {
			// message_attach — durable UI (ADR-0019 KD7)
			s.mu.Lock()
			uid := s.nextID("a")
			um := Message{
				ID:        uid,
				Role:      "attachment",
				Content:   a.Name,
				CreatedAt: time.Now(),
				Attachments: []UIAttachment{{
					ID: a.Path, Name: a.Name, MIME: a.Mime, Kind: "document", Size: a.Size,
				}},
			}
			// If path looks like att id, mark as image when mime says so
			if strings.HasPrefix(a.Mime, "image/") {
				um.Attachments[0].Kind = "image"
			}
			s.appendUI(um)
			s.mu.Unlock()
			s.publish(Event{Type: "message", Message: &um})
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
		var stripped bool
		prompt, stripped = ApplyCapabilityFilter(prompt, em)
		if stripped {
			r.advisory(s, "[harness] image attachments omitted: active model has no image support (will re-include if you switch to a vision model)")
		}
		estIn := estimateAll(prompt) + toolEst
		ratio := em.UsageRatio(estIn)
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
			prompt = append([]model.Message{{Role: "system", Content: model.ContentFromText(note)}}, prompt...)
		}

		// Auto-compact path (uses turn em — ADR-0018 KD6)
		if ratio >= r.Cfg.ContextAutoCompactRatio {
			highUsageStreak++
		} else {
			highUsageStreak = 0
		}
		if highUsageStreak >= r.Cfg.ContextAutoCompactRounds {
			r.advisory(s, fmt.Sprintf("[harness] auto-compact: usage ≥%.0f%% for %d rounds", r.Cfg.ContextAutoCompactRatio*100, highUsageStreak))
			if _, err := r.compactSession(ctx, s, "auto", 12, em); err != nil {
				r.advisory(s, "[harness] auto-compact failed: "+err.Error())
			} else {
				highUsageStreak = 0
				s.mu.Lock()
				hist = make([]model.Message, len(s.history))
				copy(hist, s.history)
				s.mu.Unlock()
				prompt = trimHistory(hist, budget, toolEst)
				prompt = r.injectSoul(s, prompt)
				prompt, _ = ApplyCapabilityFilter(prompt, em)
				estIn = estimateAll(prompt) + toolEst
				s.setContextUsage(em.UsageRatio(estIn))
			}
		}

		s.setPhase("calling_model")
		s.publish(Event{Type: "status", Status: "calling_model"})
		s.publishTurnProgress()

		// KD13: materialize only deep clone for Chat (history stays sentinel)
		outbound, mErr := r.materializeImages(s.ID, prompt)
		if mErr != nil {
			r.advisory(s, "[harness] attachment materialize: "+mErr.Error())
			outbound = prompt
		}
		result, err := client.Chat(ctx, outbound, toolSpecs)
		if err != nil {
			stopNote = stopMessage(err, s)
			// Capability / provider errors surface as harness-visible errors
			if !em.CapImages && strings.Contains(strings.ToLower(err.Error()), "image") {
				stopNote = "model rejected request (images unsupported): " + err.Error()
			}
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
		estOut := estimateTokens(msg.Content.PlainText())
		lat := result.LatencyMs
		s.setLastModelLatency(lat)
		s.appendStep(TurnStep{
			Kind:    "model_call",
			Iter:    iter,
			Detail:  fmt.Sprintf("finish=%s model=%s", finish, em.Model),
			Latency: &lat,
		})
		s.publishTurnProgress()

		if r.Reg != nil {
			r.Reg.logModelCall(s, em, "assistant", msg.Content.PlainText(), tin, tout, intPtr(estIn), intPtr(estOut), &lat, finish, "")
		}

		if len(msg.ToolCalls) > 0 {
			toolRounds++
			s.setToolRounds(toolRounds)
			s.mu.Lock()
			s.history = append(s.history, model.Message{
				Role:      "assistant",
				Content:   model.ContentFromText(msg.Content.PlainText()),
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
					Content:    model.ContentFromText(toolResult),
					ToolCallID: call.ID,
					Name:       name,
				})
				s.mu.Unlock()
				s.publish(Event{Type: "message", Message: &tm})
			}
			continue
		}

		content := strings.TrimSpace(msg.Content.PlainText())
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
		s.history = append(s.history, model.Message{Role: "assistant", Content: model.ContentFromText(content)})
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
			out = append(out, model.Message{Role: "system", Content: model.ContentFromText(soul)})
			inserted = true
		}
	}
	if !inserted {
		// No leading system (unusual after trim) — still place soul at front as system
		out = append([]model.Message{{Role: "system", Content: model.ContentFromText(soul)}}, prompt...)
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

func (r *Runner) usageSnapshot(s *Session, em EffectiveModel, toolEst int, tin, tout *int) map[string]interface{} {
	s.mu.Lock()
	hist := make([]model.Message, len(s.history))
	copy(hist, s.history)
	n := len(s.ui)
	s.mu.Unlock()
	est := estimateAll(hist) + toolEst
	ratio := em.UsageRatio(est)
	out := map[string]interface{}{
		"context_limit":           em.ContextLimit,
		"max_output":              em.MaxOutput,
		"context_reserve":         em.ContextReserve,
		"budget":                  em.Budget(),
		"estimated_prompt_tokens": est,
		"usage_ratio":             ratio,
		"message_count":           n,
		"soft_warn":               ratio >= r.Cfg.ContextWarnRatio,
		"recommend_compact":       ratio >= r.Cfg.ContextAutoCompactRatio,
		"model":                   em.Model,
		"model_source":            em.Source,
		"catalog_id":              em.CatalogID,
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
// em is the current turn's EffectiveModel (including cron pin) — never re-resolved interactively.
func (r *Runner) compactSession(ctx context.Context, target *Session, style string, keepLast int, em EffectiveModel) (string, error) {
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
		fmt.Fprintf(&b, "[%s] %s\n", m.Role, truncateTitle(m.Content.PlainText(), 2000))
	}
	src := b.String()
	// Trim source to 80% of em.Budget() (ADR-0018 KD6)
	maxChars := em.Budget() * 3 * 8 / 10 // rough tokens→chars
	if maxChars < 2000 {
		maxChars = 2000
	}
	if len(src) > maxChars {
		src = "…[trimmed for compact budget]\n" + src[len(src)-maxChars:]
	}
	prompt := fmt.Sprintf(`You are a system compaction agent for Marble.
Summarize the following conversation history for style=%q.
Preserve: goals, decisions, file paths, commands, errors, open threads.
Be dense but complete. Output markdown summary only.\n\n%s`, style, src)

	var sysSess *Session
	if r.Reg != nil {
		sysSess = r.Reg.CreateSystem(fmt.Sprintf("compact · %s", targetID), targetID)
		sysSess.mu.Lock()
		sysSess.history = append(sysSess.history, model.Message{Role: "user", Content: model.ContentFromText(prompt)})
		sysSess.appendUI(Message{ID: sysSess.nextID("m"), Role: "user", Content: prompt, CreatedAt: time.Now()})
		sysSess.mu.Unlock()
	}

	summaryMsgs := []model.Message{
		{Role: "system", Content: model.ContentFromText("You compress agent transcripts into durable summaries.")},
		{Role: "user", Content: model.ContentFromText(prompt)},
	}
	client := r.clientFor(em)
	result, err := client.Chat(ctx, summaryMsgs, nil)
	if err != nil {
		return "", err
	}
	summary := strings.TrimSpace(result.Message.Content.PlainText())
	if summary == "" {
		summary = strings.TrimSpace(result.Message.Reasoning)
	}
	if summary == "" {
		return "", fmt.Errorf("empty compaction summary")
	}

	if sysSess != nil {
		sysSess.mu.Lock()
		sysSess.history = append(sysSess.history, model.Message{Role: "assistant", Content: model.ContentFromText(summary)})
		sysSess.appendUI(Message{ID: sysSess.nextID("m"), Role: "assistant", Content: summary, CreatedAt: time.Now()})
		sysSess.mu.Unlock()
		if r.Reg != nil {
			r.Reg.syncSessionRow(sysSess)
			_ = r.Reg.PersistSession(sysSess)
		}
	}

	block := model.Message{
		Role:    "system",
		Content: model.ContentFromText("[compacted history]\n" + summary),
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
