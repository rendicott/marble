package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ToolCallLog is one append-only tool_call_log row.
// HitCount is nil when the tool did not report a summary.
type ToolCallLog struct {
	ID                int64  `json:"id"`
	SessionID         string `json:"session_id"`
	Seq               int    `json:"seq"`
	TS                string `json:"ts"`
	ToolName          string `json:"tool_name"`
	ToolCallID        string `json:"tool_call_id"`
	ArgsJSON          string `json:"args_json"`
	ResultSummaryJSON string `json:"result_summary_json"`
	OK                int    `json:"ok"`
	Error             string `json:"error"`
	LatencyMS         int64  `json:"latency_ms"`
	ResultChars       int    `json:"result_chars"`
	HitCount          *int   `json:"hit_count"`
}

// migrateV15toV16 adds tool_call_log. Existing rows are not rewritten.
func (d *DB) migrateV15toV16() error {
	if d.SQL == nil {
		return fmt.Errorf("no database")
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS tool_call_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT,
			seq INTEGER,
			ts TEXT,
			tool_name TEXT,
			tool_call_id TEXT,
			args_json TEXT,
			result_summary_json TEXT,
			ok INTEGER,
			error TEXT,
			latency_ms INTEGER,
			result_chars INTEGER,
			hit_count INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tool_call_log_session_seq ON tool_call_log(session_id, seq)`,
		`CREATE INDEX IF NOT EXISTS idx_tool_call_log_tool_ts ON tool_call_log(tool_name, ts)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("migrate v16: %w", err)
		}
	}
	if _, err := tx.Exec(`UPDATE schema_meta SET schema_version = 16, updated_at = ? WHERE id = 1`, UTCNow()); err != nil {
		return fmt.Errorf("migrate v16: %w", err)
	}
	return tx.Commit()
}

// InsertToolCallLog appends one row. Seq is the next value for that session.
// Failures are the caller's to ignore; this does not retry.
func (d *DB) InsertToolCallLog(row ToolCallLog) error {
	if d == nil || !d.Writable() {
		return fmt.Errorf("database not writable")
	}
	var hit any
	if row.HitCount != nil {
		hit = *row.HitCount
	}
	_, err := d.SQL.Exec(`
		INSERT INTO tool_call_log (
			session_id, seq, ts, tool_name, tool_call_id, args_json,
			result_summary_json, ok, error, latency_ms, result_chars, hit_count
		)
		SELECT ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		FROM tool_call_log
		WHERE session_id = ?`,
		row.SessionID, row.TS, row.ToolName, row.ToolCallID, row.ArgsJSON,
		row.ResultSummaryJSON, row.OK, row.Error, row.LatencyMS, row.ResultChars, hit,
		row.SessionID,
	)
	return err
}

