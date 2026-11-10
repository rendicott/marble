package watch

import (
	"testing"

	"github.com/rendicott/marble/internal/db"
)

// /api/health must be able to say how many watches are live without walking the
// table. watch_count is the chip set (pending+running); the breakdown is included
// so an operator can see an old lost/expired row instead of guessing.
func TestHealthCountsLiveWatches(t *testing.T) {
	if got := (*Manager)(nil).Health(); got["watch_enabled"] != false || got["watch_count"] != 0 {
		t.Fatalf("nil manager must report disabled with a zero count, not an omitted key: %+v", got)
	}

	f := newFixture(t)
	empty := f.m.Health()
	if empty["watch_enabled"] != true || empty["watch_db_writable"] != true {
		t.Fatalf("fixture must report an enabled, writable watch manager: %+v", empty)
	}
	if empty["watch_count"] != 0 || empty["watch_total"] != 0 {
		t.Fatalf("no rows yet: want 0/0, got %v/%v", empty["watch_count"], empty["watch_total"])
	}

	// Two live rows, one of each live status, plus one of every terminal status.
	f.seed("w_pend", "s", db.WatchPending, "")
	f.seed("w_run", "s", db.WatchRunning, "")
	f.seed("w_done", "s", db.WatchDone, db.WatchReasonExitZero)
	f.seed("w_fail", "s", db.WatchFailed, db.WatchReasonExitNonzero)
	f.seed("w_lost", "s", db.WatchLost, db.WatchReasonHarnessCrash)
	f.seed("w_exp", "s", db.WatchExpired, db.WatchReasonDeadline)

	got := f.m.Health()
	if got["watch_count"] != 2 {
		t.Fatalf("watch_count must be the live set (pending+running): %v", got["watch_count"])
	}
	if got["watch_total"] != 6 {
		t.Fatalf("watch_total must count every row: %v", got["watch_total"])
	}
	by, ok := got["watch_by_status"].(map[string]int)
	if !ok {
		t.Fatalf("watch_by_status must be a map[string]int, got %T", got["watch_by_status"])
	}
	for status, want := range map[string]int{
		db.WatchPending: 1, db.WatchRunning: 1, db.WatchDone: 1,
		db.WatchFailed: 1, db.WatchLost: 1, db.WatchExpired: 1,
	} {
		if by[status] != want {
			t.Fatalf("watch_by_status[%s] = %d, want %d (%v)", status, by[status], want, by)
		}
	}

	// A stopped manager still reports counts; the flag is what tells the operator why.
	f.m.Stop()
	if got := f.m.Health(); got["watch_stopped"] != true {
		t.Fatalf("stopped manager must say so: %+v", got)
	}
}
