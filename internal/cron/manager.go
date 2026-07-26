package cron

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/memory"
)

const (
	MaxJobs            = 50
	MaxConcurrentFires = 3
	MaxPromptChars     = 16 * 1024
	MaxNameLen         = 80
	KeepRunsPerJob     = 50
	RunsMaxAge         = 30 * 24 * time.Hour
	tickInterval       = 2 * time.Second
)

// FireResult is returned by FireFunc.
type FireResult struct {
	SessionID     string
	CreatedSession bool
	Status        string // ok | skipped_busy | skipped_limp | error
	Error         string
}

// FireFunc injects prompt into session (creating if needed) and starts a turn.
// sessionID may be empty — implementor creates a session titled with jobName.
// modelID is optional catalog pin for this fire only (ADR-0018); empty = session/process resolve.
type FireFunc func(jobID, jobName, sessionID, prompt, modelID string) FireResult

// HealthyFunc reports whether the harness can start model turns (not limp / model-down).
type HealthyFunc func() bool

// Manager owns durable cron jobs (ADR-0015).
type Manager struct {
	db      *db.DB
	onFire  FireFunc
	healthy HealthyFunc

	mu       sync.Mutex
	stop     chan struct{}
	running  int32 // concurrent in-flight fires
	pruneEvery time.Duration
	lastPrune  time.Time
}

// New creates a manager. db must be writable for scheduling; limp → no-op tick.
func New(sqldb *db.DB, onFire FireFunc, healthy HealthyFunc) *Manager {
	m := &Manager{
		db:         sqldb,
		onFire:     onFire,
		healthy:    healthy,
		stop:       make(chan struct{}),
		pruneEvery: time.Hour,
	}
	go m.loop()
	return m
}

// Stop stops the ticker.
func (m *Manager) Stop() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
}

// CreateInput is validated create payload.
type CreateInput struct {
	Name         string
	Enabled      *bool
	ScheduleKind string
	CronExpr     string
	IntervalSec  int
	Timezone     string
	SessionID    string
	Prompt       string
	MaxRuns      *int
	CreatedBy    string // ui | agent | system
	ModelID      string // optional catalog pin (ADR-0018)
}

// UpdateInput patches a job; nil pointers mean leave unchanged.
type UpdateInput struct {
	Name         *string
	Enabled      *bool
	ScheduleKind *string
	CronExpr     *string
	IntervalSec  *int
	Timezone     *string
	SessionID    *string
	Prompt       *string
	MaxRuns      **int // pointer to *int: nil field = no change; *nil = clear max_runs
	ModelID      *string
}

// Job is the API/tool view of a cron job.
type Job struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Enabled      bool    `json:"enabled"`
	ScheduleKind string  `json:"schedule_kind"`
	CronExpr     string  `json:"cron_expr,omitempty"`
	IntervalSec  int     `json:"interval_sec,omitempty"`
	Timezone     string  `json:"timezone"`
	SessionID    string  `json:"session_id,omitempty"`
	Prompt       string  `json:"prompt"`
	CreatedBy    string  `json:"created_by"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	NextRunAt    string  `json:"next_run_at,omitempty"`
	LastRunAt    string  `json:"last_run_at,omitempty"`
	LastStatus   string  `json:"last_status,omitempty"`
	LastError    string  `json:"last_error,omitempty"`
	RunCount     int     `json:"run_count"`
	MaxRuns      *int     `json:"max_runs,omitempty"`
	ModelID      string   `json:"model_id,omitempty"`
	Preview      []string `json:"preview,omitempty"`
}

// Run is a fire history row.
type Run struct {
	ID          string `json:"id"`
	JobID       string `json:"job_id"`
	ScheduledAt string `json:"scheduled_at"`
	StartedAt   string `json:"started_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	ModelID     string `json:"model_id,omitempty"` // requested pin at fire
}

func (m *Manager) requireDB() error {
	if m == nil || m.db == nil || !m.db.Writable() {
		return fmt.Errorf("cron unavailable (database not writable)")
	}
	return nil
}

