// Package clerk implements ADR-0023 session attention dashboard.
package clerk

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/model"
	"github.com/rendicott/marble/internal/session"
)

const (
	maxSnippetRunes   = 80
	maxSummaryRunes   = 160
	maxTranscriptMsgs = 12
	maxTranscriptRunes = 8000
	refreshMinGap     = 30 * time.Second
)

// Manager owns summarizer queue + roster assembly.
type Manager struct {
	DB  *db.DB
	Reg *session.Registry
	// ClientForProcess returns a model client for process-default summarization.
	ClientForProcess func() *model.Client
	ProcessModelName func() string

	mu       sync.Mutex
	pending  map[string]struct{} // session ids waiting for summarize
	running  bool
	lastRef  time.Time // global rate limit for force refresh
	stopCh   chan struct{}
	wakeCh   chan struct{}
	started  bool
}

// New creates a Clerk manager (start with Start).
func New(sqldb *db.DB, reg *session.Registry, clientFn func() *model.Client, modelName func() string) *Manager {
	return &Manager{
		DB:               sqldb,
		Reg:              reg,
		ClientForProcess: clientFn,
		ProcessModelName: modelName,
		pending:          make(map[string]struct{}),
		stopCh:           make(chan struct{}),
		wakeCh:           make(chan struct{}, 1),
	}
}

// Start launches the global summarizer worker and seeds missing idle summaries.
func (m *Manager) Start() {
	if m == nil || m.started {
		return
	}
	m.started = true
	go m.loop()
	go m.seedMissingIdle()
	log.Printf("clerk: dashboard worker started")
}

// seedMissingIdle enqueues idle user sessions that have no clerk summary yet
// (e.g. first boot after upgrade or harness restart).
func (m *Manager) seedMissingIdle() {
	// Let registry finish disk index load.
	time.Sleep(2 * time.Second)
	if m == nil || m.Reg == nil || m.DB == nil || !m.DB.Writable() {
		return
	}
	n := 0
	for _, sum := range m.Reg.List() {
		if sum.Kind == "system" || sum.Status == "closed" || sum.Busy {
			continue
		}
		if live, ok := m.Reg.Get(sum.ID); ok && live.IsBusy() {
			continue
		}
		st, _ := m.DB.GetClerkState(sum.ID)
		if st != nil && strings.TrimSpace(st.Summary) != "" {
			continue
		}
		// Seed idle_since from session updated_at so roster sort works before LLM returns
		if st == nil || !st.IdleSince.Valid {
			var idle *string
			if !sum.UpdatedAt.IsZero() {
				is := sum.UpdatedAt.UTC().Format(time.RFC3339)
				idle = &is
			}
			_ = m.DB.PatchClerkMeta(sum.ID, sum.Title, idle, false)
		}
		m.mu.Lock()
		m.pending[sum.ID] = struct{}{}
		m.mu.Unlock()
		n++
	}
	if n > 0 {
		log.Printf("clerk: seeded %d idle session(s) for summary", n)
		m.wake()
	}
}

// Stop stops the worker.
func (m *Manager) Stop() {
	if m == nil || !m.started {
		return
	}
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
}

func (m *Manager) wake() {
	select {
	case m.wakeCh <- struct{}{}:
	default:
	}
}

// OnUserMessage updates last-user snippet (any time). Sending a message
// also clears snooze — operator is actively engaging.
func (m *Manager) OnUserMessage(sessionID, display string) {
	if m == nil || m.DB == nil || !m.DB.Writable() {
		return
	}
	snip := snippet(display, maxSnippetRunes)
	_ = m.DB.PatchClerkMeta(sessionID, snip, nil, false)
	_ = m.DB.ClearClerkSnooze(sessionID)
}

