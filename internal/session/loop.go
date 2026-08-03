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
	if r.Reg != nil && r.Reg.OnSessionBusy != nil {
		r.Reg.OnSessionBusy(s.ID)
	}

	display := text
	if display == "" && len(uis) > 0 {
		display = uis[0].Name
		if len(uis) > 1 {
			display = fmt.Sprintf("%d attachments", len(uis))
		}
	}

	s.mu.Lock()
	// Auto-title from latest user message unless operator pinned a custom name.
	// System agents and cron-titled sessions never auto-title.
	titleUpdated := false
	if !continuation && shouldAutoTitleLocked(s) && strings.TrimSpace(display) != "" {
		nt := truncateTitle(display, 48)
		if nt != "" && nt != s.Title {
			s.Title = nt
			titleUpdated = true
		}
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
	titleSnap := s.Title
	customSnap := s.TitleCustom
	s.mu.Unlock()

	if titleUpdated {
		s.publish(Event{
			Type:        "session_meta",
			SessionID:   s.ID,
			Title:       titleSnap,
			TitleCustom: customSnap,
			At:          time.Now(),
		})
	}
	if r.Reg != nil && r.Reg.OnUserMessage != nil && !continuation {
		r.Reg.OnUserMessage(s.ID, display)
	}

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
			// ADR-0023 Clerk: enqueue idle summary after any busy→idle
			if r.Reg.OnSessionIdle != nil {
				r.Reg.OnSessionIdle(s)
			}
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

	hardWall := r.Cfg.HardWall
	if hardWall <= 0 {
		hardWall = 2 * time.Hour
	}
	ctx, cancel := context.WithTimeout(context.Background(), hardWall)
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
	softWallLastAt := time.Time{} // throttle soft-wall advisories (was every model call)
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
		OnPeerConfirm: func(confirm map[string]interface{}) {
			// Live Accept/Deny card in harness UI (operator may not have peer access).
			s.publish(Event{Type: "confirm", Confirm: confirm})
		},
		OnHarnessNote: func(note string) {
			r.advisory(s, note)
		},
	}

	for iter := 0; iter < hard; iter++ {
		if err := ctx.Err(); err != nil {
			stopNote = stopMessage(err, s)
			// Hard wall (time): auto-continue unless operator stop.
			if r.shouldAutoContinueOnErr(err, s) {
				r.autoStopAndContinueTC(s, stopNote, "auto:hard-wall", tc)
				return
			}
			// Always leave a visible assistant end-turn so the UI does not look "stuck mid-tool".
			r.forceEndAssistant(s, stopNote)
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

		// Near max tool iters: hard-stop + force schedule_continuation (do not die mid-work).
		if r.nearMaxToolIters(hard, iter, toolRounds) {
			reason := fmt.Sprintf(
				"Approaching hard max tool iterations (iter %d / hard %d, tool_rounds %d, soft %d, reserve %d). Stopping this turn and auto-continuing.",
				iter, hard, toolRounds, soft, r.Cfg.AutoContinueReserve,
			)
			r.autoStopAndContinueTC(s, reason, "auto:max-iters", tc)
			return
		}

		// Soft wall advisory: first time elapsed ≥ SoftWall, then at most once per SoftWall
		// period (previously every model call after 3m — noisy for long ops turns).
		if toolRounds > 0 && r.Cfg.SoftWall > 0 {
			elapsed := time.Since(turnStart)
			if elapsed >= r.Cfg.SoftWall && (softWallLastAt.IsZero() || time.Since(softWallLastAt) >= r.Cfg.SoftWall) {
				r.advisory(s, fmt.Sprintf(
					"[harness] soft wall: continuous tool work for %s (threshold %s). Still running — will auto-continue near max-tool-iters if needed.",
					elapsed.Round(time.Second), r.Cfg.SoftWall,
				))
				softWallLastAt = time.Now()
			}
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
			reserve := r.Cfg.AutoContinueReserve
			if reserve <= 0 {
				advisories = append(advisories, fmt.Sprintf(
					"[harness] tool rounds=%d soft cap %d. Consider schedule_continuation, compact, or final user update.",
					toolRounds, soft,
				))
			} else {
				advisories = append(advisories, fmt.Sprintf(
					"[harness] tool rounds=%d soft cap %d (hard %d). Prefer finishing or compacting; harness will auto-continue when remaining iters ≤ %d.",
					toolRounds, soft, hard, reserve,
				))
			}
		}
		// ADR-0022: escalate lock advisory
		if tc.Thrash != nil && tc.Thrash.EscalateLock {
			advisories = append(advisories,
				"[harness] escalate lock ON (ADR-0022): desktop/browser clicks hard-blocked. Use computer_confirm, screenshot/snapshot, shell/API, or a different approach — not more identical clicks.",
			)
		}
		// ADR-0022 P3: soft checklist advisory mid long turn
		if toolRounds >= soft/2 && soft > 0 && (tc.Thrash == nil || !tc.Thrash.ChecklistHint) {
			advisories = append(advisories,
				"[harness] multi-step work: write a short checklist to memory or a workspace file (done/pending/evidence). Prefer verifying the user-stated success condition over provider-internal \"completed\".",
			)
			if tc.Thrash == nil {
				tc.Thrash = &tools.ThrashState{}
			}
			tc.Thrash.ChecklistHint = true
		}
		if len(advisories) > 0 {
			note := strings.Join(advisories, "\n")
			if toolRounds == soft || (ratio >= r.Cfg.ContextWarnRatio && toolRounds%10 == 0) ||
				(tc.Thrash != nil && tc.Thrash.EscalateLock && toolRounds%5 == 0) {
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
		// ADR-0022 KD13: computer-heavy long turns compact earlier
		computerHeavy := toolRounds >= 40 && tc.ComputerHeavyShare(20) >= 0.5
		needCompact := highUsageStreak >= r.Cfg.ContextAutoCompactRounds ||
			(computerHeavy && ratio >= r.Cfg.ContextWarnRatio)
		if needCompact {
			why := fmt.Sprintf("usage ≥%.0f%% for %d rounds", r.Cfg.ContextAutoCompactRatio*100, highUsageStreak)
			if computerHeavy && highUsageStreak < r.Cfg.ContextAutoCompactRounds {
				why = fmt.Sprintf("computer-heavy turn (%.0f%% of last 20 tools are computer_*) at context %.0f%%", tc.ComputerHeavyShare(20)*100, ratio*100)
			}
			r.advisory(s, "[harness] auto-compact: "+why)
			keep := 12
			if computerHeavy {
				keep = 16
			}
			if _, err := r.compactSession(ctx, s, "auto", keep, em); err != nil {
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
		// Strict providers (vLLM): only one system block at the start; never mid-history.
		outbound = normalizeOutboundChatMessages(outbound)
		result, err := client.Chat(ctx, outbound, toolSpecs)
		if err != nil {
			stopNote = stopMessage(err, s)
			// Capability / provider errors surface as harness-visible errors
			if !em.CapImages && strings.Contains(strings.ToLower(err.Error()), "image") {
				stopNote = "model rejected request (images unsupported): " + err.Error()
			}
			if r.shouldAutoContinueOnErr(err, s) {
				r.autoStopAndContinueTC(s, stopNote, "auto:hard-wall", tc)
				return
			}
			r.forceEndAssistant(s, stopNote)
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
					if r.shouldAutoContinueOnErr(err, s) {
						r.autoStopAndContinueTC(s, stopNote, "auto:hard-wall", tc)
						return
					}
					r.forceEndAssistant(s, stopNote)
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
				// Inject vision parts for tool screenshots (computer_screenshot, etc.)
				// so the model receives pixels on the next Chat call — not only UI chips.
				modelContent := toolResultContent(toolResult)
				if !em.CapImages && modelContent.HasImages() {
					// Text fallback when process/catalog model cannot see images.
					modelContent = model.ContentFromText(toolResult +
						"\n[harness] WARNING: active model CapImages=false — screenshot was stored for the UI but NOT sent to the model. Switch this session to a vision-capable model (model_list / session_set_model) to see peer screenshots.")
					r.advisory(s, "[harness] computer screenshot omitted from model context: active model has no image support — switch to a vision model to use desktop screenshots")
				}
				tm := Message{
					ID:         tid,
					Role:       "tool",
					Content:    fmt.Sprintf("%s → %s", name, compact(toolResult, 400)),
					ToolName:   name,
					ToolCallID: call.ID,
					CreatedAt:  time.Now(),
					Attachments: uiAttachmentsFromToolResult(toolResult),
				}
				s.appendUI(tm)
				s.history = append(s.history, model.Message{
					Role:       "tool",
					Content:    modelContent,
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

	p := s.Progress()
	msg := fmt.Sprintf("exceeded hard max tool iterations (%d soft=%d) after iter %d tool_rounds %d",
		hard, soft, p.Iter, p.ToolRounds)
	// Safety net if near-max check was skipped (e.g. reserve=0 path disabled mid-turn).
	if r.Cfg.AutoContinueReserve > 0 {
		r.autoStopAndContinueTC(s, msg, "auto:max-iters-exceeded", tc)
		return
	}
	r.forceEndAssistant(s, msg+" — send another message to resume, or raise --max-tool-iters")
	s.finalizeTurnProgress("error", msg)
	s.publish(Event{Type: "error", Error: msg})
	if r.Reg != nil {
		r.Reg.logEvent(s, "error", "", msg, "", "", "", nil, nil, nil, nil, nil, "", msg)
	}
}

// nearMaxToolIters is true when the next model call would consume an iteration
// within AutoContinueReserve of the hard max (and we have done tool work).
func (r *Runner) nearMaxToolIters(hard, iter, toolRounds int) bool {
	reserve := r.Cfg.AutoContinueReserve
	if reserve <= 0 || hard <= 0 {
		return false
	}
	if toolRounds <= 0 {
		// Pure chat near the limit can still finish without tools; don't force.
		return false
	}
	if reserve >= hard {
		// Misconfigured: treat as "always near" after first tool round.
		return iter >= 1
	}
	return iter >= hard-reserve
}

// shouldAutoContinueOnErr is true for hard-wall timeouts (not operator cancel/stop).
func (r *Runner) shouldAutoContinueOnErr(err error, s *Session) bool {
	if err == nil {
		return false
	}
	if s != nil && s.Progress().StopRequested {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// autoStopAndContinue ends the turn cleanly and schedules a short-delay continuation
// so long tool work resumes without the user having to type "continue".
// ADR-0022: continuation prompt carries thrash state packet (ban list, last tools, URL).
func (r *Runner) autoStopAndContinue(s *Session, reason, label string) {
	r.autoStopAndContinueTC(s, reason, label, nil)
}

func (r *Runner) autoStopAndContinueTC(s *Session, reason, label string, tc *tools.TurnContext) {
	if s == nil {
		return
	}
	const delaySec = 2
	contID := ""
	fireAt := ""
	if r.Tools != nil && r.Tools.Cont != nil {
		prompt := reason
		if tc != nil {
			prompt = tc.ContinuePacket(reason)
		} else {
			prompt = "Continue the unfinished work from the previous turn. " +
				"Do not restart completed steps. Prefer API/script over GUI thrash. " +
				"If UI stuck → computer_confirm. When done, summarize for the user.\n\n" +
				"Why: " + reason
		}
		if j, err := r.Tools.Cont.Schedule(s.ID, prompt, delaySec, "", label); err == nil && j != nil {
			contID = j.ID
			fireAt = j.FireAt.UTC().Format(time.RFC3339)
			r.advisory(s, fmt.Sprintf("[harness] auto-continuation scheduled id=%s delay=%ds label=%s", j.ID, delaySec, label))
		} else if err != nil {
			r.advisory(s, "[harness] auto-continuation schedule failed: "+err.Error())
		}
	}

	msg := reason
	if contID != "" {
		msg += fmt.Sprintf("\n\n✅ Auto-continuation scheduled (id=%s, ~%ds, fire_at=%s). This turn is stopping cleanly; work will resume automatically.",
			contID, delaySec, fireAt)
	} else {
		msg += "\n\nCould not schedule auto-continuation — send “continue” to resume."
	}

	r.forceEndAssistant(s, msg)
	// "complete" (not error): progress kept, UI idle, continuation will re-busy the session.
	s.finalizeTurnProgress("complete", reason)
	s.publish(Event{Type: "harness", Status: msg})
	if r.Reg != nil {
		r.Reg.logEvent(s, "harness_advisory", "system", msg, "", "", "", nil, nil, nil, nil, nil, "", "auto_continue")
		r.Reg.syncSessionRow(s)
	}
}

// forceEndAssistant appends a short assistant message when a turn is cut off by
// hard wall / max iters / operator stop so the session does not die mid-tool with no reply.
func (r *Runner) forceEndAssistant(s *Session, reason string) {
	if s == nil || strings.TrimSpace(reason) == "" {
		return
	}
	content := "⚠️ **Turn interrupted** (harness limit)\n\n" + reason +
		"\n\nProgress so far is kept in this session."
	if !strings.Contains(reason, "Auto-continuation") && !strings.Contains(reason, "auto-continuation") {
		content += " Send another message (e.g. “continue”) to resume."
	}
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
		r.Reg.logEvent(s, "assistant_message", "assistant", content, "", "", "", nil, nil, nil, nil, nil, "forced_end", "")
		r.Reg.syncSessionRow(s)
	}
	s.publish(Event{Type: "message", Message: &am})
}

func stopMessage(err error, s *Session) string {
	if s != nil {
		p := s.Progress()
		if p.StopRequested || errors.Is(err, context.Canceled) {
			return fmt.Sprintf("Turn stopped by operator after iter %d (tool rounds %d).", p.Iter, p.ToolRounds)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		// Duration is taken from the turn progress when available (HardWall).
		if s != nil {
			p := s.Progress()
			if p.ToolRounds > 0 || p.Iter > 0 {
				return fmt.Sprintf("turn timeout (hard wall) after iter %d tool_rounds %d — raise --hard-wall or use schedule_continuation", p.Iter, p.ToolRounds)
			}
		}
		return "turn timeout (hard wall)"
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

	// Fold compact summary into the leading system message (or a single new system).
	// A second system message after the first used to break strict backends with:
	// "System message must be at the beginning" when soul/advisories also inject system
	// roles and/or when trim reordered the stream (session 0wc75qxv9y /compact).
	compactBlock := "[compacted history]\n" + summary
	newHist := make([]model.Message, 0, 1+len(keep))
	if len(system) > 0 {
		base := system[0]
		base.Content = model.ContentFromText(strings.TrimSpace(base.Content.PlainText()) + "\n\n" + compactBlock)
		newHist = append(newHist, base)
		// Drop any extra leading systems from the pre-compact hist (shouldn't exist)
	} else {
		newHist = append(newHist, model.Message{
			Role:    "system",
			Content: model.ContentFromText(compactBlock),
		})
	}
	// Never re-inject system-role messages from the keep window (orphans break APIs).
	for _, m := range keep {
		if m.Role == "system" {
			m.Role = "user"
			m.Content = model.ContentFromText("[system note]\n" + m.Content.PlainText())
			m.ToolCalls = nil
			m.ToolCallID = ""
		}
		newHist = append(newHist, m)
	}

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
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// shouldAutoTitleLocked reports whether this session's title may be overwritten
// from the latest user message. Caller holds s.mu.
func shouldAutoTitleLocked(s *Session) bool {
	if s == nil || s.TitleCustom {
		return false
	}
	if s.Kind == "system" {
		return false
	}
	t := strings.TrimSpace(s.Title)
	if strings.HasPrefix(strings.ToLower(t), "cron:") {
		return false
	}
	return true
}

func compact(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
