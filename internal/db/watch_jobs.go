package db

import (
	"database/sql"
	"fmt"
)

// Watch statuses (ADR-0041). done, failed, lost and expired are terminal.
const (
	WatchPending = "pending"
	WatchRunning = "running"
	WatchDone    = "done"
	WatchFailed  = "failed"
	WatchLost    = "lost"
	WatchExpired = "expired"
)

// Watch end reasons (ADR-0041 transition table).
const (
	WatchReasonStartError      = "start_error"
	WatchReasonExitZero        = "exit_zero"
	WatchReasonExitNonzero     = "exit_nonzero"
	WatchReasonWaitError       = "wait_error"
	WatchReasonCancelled       = "cancelled"
	WatchReasonSessionClosed   = "session_closed"
	WatchReasonDeadline        = "deadline"
	WatchReasonHarnessShutdown = "harness_shutdown"
	WatchReasonHarnessCrash    = "harness_crash"
)

// WatchJobRow is one watch_jobs row. Empty strings are SQL NULL.
type WatchJobRow struct {
	ID             string
	SessionID      string
	Label          string
	Command        string
	CWD            string
	Prompt         string
	TaskID         string
	ContinuationID string
	Status         string
	EndReason      string
	ExitCode       *int
	OutputTail     string
	CreatedAt      string
	DeadlineAt     string
	EndedAt        string
	WakeOutcome    string
	Note           string
}

// WatchTransition is one state change. Empty optional fields leave the column as it is.
type WatchTransition struct {
	To             string
	EndReason      string
	ExitCode       *int
	OutputTail     string
	TaskID         string
	ContinuationID string
	Note           string
}

type watchEdge struct{ from, to, reason string }

// watchLegal is the ADR-0041 transition table.
var watchLegal = map[watchEdge]bool{
	{WatchPending, WatchRunning, ""}:                      true,
	{WatchPending, WatchFailed, WatchReasonStartError}:    true,
	{WatchPending, WatchLost, WatchReasonHarnessCrash}:    true,
	{WatchRunning, WatchDone, WatchReasonExitZero}:        true,
	{WatchRunning, WatchFailed, WatchReasonExitNonzero}:   true,
	{WatchRunning, WatchFailed, WatchReasonWaitError}:     true,
	{WatchRunning, WatchFailed, WatchReasonCancelled}:     true,
	{WatchRunning, WatchFailed, WatchReasonSessionClosed}: true,
	{WatchRunning, WatchExpired, WatchReasonDeadline}:     true,
	{WatchRunning, WatchLost, WatchReasonHarnessShutdown}: true,
	{WatchRunning, WatchLost, WatchReasonHarnessCrash}:    true,
}

// WatchTransitionLegal reports whether the table allows this edge.
func WatchTransitionLegal(from, to, endReason string) bool {
	return watchLegal[watchEdge{from, to, endReason}]
}

// WatchStatusTerminal reports whether a status has no transitions out of it.
func WatchStatusTerminal(status string) bool {
	switch status {
	case WatchDone, WatchFailed, WatchLost, WatchExpired:
		return true
	}
	return false
}

func (d *DB) migrateV14toV15() error {
	if d.SQL == nil {
		return fmt.Errorf("no database")
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS watch_jobs (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			label TEXT,
			command TEXT NOT NULL,
			cwd TEXT,
			prompt TEXT,
			task_id TEXT,
			continuation_id TEXT,
			status TEXT NOT NULL,
			end_reason TEXT,
			exit_code INTEGER,
			output_tail TEXT,
			created_at TEXT NOT NULL,
			deadline_at TEXT NOT NULL,
			ended_at TEXT,
			wake_outcome TEXT,
			note TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_watch_jobs_session ON watch_jobs(session_id, status)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("migrate v15: %w", err)
		}
	}
	if _, err := tx.Exec(`UPDATE schema_meta SET schema_version = 15, updated_at = ? WHERE id = 1`, UTCNow()); err != nil {
		return fmt.Errorf("migrate v15: %w", err)
	}
	return tx.Commit()
}

const watchJobColumns = `id, session_id, label, command, cwd, prompt, task_id, continuation_id,
  status, end_reason, exit_code, output_tail, created_at, deadline_at, ended_at, wake_outcome, note`

func scanWatchJob(row interface{ Scan(dest ...any) error }) (*WatchJobRow, error) {
	var j WatchJobRow
	var label, cwd, prompt, task, cont, reason, tail, ended, wake, note sql.NullString
	var code sql.NullInt64
	if err := row.Scan(&j.ID, &j.SessionID, &label, &j.Command, &cwd, &prompt, &task, &cont,
		&j.Status, &reason, &code, &tail, &j.CreatedAt, &j.DeadlineAt, &ended, &wake, &note); err != nil {
		return nil, err
	}
	j.Label, j.CWD, j.Prompt = label.String, cwd.String, prompt.String
	j.TaskID, j.ContinuationID, j.EndReason = task.String, cont.String, reason.String
	j.OutputTail, j.EndedAt, j.WakeOutcome, j.Note = tail.String, ended.String, wake.String, note.String
	if code.Valid {
		v := int(code.Int64)
		j.ExitCode = &v
	}
	return &j, nil
}

