package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/memory"
	"github.com/rendicott/marble/internal/token"
)

// RescueResult is the HTTP/CLI body for one overage-rescue call.
type RescueResult struct {
	Action          string  `json:"action"`
	Archive         string  `json:"archive,omitempty"`
	ContextLimit    int     `json:"context_limit"`
	Margin          int     `json:"margin"`
	Scale           float64 `json:"scale"`
	BeforeEst       int     `json:"before_est"`
	AfterEst        int     `json:"after_est"`
	Target          int     `json:"target"`
	OmittedMessages int     `json:"omitted_messages"`
	StubbedMessages int     `json:"stubbed_messages"`
	OmittedImages   int     `json:"omitted_images"`
	EventSeq        int     `json:"event_seq,omitempty"`
	DryRun          bool    `json:"dry_run,omitempty"`
	Error           string  `json:"error,omitempty"`
}

// OverageRescue plans a model-free projection rewrite.
// dry writes nothing and does not take the turn lock.
// at is the archive timestamp; the zero time uses the clock.
// A rescued session is archived before the live file changes, then reloaded
// from the new document. This never starts a turn.
func (r *Registry) OverageRescue(id string, dry bool, at time.Time) (RescueResult, error) {
	if r == nil {
		return RescueResult{}, fmt.Errorf("session not found")
	}
	s, err := r.EnsureLoaded(id)
	if err != nil {
		return RescueResult{}, err
	}
	if dry {
		s.mu.Lock()
		closed := s.Status == "closed"
		s.mu.Unlock()
		if closed {
			return RescueResult{}, fmt.Errorf("session is closed")
		}
	} else if !s.BeginExclusive() {
		s.mu.Lock()
		closed := s.Status == "closed"
		s.mu.Unlock()
		if closed {
			return RescueResult{}, fmt.Errorf("session is closed")
		}
		return RescueResult{}, errBusy
	} else {
		defer s.EndExclusive()
	}

	doc := s.SnapshotDoc(r.workspace, r.model)
	limit, margin, minOut, overhead, scale, err := r.rescueParams(s, doc.Messages)
	if err != nil {
		return RescueResult{}, err
	}
	plan, err := PlanRescue(doc.Messages, overhead, limit, margin, minOut, scale)
	if err != nil {
		return RescueResult{}, err
	}
	res := rescueResultFromPlan(plan)
	res.DryRun = dry
	if dry {
		return res, nil
	}
	if plan.Action != "rescued" {
		seq, err := r.writeRescueEvent(s, res)
		if err != nil {
			return res, err
		}
		res.EventSeq = seq
		return res, nil
	}

	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	rel := "backups/overage/" + id + "/" + at.Format("20060102T150405Z") + ".md"
	abs, err := r.archiveAbs(id, rel)
	if err != nil {
		return RescueResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return RescueResult{}, fmt.Errorf("archive: %w", err)
	}
	// Archive the pre-image. A failed write leaves the live transcript alone.
	if err := os.WriteFile(abs, []byte(memory.EncodeSession(doc)), 0o644); err != nil {
		return RescueResult{}, fmt.Errorf("archive: %w", err)
	}
	plan.At = at
	plan.Archive = rel
	newDoc, err := ApplyPlan(doc, plan)
	if err != nil {
		return RescueResult{}, fmt.Errorf("persist failed after archive %s: %w", rel, err)
	}
	s.mu.Lock()
	busy := s.busy
	s.loadFromDocLocked(newDoc)
	s.busy = busy
	s.dirty = true
	s.mu.Unlock()
	if err := r.PersistSession(s); err != nil {
		return RescueResult{}, fmt.Errorf("persist failed after archive %s; session left dirty: %w", rel, err)
	}
	res.Archive = rel
	res.AfterEst = plan.AfterEst
	seq, err := r.writeRescueEvent(s, res)
	if err != nil {
		return res, fmt.Errorf("event failed after archive %s; transcript was updated: %w", rel, err)
	}
	res.EventSeq = seq
	msgs := s.UIMessages()
	if len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		s.publish(Event{Type: "message", Message: &last})
	}
	sum := s.Summary()
	s.publish(Event{Type: "session_meta", Title: sum.Title, TitleCustom: sum.TitleCustom})
	return res, nil
}

