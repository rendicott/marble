package db

import (
	"database/sql"
	"fmt"
)

// SessionUsage aggregates from session_events for one session.
type SessionUsage struct {
	EventCount         int `json:"event_count"`
	UserMessages       int `json:"user_messages"`
	AssistantMessages  int `json:"assistant_messages"`
	ModelCalls         int `json:"model_calls"`
	ToolCalls          int `json:"tool_calls"`
	ToolResults        int `json:"tool_results"`
	Errors             int `json:"errors"`
	BlobCount          int `json:"blob_count"`
	TokensInReported   int `json:"tokens_in_reported"`
	TokensOutReported  int `json:"tokens_out_reported"`
	TokensInEst        int `json:"tokens_in_est"`
	TokensOutEst       int `json:"tokens_out_est"`
	LatencyMsSum       int `json:"latency_ms_sum"`
	LatencyMsAvg       int `json:"latency_ms_avg"`
	LatencyMsMax       int `json:"latency_ms_max"`
}

// ToolStat is a per-tool histogram entry.
type ToolStat struct {
	Name   string `json:"name"`
	Calls  int    `json:"calls"`
	Errors int    `json:"errors"`
}

// EventSummary is a light event row for the activity timeline (no full content).
type EventSummary struct {
	Seq               int    `json:"seq"`
	TS                string `json:"ts"`
	Kind              string `json:"kind"`
	ToolName          string `json:"tool_name,omitempty"`
	TokensInReported  *int   `json:"tokens_in_reported,omitempty"`
	TokensOutReported *int   `json:"tokens_out_reported,omitempty"`
	TokensInEst       *int   `json:"tokens_in_est,omitempty"`
	TokensOutEst      *int   `json:"tokens_out_est,omitempty"`
	LatencyMs         *int   `json:"latency_ms,omitempty"`
	Error             string `json:"error,omitempty"`
}