// ListToolCallLogs returns rows newest first. limit <= 0 defaults to 100.
// limit above 1000 is capped at 1000. Empty filters match every row.
func (d *DB) ListToolCallLogs(sessionID, tool, since string, limit int) ([]ToolCallLog, error) {
	if d == nil || !d.Writable() {
		return nil, fmt.Errorf("database not writable")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	where, args := toolLogWhere(sessionID, tool, since)
	q := `SELECT id, session_id, seq, ts, tool_name, tool_call_id, args_json,
		result_summary_json, ok, error, latency_ms, result_chars, hit_count
		FROM tool_call_log ` + where + ` ORDER BY ts DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.SQL.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ToolCallLog{}
	for rows.Next() {
		row, err := scanToolCallLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// HitCountBucket is one bar of the hit-count histogram.
type HitCountBucket struct {
	Hits  int `json:"hits"`
	Count int `json:"count"`
}

// ToolLogDocCount is how often a path appeared in result summaries.
type ToolLogDocCount struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}

// ToolLogStats is the aggregate behind GET /api/tool-log/stats.
type ToolLogStats struct {
	CallCount           int               `json:"call_count"`
	ErrorCount          int               `json:"error_count"`
	TimeoutCount        int               `json:"timeout_count"`
	EmptyResultRate     float64           `json:"empty_result_rate"`
	HitCountHistogram   []HitCountBucket  `json:"hit_count_histogram"`
	LatencyMSMedian     float64           `json:"latency_ms_median"`
	LatencyMSMean       float64           `json:"latency_ms_mean"`
	ResultCharsMedian   float64           `json:"result_chars_median"`
	ConsecutiveSameArgs int               `json:"consecutive_same_args"`
	DistinctQueryRate   float64           `json:"distinct_query_rate"`
	TopDocs             []ToolLogDocCount `json:"top_docs"`
}

// ToolCallLogStats aggregates every row matching tool and since.
// ConsecutiveSameArgs counts a call whose args_json equals the previous
// call in the same session (within the filtered set, ordered by seq).
// EmptyResultRate is the fraction of rows whose summary JSON has empty=true.
// TimeoutCount is how many rows contain the substring "timed_out":true in
// result_summary_json, which is what encoding/json writes for a bool.
// DistinctQueryRate is distinct (session, args_json) pairs over CallCount:
// 1.0 means no call repeated its arguments within a session, 1/6 is six
// identical searches. 0 when there are no rows.
// TopDocs are the ten paths most often listed under data.docs.
func (d *DB) ToolCallLogStats(tool, since string) (ToolLogStats, error) {
	st := ToolLogStats{
		HitCountHistogram: []HitCountBucket{},
		TopDocs:           []ToolLogDocCount{},
	}
	if d == nil || !d.Writable() {
		return st, fmt.Errorf("database not writable")
	}
	where, args := toolLogWhere("", tool, since)
	q := `SELECT id, session_id, seq, ts, tool_name, tool_call_id, args_json,
		result_summary_json, ok, error, latency_ms, result_chars, hit_count
		FROM tool_call_log ` + where + ` ORDER BY session_id, seq, id`
	rows, err := d.SQL.Query(q, args...)
	if err != nil {
		return st, err
	}
	defer rows.Close()

	var latency []int64
	var chars []int
	hits := map[int]int{}
	docCounts := map[string]int{}
	prevArgs := map[string]string{}
	seen := map[string]bool{}
	distinct := map[[2]string]bool{}
	emptyCount := 0

	for rows.Next() {
		row, err := scanToolCallLog(rows)
		if err != nil {
			return st, err
		}
		st.CallCount++
		if row.OK == 0 {
			st.ErrorCount++
		}
		if strings.Contains(row.ResultSummaryJSON, `"timed_out":true`) {
			st.TimeoutCount++
		}
		latency = append(latency, row.LatencyMS)
		chars = append(chars, row.ResultChars)
		if row.HitCount != nil {
			hits[*row.HitCount]++
		}
		empty, paths := summaryEmptyAndDocs(row.ResultSummaryJSON)
		if empty {
			emptyCount++
		}
		for _, p := range paths {
			docCounts[p]++
		}
		if seen[row.SessionID] && prevArgs[row.SessionID] == row.ArgsJSON {
			st.ConsecutiveSameArgs++
		}
		seen[row.SessionID] = true
		prevArgs[row.SessionID] = row.ArgsJSON
		distinct[[2]string{row.SessionID, row.ArgsJSON}] = true
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	if st.CallCount > 0 {
		st.EmptyResultRate = float64(emptyCount) / float64(st.CallCount)
		st.DistinctQueryRate = float64(len(distinct)) / float64(st.CallCount)
	}
	st.LatencyMSMedian = medianInt64(latency)
	st.LatencyMSMean = meanInt64(latency)
	st.ResultCharsMedian = medianInt(chars)

	keys := make([]int, 0, len(hits))
	for h := range hits {
		keys = append(keys, h)
	}
	sort.Ints(keys)
	for _, h := range keys {
		st.HitCountHistogram = append(st.HitCountHistogram, HitCountBucket{Hits: h, Count: hits[h]})
	}
	for p, n := range docCounts {
		st.TopDocs = append(st.TopDocs, ToolLogDocCount{Path: p, Count: n})
	}
	sort.Slice(st.TopDocs, func(i, j int) bool {
		if st.TopDocs[i].Count != st.TopDocs[j].Count {
			return st.TopDocs[i].Count > st.TopDocs[j].Count
		}
		return st.TopDocs[i].Path < st.TopDocs[j].Path
	})
	if len(st.TopDocs) > 10 {
		st.TopDocs = st.TopDocs[:10]
	}
	return st, nil
}

func toolLogWhere(sessionID, tool, since string) (string, []any) {
	return `WHERE (? = '' OR session_id = ?) AND (? = '' OR tool_name = ?) AND (? = '' OR ts >= ?)`,
		[]any{sessionID, sessionID, tool, tool, since, since}
}

type toolLogScanner interface {
	Scan(dest ...any) error
}

func scanToolCallLog(s toolLogScanner) (ToolCallLog, error) {
	var row ToolCallLog
	var hit sql.NullInt64
	err := s.Scan(
		&row.ID, &row.SessionID, &row.Seq, &row.TS, &row.ToolName, &row.ToolCallID,
		&row.ArgsJSON, &row.ResultSummaryJSON, &row.OK, &row.Error,
		&row.LatencyMS, &row.ResultChars, &hit,
	)
	if err != nil {
		return row, err
	}
	if hit.Valid {
		v := int(hit.Int64)
		row.HitCount = &v
	}
	return row, nil
}

func summaryEmptyAndDocs(raw string) (bool, []string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, nil
	}
	var body struct {
		Empty bool `json:"empty"`
		Data  struct {
			Docs []struct {
				Path string `json:"path"`
			} `json:"docs"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return false, nil
	}
	var paths []string
	for _, doc := range body.Data.Docs {
		if doc.Path != "" {
			paths = append(paths, doc.Path)
		}
	}
	return body.Empty, paths
}

func medianInt64(xs []int64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	if n%2 == 1 {
		return float64(s[n/2])
	}
	return float64(s[n/2-1]+s[n/2]) / 2
}

func meanInt64(xs []int64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum int64
	for _, n := range xs {
		sum += n
	}
	return float64(sum) / float64(len(xs))
}

func medianInt(xs []int) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int(nil), xs...)
	sort.Ints(s)
	n := len(s)
	if n%2 == 1 {
		return float64(s[n/2])
	}
	return float64(s[n/2-1]+s[n/2]) / 2
}