// Create adds a job.
func (m *Manager) Create(in CreateInput) (*Job, error) {
	if err := m.requireDB(); err != nil {
		return nil, err
	}
	n, err := m.db.CountCronJobs()
	if err != nil {
		return nil, err
	}
	if n >= MaxJobs {
		return nil, fmt.Errorf("max cron jobs (%d) reached", MaxJobs)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if len(name) > MaxNameLen {
		return nil, fmt.Errorf("name max length %d", MaxNameLen)
	}
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if len(prompt) > MaxPromptChars {
		return nil, fmt.Errorf("prompt max %d characters", MaxPromptChars)
	}
	kind := strings.ToLower(strings.TrimSpace(in.ScheduleKind))
	if kind == "" {
		kind = "cron"
	}
	if err := ValidateSchedule(kind, in.CronExpr, in.IntervalSec); err != nil {
		return nil, err
	}
	tz := strings.TrimSpace(in.Timezone)
	if tz == "" {
		tz = "Local"
	}
	if _, err := LoadLocation(tz); err != nil {
		return nil, fmt.Errorf("timezone: %w", err)
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	by := strings.TrimSpace(in.CreatedBy)
	if by == "" {
		by = "agent"
	}
	now := time.Now()
	next, err := NextRun(kind, in.CronExpr, in.IntervalSec, tz, now)
	if err != nil {
		return nil, err
	}
	id := memory.NewSessionID()
	row := db.CronJobRow{
		ID:           id,
		Name:         name,
		Enabled:      enabled,
		ScheduleKind: kind,
		CronExpr:     strings.TrimSpace(in.CronExpr),
		IntervalSec:  in.IntervalSec,
		Timezone:     tz,
		SessionID:    strings.TrimSpace(in.SessionID),
		Prompt:       prompt,
		CreatedBy:    by,
		CreatedAt:    FormatRFC3339(now),
		UpdatedAt:    FormatRFC3339(now),
		NextRunAt:    FormatRFC3339(next),
		MaxRuns:      in.MaxRuns,
		ModelID:      strings.TrimSpace(in.ModelID),
	}
	if err := m.db.InsertCronJob(row); err != nil {
		return nil, err
	}
	return m.jobFromRow(row, true), nil
}

// Get returns a job with optional run history.
func (m *Manager) Get(id string, withRuns bool) (*Job, []Run, error) {
	if err := m.requireDB(); err != nil {
		return nil, nil, err
	}
	row, err := m.db.GetCronJob(id)
	if err != nil {
		return nil, nil, err
	}
	j := m.jobFromRow(*row, true)
	var runs []Run
	if withRuns {
		rr, err := m.db.ListCronRunsForJob(id, KeepRunsPerJob)
		if err == nil {
			runs = runsFromRows(rr)
		}
	}
	return j, runs, nil
}

// List returns jobs.
func (m *Manager) List(enabledOnly bool) ([]Job, error) {
	if err := m.requireDB(); err != nil {
		return nil, err
	}
	rows, err := m.db.ListCronJobs(enabledOnly)
	if err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(rows))
	for _, r := range rows {
		out = append(out, *m.jobFromRow(r, false))
	}
	return out, nil
}

// Update patches a job.
func (m *Manager) Update(id string, in UpdateInput) (*Job, error) {
	if err := m.requireDB(); err != nil {
		return nil, err
	}
	row, err := m.db.GetCronJob(id)
	if err != nil {
		return nil, err
	}
	schedChanged := false
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, fmt.Errorf("name cannot be empty")
		}
		if len(name) > MaxNameLen {
			return nil, fmt.Errorf("name max length %d", MaxNameLen)
		}
		row.Name = name
	}
	if in.Enabled != nil {
		row.Enabled = *in.Enabled
	}
	if in.ScheduleKind != nil {
		row.ScheduleKind = strings.ToLower(strings.TrimSpace(*in.ScheduleKind))
		schedChanged = true
	}
	if in.CronExpr != nil {
		row.CronExpr = strings.TrimSpace(*in.CronExpr)
		schedChanged = true
	}
	if in.IntervalSec != nil {
		row.IntervalSec = *in.IntervalSec
		schedChanged = true
	}
	if in.Timezone != nil {
		row.Timezone = strings.TrimSpace(*in.Timezone)
		if row.Timezone == "" {
			row.Timezone = "Local"
		}
		if _, err := LoadLocation(row.Timezone); err != nil {
			return nil, fmt.Errorf("timezone: %w", err)
		}
		schedChanged = true
	}
	if in.SessionID != nil {
		row.SessionID = strings.TrimSpace(*in.SessionID)
	}
	if in.Prompt != nil {
		p := strings.TrimSpace(*in.Prompt)
		if p == "" {
			return nil, fmt.Errorf("prompt cannot be empty")
		}
		if len(p) > MaxPromptChars {
			return nil, fmt.Errorf("prompt max %d characters", MaxPromptChars)
		}
		row.Prompt = p
	}
	if in.MaxRuns != nil {
		row.MaxRuns = *in.MaxRuns
	}
	if in.ModelID != nil {
		row.ModelID = strings.TrimSpace(*in.ModelID)
	}
	if err := ValidateSchedule(row.ScheduleKind, row.CronExpr, row.IntervalSec); err != nil {
		return nil, err
	}
	now := time.Now()
	if schedChanged || row.NextRunAt == "" {
		next, err := NextRun(row.ScheduleKind, row.CronExpr, row.IntervalSec, row.Timezone, now)
		if err != nil {
			return nil, err
		}
		row.NextRunAt = FormatRFC3339(next)
	}
	row.UpdatedAt = FormatRFC3339(now)
	if err := m.db.UpdateCronJob(*row); err != nil {
		return nil, err
	}
	return m.jobFromRow(*row, true), nil
}

