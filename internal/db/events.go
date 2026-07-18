package db

import (
	"fmt"
	"unicode/utf8"
)

// Event is a session_events row (before insert).
type Event struct {
	SessionID          string
	Seq                int
	TS                 string
	Kind               string
	Role               string
	Content            string
	ToolName           string
	ToolCallID         string
	ToolArgsJSON       string
	Model              string
	TokensInReported   *int
	TokensOutReported  *int
	TokensInEst        *int
	TokensOutEst       *int
	LatencyMs          *int
	FinishReason       string
	Error              string
	MetaJSON           string
}

// AppendEvent writes an event, spilling oversize content to blobs.
func (d *DB) AppendEvent(ev Event) error {
	if !d.Writable() {
		return nil
	}
	max := d.SettingInt("db_inline_max_bytes", 32768)
	content := ev.Content
	truncated := 0
	var blobID interface{}

	if len(content) > max {
		id, err := d.SpillBlob(ev.SessionID, []byte(content))
		if err != nil {
			return err
		}
		blobID = id
		truncated = 1
		content = truncateBytes(content, max)
	}

	argsJSON := ev.ToolArgsJSON
	if len(argsJSON) > max {
		// spill args too if huge
		id, err := d.SpillBlob(ev.SessionID, []byte(argsJSON))
		if err == nil {
			if blobID == nil {
				blobID = id
			}
			argsJSON = truncateBytes(argsJSON, max)
			truncated = 1
		}
	}

	_, err := d.SQL.Exec(`
		INSERT INTO session_events (
			session_id, seq, ts, kind, role, content, content_truncated, blob_id,
			tool_name, tool_call_id, tool_args_json, model,
			tokens_in_reported, tokens_out_reported, tokens_in_est, tokens_out_est,
			latency_ms, finish_reason, error, meta_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		ev.SessionID, ev.Seq, ev.TS, ev.Kind, nullStr(ev.Role), content, truncated, blobID,
		nullStr(ev.ToolName), nullStr(ev.ToolCallID), nullStr(argsJSON), nullStr(ev.Model),
		ev.TokensInReported, ev.TokensOutReported, ev.TokensInEst, ev.TokensOutEst,
		ev.LatencyMs, nullStr(ev.FinishReason), nullStr(ev.Error), nullStr(ev.MetaJSON),
	)
	return err
}

// NextEventSeq returns max(seq)+1 for a session.
func (d *DB) NextEventSeq(sessionID string) (int, error) {
	if !d.Writable() {
		return 1, nil
	}
	var max sqlNullInt
	err := d.SQL.QueryRow(`SELECT MAX(seq) FROM session_events WHERE session_id=?`, sessionID).Scan(&max)
	if err != nil {
		return 1, err
	}
	if !max.valid {
		return 1, nil
	}
	return max.v + 1, nil
}

type sqlNullInt struct {
	v     int
	valid bool
}

func (n *sqlNullInt) Scan(src interface{}) error {
	if src == nil {
		n.valid = false
		return nil
	}
	n.valid = true
	switch t := src.(type) {
	case int64:
		n.v = int(t)
	case int:
		n.v = t
	default:
		return fmt.Errorf("unexpected type %T", src)
	}
	return nil
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func truncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	// avoid splitting mid-rune
	if max > 32 {
		max = max - 32
	}
	for max > 0 && !utf8.ValidString(s[:max]) {
		max--
	}
	return s[:max] + "\n…[truncated; full content in blob]"
}
