package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rendicott/marble/internal/bgtask"
	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/continuation"
	"github.com/rendicott/marble/internal/db"
)

type wakeRec struct {
	session, text string
}

// fixture wires real bgtask and continuation managers to a temp DB, with a
// captured wake function in place of Runner.PostContinuation.
type fixture struct {
	t    *testing.T
	d    *db.DB
	bg   *bgtask.Manager
	cont *continuation.Manager
	m    *Manager

	mu     sync.Mutex
	wakes  []wakeRec
	result func(n int) WakeResult
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, d: d, bg: bgtask.New(nil)}
	t.Cleanup(func() {
		f.m.Stop()
		f.bg.KillAllRunning()
		f.cont.Stop()
		d.Close()
	})
	f.attach()
	return f
}

// attach builds fresh managers over the same DB, as a restarted harness would.
func (f *fixture) attach() {
	var m *Manager
	f.cont = continuation.New(func(j continuation.Job) { m.OnFire(j) }, func(id string) bool {
		running, ok := f.bg.IsRunning(id)
		return ok && !running
	})
	m = New(f.d, f.bg, f.cont, f.wake)
	m.RetryInterval = 20 * time.Millisecond
	f.m = m
}

func (f *fixture) wake(session, text string) WakeResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.result != nil {
		if r := f.result(len(f.wakes)); r.Status != WakeStarted {
			return r
		}
	}
	f.wakes = append(f.wakes, wakeRec{session, text})
	return WakeResult{Status: WakeStarted}
}

func (f *fixture) setResult(fn func(n int) WakeResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.result = fn
}

func (f *fixture) captured() []wakeRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]wakeRec(nil), f.wakes...)
}

