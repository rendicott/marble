// Package watch implements ADR-0041 async watch obligations: a background command
// and a continuation joined by one durable watch_jobs row, so that the session is
// always told how the command ended.
//
// Every state change is a compare-and-set, and the path that wins it owns the wake.
package watch

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rendicott/marble/internal/bgtask"
	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/continuation"
	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/memory"
)

const (
	defaultDeadlineSec = 3600
	maxDeadlineSec     = 86400
	bootDelaySec       = 1
	maxWakeErrors      = 6
	listLimit          = 50

	wakeStarted       = "started"
	wakeToolResult    = "answered:tool_result"
	wakeUndeliverable = "undeliverable:"
	whyClosed         = "session_closed"
)

// ErrNotWritable refuses a watch that could not be persisted.
var ErrNotWritable = errors.New("watch refused: the database is not writable, so this watch could not be durable")

// WakeStatus is what happened when a wake was offered to a session.
type WakeStatus int

const (
	WakeStarted WakeStatus = iota // a turn started with the wake text
	WakeBusy                      // the session is mid-turn; retry
	WakeClosed                    // the session is closed; no retry
	WakeError                     // the session could not be loaded; bounded retry
)

// WakeResult is returned by a WakeFunc. Detail explains WakeError.
type WakeResult struct {
	Status WakeStatus
	Detail string
}

// WakeFunc offers text to a session as a continuation turn.
type WakeFunc func(sessionID, text string) WakeResult

// Manager owns the watch state machine. Set the optional hooks before first use.
type Manager struct {
	db   *db.DB
	bg   *bgtask.Manager
	cont *continuation.Manager
	wake WakeFunc

	// SessionClosed reports a closed session at boot, to record it rather than wake it.
	SessionClosed func(sessionID string) bool
	// OnUndelivered reports a wake that can never be delivered (ADR-0041 channel d).
	OnUndelivered func(row db.WatchJobRow, why string)
	// OnEvent records watch_started, watch_fired and watch_undelivered session events.
	OnEvent func(sessionID, kind, detail string)
	// Notify tells live UIs that a session's watches changed.
	Notify func(sessionID string)
	// RetryInterval is how often an undelivered wake is retried. Default 10s.
	RetryInterval time.Duration

	// armMu spans ScheduleWatch through pending->running, so a command that exits
	// at once cannot fire while its row is still pending.
	armMu sync.Mutex

	mu         sync.Mutex
	reconciled map[string]bool

	stopped  atomic.Bool
	stop     chan struct{}
	stopOnce sync.Once
}

// New creates a Manager. wake must be non-nil.
func New(d *db.DB, bg *bgtask.Manager, cont *continuation.Manager, wake WakeFunc) *Manager {
	return &Manager{
		db: d, bg: bg, cont: cont, wake: wake,
		RetryInterval: 10 * time.Second,
		reconciled:    make(map[string]bool),
		stop:          make(chan struct{}),
	}
}

// Stop ends wake retries and refuses new watches. Unsettled rows are redelivered at next boot.
func (m *Manager) Stop() {
	m.stopped.Store(true)
	m.stopOnce.Do(func() { close(m.stop) })
}

// Health reports watch state for GET /api/health. Counts come from one grouped
// query, so a probe never walks the table. watch_count is the live set
// (pending+running) — the same set /pending shows as chips.
func (m *Manager) Health() map[string]interface{} {
	if m == nil {
		return map[string]interface{}{"watch_enabled": false, "watch_count": 0}
	}
	out := map[string]interface{}{
		"watch_enabled": true,
		"watch_stopped": m.stopped.Load(),
	}
	if m.db == nil || !m.db.Writable() {
		out["watch_db_writable"] = false
		out["watch_count"] = 0
		return out
	}
	out["watch_db_writable"] = true
	byStatus, err := m.db.CountWatchJobsByStatus()
	if err != nil {
		out["watch_error"] = err.Error()
		out["watch_count"] = 0
		return out
	}
	live := byStatus[db.WatchPending] + byStatus[db.WatchRunning]
	out["watch_count"] = live
	out["watch_total"] = byStatus["total"]
	out["watch_by_status"] = byStatus
	return out
}

