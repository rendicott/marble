package session

import (
	"database/sql"
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
	row := db.SessionRow{
		ID:           s.ID,
		Title:        s.Title,
		Status:       s.Status,
		CreatedAt:    s.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:    s.UpdatedAt.UTC().Format(time.RFC3339),
		MessageCount: len(s.ui),
		Dirty:        s.dirty,
		Workspace:    r.workspace,
		Model:        r.model,
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