func rescueResultFromPlan(plan RescuePlan) RescueResult {
	return RescueResult{
		Action:          plan.Action,
		ContextLimit:    plan.Limit,
		Margin:          plan.Margin,
		Scale:           plan.Scale,
		BeforeEst:       plan.BeforeEst,
		AfterEst:        plan.AfterEst,
		Target:          plan.Target,
		OmittedMessages: plan.OmittedMessages,
		StubbedMessages: plan.StubbedMessages,
		OmittedImages:   plan.OmittedImages,
		Error:           plan.Error,
	}
}

func (r *Registry) rescueParams(s *Session, msgs []memory.TranscriptMessage) (limit, margin, minOut, overhead int, scale float64, err error) {
	if r.runner == nil {
		return 0, 0, 0, 0, 0, fmt.Errorf("context limit is not configured")
	}
	em := r.runner.resolveEffective(s, TurnOpts{})
	limit = em.ContextLimit
	if limit <= 0 {
		return 0, 0, 0, 0, 0, fmt.Errorf("context limit is not configured")
	}
	margin = rescueMargin(limit)
	minOut = retryMinOut
	overhead = rescueOverhead(r, s, em, msgs)
	scale = 1.25
	if obs, ok := s.LastContextLimit(); ok && obs.Messages > 0 && obs.Est > 0 {
		scale = float64(obs.Messages) / float64(obs.Est)
	}
	return limit, margin, minOut, overhead, scale, nil
}

func rescueMargin(limit int) int {
	m := 4096
	if v := limit / 64; v > m {
		m = v
	}
	return m
}

func rescueOverhead(r *Registry, s *Session, em EffectiveModel, msgs []memory.TranscriptMessage) int {
	n := token.Estimate(defaultSystemPrompt)
	if marker := newestHarnessMarker(msgs); marker != "" {
		n += token.Estimate("\n\n" + marker)
	}
	s.mu.Lock()
	kind := s.Kind
	s.mu.Unlock()
	if kind != "system" && r.store != nil {
		if soul, err := r.store.ReadSoul(); err == nil && strings.TrimSpace(soul) != "" {
			n += token.Estimate(strings.TrimSpace(soul))
		}
	}
	if kind != "system" {
		name, proto := s.ClientAdvertiseSnapshot()
		if IsWonderstandProtocol(name, proto) {
			n += token.Estimate(WonderstandPromptPack)
		}
	}
	if em.CapTools && r.runner != nil && r.runner.Tools != nil {
		n += estimateTools(r.runner.Tools.Specs())
	}
	return n
}

func (r *Registry) archiveAbs(id, rel string) (string, error) {
	if r.store == nil {
		return "", fmt.Errorf("archive: no memory store")
	}
	if id == "" || id != filepath.Base(id) || strings.Contains(id, "..") {
		return "", fmt.Errorf("archive: bad session id")
	}
	root, err := filepath.Abs(r.store.Root)
	if err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	abs, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive: path escapes memory root")
	}
	return abs, nil
}

func (r *Registry) writeRescueEvent(s *Session, res RescueResult) (int, error) {
	if r.sqldb == nil || !r.sqldb.Writable() {
		return 0, nil
	}
	seq, err := r.sqldb.NextEventSeq(s.ID)
	if err != nil {
		return 0, err
	}
	body := res
	body.EventSeq = seq
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	ev := db.Event{
		SessionID: s.ID,
		Seq:       seq,
		TS:        time.Now().UTC().Format(time.RFC3339),
		Kind:      "overage_rescue",
		Role:      "system",
		Content:   string(raw),
		Model:     r.model,
		Error:     res.Error,
	}
	if err := r.sqldb.AppendEvent(ev); err != nil {
		return 0, err
	}
	return seq, nil
}