func (m *Manager) notify(sessionID string) {
	if m.Notify != nil {
		m.Notify(sessionID)
	}
}

func (m *Manager) event(sessionID, kind, detail string) {
	if m.OnEvent != nil {
		m.OnEvent(sessionID, kind, detail)
	}
}

// CreateArgs are the watch tool's create parameters.
type CreateArgs struct {
	Command     string
	CWD         string
	Label       string
	Prompt      string
	DeadlineSec int
}

// Create records a pending row, starts the command and arms the wake. Any failure
// leaves the row failed/start_error with the task killed.
func (m *Manager) Create(sessionID string, a CreateArgs) (*db.WatchJobRow, error) {
	if m == nil || !m.db.Writable() {
		return nil, ErrNotWritable
	}
	if m.stopped.Load() {
		return nil, fmt.Errorf("watch refused: the harness is shutting down")
	}
	command := strings.TrimSpace(a.Command)
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}
	deadline := a.DeadlineSec
	if deadline <= 0 {
		deadline = defaultDeadlineSec
	}
	if deadline > maxDeadlineSec {
		return nil, fmt.Errorf("deadline_sec %d exceeds max %d", deadline, maxDeadlineSec)
	}

	now := time.Now().UTC()
	row := db.WatchJobRow{
		ID:         "w_" + memory.NewSessionID(),
		SessionID:  sessionID,
		Label:      cleanLabel(config.ScrubSecrets(a.Label), config.ScrubSecrets(command)),
		Command:    config.ScrubSecrets(command),
		CWD:        a.CWD,
		Prompt:     config.ScrubSecrets(a.Prompt),
		Status:     db.WatchPending,
		CreatedAt:  now.Format(time.RFC3339),
		DeadlineAt: now.Add(time.Duration(deadline) * time.Second).Format(time.RFC3339),
	}
	if err := m.db.InsertWatchJob(row); err != nil {
		return nil, fmt.Errorf("watch not created: %w", err)
	}

	task, err := m.bg.Start(sessionID, command, a.CWD, row.Label)
	if err != nil {
		if task != nil {
			_ = m.bg.Kill(task.ID, true)
		}
		m.failStart(&row, err)
		return nil, err
	}

	m.armMu.Lock()
	defer m.armMu.Unlock()
	job, err := m.cont.ScheduleWatch(sessionID, "watch "+row.ID+": verdict rendered when it exits",
		deadline, task.ID, row.Label, continuation.SourceWatch, row.ID)
	if err != nil {
		_ = m.bg.Kill(task.ID, true)
		m.failStart(&row, err)
		return nil, err
	}
	won, err := m.db.TransitionWatchJob(row.ID, db.WatchPending, db.WatchTransition{
		To: db.WatchRunning, TaskID: task.ID, ContinuationID: job.ID,
	})
	if err != nil || !won {
		if err == nil {
			err = fmt.Errorf("watch %s changed state while starting", row.ID)
		}
		m.cont.Cancel(job.ID)
		_ = m.bg.Kill(task.ID, true)
		m.failStart(&row, err)
		return nil, err
	}
	m.event(sessionID, "watch_started", row.ID+" "+row.Label)
	m.notify(sessionID)
	return m.db.GetWatchJob(row.ID)
}

// failStart ends a pending row. The tool result reports the error, so no wake follows.
func (m *Manager) failStart(row *db.WatchJobRow, cause error) {
	won, err := m.db.TransitionWatchJob(row.ID, db.WatchPending, db.WatchTransition{
		To: db.WatchFailed, EndReason: db.WatchReasonStartError, Note: cause.Error(),
	})
	if err != nil {
		log.Printf("watch %s: record start failure: %v", row.ID, err)
		return
	}
	if won {
		if _, err := m.db.SettleWatchWake(row.ID, wakeToolResult); err != nil {
			log.Printf("watch %s: settle wake: %v", row.ID, err)
		}
	}
}

