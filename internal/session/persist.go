package session

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/token"
)

func (r *Registry) syncSessionRow(s *Session) {
	if r.sqldb == nil || !r.sqldb.Writable() {
		return
	}
	s.mu.Lock()
	model := s.ProviderModel
	if model == "" {
		model = r.model
	}
	row := db.SessionRow{
		ID:           s.ID,
		Title:        s.Title,
		Status:       s.Status,
		CreatedAt:    s.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:    s.UpdatedAt.UTC().Format(time.RFC3339),
		MessageCount: len(s.ui),
		Dirty:        s.dirty,
		Workspace:    r.workspace,
		Model:        model,
		ModelID:      s.ModelID,
		ComputerID:   s.ComputerID,
		MDPath:       filepath.Join("session", s.ID+".md"),
	}
	if s.ClosedAt != nil {
		row.ClosedAt = sql.NullString{String: s.ClosedAt.UTC().Format(time.RFC3339), Valid: true}
	}
	if row.Status == "" {
		row.Status = "active"
	}
	s.mu.Unlock()
	_ = r.sqldb.UpsertSession(row)
}

func (r *Registry) logEvent(s *Session, kind, role, content, toolName, toolCallID, toolArgs string, usageIn, usageOut, estIn, estOut, latency *int, finish, errStr string) {
	r.logEventMeta(s, kind, role, content, toolName, toolCallID, toolArgs, usageIn, usageOut, estIn, estOut, latency, finish, errStr, "")
}

func (r *Registry) logEventMeta(s *Session, kind, role, content, toolName, toolCallID, toolArgs string, usageIn, usageOut, estIn, estOut, latency *int, finish, errStr, metaJSON string) {
	if r.sqldb == nil || !r.sqldb.Writable() {
		return
	}
	seq, err := r.sqldb.NextEventSeq(s.ID)
	if err != nil {
		seq = int(time.Now().UnixNano() % 1000000)
	}
	ev := db.Event{
		SessionID:    s.ID,
		Seq:          seq,
		TS:           time.Now().UTC().Format(time.RFC3339),
		Kind:         kind,
		Role:         role,
		Content:      content,
		ToolName:     toolName,
		ToolCallID:   toolCallID,
		ToolArgsJSON: toolArgs,
		Model:        r.model,
		FinishReason: finish,
		Error:        errStr,
		MetaJSON:     metaJSON,
	}
	ev.TokensInReported = usageIn
	ev.TokensOutReported = usageOut
	ev.TokensInEst = estIn
	ev.TokensOutEst = estOut
	ev.LatencyMs = latency
	_ = r.sqldb.AppendEvent(ev)
}

// logModelCall records a model_call with effective model + meta_json (ADR-0018).
func (r *Registry) logModelCall(s *Session, em EffectiveModel, role, content string, usageIn, usageOut, estIn, estOut, latency *int, finish, errStr string) {
	if r.sqldb == nil || !r.sqldb.Writable() {
		return
	}
	seq, err := r.sqldb.NextEventSeq(s.ID)
	if err != nil {
		seq = int(time.Now().UnixNano() % 1000000)
	}
	meta, _ := json.Marshal(map[string]interface{}{
		"catalog_id":     em.CatalogID,
		"source":         em.Source,
		"display_name":   em.DisplayName,
		"context_limit":  em.ContextLimit,
		"budget":         em.Budget(),
	})
	ev := db.Event{
		SessionID:    s.ID,
		Seq:          seq,
		TS:           time.Now().UTC().Format(time.RFC3339),
		Kind:         "model_call",
		Role:         role,
		Content:      content,
		Model:        em.Model,
		FinishReason: finish,
		Error:        errStr,
		MetaJSON:     string(meta),
	}
	ev.TokensInReported = usageIn
	ev.TokensOutReported = usageOut
	ev.TokensInEst = estIn
	ev.TokensOutEst = estOut
	ev.LatencyMs = latency
	_ = r.sqldb.AppendEvent(ev)
}

func estimateTokens(s string) int {
	return token.Estimate(s)
}

func intPtr(n int) *int { return &n }
