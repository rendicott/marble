package db

import (
	"database/sql"
	"errors"
	"testing"
)

func openWatchDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func seedWatch(t *testing.T, d *DB, id, status string) {
	t.Helper()
	if err := d.InsertWatchJob(WatchJobRow{
		ID: id, SessionID: "s1", Label: "lbl", Command: "true", Status: WatchPending,
		CreatedAt: UTCNow(), DeadlineAt: UTCNow(),
	}); err != nil {
		t.Fatal(err)
	}
	if status == WatchPending {
		return
	}
	if ok, err := d.TransitionWatchJob(id, WatchPending, WatchTransition{To: WatchRunning, TaskID: "t", ContinuationID: "c"}); !ok || err != nil {
		t.Fatalf("to running: %v %v", ok, err)
	}
	if status == WatchRunning {
		return
	}
	reasons := map[string]string{
		WatchDone: WatchReasonExitZero, WatchFailed: WatchReasonExitNonzero,
		WatchLost: WatchReasonHarnessCrash, WatchExpired: WatchReasonDeadline,
	}
	if ok, err := d.TransitionWatchJob(id, WatchRunning, WatchTransition{To: status, EndReason: reasons[status]}); !ok || err != nil {
		t.Fatalf("to %s: %v %v", status, ok, err)
	}
}

func TestMigrateV14toV15(t *testing.T) {
	root := t.TempDir()
	d, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL.Exec(`DROP TABLE watch_jobs`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL.Exec(`UPDATE schema_meta SET schema_version = 14 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	d.Close()

	d2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if d2.Mode != ModeNormal {
		t.Fatalf("mode %s: %s", d2.Mode, d2.Reason)
	}
	if got := schemaVersion(t, d2); got != 15 {
		t.Fatalf("schema %d", got)
	}
	var idx int
	if err := d2.SQL.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_watch_jobs_session'`).Scan(&idx); err != nil || idx != 1 {
		t.Fatalf("index: %d %v", idx, err)
	}
	rows, err := d2.SQL.Query(`SELECT name FROM pragma_table_info('watch_jobs')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		have[n] = true
	}
	for _, c := range []string{"id", "session_id", "label", "command", "cwd", "prompt", "task_id", "continuation_id",
		"status", "end_reason", "exit_code", "output_tail", "created_at", "deadline_at", "ended_at", "wake_outcome", "note"} {
		if !have[c] {
			t.Errorf("missing column %s", c)
		}
	}
	if len(have) != 17 {
		t.Errorf("columns: %v", have)
	}
}

func TestWatchRoundTrip(t *testing.T) {
	d := openWatchDB(t)
	seedWatch(t, d, "w1", WatchRunning)
	code := 3
	ok, err := d.TransitionWatchJob("w1", WatchRunning, WatchTransition{
		To: WatchFailed, EndReason: WatchReasonExitNonzero, ExitCode: &code, OutputTail: "boom", Note: "n",
	})
	if !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	got, err := d.GetWatchJob("w1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != WatchFailed || got.EndReason != WatchReasonExitNonzero || got.ExitCode == nil || *got.ExitCode != 3 ||
		got.OutputTail != "boom" || got.Note != "n" || got.EndedAt == "" || got.TaskID != "t" || got.ContinuationID != "c" {
		t.Fatalf("%+v", got)
	}
	if _, err := d.GetWatchJob("nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing row: %v", err)
	}
}

func TestWatchExitCodeZeroIsNotNull(t *testing.T) {
	d := openWatchDB(t)
	seedWatch(t, d, "w1", WatchRunning)
	code := 0
	if ok, err := d.TransitionWatchJob("w1", WatchRunning, WatchTransition{To: WatchDone, EndReason: WatchReasonExitZero, ExitCode: &code}); !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	got, _ := d.GetWatchJob("w1")
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("exit code %v", got.ExitCode)
	}
}

func TestWatchCASLoserAffectsNothing(t *testing.T) {
	d := openWatchDB(t)
	seedWatch(t, d, "w1", WatchDone)
	ok, err := d.TransitionWatchJob("w1", WatchRunning, WatchTransition{To: WatchFailed, EndReason: WatchReasonCancelled, Note: "late"})
	if ok || err != nil {
		t.Fatalf("loser should see false,nil: %v %v", ok, err)
	}
	got, _ := d.GetWatchJob("w1")
	if got.Status != WatchDone || got.EndReason != WatchReasonExitZero || got.Note != "" {
		t.Fatalf("loser wrote: %+v", got)
	}
}

func TestWatchIllegalEdgeRejected(t *testing.T) {
	d := openWatchDB(t)
	seedWatch(t, d, "w1", WatchPending)
	ok, err := d.TransitionWatchJob("w1", WatchPending, WatchTransition{To: WatchDone, EndReason: WatchReasonExitZero})
	if ok || err == nil {
		t.Fatalf("illegal edge: %v %v", ok, err)
	}
	if got, _ := d.GetWatchJob("w1"); got.Status != WatchPending {
		t.Fatalf("status %s", got.Status)
	}
}

func TestSettleWatchWakeOnce(t *testing.T) {
	d := openWatchDB(t)
	seedWatch(t, d, "w1", WatchDone)
	if ok, err := d.SettleWatchWake("w1", "started"); !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	if ok, err := d.SettleWatchWake("w1", "undeliverable:x"); ok || err != nil {
		t.Fatalf("second settle: %v %v", ok, err)
	}
	if got, _ := d.GetWatchJob("w1"); got.WakeOutcome != "started" {
		t.Fatalf("%q", got.WakeOutcome)
	}
}

func TestWatchListQueries(t *testing.T) {
	d := openWatchDB(t)
	seedWatch(t, d, "a-pending", WatchPending)
	seedWatch(t, d, "b-running", WatchRunning)
	seedWatch(t, d, "c-done", WatchDone)
	seedWatch(t, d, "d-settled", WatchFailed)
	if _, err := d.SettleWatchWake("d-settled", "started"); err != nil {
		t.Fatal(err)
	}
	live, err := d.ListLiveWatchJobs("s1")
	if err != nil || len(live) != 2 || live[0].ID != "a-pending" || live[1].ID != "b-running" {
		t.Fatalf("live: %+v %v", live, err)
	}
	if other, _ := d.ListLiveWatchJobs("other"); len(other) != 0 {
		t.Fatalf("other session: %+v", other)
	}
	if all, _ := d.ListLiveWatchJobs(""); len(all) != 2 {
		t.Fatalf("all: %+v", all)
	}
	unsettled, err := d.ListUnsettledWatchJobs()
	if err != nil || len(unsettled) != 1 || unsettled[0].ID != "c-done" {
		t.Fatalf("unsettled: %+v %v", unsettled, err)
	}
	all, err := d.ListWatchJobs("s1", 0)
	if err != nil || len(all) != 4 {
		t.Fatalf("list: %+v %v", all, err)
	}
}

func TestWatchLimpRefuses(t *testing.T) {
	d := &DB{Mode: ModeLimp}
	if err := d.InsertWatchJob(WatchJobRow{ID: "x", Status: WatchPending}); err == nil {
		t.Fatal("limp insert should error")
	}
	if _, err := d.ListLiveWatchJobs("s"); err == nil {
		t.Fatal("limp list should error")
	}
}