// GetSession returns a sessions row by id, or nil if missing.
func (d *DB) GetSession(id string) (*SessionRow, error) {
	if d == nil || !d.Writable() {
		return nil, nil
	}
	var s SessionRow
	var dirty int
	err := d.SQL.QueryRow(`
		SELECT id, title, status, created_at, updated_at, closed_at, message_count, dirty, workspace, model, md_path
		FROM sessions WHERE id=?
	`, id).Scan(
		&s.ID, &s.Title, &s.Status, &s.CreatedAt, &s.UpdatedAt, &s.ClosedAt,
		&s.MessageCount, &dirty, &s.Workspace, &s.Model, &s.MDPath,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.Dirty = dirty != 0
	return &s, nil
}

// SessionUsageAgg returns SUM/COUNT aggregates for a session.
func (d *DB) SessionUsageAgg(sessionID string) (SessionUsage, error) {
	var u SessionUsage
	if d == nil || !d.Writable() {
		return u, nil
	}
	var latCount int
	err := d.SQL.QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN kind='user_message' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN kind='assistant_message' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN kind='model_call' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN kind='tool_call' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN kind='tool_result' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN kind='error' OR (error IS NOT NULL AND error != '') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN blob_id IS NOT NULL AND blob_id != '' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(tokens_in_reported), 0),
			COALESCE(SUM(tokens_out_reported), 0),
			COALESCE(SUM(tokens_in_est), 0),
			COALESCE(SUM(tokens_out_est), 0),
			COALESCE(SUM(CASE WHEN kind='model_call' AND latency_ms IS NOT NULL THEN latency_ms ELSE 0 END), 0),
			COALESCE(MAX(CASE WHEN kind='model_call' THEN latency_ms END), 0),
			COALESCE(SUM(CASE WHEN kind='model_call' AND latency_ms IS NOT NULL THEN 1 ELSE 0 END), 0)
		FROM session_events WHERE session_id=?
	`, sessionID).Scan(
		&u.EventCount,
		&u.UserMessages,
		&u.AssistantMessages,
		&u.ModelCalls,
		&u.ToolCalls,
		&u.ToolResults,
		&u.Errors,
		&u.BlobCount,
		&u.TokensInReported,
		&u.TokensOutReported,
		&u.TokensInEst,
		&u.TokensOutEst,
		&u.LatencyMsSum,
		&u.LatencyMsMax,
		&latCount,
	)
	if err != nil {
		return u, err
	}
	if latCount > 0 {
		u.LatencyMsAvg = u.LatencyMsSum / latCount
	}
	return u, nil
}

// SessionToolStats returns tool_call histogram and error counts by tool_name.
func (d *DB) SessionToolStats(sessionID string) ([]ToolStat, error) {
	if d == nil || !d.Writable() {
		return nil, nil
	}
	rows, err := d.SQL.Query(`
		SELECT
			COALESCE(tool_name, '') AS name,
			COALESCE(SUM(CASE WHEN kind='tool_call' THEN 1 ELSE 0 END), 0) AS calls,
			COALESCE(SUM(CASE
				WHEN kind='tool_result' AND error IS NOT NULL AND error != '' THEN 1
				WHEN kind='error' AND tool_name IS NOT NULL THEN 1
				ELSE 0 END), 0) AS errors
		FROM session_events
		WHERE session_id=? AND tool_name IS NOT NULL AND tool_name != ''
		GROUP BY tool_name
		ORDER BY calls DESC, name ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolStat
	for rows.Next() {
		var t ToolStat
		if err := rows.Scan(&t.Name, &t.Calls, &t.Errors); err != nil {
			return nil, err
		}
		if t.Name == "" {
			continue
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SessionRecentEvents returns the last n events by seq DESC (no content bodies).
// error strings longer than maxErr are truncated.
func (d *DB) SessionRecentEvents(sessionID string, n, maxErr int) ([]EventSummary, error) {
	if d == nil || !d.Writable() {
		return nil, nil
	}
	if n <= 0 {
		n = 30
	}
	if maxErr <= 0 {
		maxErr = 500
	}
	rows, err := d.SQL.Query(`
		SELECT seq, ts, kind, tool_name,
			tokens_in_reported, tokens_out_reported, tokens_in_est, tokens_out_est,
			latency_ms, error
		FROM session_events
		WHERE session_id=?
		ORDER BY seq DESC
		LIMIT ?
	`, sessionID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventSummary
	for rows.Next() {
		var e EventSummary
		var tool, errStr sql.NullString
		var tinR, toutR, tinE, toutE, lat sql.NullInt64
		if err := rows.Scan(
			&e.Seq, &e.TS, &e.Kind, &tool,
			&tinR, &toutR, &tinE, &toutE, &lat, &errStr,
		); err != nil {
			return nil, err
		}
		if tool.Valid {
			e.ToolName = tool.String
		}
		e.TokensInReported = nullIntPtr(tinR)
		e.TokensOutReported = nullIntPtr(toutR)
		e.TokensInEst = nullIntPtr(tinE)
		e.TokensOutEst = nullIntPtr(toutE)
		e.LatencyMs = nullIntPtr(lat)
		if errStr.Valid && errStr.String != "" {
			e.Error = truncateRunes(errStr.String, maxErr)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullIntPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

func truncateRunes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		// also handle by rune for multi-byte
		r := []rune(s)
		if max > 0 && len(r) > max {
			return string(r[:max]) + "…"
		}
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// SessionInfoBundle is everything needed for the info API from DB.
type SessionInfoBundle struct {
	Row     *SessionRow
	Usage   SessionUsage
	Tools   []ToolStat
	Recent  []EventSummary
	Partial bool
}

// LoadSessionInfo loads row + aggregates + tools + recent events.
// When DB is not writable, returns empty bundle with Partial=true.
func (d *DB) LoadSessionInfo(sessionID string, recentN int) (SessionInfoBundle, error) {
	var b SessionInfoBundle
	if d == nil || !d.Writable() {
		b.Partial = true
		return b, nil
	}
	row, err := d.GetSession(sessionID)
	if err != nil {
		return b, fmt.Errorf("get session: %w", err)
	}
	b.Row = row
	u, err := d.SessionUsageAgg(sessionID)
	if err != nil {
		return b, fmt.Errorf("usage: %w", err)
	}
	b.Usage = u
	tools, err := d.SessionToolStats(sessionID)
	if err != nil {
		return b, fmt.Errorf("tools: %w", err)
	}
	b.Tools = tools
	recent, err := d.SessionRecentEvents(sessionID, recentN, 500)
	if err != nil {
		return b, fmt.Errorf("recent: %w", err)
	}
	b.Recent = recent
	return b, nil
}