// InsertWatchJob inserts a new row, which must be pending.
func (d *DB) InsertWatchJob(j WatchJobRow) error {
	if !d.Writable() {
		return fmt.Errorf("database not writable")
	}
	if j.Status != WatchPending {
		return fmt.Errorf("watch %s must start pending, got %q", j.ID, j.Status)
	}
	_, err := d.SQL.Exec(`INSERT INTO watch_jobs (
  id, session_id, label, command, cwd, prompt, status, created_at, deadline_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.SessionID, nullStr(j.Label), j.Command, nullStr(j.CWD), nullStr(j.Prompt),
		j.Status, j.CreatedAt, j.DeadlineAt)
	return err
}

// GetWatchJob returns one row; sql.ErrNoRows when it does not exist.
func (d *DB) GetWatchJob(id string) (*WatchJobRow, error) {
	if !d.Writable() {
		return nil, fmt.Errorf("database not writable")
	}
	return scanWatchJob(d.SQL.QueryRow(`SELECT `+watchJobColumns+` FROM watch_jobs WHERE id = ?`, id))
}

// TransitionWatchJob moves a row from one status to another by compare-and-set.
// It returns false with a nil error when zero rows matched: another path won.
// An edge outside the ADR-0041 table is an error and touches nothing.
func (d *DB) TransitionWatchJob(id, from string, tr WatchTransition) (bool, error) {
	if !d.Writable() {
		return false, fmt.Errorf("database not writable")
	}
	if !WatchTransitionLegal(from, tr.To, tr.EndReason) {
		return false, fmt.Errorf("illegal watch transition %s -> %s (%q)", from, tr.To, tr.EndReason)
	}
	var ended interface{}
	if WatchStatusTerminal(tr.To) {
		ended = UTCNow()
	}
	var code interface{}
	if tr.ExitCode != nil {
		code = *tr.ExitCode
	}
	res, err := d.SQL.Exec(`UPDATE watch_jobs SET
  status = ?,
  end_reason = COALESCE(?, end_reason),
  exit_code = COALESCE(?, exit_code),
  output_tail = COALESCE(?, output_tail),
  task_id = COALESCE(?, task_id),
  continuation_id = COALESCE(?, continuation_id),
  note = COALESCE(?, note),
  ended_at = COALESCE(?, ended_at)
WHERE id = ? AND status = ?`,
		tr.To, nullStr(tr.EndReason), code, nullStr(tr.OutputTail), nullStr(tr.TaskID),
		nullStr(tr.ContinuationID), nullStr(tr.Note), ended, id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// SettleWatchWake records how a wake ended, once: false means it was already settled.
func (d *DB) SettleWatchWake(id, outcome string) (bool, error) {
	if !d.Writable() {
		return false, fmt.Errorf("database not writable")
	}
	res, err := d.SQL.Exec(`UPDATE watch_jobs SET wake_outcome = ? WHERE id = ? AND wake_outcome IS NULL`, outcome, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (d *DB) queryWatchJobs(where string, args ...interface{}) ([]WatchJobRow, error) {
	if !d.Writable() {
		return nil, fmt.Errorf("database not writable")
	}
	rows, err := d.SQL.Query(`SELECT `+watchJobColumns+` FROM watch_jobs `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WatchJobRow
	for rows.Next() {
		j, err := scanWatchJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// ListWatchJobs returns a session's watches, newest first.
func (d *DB) ListWatchJobs(sessionID string, limit int) ([]WatchJobRow, error) {
	if limit <= 0 {
		limit = 50
	}
	return d.queryWatchJobs(`WHERE session_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, sessionID, limit)
}

// ListLiveWatchJobs returns pending and running rows in a stable order, for one
// session or, when sessionID is empty, every session.
func (d *DB) ListLiveWatchJobs(sessionID string) ([]WatchJobRow, error) {
	if sessionID == "" {
		return d.queryWatchJobs(`WHERE status IN ('pending', 'running') ORDER BY created_at, id`)
	}
	return d.queryWatchJobs(`WHERE session_id = ? AND status IN ('pending', 'running') ORDER BY created_at, id`, sessionID)
}

// ListUnsettledWatchJobs returns terminal rows whose wake has not been settled.
func (d *DB) ListUnsettledWatchJobs() ([]WatchJobRow, error) {
	return d.queryWatchJobs(`WHERE status IN ('done', 'failed', 'lost', 'expired') AND wake_outcome IS NULL ORDER BY created_at, id`)
}

// CountWatchJobsByStatus returns one count per status present in watch_jobs, plus
// the "total" key. Aggregated in SQL: /api/health polls it, so it must not walk rows.
func (d *DB) CountWatchJobsByStatus() (map[string]int, error) {
	if !d.Writable() {
		return nil, fmt.Errorf("database not writable")
	}
	rows, err := d.SQL.Query(`SELECT status, COUNT(*) FROM watch_jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	total := 0
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
		total += n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out["total"] = total
	return out, nil
}
