package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CronJobRow is a durable cron job (ADR-0015).
type CronJobRow struct {
	ID          string
	Name        string
	Enabled     bool
	ScheduleKind string // cron | interval
	CronExpr    string
	IntervalSec int
	Timezone    string
	SessionID   string
	Prompt      string
	CreatedBy   string
	CreatedAt   string
	UpdatedAt   string
	NextRunAt   string
	LastRunAt   string
	LastStatus  string
	LastError   string
	RunCount    int
	MaxRuns     *int
}

// CronRunRow is one fire attempt.
type CronRunRow struct {
	ID          string
	JobID       string
	ScheduledAt string
	StartedAt   string
	FinishedAt  string
	Status      string
	Error       string
	SessionID   string
}

func (d *DB) migrateV1toV2() error {
	if d.SQL == nil {
		return fmt.Errorf("no database")
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS cron_jobs (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			schedule_kind TEXT NOT NULL,
			cron_expr TEXT,
			interval_sec INTEGER,
			timezone TEXT NOT NULL DEFAULT 'Local',
			session_id TEXT,
			prompt TEXT NOT NULL,
			created_by TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			next_run_at TEXT,
			last_run_at TEXT,
			last_status TEXT,
			last_error TEXT,
			run_count INTEGER NOT NULL DEFAULT 0,
			max_runs INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS cron_runs (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL,
			scheduled_at TEXT NOT NULL,
			started_at TEXT,
			finished_at TEXT,
			status TEXT NOT NULL,
			error TEXT,
			session_id TEXT,
			FOREIGN KEY(job_id) REFERENCES cron_jobs(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cron_jobs_next ON cron_jobs(enabled, next_run_at)`,
		`CREATE INDEX IF NOT EXISTS idx_cron_runs_job ON cron_runs(job_id, scheduled_at DESC)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("migrate v2: %w\nstmt: %s", err, s)
		}
	}
	now := UTCNow()
	if _, err := tx.Exec(
		`UPDATE schema_meta SET schema_version = 2, updated_at = ? WHERE id = 1`,
		now,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// InsertCronJob inserts a new job.
func (d *DB) InsertCronJob(j CronJobRow) error {
	if !d.Writable() {
		return fmt.Errorf("database not writable")
	}
	maxRuns := interface{}(nil)
	if j.MaxRuns != nil {
		maxRuns = *j.MaxRuns
	}
	en := 0
	if j.Enabled {
		en = 1
	}
	_, err := d.SQL.Exec(`
INSERT INTO cron_jobs (
  id, name, enabled, schedule_kind, cron_expr, interval_sec, timezone,
  session_id, prompt, created_by, created_at, updated_at, next_run_at,
  last_run_at, last_status, last_error, run_count, max_runs
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.Name, en, j.ScheduleKind, cronNullStr(j.CronExpr), cronNullInt(j.IntervalSec),
		j.Timezone, cronNullStr(j.SessionID), j.Prompt, j.CreatedBy, j.CreatedAt, j.UpdatedAt,
		cronNullStr(j.NextRunAt), cronNullStr(j.LastRunAt), cronNullStr(j.LastStatus), cronNullStr(j.LastError),
		j.RunCount, maxRuns,
	)
	return err
}

// UpdateCronJob replaces mutable fields (full row write).
func (d *DB) UpdateCronJob(j CronJobRow) error {
	if !d.Writable() {
		return fmt.Errorf("database not writable")
	}
	maxRuns := interface{}(nil)
	if j.MaxRuns != nil {
		maxRuns = *j.MaxRuns
	}
	en := 0
	if j.Enabled {
		en = 1
	}
	res, err := d.SQL.Exec(`
UPDATE cron_jobs SET
  name=?, enabled=?, schedule_kind=?, cron_expr=?, interval_sec=?, timezone=?,
  session_id=?, prompt=?, updated_at=?, next_run_at=?, last_run_at=?,
  last_status=?, last_error=?, run_count=?, max_runs=?
WHERE id=?`,
		j.Name, en, j.ScheduleKind, cronNullStr(j.CronExpr), cronNullInt(j.IntervalSec),
		j.Timezone, cronNullStr(j.SessionID), j.Prompt, j.UpdatedAt, cronNullStr(j.NextRunAt),
		cronNullStr(j.LastRunAt), cronNullStr(j.LastStatus), cronNullStr(j.LastError),
		j.RunCount, maxRuns, j.ID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("cron job not found")
	}
	return nil
}

// DeleteCronJob removes a job (runs cascade).
func (d *DB) DeleteCronJob(id string) error {
	if !d.Writable() {
		return fmt.Errorf("database not writable")
	}
	res, err := d.SQL.Exec(`DELETE FROM cron_jobs WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("cron job not found")
	}
	return nil
}

// GetCronJob loads one job.
func (d *DB) GetCronJob(id string) (*CronJobRow, error) {
	if d.SQL == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	row := d.SQL.QueryRow(`
SELECT id, name, enabled, schedule_kind, cron_expr, interval_sec, timezone,
  session_id, prompt, created_by, created_at, updated_at, next_run_at,
  last_run_at, last_status, last_error, run_count, max_runs
FROM cron_jobs WHERE id = ?`, id)
	return scanCronJob(row)
}

// ListCronJobs returns all jobs, newest updated first.
func (d *DB) ListCronJobs(enabledOnly bool) ([]CronJobRow, error) {
	if d.SQL == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	q := `
SELECT id, name, enabled, schedule_kind, cron_expr, interval_sec, timezone,
  session_id, prompt, created_by, created_at, updated_at, next_run_at,
  last_run_at, last_status, last_error, run_count, max_runs
FROM cron_jobs`
	if enabledOnly {
		q += ` WHERE enabled = 1`
	}
	q += ` ORDER BY updated_at DESC`
	rows, err := d.SQL.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CronJobRow
	for rows.Next() {
		j, err := scanCronJobRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// ListDueCronJobs returns enabled jobs with next_run_at <= nowRFC3339.
func (d *DB) ListDueCronJobs(nowRFC3339 string, limit int) ([]CronJobRow, error) {
	if d.SQL == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.SQL.Query(`
SELECT id, name, enabled, schedule_kind, cron_expr, interval_sec, timezone,
  session_id, prompt, created_by, created_at, updated_at, next_run_at,
  last_run_at, last_status, last_error, run_count, max_runs
FROM cron_jobs
WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ?
ORDER BY next_run_at ASC
LIMIT ?`, nowRFC3339, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CronJobRow
	for rows.Next() {
		j, err := scanCronJobRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// CountCronJobs returns total job count.
func (d *DB) CountCronJobs() (int, error) {
	if d.SQL == nil {
		return 0, fmt.Errorf("database unavailable")
	}
	var n int
	err := d.SQL.QueryRow(`SELECT COUNT(*) FROM cron_jobs`).Scan(&n)
	return n, err
}

// InsertCronRun appends a run row.
func (d *DB) InsertCronRun(r CronRunRow) error {
	if !d.Writable() {
		return fmt.Errorf("database not writable")
	}
	_, err := d.SQL.Exec(`
INSERT INTO cron_runs (id, job_id, scheduled_at, started_at, finished_at, status, error, session_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.JobID, r.ScheduledAt, cronNullStr(r.StartedAt), cronNullStr(r.FinishedAt),
		r.Status, cronNullStr(r.Error), cronNullStr(r.SessionID),
	)
	return err
}

// ListCronRunsForJob returns recent runs for a job.
func (d *DB) ListCronRunsForJob(jobID string, limit int) ([]CronRunRow, error) {
	if d.SQL == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.SQL.Query(`
SELECT id, job_id, scheduled_at, started_at, finished_at, status, error, session_id
FROM cron_runs WHERE job_id = ?
ORDER BY scheduled_at DESC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCronRuns(rows)
}

// ListCronRuns returns global recent runs.
func (d *DB) ListCronRuns(limit int) ([]CronRunRow, error) {
	if d.SQL == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.SQL.Query(`
SELECT id, job_id, scheduled_at, started_at, finished_at, status, error, session_id
FROM cron_runs
ORDER BY scheduled_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCronRuns(rows)
}

// PruneCronRuns keeps last keepPerJob runs per job and drops older than maxAge.
func (d *DB) PruneCronRuns(keepPerJob int, maxAge time.Duration) error {
	if !d.Writable() {
		return nil
	}
	if keepPerJob <= 0 {
		keepPerJob = 50
	}
	cutoff := time.Now().Add(-maxAge).UTC().Format(time.RFC3339Nano)
	// Age prune
	if maxAge > 0 {
		_, _ = d.SQL.Exec(`DELETE FROM cron_runs WHERE scheduled_at < ?`, cutoff)
	}
	// Per-job cap via subquery (SQLite)
	_, err := d.SQL.Exec(`
DELETE FROM cron_runs WHERE id IN (
  SELECT id FROM (
    SELECT id,
      ROW_NUMBER() OVER (PARTITION BY job_id ORDER BY scheduled_at DESC) AS rn
    FROM cron_runs
  ) WHERE rn > ?
)`, keepPerJob)
	// ROW_NUMBER may not exist on older SQLite — fall back silently if fails
	if err != nil {
		// best-effort: ignore window function failure
		return nil
	}
	return nil
}

func scanCronRuns(rows *sql.Rows) ([]CronRunRow, error) {
	var out []CronRunRow
	for rows.Next() {
		var r CronRunRow
		var started, finished, errStr, sid sql.NullString
		if err := rows.Scan(&r.ID, &r.JobID, &r.ScheduledAt, &started, &finished, &r.Status, &errStr, &sid); err != nil {
			return nil, err
		}
		r.StartedAt = started.String
		r.FinishedAt = finished.String
		r.Error = errStr.String
		r.SessionID = sid.String
		out = append(out, r)
	}
	return out, rows.Err()
}

type scannable interface {
	Scan(dest ...interface{}) error
}

func scanCronJob(row scannable) (*CronJobRow, error) {
	var j CronJobRow
	var en int
	var cronExpr, sess, next, last, lstat, lerr sql.NullString
	var interval sql.NullInt64
	var maxRuns sql.NullInt64
	err := row.Scan(
		&j.ID, &j.Name, &en, &j.ScheduleKind, &cronExpr, &interval, &j.Timezone,
		&sess, &j.Prompt, &j.CreatedBy, &j.CreatedAt, &j.UpdatedAt, &next,
		&last, &lstat, &lerr, &j.RunCount, &maxRuns,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("cron job not found")
		}
		return nil, err
	}
	j.Enabled = en != 0
	j.CronExpr = cronExpr.String
	if interval.Valid {
		j.IntervalSec = int(interval.Int64)
	}
	j.SessionID = sess.String
	j.NextRunAt = next.String
	j.LastRunAt = last.String
	j.LastStatus = lstat.String
	j.LastError = lerr.String
	if maxRuns.Valid {
		v := int(maxRuns.Int64)
		j.MaxRuns = &v
	}
	if j.Timezone == "" {
		j.Timezone = "Local"
	}
	return &j, nil
}

func scanCronJobRows(rows *sql.Rows) (*CronJobRow, error) {
	return scanCronJob(rows)
}

func cronNullStr(s string) interface{} {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func cronNullInt(n int) interface{} {
	if n <= 0 {
		return nil
	}
	return n
}