// Delete removes a job.
func (m *Manager) Delete(id string) error {
	if err := m.requireDB(); err != nil {
		return err
	}
	return m.db.DeleteCronJob(id)
}

// ListRuns returns global recent runs.
func (m *Manager) ListRuns(limit int) ([]Run, error) {
	if err := m.requireDB(); err != nil {
		return nil, err
	}
	rr, err := m.db.ListCronRuns(limit)
	if err != nil {
		return nil, err
	}
	return runsFromRows(rr), nil
}

// BoundSessions maps session_id → job name(s) for sessions targeted by any cron job.
// Used by the UI to badge cron sessions (clock icon).
func (m *Manager) BoundSessions() map[string][]string {
	out := make(map[string][]string)
	if m == nil || m.db == nil || m.db.SQL == nil {
		return out
	}
	jobs, err := m.db.ListCronJobs(false)
	if err != nil {
		return out
	}
	for _, j := range jobs {
		sid := strings.TrimSpace(j.SessionID)
		if sid == "" {
			continue
		}
		out[sid] = append(out[sid], j.Name)
	}
	return out
}

// Preview computes next fire times for a schedule without saving.
func (m *Manager) Preview(kind, cronExpr string, intervalSec int, tz string, n int) ([]string, error) {
	if tz == "" {
		tz = "Local"
	}
	if err := ValidateSchedule(kind, cronExpr, intervalSec); err != nil {
		return nil, err
	}
	times, err := PreviewNext(kind, cronExpr, intervalSec, tz, time.Now(), n)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(times))
	for _, t := range times {
		out = append(out, FormatRFC3339(t))
	}
	return out, nil
}

// RunNow fires a job immediately without shifting next_run_at schedule phase
// (still records a run; next_run_at left as-is unless empty).
func (m *Manager) RunNow(id string) (*Run, error) {
	if err := m.requireDB(); err != nil {
		return nil, err
	}
	row, err := m.db.GetCronJob(id)
	if err != nil {
		return nil, err
	}
	run := m.fireJob(*row, true)
	return &run, nil
}

func (m *Manager) loop() {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.tick()
		}
	}
}

func (m *Manager) tick() {
	if m.db == nil || !m.db.Writable() {
		return
	}
	now := time.Now()
	if now.Sub(m.lastPrune) > m.pruneEvery {
		_ = m.db.PruneCronRuns(KeepRunsPerJob, RunsMaxAge)
		m.lastPrune = now
	}

	// Concurrent fire budget
	if atomic.LoadInt32(&m.running) >= MaxConcurrentFires {
		return
	}

	due, err := m.db.ListDueCronJobs(FormatRFC3339(now), MaxConcurrentFires)
	if err != nil || len(due) == 0 {
		return
	}
	for _, job := range due {
		if atomic.LoadInt32(&m.running) >= MaxConcurrentFires {
			break
		}
		// Advance next_run_at first (no catch-up storm; Q8)
		next, err := NextRun(job.ScheduleKind, job.CronExpr, job.IntervalSec, job.Timezone, now)
		if err != nil {
			log.Printf("cron: next for %s: %v", job.ID, err)
			continue
		}
		// Skip past due slots until next is in the future
		for next.Before(now) || next.Equal(now) {
			n2, err := NextRun(job.ScheduleKind, job.CronExpr, job.IntervalSec, job.Timezone, next)
			if err != nil || !n2.After(next) {
				break
			}
			next = n2
			if job.ScheduleKind == "interval" {
				break
			}
		}
		job.NextRunAt = FormatRFC3339(next)
		job.UpdatedAt = FormatRFC3339(now)
		_ = m.db.UpdateCronJob(job)

		// max_runs check
		if job.MaxRuns != nil && job.RunCount >= *job.MaxRuns {
			job.Enabled = false
			job.LastStatus = "max_runs"
			_ = m.db.UpdateCronJob(job)
			continue
		}

		jcopy := job
		atomic.AddInt32(&m.running, 1)
		go func() {
			defer atomic.AddInt32(&m.running, -1)
			m.fireJob(jcopy, false)
		}()
	}
}

