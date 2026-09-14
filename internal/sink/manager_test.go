package sink

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/rendicott/marble/internal/session"
)

func testSession(id, title string) *session.Session {
	reg := session.NewRegistry(nil, nil, nil, "/ws", "model")
	s := reg.Create(title)
	s.ID = id
	return s
}

func TestManagerFiresOnIdle(t *testing.T) {
	var hits atomic.Int32
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		hits.Add(1)
		w.WriteHeader(204)
	}))
	defer srv.Close()

	cfg := Config{
		DeepLinkBase: "https://marble.example",
		Sinks: []SinkSpec{{
			ID:       "wh",
			Type:     "webhook",
			Enabled:  true,
			URL:      srv.URL,
			Template: "{{.Message}}",
			Headers:  map[string]string{"Content-Type": "text/plain"},
		}},
	}
	cfg.Normalize()
	m := NewManager(cfg, "", "/ws")
	m.sync = true
	m.httpClient = srv.Client()

	s := testSession("sess1", "Demo")
	m.HandleEvent(s, session.Event{Type: "message", Message: &session.Message{Role: "user", Content: "hi"}})
	m.HandleEvent(s, session.Event{Type: "status", Status: "running"})
	m.HandleEvent(s, session.Event{Type: "message", Message: &session.Message{Role: "assistant", Content: "hello from marble"}})
	m.HandleEvent(s, session.Event{Type: "status", Status: "idle"})

	if hits.Load() != 1 {
		t.Fatalf("hits=%d history=%+v", hits.Load(), m.History())
	}
	if body != "hello from marble" {
		t.Fatalf("body %q", body)
	}
	// idempotent second idle of same turn should not exist; new idle without running is skip
	m.HandleEvent(s, session.Event{Type: "status", Status: "idle"})
	if hits.Load() != 1 {
		t.Fatalf("spurious idle hits=%d", hits.Load())
	}
}

func TestManagerSkipToolOnly(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	cfg := Config{Sinks: []SinkSpec{{
		ID: "wh", Type: "webhook", Enabled: true, URL: srv.URL, Template: "x",
	}}}
	cfg.Normalize()
	m := NewManager(cfg, "", "")
	m.sync = true
	m.httpClient = srv.Client()
	s := testSession("s2", "t")
	m.HandleEvent(s, session.Event{Type: "message", Message: &session.Message{Role: "user", Content: "go"}})
	m.HandleEvent(s, session.Event{Type: "status", Status: "idle"})
	if hits.Load() != 0 {
		t.Fatal("tool-only should skip")
	}
}

func TestManagerErrorStillFires(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	cfg := Config{Sinks: []SinkSpec{{
		ID: "wh", Type: "webhook", Enabled: true, URL: srv.URL, Template: "{{.Kind}}:{{.Message}}",
	}}}
	cfg.Normalize()
	m := NewManager(cfg, "", "")
	m.sync = true
	m.httpClient = srv.Client()
	s := testSession("s3", "t")
	m.HandleEvent(s, session.Event{Type: "message", Message: &session.Message{Role: "user", Content: "go"}})
	m.HandleEvent(s, session.Event{Type: "error", Error: "boom"})
	m.HandleEvent(s, session.Event{Type: "status", Status: "idle"})
	if hits.Load() != 1 {
		t.Fatalf("hits=%d hist=%+v", hits.Load(), m.History())
	}
}

func TestSessionOverrideOff(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	cfg := Config{Sinks: []SinkSpec{{
		ID: "wh", Type: "webhook", Enabled: true, URL: srv.URL, Template: "x",
	}}}
	cfg.Normalize()
	m := NewManager(cfg, "", "")
	m.sync = true
	m.httpClient = srv.Client()
	s := testSession("s4", "t")
	s.SetSinkOverrides(map[string]string{"wh": session.SinkOff})
	m.HandleEvent(s, session.Event{Type: "message", Message: &session.Message{Role: "user", Content: "go"}})
	m.HandleEvent(s, session.Event{Type: "message", Message: &session.Message{Role: "assistant", Content: "ok"}})
	m.HandleEvent(s, session.Event{Type: "status", Status: "idle"})
	if hits.Load() != 0 {
		t.Fatal("override off should skip")
	}
}

func TestSessionOverrideOn(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	cfg := Config{Sinks: []SinkSpec{{
		ID: "wh", Type: "webhook", Enabled: false, URL: srv.URL, Template: "x",
	}}}
	cfg.Normalize()
	m := NewManager(cfg, "", "")
	m.sync = true
	m.httpClient = srv.Client()
	s := testSession("s5", "t")
	s.SetSinkOverrides(map[string]string{"wh": session.SinkOn})
	m.HandleEvent(s, session.Event{Type: "message", Message: &session.Message{Role: "user", Content: "go"}})
	m.HandleEvent(s, session.Event{Type: "message", Message: &session.Message{Role: "assistant", Content: "ok"}})
	m.HandleEvent(s, session.Event{Type: "status", Status: "idle"})
	if hits.Load() != 1 {
		t.Fatalf("override on should deliver, hits=%d", hits.Load())
	}
}

func TestCoalesce(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	cfg := Config{Sinks: []SinkSpec{{
		ID: "wh", Type: "webhook", Enabled: true, URL: srv.URL, Template: "x",
		Filters: Filters{MinIntervalSec: 60},
	}}}
	cfg.Normalize()
	m := NewManager(cfg, "", "")
	m.sync = true
	m.httpClient = srv.Client()
	s := testSession("s6", "t")
	fire := func(msg string) {
		m.HandleEvent(s, session.Event{Type: "message", Message: &session.Message{Role: "user", Content: "go"}})
		m.HandleEvent(s, session.Event{Type: "message", Message: &session.Message{Role: "assistant", Content: msg}})
		m.HandleEvent(s, session.Event{Type: "status", Status: "idle"})
	}
	fire("one")
	fire("two")
	if hits.Load() != 1 {
		t.Fatalf("coalesce hits=%d hist=%+v", hits.Load(), m.History())
	}
}

func TestUpsertAndDeleteSink(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sinks.json"
	m := NewManager(DefaultConfig(), path, "/ws")
	got, err := m.UpsertSink(SinkSpec{ID: "dbg", Type: "stdout", Enabled: true, Format: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "dbg" || got.ValidationNote != "" {
		t.Fatalf("%+v", got)
	}
	if _, ok := m.GetSink("dbg"); !ok {
		t.Fatal("missing after upsert")
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Sinks) != 1 || loaded.Sinks[0].ID != "dbg" {
		t.Fatalf("%+v", loaded)
	}
	got, err = m.UpsertSink(SinkSpec{ID: "dbg", Type: "stdout", Enabled: false, Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.Format != "json" {
		t.Fatalf("%+v", got)
	}
	if err := m.DeleteSink("dbg"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.GetSink("dbg"); ok {
		t.Fatal("still present")
	}
	if err := m.DeleteSink("dbg"); err == nil {
		t.Fatal("expected not found")
	}
}