// OnSessionBusy clears idle_since when a turn starts.
func (m *Manager) OnSessionBusy(sessionID string) {
	if m == nil || m.DB == nil || !m.DB.Writable() {
		return
	}
	// Keep snippet; clear idle marker
	row, _ := m.DB.GetClerkState(sessionID)
	snip := ""
	if row != nil {
		snip = row.LastUserSnippet
	}
	_ = m.DB.PatchClerkMeta(sessionID, snip, nil, true)
	// Drop pending summarize for this session (will re-queue on next idle)
	m.mu.Lock()
	delete(m.pending, sessionID)
	m.mu.Unlock()
}

// SnoozeDuration presets accepted by Snooze.
var SnoozePresets = map[string]time.Duration{
	"1h":  time.Hour,
	"4h":  4 * time.Hour,
	"1d":  24 * time.Hour,
	"3d":  72 * time.Hour,
	"1w":  7 * 24 * time.Hour,
}

// Snooze sets or clears a session snooze.
// duration: "1h"|"4h"|"1d"|"3d"|"1w"|"tomorrow"|"" (clear). untilRFC3339 optional override.
func (m *Manager) Snooze(sessionID, duration, untilRFC3339 string) (until string, err error) {
	if m == nil || m.DB == nil || !m.DB.Writable() {
		return "", fmt.Errorf("clerk unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("session_id required")
	}
	duration = strings.TrimSpace(strings.ToLower(duration))
	untilRFC3339 = strings.TrimSpace(untilRFC3339)

	if duration == "clear" || duration == "off" || duration == "0" ||
		(duration == "" && untilRFC3339 == "") {
		if err := m.DB.ClearClerkSnooze(sessionID); err != nil {
			return "", err
		}
		return "", nil
	}

	var untilTime time.Time
	if untilRFC3339 != "" {
		t, perr := time.Parse(time.RFC3339, untilRFC3339)
		if perr != nil {
			return "", fmt.Errorf("invalid until: %w", perr)
		}
		untilTime = t.UTC()
	} else if duration == "tomorrow" {
		// Local morning next day ~09:00 local → store UTC
		now := time.Now()
		loc := now.Location()
		y, mo, d := now.In(loc).Date()
		untilTime = time.Date(y, mo, d+1, 9, 0, 0, 0, loc).UTC()
	} else if d, ok := SnoozePresets[duration]; ok {
		untilTime = time.Now().UTC().Add(d)
	} else {
		return "", fmt.Errorf("unknown duration %q (use 1h, 4h, 1d, 3d, 1w, tomorrow, or clear)", duration)
	}
	if !untilTime.After(time.Now().UTC()) {
		return "", fmt.Errorf("snooze until must be in the future")
	}
	s := untilTime.Format(time.RFC3339)
	if err := m.DB.SetClerkSnooze(sessionID, &s); err != nil {
		return "", err
	}
	return s, nil
}

// OnSessionIdle records idle_since and enqueues summarization (KD7).
func (m *Manager) OnSessionIdle(s *session.Session) {
	if m == nil || s == nil || m.DB == nil || !m.DB.Writable() {
		return
	}
	if s.Kind == "system" {
		return // KD4
	}
	id := s.ID
	snip := lastUserSnippetFromSession(s)
	now := time.Now().UTC().Format(time.RFC3339)
	_ = m.DB.PatchClerkMeta(id, snip, &now, false)

	m.mu.Lock()
	m.pending[id] = struct{}{} // coalesce: latest idle wins
	m.mu.Unlock()
	m.wake()
}

// EnqueueRefresh queues idle sessions for re-summarize (rate-limited).
func (m *Manager) EnqueueRefresh(sessionIDs []string) (queued int, err error) {
	if m == nil || m.DB == nil || !m.DB.Writable() {
		return 0, fmt.Errorf("clerk unavailable")
	}
	m.mu.Lock()
	if time.Since(m.lastRef) < refreshMinGap {
		m.mu.Unlock()
		return 0, fmt.Errorf("refresh rate limited (wait %s)", refreshMinGap.Round(time.Second))
	}
	m.lastRef = time.Now()
	for _, id := range sessionIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		m.pending[id] = struct{}{}
		queued++
	}
	m.mu.Unlock()
	m.wake()
	return queued, nil
}

