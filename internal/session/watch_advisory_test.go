package session

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/model"
	"github.com/rendicott/marble/internal/tools"
	"github.com/rendicott/marble/internal/watch"
)

// runCapturingPrompt runs one turn and returns what the model was actually sent.
func runCapturingPrompt(t *testing.T, sessionID string, m *watch.Manager) (string, *Session) {
	t.Helper()
	var mu sync.Mutex
	var sent []model.ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req model.ChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode: %v", err)
		}
		mu.Lock()
		sent = append(sent, req)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`))
	}))
	defer srv.Close()

	s := newSession(sessionID, "watch")
	s.history = append(s.history, model.Message{Role: "user", Content: model.ContentFromText("hello")})
	s.ui = []Message{{ID: "m-user", Role: "user", Content: "hello", CreatedAt: time.Now()}}
	r := overageRunner(srv.URL, 100000, 1000, 0)
	r.Tools = &tools.Registry{Watch: m}
	r.runTurn(s)

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("model calls %d", len(sent))
	}
	var b strings.Builder
	for _, msg := range sent[0].Messages {
		b.WriteString(msg.Content.PlainText())
		b.WriteString("\n")
	}
	return b.String(), s
}

func seedLiveWatch(t *testing.T, d *db.DB, id, sessionID string) {
	t.Helper()
	now := db.UTCNow()
	if err := d.InsertWatchJob(db.WatchJobRow{
		ID: id, SessionID: sessionID, Label: "CI v1", Command: "gh run watch", Status: db.WatchPending, CreatedAt: now, DeadlineAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.TransitionWatchJob(id, db.WatchPending, db.WatchTransition{To: db.WatchRunning, TaskID: "t"}); !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
}

const wantWatchLine = `[harness] watch w_7c31 ("CI v1") still running. Do not report this work as finished. You will be woken when it exits.`

func TestWatchAdvisoryInPromptForLiveWatch(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	seedLiveWatch(t, d, "w_7c31", "sess1")
	prompt, s := runCapturingPrompt(t, "sess1", watch.New(d, nil, nil, nil))
	if strings.Count(prompt, wantWatchLine) != 1 {
		t.Fatalf("expected the advisory once in:\n%s", prompt)
	}
	// Ephemeral: it must not land in the stored history.
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.history {
		if strings.Contains(m.Content.PlainText(), "still running") {
			t.Fatalf("advisory leaked into history: %+v", m)
		}
	}
}

func TestWatchAdvisoryAbsentWithoutLiveWatch(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	seedLiveWatch(t, d, "w_other", "another-session")
	seedLiveWatch(t, d, "w_ended", "sess2")
	if ok, err := d.TransitionWatchJob("w_ended", db.WatchRunning, db.WatchTransition{To: db.WatchDone, EndReason: db.WatchReasonExitZero}); !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	if prompt, _ := runCapturingPrompt(t, "sess2", watch.New(d, nil, nil, nil)); strings.Contains(prompt, "still running") {
		t.Fatalf("unexpected advisory in:\n%s", prompt)
	}
}

func TestWatchAdvisoryNilManager(t *testing.T) {
	if prompt, _ := runCapturingPrompt(t, "sess3", nil); strings.Contains(prompt, "still running") {
		t.Fatalf("unexpected advisory in:\n%s", prompt)
	}
}

// Channel (c) reaches the operator only, on the final-answer branch of a live watch.
func TestTurnEndNoteOnlyWithLiveWatch(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	seedLiveWatch(t, d, "w_note", "sess4")
	m := watch.New(d, nil, nil, nil)
	hasNote := func(s *Session) bool {
		for _, st := range s.Progress().Steps {
			if st.Kind == "advisory" && strings.Contains(st.Detail, "turn ended with") {
				return true
			}
		}
		return false
	}
	if _, s := runCapturingPrompt(t, "sess4", m); !hasNote(s) {
		t.Fatal("no turn-end note for a live watch")
	}
	if _, s := runCapturingPrompt(t, "sess5", m); hasNote(s) {
		t.Fatal("note without a live watch")
	}
}