// owned loads a row and checks it belongs to the session; empty sessionID skips that check.
func (m *Manager) owned(sessionID, id string) (*db.WatchJobRow, error) {
	if m == nil || !m.db.Writable() {
		return nil, ErrNotWritable
	}
	row, err := m.db.GetWatchJob(id)
	if err != nil || (sessionID != "" && row.SessionID != sessionID) {
		return nil, fmt.Errorf("watch %q not found in this session", id)
	}
	return row, nil
}

// View is a row as shown to the agent and the UI.
type View struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Command     string `json:"command"`
	TaskID      string `json:"task_id,omitempty"`
	Status      string `json:"status"`
	EndReason   string `json:"end_reason,omitempty"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	ElapsedSec  int    `json:"elapsed_sec"`
	CreatedAt   string `json:"created_at"`
	DeadlineAt  string `json:"deadline_at"`
	EndedAt     string `json:"ended_at,omitempty"`
	WakeOutcome string `json:"wake_outcome,omitempty"`
	Note        string `json:"note,omitempty"`
	OutputTail  string `json:"output_tail,omitempty"`
}

func viewOf(r *db.WatchJobRow) View {
	return View{
		ID: r.ID, Label: r.Label, Command: r.Command, TaskID: r.TaskID, Status: r.Status, EndReason: r.EndReason,
		ExitCode: r.ExitCode, ElapsedSec: int(elapsed(r, time.Now()).Seconds()), CreatedAt: r.CreatedAt,
		DeadlineAt: r.DeadlineAt, EndedAt: r.EndedAt, WakeOutcome: r.WakeOutcome, Note: r.Note,
		OutputTail: r.OutputTail,
	}
}

// Status returns one watch with its live tail while it runs.
func (m *Manager) Status(sessionID, id string) (*View, error) {
	row, err := m.owned(sessionID, id)
	if err != nil {
		return nil, err
	}
	v := viewOf(row)
	if t, ok := m.liveTask(row); ok {
		v.OutputTail = tailOf(t.Combined)
	}
	return &v, nil
}

func (m *Manager) liveTask(row *db.WatchJobRow) (*bgtask.Task, bool) {
	if row.Status != db.WatchRunning || row.TaskID == "" {
		return nil, false
	}
	return m.bg.Get(row.TaskID)
}

// List returns a session's watches newest first, without output tails.
func (m *Manager) List(sessionID string) ([]View, error) {
	return m.views(sessionID, false)
}

// ListWithTails is List including the tail of each watch, for the UI.
func (m *Manager) ListWithTails(sessionID string) ([]View, error) {
	return m.views(sessionID, true)
}

func (m *Manager) views(sessionID string, tails bool) ([]View, error) {
	if m == nil || !m.db.Writable() {
		return nil, ErrNotWritable
	}
	rows, err := m.db.ListWatchJobs(sessionID, listLimit)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(rows))
	for i := range rows {
		v := viewOf(&rows[i])
		if tails {
			if t, ok := m.liveTask(&rows[i]); ok {
				v.OutputTail = tailOf(t.Combined)
			}
		} else {
			v.OutputTail = ""
		}
		out = append(out, v)
	}
	return out, nil
}

func (m *Manager) liveRows(sessionID string) []db.WatchJobRow {
	if m == nil || !m.db.Writable() {
		return nil
	}
	rows, err := m.db.ListLiveWatchJobs(sessionID)
	if err != nil {
		log.Printf("watch: list live for %s: %v", sessionID, err)
		return nil
	}
	return rows
}

// AdvisoryLine is the standing per-call advisory for live watches, or "". It has no
// elapsed time and a stable order: it heads the prompt and must not defeat prefix caching.
func (m *Manager) AdvisoryLine(sessionID string) string {
	rows := m.liveRows(sessionID)
	switch len(rows) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("[harness] %s still running. Do not report this work as finished. You will be woken when it exits.", identify(&rows[0]))
	}
	names := make([]string, len(rows))
	for i := range rows {
		names[i] = fmt.Sprintf("%s (%q)", rows[i].ID, rows[i].Label)
	}
	return fmt.Sprintf("[harness] %d watches still running: %s. Do not report this work as finished. You will be woken when each exits.",
		len(rows), strings.Join(names, ", "))
}

// TurnEndNote is the operator-only status for a turn that ended with live watches, or "".
func (m *Manager) TurnEndNote(sessionID string) string {
	rows := m.liveRows(sessionID)
	if len(rows) == 0 {
		return ""
	}
	names := make([]string, len(rows))
	for i := range rows {
		names[i] = fmt.Sprintf("%s (%q)", rows[i].ID, rows[i].Label)
	}
	return fmt.Sprintf("[harness] turn ended with %d watch(es) open: %s. The session will be woken when they exit.",
		len(rows), strings.Join(names, ", "))
}

// finalTail captures a running watch's output for its terminal row.
func (m *Manager) finalTail(row *db.WatchJobRow) string {
	if t, ok := m.liveTask(row); ok {
		return tailOf(t.Combined)
	}
	return ""
}

// Cancel ends a running watch. The row becomes terminal before the kill signal, or
// the killed task would read as exit_nonzero. An agent's cancel is answered by its
// tool result; an operator's arrives outside any turn and wakes the session. The
// bool is false when another path had already ended the watch.
func (m *Manager) Cancel(sessionID, id, reason string, byOperator bool) (*db.WatchJobRow, bool, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, false, fmt.Errorf("reason is required")
	}
	row, err := m.owned(sessionID, id)
	if err != nil {
		return nil, false, err
	}
	if row.Status == db.WatchPending {
		return row, false, fmt.Errorf("watch %s is still starting", id)
	}
	if row.Status != db.WatchRunning {
		return row, false, nil
	}
	won, err := m.db.TransitionWatchJob(id, db.WatchRunning, db.WatchTransition{
		To: db.WatchFailed, EndReason: db.WatchReasonCancelled, OutputTail: m.finalTail(row), Note: reason,
	})
	if err != nil {
		return row, false, err
	}
	if !won {
		row, err = m.db.GetWatchJob(id)
		return row, false, err
	}
	m.cont.Cancel(row.ContinuationID)
	_ = m.bg.Kill(row.TaskID, false)
	if row, err = m.db.GetWatchJob(id); err != nil {
		return nil, true, err
	}
	if byOperator {
		m.deliver(row, Render(row), "")
	} else if _, err := m.db.SettleWatchWake(id, wakeToolResult); err != nil {
		log.Printf("watch %s: settle wake: %v", id, err)
	}
	m.notify(row.SessionID)
	return row, true, nil
}

// classify maps a fired job's task state onto a terminal transition.
func classify(t *bgtask.Task, found bool) db.WatchTransition {
	switch {
	case !found:
		return db.WatchTransition{To: db.WatchFailed, EndReason: db.WatchReasonWaitError, Note: "background task no longer tracked"}
	case t.Status == bgtask.StatusRunning:
		// Only the deadline fires a job while its task is still running.
		return db.WatchTransition{To: db.WatchExpired, EndReason: db.WatchReasonDeadline, OutputTail: tailOf(t.Combined)}
	case t.ExitCode == nil:
		return db.WatchTransition{To: db.WatchFailed, EndReason: db.WatchReasonWaitError, OutputTail: tailOf(t.Combined), Note: t.Error}
	case *t.ExitCode == 0:
		return db.WatchTransition{To: db.WatchDone, EndReason: db.WatchReasonExitZero, ExitCode: t.ExitCode, OutputTail: tailOf(t.Combined)}
	default:
		return db.WatchTransition{To: db.WatchFailed, EndReason: db.WatchReasonExitNonzero, ExitCode: t.ExitCode, OutputTail: tailOf(t.Combined)}
	}
}

// OnFire handles a due continuation with source watch or watch_boot.
func (m *Manager) OnFire(j continuation.Job) {
	if m.stopped.Load() {
		return
	}
	m.armMu.Lock()
	row, err := m.db.GetWatchJob(j.WatchID)
	m.armMu.Unlock()
	if err != nil {
		log.Printf("watch %s: fire: %v", j.WatchID, err)
		m.cont.SetOutcome(j.ID, "dropped: "+err.Error())
		return
	}
	if j.Source == continuation.SourceWatchBoot {
		if row.WakeOutcome == "" {
			m.deliver(row, j.Prompt, j.ID)
		}
		return
	}
	if row.Status != db.WatchRunning {
		m.cont.SetOutcome(j.ID, "dropped: watch already "+row.Status)
		return
	}
	t, found := m.bg.Get(row.TaskID)
	tr := classify(t, found)
	won, err := m.db.TransitionWatchJob(row.ID, db.WatchRunning, tr)
	if err != nil {
		// The row stays running, so the next boot reports it as lost.
		log.Printf("watch %s: finalize: %v", row.ID, err)
		m.cont.SetOutcome(j.ID, "dropped: "+err.Error())
		return
	}
	if !won {
		m.cont.SetOutcome(j.ID, "dropped: watch already finalized")
		return
	}
	if tr.To == db.WatchExpired {
		_ = m.bg.Kill(row.TaskID, false)
	}
	if row, err = m.db.GetWatchJob(row.ID); err != nil {
		log.Printf("watch %s: reread: %v", j.WatchID, err)
		return
	}
	m.notify(row.SessionID)
	m.deliver(row, Render(row), j.ID)
}

// deliver offers the wake and settles it, retrying a busy session for as long as it
// takes. Unlike plain continuations there is no busy cap (ADR-0041 R2).
func (m *Manager) deliver(row *db.WatchJobRow, text, jobID string) {
	if m.stopped.Load() {
		return
	}
	res := m.wake(row.SessionID, text)
	if m.settled(row, jobID, res) {
		return
	}
	go m.retry(row, text, jobID, res)
}

func (m *Manager) retry(row *db.WatchJobRow, text, jobID string, last WakeResult) {
	errs := 0
	if last.Status == WakeError {
		errs = 1
	}
	t := time.NewTicker(m.RetryInterval)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
		}
		res := m.wake(row.SessionID, text)
		if m.settled(row, jobID, res) {
			return
		}
		if res.Status != WakeError {
			continue
		}
		if errs++; errs >= maxWakeErrors {
			m.undeliverable(row, jobID, "load_error: "+res.Detail)
			return
		}
	}
}

// settled records a final wake result and reports whether the wake is over.
func (m *Manager) settled(row *db.WatchJobRow, jobID string, res WakeResult) bool {
	switch res.Status {
	case WakeStarted:
		if _, err := m.db.SettleWatchWake(row.ID, wakeStarted); err != nil {
			log.Printf("watch %s: settle wake: %v", row.ID, err)
		}
		m.cont.SetOutcome(jobID, wakeStarted)
		m.event(row.SessionID, "watch_fired", row.ID+" "+row.Status+"/"+row.EndReason)
		m.notify(row.SessionID)
		return true
	case WakeClosed:
		m.undeliverable(row, jobID, whyClosed)
		return true
	default:
		m.cont.SetOutcome(jobID, "waiting: session busy")
		return false
	}
}

// undeliverable records a wake that can never happen and tells the operator, once.
func (m *Manager) undeliverable(row *db.WatchJobRow, jobID, why string) {
	m.cont.SetOutcome(jobID, "dropped: "+why)
	won, err := m.db.SettleWatchWake(row.ID, wakeUndeliverable+why)
	if err != nil {
		log.Printf("watch %s: settle wake: %v", row.ID, err)
		return
	}
	if !won {
		return
	}
	m.event(row.SessionID, "watch_undelivered", row.ID+" "+why)
	if m.OnUndelivered != nil {
		m.OnUndelivered(*row, why)
	}
	m.notify(row.SessionID)
}

// UndeliveredMessage is the operator-facing text for a wake that could not be delivered.
func UndeliveredMessage(r db.WatchJobRow, why string) string {
	return fmt.Sprintf("%s ended (%s/%s) but its session could not be woken: %s.", identify(&r), r.Status, r.EndReason, why)
}

// OnSessionClose finalizes a closing session's watches. Call before the session's
// tasks are killed: the row must be terminal first, and no wake is possible.
func (m *Manager) OnSessionClose(sessionID string) {
	for _, row := range m.liveRows(sessionID) {
		if row.Status != db.WatchRunning {
			continue
		}
		won, err := m.db.TransitionWatchJob(row.ID, db.WatchRunning, db.WatchTransition{
			To: db.WatchFailed, EndReason: db.WatchReasonSessionClosed, OutputTail: m.finalTail(&row),
		})
		if err != nil {
			log.Printf("watch %s: session close: %v", row.ID, err)
			continue
		}
		if !won {
			continue
		}
		if fresh, err := m.db.GetWatchJob(row.ID); err == nil {
			row = *fresh
		}
		m.undeliverable(&row, row.ContinuationID, whyClosed)
	}
}

// OnSIGTERM moves running rows to lost before the harness kills their tasks, so the
// ticker cannot report the kill as a failure. Boot reconcile delivers these wakes.
func (m *Manager) OnSIGTERM() int {
	if m == nil {
		return 0
	}
	m.Stop()
	if !m.db.Writable() {
		return 0
	}
	n := 0
	for _, row := range m.liveRows("") {
		if row.Status != db.WatchRunning {
			continue
		}
		won, err := m.db.TransitionWatchJob(row.ID, db.WatchRunning, db.WatchTransition{
			To: db.WatchLost, EndReason: db.WatchReasonHarnessShutdown, OutputTail: m.finalTail(&row),
		})
		if err != nil {
			log.Printf("watch %s: shutdown: %v", row.ID, err)
		}
		if won {
			n++
		}
	}
	return n
}

// Reconcile runs once at boot, after continuations are wired. Live rows died with
// the last process and become lost; every terminal row whose wake never settled
// gets a new one-shot continuation, since the original died too. Repeat calls in
// one process schedule nothing further.
func (m *Manager) Reconcile() (int, error) {
	if m == nil || !m.db.Writable() {
		return 0, nil
	}
	live, err := m.db.ListLiveWatchJobs("")
	if err != nil {
		return 0, err
	}
	for i := range live {
		if _, err := m.db.TransitionWatchJob(live[i].ID, live[i].Status, db.WatchTransition{
			To: db.WatchLost, EndReason: db.WatchReasonHarnessCrash,
		}); err != nil {
			log.Printf("watch %s: reconcile: %v", live[i].ID, err)
		}
	}
	rows, err := m.db.ListUnsettledWatchJobs()
	if err != nil {
		return 0, err
	}
	woken := 0
	for i := range rows {
		row := &rows[i]
		m.mu.Lock()
		seen := m.reconciled[row.ID]
		m.reconciled[row.ID] = true
		m.mu.Unlock()
		if seen {
			continue
		}
		if m.SessionClosed != nil && m.SessionClosed(row.SessionID) {
			m.undeliverable(row, "", whyClosed)
			continue
		}
		if _, err := m.cont.ScheduleWatch(row.SessionID, Render(row), bootDelaySec, "", row.Label,
			continuation.SourceWatchBoot, row.ID); err != nil {
			log.Printf("watch %s: reconcile wake: %v", row.ID, err)
			continue
		}
		woken++
	}
	return woken, nil
}
