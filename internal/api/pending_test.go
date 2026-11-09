package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rendicott/marble/internal/bgtask"
	"github.com/rendicott/marble/internal/continuation"
	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/tools"
	"github.com/rendicott/marble/internal/watch"
)

func TestPendingEmptyListsAreArrays(t *testing.T) {
	s := &Server{}
	b, _ := json.Marshal(s.pendingFor("nope"))
	for _, k := range []string{`"continuations":[]`, `"bg_tasks":[]`, `"agent_tasks":[]`, `"watches":[]`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("UI expects arrays, got %s", b)
		}
	}
}

func TestPendingListsWatchesAndCancelWakes(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	bg := bgtask.New(nil)
	defer bg.KillAllRunning()
	cont := continuation.New(nil, nil)
	defer cont.Stop()
	var wakes []string
	m := watch.New(d, bg, cont, func(_, text string) watch.WakeResult {
		wakes = append(wakes, text)
		return watch.WakeResult{Status: watch.WakeStarted}
	})
	row, err := m.Create("s1", watch.CreateArgs{Command: "sleep 30", Label: "chip"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Tools: &tools.Registry{Watch: m}}
	view := s.pendingFor("s1")
	if len(view.Watches) != 1 || view.Watches[0].ID != row.ID || view.Watches[0].Status != db.WatchRunning {
		t.Fatalf("%+v", view.Watches)
	}
	if len(view.Continuations) != 0 || len(view.BgTasks) != 0 {
		t.Fatalf("a watch is one chip: %+v %+v", view.Continuations, view.BgTasks)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	s.handleWatchCancel(rec, req, "s1", row.ID)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`))
	s.handleWatchCancel(rec, req, "other-session", row.ID)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-session cancel: %d %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`))
	s.handleWatchCancel(rec, req, "s1", row.ID)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cancelled": true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got, _ := d.GetWatchJob(row.ID)
	if got.Status != db.WatchFailed || got.EndReason != db.WatchReasonCancelled || got.WakeOutcome != "started" {
		t.Fatalf("%+v", got)
	}
	if len(wakes) != 1 || !strings.Contains(wakes[0], "cancelled by the operator") {
		t.Fatalf("UI cancel must wake the session: %v", wakes)
	}
}
