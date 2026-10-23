package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/memory"
	"github.com/rendicott/marble/internal/session"
)

func TestOverageRescueDryRunDoesNotTouchFile(t *testing.T) {
	reg, store, d, id := overageRescueFixture(t)
	path := store.SessionPath(id)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Registry: reg}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/overage-rescue", strings.NewReader(`{"dry_run":true}`))
	s.handleSessionSub(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["dry_run"] != true {
		t.Fatalf("body %s", w.Body.String())
	}
	if _, ok := body["archive"]; ok {
		t.Fatalf("dry run included archive: %s", w.Body.String())
	}
	if _, ok := body["event_seq"]; ok {
		t.Fatalf("dry run included event_seq: %s", w.Body.String())
	}
	if body["action"] != "rescued" {
		t.Fatalf("action %v body %s", body["action"], w.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	st2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(st2.ModTime()) || string(before) != string(after) {
		t.Fatal("dry run changed the session file")
	}
	if _, err := os.Stat(filepath.Join(store.Root, "backups", "overage", id)); !os.IsNotExist(err) {
		t.Fatal("dry run wrote an archive")
	}
	var n int
	if err := d.SQL.QueryRow(`SELECT COUNT(*) FROM session_events WHERE session_id=? AND kind='overage_rescue'`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("dry run wrote %d events", n)
	}
	live, _ := reg.Get(id)
	if live.IsBusy() || live.Progress().Active {
		t.Fatal("dry run started a turn")
	}
}

func TestOverageRescueApplyArchivesAndDoesNotStartTurn(t *testing.T) {
	reg, store, d, id := overageRescueFixture(t)
	live, _ := reg.EnsureLoaded(id)
	beforeN := len(live.UIMessages())
	s := &Server{Registry: reg}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/overage-rescue", strings.NewReader(`{}`))
	s.handleSessionSub(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["action"] != "rescued" {
		t.Fatalf("body %s", w.Body.String())
	}
	archive, _ := body["archive"].(string)
	if archive == "" || !strings.HasPrefix(archive, "backups/overage/"+id+"/") {
		t.Fatalf("archive %q", archive)
	}
	if _, ok := body["event_seq"]; !ok {
		t.Fatalf("missing event_seq %s", w.Body.String())
	}
	abs := filepath.Join(store.Root, filepath.FromSlash(archive))
	archived, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	adoc, err := memory.DecodeSession(string(archived))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range adoc.Messages {
		if m.ModelOmit != "" || m.ModelStub != "" {
			t.Fatal("archive is not the pre-image")
		}
	}
	live, _ = reg.Get(id)
	if live.IsBusy() || live.Progress().Active {
		t.Fatal("rescue started or left a turn")
	}
	msgs := live.UIMessages()
	if len(msgs) != beforeN+1 {
		t.Fatalf("messages %d want %d", len(msgs), beforeN+1)
	}
	if msgs[len(msgs)-1].Role != "harness" {
		t.Fatalf("last role %s", msgs[len(msgs)-1].Role)
	}
	omitted := 0
	for _, m := range msgs[:beforeN] {
		if m.Role == "user" && m.Content == "" {
			t.Fatal("deleted transcript text")
		}
		if m.ModelOmit == "overage-rescue" && m.ModelStub == "" {
			omitted++
		}
	}
	if omitted == 0 {
		t.Fatal("expected omitted rounds")
	}
	var n int
	var content, errStr string
	if err := d.SQL.QueryRow(`SELECT COUNT(*) FROM session_events WHERE session_id=? AND kind='overage_rescue'`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("events %d", n)
	}
	if err := d.SQL.QueryRow(`SELECT content, COALESCE(error,'') FROM session_events WHERE session_id=? AND kind='overage_rescue'`, id).Scan(&content, &errStr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, `"action":"rescued"`) || errStr != "" {
		t.Fatalf("event content %s err %q", content, errStr)
	}

	// Second run is a noop: one more event, no second archive, no second marker.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/overage-rescue", strings.NewReader(`{}`))
	s.handleSessionSub(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("second status %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["action"] != "noop" || body["archive"] != "" {
		t.Fatalf("second body %s", w.Body.String())
	}
	if len(live.UIMessages()) != beforeN+1 {
		t.Fatalf("noop added messages: %d", len(live.UIMessages()))
	}
	entries, err := os.ReadDir(filepath.Join(store.Root, "backups", "overage", id))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("archives %+v", entries)
	}
	if err := d.SQL.QueryRow(`SELECT COUNT(*) FROM session_events WHERE session_id=? AND kind='overage_rescue'`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("events after noop %d", n)
	}

	if !live.BeginExclusive() {
		t.Fatal("could not take the turn lock")
	}
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/overage-rescue", strings.NewReader(`{}`))
	s.handleSessionSub(w, req)
	live.EndExclusive()
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "session busy") {
		t.Fatalf("busy status %d %s", w.Code, w.Body.String())
	}
}

func TestOverageRescueClosedAndMissing(t *testing.T) {
	reg, store, _, _ := overageRescueFixture(t)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	closed := &memory.SessionDoc{
		SessionMeta: memory.SessionMeta{
			ID: "ovclosed1", Title: "c", Status: "closed", CreatedAt: now, UpdatedAt: now,
		},
		Messages: []memory.TranscriptMessage{{ID: "m", Role: "user", Content: "hi", CreatedAt: now}},
	}
	if err := store.WriteSession(closed); err != nil {
		t.Fatal(err)
	}
	s := &Server{Registry: reg}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/ovclosed1/overage-rescue", strings.NewReader(`{}`))
	s.handleSessionSub(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "session is closed") {
		t.Fatalf("closed %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sessions/missing01/overage-rescue", strings.NewReader(`{}`))
	s.handleSessionSub(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/ovclosed1/overage-rescue", nil)
	s.handleSessionSub(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("get %d", w.Code)
	}
}

func overageRescueFixture(t *testing.T) (*session.Registry, *memory.Store, *db.DB, string) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store, err := memory.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	runner := &session.Runner{Cfg: config.Config{
		Model:          "m",
		BaseURL:        "http://127.0.0.1:9/v1",
		ContextLimit:   20000,
		MaxOutput:      1024,
		ContextReserve: 0,
	}}
	reg := session.NewRegistry(runner, store, d, dir, "m")
	runner.Reg = reg
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	id := "ovhttp01"
	var msgs []memory.TranscriptMessage
	for i := 0; i < 4; i++ {
		msgs = append(msgs, memory.TranscriptMessage{
			ID:        "m" + string(rune('a'+i)),
			Role:      "user",
			Content:   strings.Repeat("abcd", 5000),
			CreatedAt: now.Add(time.Duration(i) * time.Minute),
		})
	}
	doc := &memory.SessionDoc{
		SessionMeta: memory.SessionMeta{
			ID: id, Title: "over", Status: "active", Kind: "user", CreatedAt: now, UpdatedAt: now,
		},
		Messages: msgs,
	}
	if err := store.WriteSession(doc); err != nil {
		t.Fatal(err)
	}
	return reg, store, d, id
}