func (m *Manager) loop() {
	for {
		select {
		case <-m.stopCh:
			return
		case <-m.wakeCh:
			m.drainOne()
		}
	}
}

func (m *Manager) drainOne() {
	for {
		id := m.popPending()
		if id == "" {
			return
		}
		// Drop if busy again (KD9)
		if m.Reg != nil {
			if s, ok := m.Reg.Get(id); ok && s.IsBusy() {
				continue
			}
		}
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("clerk: summarize panic session=%s: %v", id, rec)
				}
			}()
			m.summarize(id)
		}()
	}
}

func (m *Manager) popPending() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.pending {
		delete(m.pending, id)
		return id
	}
	return ""
}

type sumOut struct {
	Summary     string   `json:"summary"`
	NeedsUser   bool     `json:"needs_user"`
	ActionItems []string `json:"action_items"`
}

func (m *Manager) summarize(sessionID string) {
	if m.ClientForProcess == nil || m.Reg == nil {
		return
	}
	base := m.ClientForProcess()
	if base == nil {
		log.Printf("clerk: no process client for %s", sessionID)
		return
	}
	// Dedicated short-output client so thinking models don't burn huge max_tokens.
	const clerkMaxTokens = 1536
	client := model.New(base.BaseURL, base.Model, clerkMaxTokens, base.APIKey)

	s, err := m.Reg.EnsureLoaded(sessionID)
	if err != nil || s == nil {
		log.Printf("clerk: load session %s: %v", sessionID, err)
		return
	}
	if s.Kind == "system" || s.IsBusy() {
		return
	}

	transcript := buildTranscript(s)
	if strings.TrimSpace(transcript) == "" {
		transcript = "(empty transcript)"
	}
	prompt := `You maintain a short operator dashboard entry for one Marble agent session.
Given recent transcript, return ONLY JSON (no markdown fences, no prose):
{"summary":"≤120 chars plain language","needs_user":true|false,"action_items":["…"]}
Rules:
- needs_user=true if the agent is waiting on a choice, approval, OTP, missing input, or an explicit question to the human.
- needs_user=false if the agent delivered a status/report with nothing to decide.
- For needs_user, summary should state the decision crisply (e.g. "foo or bar?").
- For idle done, summary should be outcome-oriented (e.g. "foo done").
- action_items: short bullets the human must act on; empty array if none.

Transcript:
` + transcript

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	msgs := []model.Message{
		{Role: "system", Content: model.ContentFromText("You output only compact JSON for a session dashboard. No thinking aloud.")},
		{Role: "user", Content: model.ContentFromText(prompt)},
	}
	start := time.Now()
	result, err := client.Chat(ctx, msgs, nil)
	modelName := ""
	if m.ProcessModelName != nil {
		modelName = m.ProcessModelName()
	}

	var out sumOut
	sumErr := ""
	if err != nil {
		sumErr = err.Error()
		// KD6 fallback heuristics
		out = fallbackFromSession(s)
	} else {
		raw := strings.TrimSpace(result.Message.Content.PlainText())
		if raw == "" {
			raw = strings.TrimSpace(result.Message.Reasoning)
		}
		if jerr := parseSumOut(raw, &out); jerr != nil {
			sumErr = "parse: " + jerr.Error()
			out = fallbackFromSession(s)
		}
	}
	out.Summary = clipRunes(strings.TrimSpace(out.Summary), maxSummaryRunes)
	if out.Summary == "" {
		out.Summary = fallbackFromSession(s).Summary
	}
	if out.ActionItems == nil {
		out.ActionItems = []string{}
	}
	// cap action items
	if len(out.ActionItems) > 5 {
		out.ActionItems = out.ActionItems[:5]
	}
	ai, _ := json.Marshal(out.ActionItems)

	// re-read meta so we don't wipe idle_since/snippet
	prev, _ := m.DB.GetClerkState(sessionID)
	snip := lastUserSnippetFromSession(s)
	idle := sql.NullString{}
	if prev != nil && prev.IdleSince.Valid && prev.IdleSince.String != "" {
		idle = prev.IdleSince
	} else if !s.UpdatedAt.IsZero() {
		// Prefer last session activity over "now" so long-idle sort stays meaningful
		idle = sql.NullString{String: s.UpdatedAt.UTC().Format(time.RFC3339), Valid: true}
	} else {
		idle = sql.NullString{String: time.Now().UTC().Format(time.RFC3339), Valid: true}
	}
	if snip == "" && prev != nil {
		snip = prev.LastUserSnippet
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := m.DB.UpsertClerkState(db.ClerkStateRow{
		SessionID:        sessionID,
		Summary:          out.Summary,
		NeedsUser:        out.NeedsUser,
		ActionItemsJSON:  string(ai),
		LastUserSnippet:  snip,
		IdleSince:        idle,
		SummaryUpdatedAt: sql.NullString{String: now, Valid: true},
		SummaryModel:     modelName,
		SummaryError:     sumErr,
	}); err != nil {
		log.Printf("clerk: upsert %s: %v", sessionID, err)
		return
	}
	log.Printf("clerk: summarized %s needs_user=%v latency=%s err=%q",
		sessionID, out.NeedsUser, time.Since(start).Round(time.Millisecond), sumErr)
}

// parseSumOut extracts JSON object from model text (handles fences / trailing prose).
func parseSumOut(raw string, out *sumOut) error {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	if err := json.Unmarshal([]byte(raw), out); err == nil {
		return nil
	}
	// Find outermost { … }
	i := strings.Index(raw, "{")
	j := strings.LastIndex(raw, "}")
	if i >= 0 && j > i {
		if err := json.Unmarshal([]byte(raw[i:j+1]), out); err == nil {
			return nil
		}
	}
	return fmt.Errorf("no JSON object in model output")
}

func fallbackFromSession(s *session.Session) sumOut {
	lastAsst := lastRoleContent(s, "assistant")
	needs := heuristicNeedsUser(lastAsst)
	sum := snippet(lastAsst, maxSummaryRunes)
	if sum == "" {
		sum = snippet(lastUserSnippetFromSession(s), maxSummaryRunes)
	}
	if sum == "" {
		sum = "Session idle"
	}
	items := []string{}
	if needs {
		items = append(items, "Review agent question and reply")
	}
	return sumOut{Summary: sum, NeedsUser: needs, ActionItems: items}
}

func heuristicNeedsUser(assistant string) bool {
	t := strings.TrimSpace(assistant)
	if t == "" {
		return false
	}
	if strings.HasSuffix(t, "?") {
		return true
	}
	low := strings.ToLower(t)
	for _, k := range []string{"which option", "do you want", "please confirm", "choose ", "choose:", "option a", "option b", "shall i", "should i", "let me know"} {
		if strings.Contains(low, k) {
			return true
		}
	}
	return false
}

func buildTranscript(s *session.Session) string {
	msgs := s.UIMessages()
	// Prefer tail of messages
	if len(msgs) > maxTranscriptMsgs {
		msgs = msgs[len(msgs)-maxTranscriptMsgs:]
	}
	var b strings.Builder
	for _, m := range msgs {
		role := m.Role
		if role == "tool" {
			// Keep tool errors only
			c := m.Content
			if !strings.Contains(strings.ToLower(c), "error") && !strings.Contains(strings.ToLower(c), "fail") {
				continue
			}
			role = "tool_error"
		}
		if role == "harness" || role == "error" {
			// include briefly
		}
		line := fmt.Sprintf("[%s] %s\n", role, clipRunes(m.Content, 800))
		if b.Len()+len(line) > maxTranscriptRunes {
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

func lastUserSnippetFromSession(s *session.Session) string {
	return snippet(lastRoleContent(s, "user"), maxSnippetRunes)
}

func lastRoleContent(s *session.Session, role string) string {
	msgs := s.UIMessages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == role {
			return msgs[i].Content
		}
	}
	return ""
}

func snippet(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	s = strings.Join(strings.Fields(s), " ")
	return clipRunes(s, max)
}

func clipRunes(s string, max int) string {
	if max <= 0 || s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	if max < 2 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

// Row is one dashboard entry for the API/UI.
type Row struct {
	SessionID       string   `json:"session_id"`
	Title           string   `json:"title"`
	TitleCustom     bool     `json:"title_custom,omitempty"`
	Kind            string   `json:"kind"`
	Status          string   `json:"status"`
	Busy            bool     `json:"busy"`
	Attention       string   `json:"attention"` // working | idle | needs_user
	IdleSince       string   `json:"idle_since,omitempty"`
	IdleForSec      int64    `json:"idle_for_sec,omitempty"`
	LastUserSnippet string   `json:"last_user_snippet"`
	Summary         string   `json:"summary"`
	ActionItems     []string `json:"action_items"`
	SummaryUpdated  string   `json:"summary_updated_at,omitempty"`
	SummaryError    string   `json:"summary_error,omitempty"`
	JumpURL         string   `json:"jump_url"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
	// Snooze: active when SnoozedUntil is in the future.
	Snoozed       bool   `json:"snoozed"`
	SnoozedUntil  string `json:"snoozed_until,omitempty"`
	SnoozeLeftSec int64  `json:"snooze_left_sec,omitempty"`
}

// List builds the sorted roster (KD2–KD4). Snoozed rows sort last.
// When includeSnoozed is false, active snoozes are omitted from the list
// (still counted via ListMeta if needed by the API).
func (m *Manager) List(includeClosed bool) ([]Row, error) {
	return m.list(includeClosed, true)
}

// ListFiltered builds the roster; includeSnoozed controls whether active
// snoozes appear (still demoted when included).
func (m *Manager) ListFiltered(includeClosed, includeSnoozed bool) ([]Row, error) {
	return m.list(includeClosed, includeSnoozed)
}

func (m *Manager) list(includeClosed, includeSnoozed bool) ([]Row, error) {
	if m == nil || m.Reg == nil {
		return nil, fmt.Errorf("clerk unavailable")
	}
	sums := m.Reg.List()
	var states map[string]db.ClerkStateRow
	if m.DB != nil && m.DB.Writable() {
		rows, err := m.DB.ListClerkStates()
		if err == nil {
			states = make(map[string]db.ClerkStateRow, len(rows))
			for _, r := range rows {
				states[r.SessionID] = r
			}
		}
	}
	if states == nil {
		states = map[string]db.ClerkStateRow{}
	}

	now := time.Now().UTC()
	var out []Row
	snoozedHidden := 0
	for _, sum := range sums {
		if sum.Kind == "system" {
			continue // KD4
		}
		if !includeClosed && sum.Status == "closed" {
			continue // KD3
		}
		st := states[sum.ID]
		// Live busy wins
		busy := sum.Busy
		if live, ok := m.Reg.Get(sum.ID); ok {
			busy = live.IsBusy()
		}

		snip := st.LastUserSnippet
		if snip == "" {
			snip = sum.Title
		}
		items := []string{}
		if st.ActionItemsJSON != "" {
			_ = json.Unmarshal([]byte(st.ActionItemsJSON), &items)
		}
		if items == nil {
			items = []string{}
		}

		att := "idle"
		idleSince := ""
		var idleSec int64
		if busy {
			att = "working"
		} else {
			if st.NeedsUser {
				att = "needs_user"
			}
			if st.IdleSince.Valid && st.IdleSince.String != "" {
				idleSince = st.IdleSince.String
				if t, err := time.Parse(time.RFC3339, st.IdleSince.String); err == nil {
					idleSec = int64(now.Sub(t.UTC()).Seconds())
					if idleSec < 0 {
						idleSec = 0
					}
				}
			} else if !sum.UpdatedAt.IsZero() {
				// fallback idle clock from session updated
				idleSince = sum.UpdatedAt.UTC().Format(time.RFC3339)
				idleSec = int64(now.Sub(sum.UpdatedAt.UTC()).Seconds())
				if idleSec < 0 {
					idleSec = 0
				}
			}
		}

		summary := st.Summary
		if busy {
			summary = "" // R1: no LLM summary for working
		}

		snoozed, snoozeUntil, snoozeLeft := activeSnooze(st.SnoozedUntil, now)
		if snoozed && !includeSnoozed {
			snoozedHidden++
			continue
		}

		out = append(out, Row{
			SessionID:       sum.ID,
			Title:           sum.Title,
			TitleCustom:     sum.TitleCustom,
			Kind:            sum.Kind,
			Status:          sum.Status,
			Busy:            busy,
			Attention:       att,
			IdleSince:       idleSince,
			IdleForSec:      idleSec,
			LastUserSnippet: snip,
			Summary:         summary,
			ActionItems:     items,
			SummaryUpdated:  nullStr(st.SummaryUpdatedAt),
			SummaryError:    st.SummaryError,
			JumpURL:         "/s/" + sum.ID,
			UpdatedAt:       sum.UpdatedAt.UTC().Format(time.RFC3339),
			Snoozed:         snoozed,
			SnoozedUntil:    snoozeUntil,
			SnoozeLeftSec:   snoozeLeft,
		})
	}

	sortRows(out)
	// Stash hidden count on empty sentinel? API will call CountSnoozed separately.
	_ = snoozedHidden
	return out, nil
}

// CountSnoozed returns how many non-system (optionally non-closed) sessions
// currently have an active snooze.
func (m *Manager) CountSnoozed(includeClosed bool) int {
	if m == nil || m.Reg == nil {
		return 0
	}
	states := map[string]db.ClerkStateRow{}
	if m.DB != nil && m.DB.Writable() {
		if rows, err := m.DB.ListClerkStates(); err == nil {
			for _, r := range rows {
				states[r.SessionID] = r
			}
		}
	}
	now := time.Now().UTC()
	n := 0
	for _, sum := range m.Reg.List() {
		if sum.Kind == "system" {
			continue
		}
		if !includeClosed && sum.Status == "closed" {
			continue
		}
		st := states[sum.ID]
		if sn, _, _ := activeSnooze(st.SnoozedUntil, now); sn {
			n++
		}
	}
	return n
}

func activeSnooze(ns sql.NullString, now time.Time) (active bool, until string, leftSec int64) {
	if !ns.Valid || strings.TrimSpace(ns.String) == "" {
		return false, "", 0
	}
	t, err := time.Parse(time.RFC3339, ns.String)
	if err != nil {
		return false, "", 0
	}
	t = t.UTC()
	if !t.After(now) {
		return false, "", 0
	}
	left := int64(t.Sub(now).Seconds())
	if left < 0 {
		left = 0
	}
	return true, t.Format(time.RFC3339), left
}

func nullStr(n sql.NullString) string {
	if n.Valid {
		return n.String
	}
	return ""
}

func sortRows(rows []Row) {
	// KD2 + snooze: active snoozes always last (soonest wake first among them).
	// Then needs_user longest idle, idle longest idle, working most recently updated.
	rank := func(r Row) int {
		if r.Snoozed {
			return 3
		}
		switch r.Attention {
		case "needs_user":
			return 0
		case "idle":
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := rank(rows[i]), rank(rows[j])
		if ri != rj {
			return ri < rj
		}
		if ri == 3 {
			// sooner unsnooze first
			return rows[i].SnoozeLeftSec < rows[j].SnoozeLeftSec
		}
		if ri < 2 {
			return rows[i].IdleForSec > rows[j].IdleForSec
		}
		return rows[i].UpdatedAt > rows[j].UpdatedAt
	})
}