// fireJob executes one fire. skipScheduleAdvance is true for RunNow.
func (m *Manager) fireJob(job db.CronJobRow, runNow bool) Run {
	now := time.Now()
	runID := memory.NewSessionID()
	run := db.CronRunRow{
		ID:          runID,
		JobID:       job.ID,
		ScheduledAt: FormatRFC3339(now),
		StartedAt:   FormatRFC3339(now),
		SessionID:   job.SessionID,
		ModelID:     job.ModelID, // requested pin (ADR-0018); not post-fallthrough
	}

	if m.healthy != nil && !m.healthy() {
		run.Status = "skipped_limp"
		run.FinishedAt = FormatRFC3339(time.Now())
		_ = m.db.InsertCronRun(run)
		job.LastRunAt = run.FinishedAt
		job.LastStatus = run.Status
		job.UpdatedAt = run.FinishedAt
		_ = m.db.UpdateCronJob(job)
		return runFromRow(run)
	}

	if m.onFire == nil {
		run.Status = "error"
		run.Error = "fire callback not configured"
		run.FinishedAt = FormatRFC3339(time.Now())
		_ = m.db.InsertCronRun(run)
		return runFromRow(run)
	}

	prefix := fmt.Sprintf("[cron:%s %s]\n", job.ID, job.Name)
	prompt := prefix + job.Prompt
	res := m.onFire(job.ID, job.Name, job.SessionID, prompt, job.ModelID)

	run.Status = res.Status
	if run.Status == "" {
		run.Status = "ok"
	}
	run.Error = res.Error
	run.SessionID = res.SessionID
	if res.CreatedSession {
		if run.Status == "ok" {
			run.Status = "created_session"
		}
		// rebind job
		job.SessionID = res.SessionID
	}
	run.FinishedAt = FormatRFC3339(time.Now())
	_ = m.db.InsertCronRun(run)

	// Reload job to avoid clobbering next_run_at advanced in tick
	if latest, err := m.db.GetCronJob(job.ID); err == nil {
		job = *latest
		if res.CreatedSession && res.SessionID != "" {
			job.SessionID = res.SessionID
		}
	}
	job.LastRunAt = run.FinishedAt
	job.LastStatus = run.Status
	job.LastError = run.Error
	if res.Status == "ok" || res.Status == "created_session" || res.Status == "" {
		job.RunCount++
		if job.MaxRuns != nil && job.RunCount >= *job.MaxRuns {
			job.Enabled = false
		}
	}
	job.UpdatedAt = FormatRFC3339(time.Now())
	_ = m.db.UpdateCronJob(job)

	_ = runNow // schedule already left alone for run-now (tick advances before fire)
	return runFromRow(run)
}

func (m *Manager) jobFromRow(r db.CronJobRow, withPreview bool) *Job {
	j := &Job{
		ID:           r.ID,
		Name:         r.Name,
		Enabled:      r.Enabled,
		ScheduleKind: r.ScheduleKind,
		CronExpr:     r.CronExpr,
		IntervalSec:  r.IntervalSec,
		Timezone:     r.Timezone,
		SessionID:    r.SessionID,
		Prompt:       r.Prompt,
		CreatedBy:    r.CreatedBy,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
		NextRunAt:    r.NextRunAt,
		LastRunAt:    r.LastRunAt,
		LastStatus:   r.LastStatus,
		LastError:    r.LastError,
		RunCount:     r.RunCount,
		MaxRuns:      r.MaxRuns,
		ModelID:      r.ModelID,
	}
	if withPreview {
		if prev, err := PreviewNext(r.ScheduleKind, r.CronExpr, r.IntervalSec, r.Timezone, time.Now(), 5); err == nil {
			for _, t := range prev {
				j.Preview = append(j.Preview, FormatRFC3339(t))
			}
		}
	}
	return j
}

func runsFromRows(rr []db.CronRunRow) []Run {
	out := make([]Run, 0, len(rr))
	for _, r := range rr {
		out = append(out, runFromRow(r))
	}
	return out
}

func runFromRow(r db.CronRunRow) Run {
	return Run{
		ID:          r.ID,
		JobID:       r.JobID,
		ScheduledAt: r.ScheduledAt,
		StartedAt:   r.StartedAt,
		FinishedAt:  r.FinishedAt,
		Status:      r.Status,
		Error:       r.Error,
		SessionID:   r.SessionID,
		ModelID:     r.ModelID,
	}
}