func (f *fixture) row(id string) *db.WatchJobRow {
	f.t.Helper()
	r, err := f.d.GetWatchJob(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) until(what string, cond func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			f.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *fixture) settled(id string) {
	f.t.Helper()
	f.until("wake_outcome on "+id, func() bool { return f.row(id).WakeOutcome != "" })
}

func (f *fixture) create(session, command string, deadline int) *db.WatchJobRow {
	f.t.Helper()
	r, err := f.m.Create(session, CreateArgs{Command: command, Label: "lbl", DeadlineSec: deadline})
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

// seed inserts a row and drives it to status through legal transitions.
func (f *fixture) seed(id, session, status, reason string) {
	f.t.Helper()
	now := db.UTCNow()
	if err := f.d.InsertWatchJob(db.WatchJobRow{
		ID: id, SessionID: session, Label: "lbl", Command: "cmd", Status: db.WatchPending, CreatedAt: now, DeadlineAt: now,
	}); err != nil {
		f.t.Fatal(err)
	}
	if status == db.WatchPending {
		return
	}
	if reason == db.WatchReasonStartError {
		if ok, err := f.d.TransitionWatchJob(id, db.WatchPending, db.WatchTransition{To: status, EndReason: reason}); !ok || err != nil {
			f.t.Fatalf("seed: %v %v", ok, err)
		}
		return
	}
	if ok, err := f.d.TransitionWatchJob(id, db.WatchPending, db.WatchTransition{To: db.WatchRunning, TaskID: "t"}); !ok || err != nil {
		f.t.Fatalf("seed running: %v %v", ok, err)
	}
	if status != db.WatchRunning {
		if ok, err := f.d.TransitionWatchJob(id, db.WatchRunning, db.WatchTransition{To: status, EndReason: reason}); !ok || err != nil {
			f.t.Fatalf("seed %s: %v %v", status, ok, err)
		}
	}
}

// ── state machine ─────────────────────────────────────────────────────────

var (
	allStatuses = []string{db.WatchPending, db.WatchRunning, db.WatchDone, db.WatchFailed, db.WatchLost, db.WatchExpired}
	allReasons  = []string{"", db.WatchReasonStartError, db.WatchReasonExitZero, db.WatchReasonExitNonzero, db.WatchReasonWaitError,
		db.WatchReasonCancelled, db.WatchReasonSessionClosed, db.WatchReasonDeadline, db.WatchReasonHarnessShutdown, db.WatchReasonHarnessCrash}
	// The ADR-0041 table, restated independently of the implementation.
	legalEdges = map[[3]string]bool{
		{db.WatchPending, db.WatchRunning, ""}:                         true,
		{db.WatchPending, db.WatchFailed, db.WatchReasonStartError}:    true,
		{db.WatchPending, db.WatchLost, db.WatchReasonHarnessCrash}:    true,
		{db.WatchRunning, db.WatchDone, db.WatchReasonExitZero}:        true,
		{db.WatchRunning, db.WatchFailed, db.WatchReasonExitNonzero}:   true,
		{db.WatchRunning, db.WatchFailed, db.WatchReasonWaitError}:     true,
		{db.WatchRunning, db.WatchFailed, db.WatchReasonCancelled}:     true,
		{db.WatchRunning, db.WatchFailed, db.WatchReasonSessionClosed}: true,
		{db.WatchRunning, db.WatchExpired, db.WatchReasonDeadline}:     true,
		{db.WatchRunning, db.WatchLost, db.WatchReasonHarnessShutdown}: true,
		{db.WatchRunning, db.WatchLost, db.WatchReasonHarnessCrash}:    true,
	}
)

func seedReason(status string) string {
	switch status {
	case db.WatchDone:
		return db.WatchReasonExitZero
	case db.WatchFailed:
		return db.WatchReasonExitNonzero
	case db.WatchLost:
		return db.WatchReasonHarnessCrash
	case db.WatchExpired:
		return db.WatchReasonDeadline
	}
	return ""
}

func TestLegalTransitionsSucceed(t *testing.T) {
	f := newFixture(t)
	i := 0
	for e := range legalEdges {
		i++
		id := fmt.Sprintf("legal-%d", i)
		f.seed(id, "s", e[0], seedReason(e[0]))
		ok, err := f.d.TransitionWatchJob(id, e[0], db.WatchTransition{To: e[1], EndReason: e[2]})
		if err != nil || !ok {
			t.Errorf("%v: ok=%v err=%v", e, ok, err)
			continue
		}
		if r := f.row(id); r.Status != e[1] || r.EndReason != e[2] {
			t.Errorf("%v: row %+v", e, r)
		}
	}
}

func TestIllegalTransitionsAffectZeroRows(t *testing.T) {
	f := newFixture(t)
	i := 0
	for _, from := range allStatuses {
		for _, to := range allStatuses {
			for _, reason := range allReasons {
				if legalEdges[[3]string{from, to, reason}] {
					continue
				}
				i++
				id := fmt.Sprintf("illegal-%d", i)
				f.seed(id, "s", from, seedReason(from))
				before := *f.row(id)
				ok, _ := f.d.TransitionWatchJob(id, from, db.WatchTransition{To: to, EndReason: reason, Note: "x"})
				if ok {
					t.Errorf("%s -> %s (%q) was allowed", from, to, reason)
				}
				if after := *f.row(id); after.Status != before.Status || after.EndReason != before.EndReason || after.Note != "" {
					t.Errorf("%s -> %s (%q) changed the row: %+v", from, to, reason, after)
				}
			}
		}
	}
}

func TestStaleFromStatusAffectsZeroRows(t *testing.T) {
	f := newFixture(t)
	for _, terminal := range []string{db.WatchDone, db.WatchFailed, db.WatchLost, db.WatchExpired} {
		id := "stale-" + terminal
		f.seed(id, "s", terminal, seedReason(terminal))
		ok, err := f.d.TransitionWatchJob(id, db.WatchRunning, db.WatchTransition{To: db.WatchFailed, EndReason: db.WatchReasonCancelled})
		if ok || err != nil {
			t.Errorf("%s: ok=%v err=%v", terminal, ok, err)
		}
		if r := f.row(id); r.Status != terminal || r.EndReason != seedReason(terminal) {
			t.Errorf("%s: row %+v", terminal, r)
		}
	}
}

// The cancel wins the CAS, then bgtask reports the kill as exited/-1. The row must
// still say cancelled; if the kill came first it would read as exit_nonzero.
func TestCancelBeatsTaskExit(t *testing.T) {
	f := newFixture(t)
	r := f.create("s", "sleep 30", 60)
	got, won, err := f.m.Cancel("s", r.ID, "not needed", false)
	if err != nil || !won {
		t.Fatalf("won=%v err=%v", won, err)
	}
	if got.Status != db.WatchFailed || got.EndReason != db.WatchReasonCancelled || got.Note != "not needed" {
		t.Fatalf("%+v", got)
	}
	f.until("task exit", func() bool {
		running, _ := f.bg.IsRunning(r.TaskID)
		return !running
	})
	task, _ := f.bg.Get(r.TaskID)
	if task.Status != bgtask.StatusExited || task.ExitCode == nil || *task.ExitCode != -1 {
		t.Fatalf("expected bgtask exited/-1, got %s %v", task.Status, task.ExitCode)
	}
	// Past a ticker round, the cancelled continuation must not have fired.
	time.Sleep(700 * time.Millisecond)
	final := f.row(r.ID)
	if final.Status != db.WatchFailed || final.EndReason != db.WatchReasonCancelled || final.ExitCode != nil {
		t.Fatalf("%+v", final)
	}
	if n := len(f.captured()); n != 0 {
		t.Fatalf("agent cancel woke the session %d times", n)
	}
	if final.WakeOutcome != wakeToolResult {
		t.Fatalf("wake_outcome %q", final.WakeOutcome)
	}
}

// Whichever of cancel and task exit wins, there is one terminal state and at most one wake.
func TestCancelRacingTaskExit(t *testing.T) {
	f := newFixture(t)
	cancelled, exited := 0, 0
	for i := 0; i < 12; i++ {
		r := f.create("s", "true", 60)
		// Stagger across the 500ms ticker so both orders occur.
		time.Sleep(time.Duration(i*45) * time.Millisecond)
		_, won, err := f.m.Cancel("s", r.ID, "race", false)
		if err != nil {
			t.Fatal(err)
		}
		f.settled(r.ID)
		final := f.row(r.ID)
		if !db.WatchStatusTerminal(final.Status) {
			t.Fatalf("not terminal: %+v", final)
		}
		switch {
		case won:
			cancelled++
			if final.EndReason != db.WatchReasonCancelled {
				t.Fatalf("cancel won but %+v", final)
			}
		default:
			exited++
			if final.Status != db.WatchDone || final.EndReason != db.WatchReasonExitZero {
				t.Fatalf("exit won but %+v", final)
			}
		}
	}
	if n := len(f.captured()); n != exited {
		t.Fatalf("wakes %d, want one per exit win (%d); cancelled=%d", n, exited, cancelled)
	}
}

func TestConcurrentCASHasOneWinner(t *testing.T) {
	f := newFixture(t)
	f.seed("w", "s", db.WatchRunning, "")
	reasons := []db.WatchTransition{
		{To: db.WatchFailed, EndReason: db.WatchReasonCancelled},
		{To: db.WatchDone, EndReason: db.WatchReasonExitZero},
		{To: db.WatchExpired, EndReason: db.WatchReasonDeadline},
		{To: db.WatchLost, EndReason: db.WatchReasonHarnessShutdown},
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for _, tr := range reasons {
		tr := tr
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := f.d.TransitionWatchJob("w", db.WatchRunning, tr)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("winners %d", wins)
	}
}

// ── reconcile ─────────────────────────────────────────────────────────────

func TestReconcileLiveRowsBecomeLostAndWakeOnce(t *testing.T) {
	f := newFixture(t)
	f.seed("p", "s", db.WatchPending, "")
	f.seed("r", "s", db.WatchRunning, "")
	if n, err := f.m.Reconcile(); err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	for _, id := range []string{"p", "r"} {
		if r := f.row(id); r.Status != db.WatchLost || r.EndReason != db.WatchReasonHarnessCrash {
			t.Fatalf("%+v", r)
		}
		f.settled(id)
		if r := f.row(id); r.WakeOutcome != wakeStarted {
			t.Fatalf("%+v", r)
		}
	}
	wakes := f.captured()
	if len(wakes) != 2 {
		t.Fatalf("wakes %d", len(wakes))
	}
	if !strings.Contains(wakes[0].text, "may still be running unsupervised") {
		t.Fatalf("%s", wakes[0].text)
	}
}

func TestReconcileRedeliversOnlyUnsettled(t *testing.T) {
	f := newFixture(t)
	f.seed("unsettled", "s", db.WatchDone, db.WatchReasonExitZero)
	f.seed("settled", "s", db.WatchFailed, db.WatchReasonExitNonzero)
	if _, err := f.d.SettleWatchWake("settled", "started"); err != nil {
		t.Fatal(err)
	}
	if n, err := f.m.Reconcile(); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	f.settled("unsettled")
	wakes := f.captured()
	if len(wakes) != 1 || !strings.Contains(wakes[0].text, "watch unsettled") {
		t.Fatalf("%+v", wakes)
	}
}

func TestReconcileClosedSessionIsUndeliverable(t *testing.T) {
	f := newFixture(t)
	f.seed("closed", "gone", db.WatchRunning, "")
	f.m.SessionClosed = func(sid string) bool { return sid == "gone" }
	var undelivered []string
	f.m.OnUndelivered = func(r db.WatchJobRow, why string) { undelivered = append(undelivered, r.ID+":"+why) }
	if n, err := f.m.Reconcile(); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	r := f.row("closed")
	if r.Status != db.WatchLost || r.WakeOutcome != "undeliverable:session_closed" {
		t.Fatalf("%+v", r)
	}
	if len(undelivered) != 1 || undelivered[0] != "closed:session_closed" {
		t.Fatalf("%v", undelivered)
	}
	f.attach()
	f.m.SessionClosed = func(string) bool { return true }
	if n, _ := f.m.Reconcile(); n != 0 {
		t.Fatalf("retried: %d", n)
	}
	time.Sleep(1200 * time.Millisecond)
	if n := len(f.captured()); n != 0 {
		t.Fatalf("wakes %d", n)
	}
}

func TestReconcileTwiceDoesNotDoubleWake(t *testing.T) {
	f := newFixture(t)
	f.seed("r", "s", db.WatchRunning, "")
	f.seed("d", "s", db.WatchDone, db.WatchReasonExitZero)
	if n, err := f.m.Reconcile(); err != nil || n != 2 {
		t.Fatalf("first: n=%d err=%v", n, err)
	}
	if n, err := f.m.Reconcile(); err != nil || n != 0 {
		t.Fatalf("second, before the wakes fire: n=%d err=%v", n, err)
	}
	f.settled("r")
	f.settled("d")
	if n, err := f.m.Reconcile(); err != nil || n != 0 {
		t.Fatalf("third, after the wakes: n=%d err=%v", n, err)
	}
	time.Sleep(1200 * time.Millisecond)
	if n := len(f.captured()); n != 2 {
		t.Fatalf("wakes %d, want 2", n)
	}
}

// A process that died before delivering the wake leaves it unsettled; the next one delivers it.
func TestReconcileAfterRestartRedelivers(t *testing.T) {
	f := newFixture(t)
	f.seed("d", "s", db.WatchDone, db.WatchReasonExitZero)
	f.setResult(func(int) WakeResult { return WakeResult{Status: WakeBusy} })
	if _, err := f.m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	f.until("boot job to fire", func() bool {
		for _, j := range f.cont.List("s") {
			if j.Fired {
				return true
			}
		}
		return false
	})
	f.m.Stop()
	f.cont.Stop()
	if r := f.row("d"); r.WakeOutcome != "" {
		t.Fatalf("settled while busy: %+v", r)
	}
	f.setResult(nil)
	f.attach()
	if n, err := f.m.Reconcile(); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	f.settled("d")
	if n := len(f.captured()); n != 1 {
		t.Fatalf("wakes %d", n)
	}
}

// ── SIGTERM and session close ─────────────────────────────────────────────

func TestSIGTERMMarksLostAndDoesNotWake(t *testing.T) {
	f := newFixture(t)
	r := f.create("s", "echo partial; sleep 30", 60)
	f.until("output", func() bool {
		task, _ := f.bg.Get(r.TaskID)
		return strings.Contains(task.Combined, "partial")
	})
	if n := f.m.OnSIGTERM(); n != 1 {
		t.Fatalf("marked %d", n)
	}
	f.bg.KillAllRunning()
	time.Sleep(1200 * time.Millisecond)
	if n := len(f.captured()); n != 0 {
		t.Fatalf("shutdown woke the session %d times", n)
	}
	got := f.row(r.ID)
	if got.Status != db.WatchLost || got.EndReason != db.WatchReasonHarnessShutdown || !strings.Contains(got.OutputTail, "partial") || got.WakeOutcome != "" {
		t.Fatalf("%+v", got)
	}
	f.attach()
	if n, err := f.m.Reconcile(); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	f.settled(r.ID)
	wakes := f.captured()
	if len(wakes) != 1 || !strings.Contains(wakes[0].text, "may be half-done") || !strings.Contains(wakes[0].text, "partial") {
		t.Fatalf("%+v", wakes)
	}
}

func TestSessionCloseFinalizesUndeliverable(t *testing.T) {
	f := newFixture(t)
	r := f.create("s", "sleep 30", 60)
	var why []string
	f.m.OnUndelivered = func(_ db.WatchJobRow, w string) { why = append(why, w) }
	f.m.OnSessionClose("s")
	f.bg.KillSession("s")
	f.cont.CancelSession("s")
	got := f.row(r.ID)
	if got.Status != db.WatchFailed || got.EndReason != db.WatchReasonSessionClosed || got.WakeOutcome != "undeliverable:session_closed" {
		t.Fatalf("%+v", got)
	}
	if len(why) != 1 {
		t.Fatalf("%v", why)
	}
	time.Sleep(700 * time.Millisecond)
	if n := len(f.captured()); n != 0 {
		t.Fatalf("wakes %d", n)
	}
}

// ── wake delivery ─────────────────────────────────────────────────────────

func TestBusySessionIsRetriedUntilDelivered(t *testing.T) {
	f := newFixture(t)
	attempts := 0
	f.setResult(func(int) WakeResult {
		attempts++
		if attempts <= 5 {
			return WakeResult{Status: WakeBusy}
		}
		return WakeResult{Status: WakeStarted}
	})
	r := f.create("s", "exit 1", 60)
	f.settled(r.ID)
	if got := f.row(r.ID); got.WakeOutcome != wakeStarted {
		t.Fatalf("%+v", got)
	}
	if n := len(f.captured()); n != 1 {
		t.Fatalf("wakes %d", n)
	}
}

func TestClosedAtDeliveryIsUndeliverable(t *testing.T) {
	f := newFixture(t)
	f.setResult(func(int) WakeResult { return WakeResult{Status: WakeClosed} })
	r := f.create("s", "true", 60)
	f.settled(r.ID)
	if got := f.row(r.ID); got.WakeOutcome != "undeliverable:session_closed" {
		t.Fatalf("%+v", got)
	}
}

func TestPersistentLoadErrorBecomesUndeliverable(t *testing.T) {
	f := newFixture(t)
	f.setResult(func(int) WakeResult { return WakeResult{Status: WakeError, Detail: "no such session"} })
	r := f.create("s", "true", 60)
	f.settled(r.ID)
	if got := f.row(r.ID); got.WakeOutcome != "undeliverable:load_error: no such session" {
		t.Fatalf("%+v", got)
	}
}

// ── seams ─────────────────────────────────────────────────────────────────

func TestCreateYieldsOneRowAndOneMatchingContinuation(t *testing.T) {
	f := newFixture(t)
	r := f.create("s", "sleep 30", 120)
	if r.Status != db.WatchRunning || r.TaskID == "" || r.ContinuationID == "" {
		t.Fatalf("%+v", r)
	}
	rows, err := f.d.ListWatchJobs("s", 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%+v %v", rows, err)
	}
	jobs := f.cont.List("s")
	if len(jobs) != 1 {
		t.Fatalf("%+v", jobs)
	}
	j := jobs[0]
	if j.WaitTask != r.TaskID || j.ID != r.ContinuationID || j.Source != continuation.SourceWatch || j.WatchID != r.ID {
		t.Fatalf("job %+v row %+v", j, r)
	}
	if left := time.Until(j.FireAt); left < 110*time.Second || left > 120*time.Second {
		t.Fatalf("deadline not applied: %s", left)
	}
}

func TestCreateRefusedWhenNotWritable(t *testing.T) {
	cont := continuation.New(nil, nil)
	defer cont.Stop()
	m := New(&db.DB{Mode: db.ModeLimp}, bgtask.New(nil), cont, func(string, string) WakeResult { return WakeResult{} })
	if _, err := m.Create("s", CreateArgs{Command: "true"}); err != ErrNotWritable {
		t.Fatalf("err %v", err)
	}
	if line := m.AdvisoryLine("s"); line != "" {
		t.Fatalf("limp advisory %q", line)
	}
}

func TestCreateValidation(t *testing.T) {
	f := newFixture(t)
	for name, a := range map[string]CreateArgs{
		"no command": {},
		"over 86400": {Command: "true", DeadlineSec: 86401},
	} {
		if _, err := f.m.Create("s", a); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if rows, _ := f.d.ListWatchJobs("s", 0); len(rows) != 0 {
		t.Fatalf("validation failure left rows: %+v", rows)
	}
}

func TestStartFailureRecordsStartError(t *testing.T) {
	f := newFixture(t)
	_, err := f.m.Create("s", CreateArgs{Command: "true", CWD: "/nonexistent/watch/dir", Label: "x"})
	if err == nil {
		t.Fatal("expected start error")
	}
	rows, _ := f.d.ListWatchJobs("s", 0)
	if len(rows) != 1 {
		t.Fatalf("%+v", rows)
	}
	r := rows[0]
	if r.Status != db.WatchFailed || r.EndReason != db.WatchReasonStartError || r.Note == "" {
		t.Fatalf("%+v", r)
	}
	if line := f.m.AdvisoryLine("s"); line != "" {
		t.Fatalf("failed start still advertised: %q", line)
	}
	if n, _ := f.m.Reconcile(); n != 0 {
		t.Fatal("start error was answered by the tool result; no boot wake is due")
	}
}

func TestOutputTailIsScrubbed(t *testing.T) {
	const secret = "sk-watch-test-secret-value"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.RelMemoryEnv), []byte("WATCH_TEST_API_KEY="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config.SetMemoryDirForEnv(dir)
	t.Cleanup(func() { config.SetMemoryDirForEnv(""); config.InvalidateEnvOverlay() })
	f := newFixture(t)
	r := f.create("s", "echo token="+secret+"; exit 1", 60)
	f.settled(r.ID)
	got := f.row(r.ID)
	if strings.Contains(got.OutputTail, secret) || !strings.Contains(got.OutputTail, "[secret:WATCH_TEST_API_KEY]") {
		t.Fatalf("tail %q", got.OutputTail)
	}
	if strings.Contains(got.Command, secret) {
		t.Fatalf("command %q", got.Command)
	}
	if text := f.captured()[0].text; strings.Contains(text, secret) {
		t.Fatalf("wake leaked: %s", text)
	}
}

func TestAdvisoryLine(t *testing.T) {
	f := newFixture(t)
	if got := f.m.AdvisoryLine("s"); got != "" {
		t.Fatalf("no watch: %q", got)
	}
	f.seed("w_a", "s", db.WatchRunning, "")
	want := `[harness] watch w_a ("lbl") still running. Do not report this work as finished. You will be woken when it exits.`
	if got := f.m.AdvisoryLine("s"); got != want {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(f.m.AdvisoryLine("other"), "w_a") {
		t.Fatal("leaked across sessions")
	}
	f.seed("w_b", "s", db.WatchPending, "")
	f.seed("w_c", "s", db.WatchRunning, "")
	got := f.m.AdvisoryLine("s")
	if strings.Contains(got, "\n") {
		t.Fatalf("must be one line: %q", got)
	}
	for _, id := range []string{"w_a", "w_b", "w_c"} {
		if !strings.Contains(got, id) {
			t.Fatalf("dropped %s: %q", id, got)
		}
	}
	for _, id := range []string{"w_a", "w_b", "w_c"} {
		st := f.row(id).Status
		if _, err := f.d.TransitionWatchJob(id, st, db.WatchTransition{To: db.WatchLost, EndReason: db.WatchReasonHarnessCrash}); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.m.AdvisoryLine("s"); got != "" {
		t.Fatalf("terminal rows still advertised: %q", got)
	}
}

func TestStatusShowsLiveTailAndListOmitsIt(t *testing.T) {
	f := newFixture(t)
	r := f.create("s", "echo live-line; sleep 30", 60)
	f.until("live tail", func() bool {
		v, err := f.m.Status("s", r.ID)
		return err == nil && strings.Contains(v.OutputTail, "live-line")
	})
	if _, err := f.m.Status("other", r.ID); err == nil {
		t.Fatal("status crossed sessions")
	}
	list, err := f.m.List("s")
	if err != nil || len(list) != 1 || list[0].OutputTail != "" || list[0].Status != db.WatchRunning {
		t.Fatalf("%+v %v", list, err)
	}
	if _, _, err := f.m.Cancel("other", r.ID, "x", false); err == nil {
		t.Fatal("cancel crossed sessions")
	}
	if _, _, err := f.m.Cancel("s", r.ID, " ", false); err == nil {
		t.Fatal("cancel without reason")
	}
}

func TestOperatorCancelWakesSession(t *testing.T) {
	f := newFixture(t)
	r := f.create("s", "sleep 30", 60)
	if _, won, err := f.m.Cancel("s", r.ID, "operator said stop", true); err != nil || !won {
		t.Fatalf("%v %v", won, err)
	}
	f.settled(r.ID)
	wakes := f.captured()
	if len(wakes) != 1 || !strings.Contains(wakes[0].text, "operator said stop") {
		t.Fatalf("%+v", wakes)
	}
	if got := f.row(r.ID); got.WakeOutcome != wakeStarted {
		t.Fatalf("%+v", got)
	}
}

func TestDeadlineExpiresAndKills(t *testing.T) {
	f := newFixture(t)
	r := f.create("s", "echo before-deadline; sleep 30", 1)
	f.settled(r.ID)
	got := f.row(r.ID)
	if got.Status != db.WatchExpired || got.EndReason != db.WatchReasonDeadline || !strings.Contains(got.OutputTail, "before-deadline") {
		t.Fatalf("%+v", got)
	}
	f.until("command killed", func() bool {
		running, _ := f.bg.IsRunning(r.TaskID)
		return !running
	})
	if text := f.captured()[0].text; !strings.Contains(text, "Only the local command was stopped") {
		t.Fatalf("%s", text)
	}
}

// A command that exits at once must not lose its wake to the arm window.
func TestInstantExitStillWakesOnce(t *testing.T) {
	f := newFixture(t)
	ids := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		ids = append(ids, f.create("s", "exit 0", 60).ID)
	}
	for _, id := range ids {
		f.settled(id)
		if got := f.row(id); got.Status != db.WatchDone {
			t.Fatalf("%+v", got)
		}
	}
	if n := len(f.captured()); n != len(ids) {
		t.Fatalf("wakes %d want %d", n, len(ids))
	}
}

// ── end to end ────────────────────────────────────────────────────────────

func TestEndToEndFailedCommand(t *testing.T) {
	f := newFixture(t)
	start := time.Now()
	r := f.create("s", "sleep 1; echo boom >&2; exit 3", 30)
	if time.Since(start) > 700*time.Millisecond {
		t.Fatalf("create blocked for %s", time.Since(start))
	}
	if r.Status != db.WatchRunning || f.row(r.ID).Status != db.WatchRunning {
		t.Fatalf("not running on return: %+v", r)
	}
	if n := len(f.captured()); n != 0 {
		t.Fatalf("woke early: %d", n)
	}
	f.settled(r.ID)
	got := f.row(r.ID)
	if got.Status != db.WatchFailed || got.EndReason != db.WatchReasonExitNonzero {
		t.Fatalf("%+v", got)
	}
	if got.ExitCode == nil || *got.ExitCode != 3 {
		t.Fatalf("exit code %v", got.ExitCode)
	}
	if !strings.Contains(got.OutputTail, "boom") {
		t.Fatalf("tail %q", got.OutputTail)
	}
	if got.WakeOutcome != "started" {
		t.Fatalf("wake_outcome %q", got.WakeOutcome)
	}
	time.Sleep(700 * time.Millisecond)
	wakes := f.captured()
	if len(wakes) != 1 {
		t.Fatalf("wakes %d", len(wakes))
	}
	if wakes[0].session != "s" || !strings.Contains(wakes[0].text, "exit 3") || !strings.Contains(wakes[0].text, "boom") {
		t.Fatalf("%+v", wakes[0])
	}
}
