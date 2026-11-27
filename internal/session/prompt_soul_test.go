package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/memory"
	"github.com/rendicott/marble/internal/model"
	"github.com/rendicott/marble/internal/tools"
)

func promptSoulContents(t *testing.T, d *db.DB, sessionID string) []string {
	t.Helper()
	rows, err := d.SQL.Query(`SELECT content FROM session_events WHERE session_id=? AND kind=? ORDER BY seq`, sessionID, "prompt_soul")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func fileSoulEvent(t *testing.T, path, source string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		b = nil
	}
	sha, chars := memory.SoulVersion(string(b))
	return fmt.Sprintf("soul_sha=%s chars=%d source=%s path=soul.md", sha, chars, source)
}

func TestPromptSoulEventOnRoutedTurn(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	gitInit(t, repo)
	sqldb, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	store, err := memory.New(root)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(nil, store, sqldb, repo, "m")
	r := &Runner{Cfg: config.Config{Workspace: repo}, Reg: reg}
	s := newSession("soulroute1", "t")
	s.appendUI(Message{Role: "user", Content: "fix the bug"})
	s.initTurnProgress(80, 65)

	r.runRoutedTurn(s, context.Background(), &db.AgentPresetRow{ID: "grok", Driver: "grok"}, TurnOpts{})
	got := promptSoulContents(t, sqldb, s.ID)
	wantEmpty := "soul_sha=e3b0c44298fc chars=0 source=subprocess path=soul.md"
	if len(got) != 1 || got[0] != wantEmpty {
		t.Fatalf("blank turn events %#v", got)
	}

	const soul = "SOUL-ROUTE-1\nkeep this out of the event"
	if err := os.WriteFile(store.SoulPath(), []byte(soul), 0o644); err != nil {
		t.Fatal(err)
	}
	r.runRoutedTurn(s, context.Background(), &db.AgentPresetRow{ID: "grok", Driver: "grok"}, TurnOpts{})
	got = promptSoulContents(t, sqldb, s.ID)
	if len(got) != 2 {
		t.Fatalf("events after second turn: %#v", got)
	}
	if got[0] != wantEmpty {
		t.Fatalf("first event changed: %s", got[0])
	}
	want := fileSoulEvent(t, store.SoulPath(), "subprocess")
	if got[1] != want {
		t.Fatalf("event %q want %q", got[1], want)
	}
	if strings.Contains(got[1], "SOUL-ROUTE-1") || strings.Contains(got[1], "keep this out") {
		t.Fatalf("event logged soul text: %s", got[1])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var sawSoul bool
	for _, step := range s.turn.prog.Steps {
		if strings.Contains(step.Detail, "soul") {
			sawSoul = true
		}
	}
	if !sawSoul {
		t.Fatalf("context marker omitted soul: %+v", s.turn.prog.Steps)
	}
}

func TestPromptSoulEventOncePerLoopTurn(t *testing.T) {
	var mu sync.Mutex
	var prompts []string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req model.ChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode: %v", err)
		}
		var b strings.Builder
		for _, msg := range req.Messages {
			b.WriteString(msg.Content.PlainText())
			b.WriteByte('\n')
		}
		mu.Lock()
		n := calls
		calls++
		prompts = append(prompts, b.String())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n%2 == 0 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"not_a_real_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":1,"total_tokens":21}}`))
	}))
	defer srv.Close()

	root := t.TempDir()
	sqldb, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	store, err := memory.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSoul("SOUL-ALPHA-7f3a\n"); err != nil {
		t.Fatal(err)
	}
	runner := overageRunner(srv.URL, 100000, 1000, 0)
	reg := NewRegistry(runner, store, sqldb, root, runner.Cfg.Model)
	runner.Reg = reg
	runner.Tools = &tools.Registry{}
	s := newSession("soulloop1", "soul")
	s.history = append(s.history, model.Message{Role: "user", Content: model.ContentFromText("hello")})
	s.ui = []Message{{ID: "m-user", Role: "user", Content: "hello", CreatedAt: time.Now()}}
	reg.mu.Lock()
	reg.sessions[s.ID] = s
	reg.mu.Unlock()

	runner.runTurn(s)
	mu.Lock()
	nCalls := calls
	gotPrompts := append([]string(nil), prompts...)
	mu.Unlock()
	if nCalls != 2 {
		t.Fatalf("model calls %d phase %s", nCalls, s.Progress().Phase)
	}
	events := promptSoulContents(t, sqldb, s.ID)
	want := fileSoulEvent(t, store.SoulPath(), "loop")
	if len(events) != 1 || events[0] != want {
		t.Fatalf("events %#v want %q", events, want)
	}
	if strings.Contains(events[0], "SOUL-ALPHA") {
		t.Fatalf("event logged soul text: %s", events[0])
	}
	if !strings.Contains(gotPrompts[0], "SOUL-ALPHA-7f3a") || !strings.Contains(gotPrompts[1], "SOUL-ALPHA-7f3a") {
		t.Fatalf("soul missing from a model call:\n---\n%s\n---\n%s", gotPrompts[0], gotPrompts[1])
	}

	if err := store.WriteSoul("SOUL-BETA-91c2\n"); err != nil {
		t.Fatal(err)
	}
	runner.runTurn(s)
	mu.Lock()
	nCalls = calls
	gotPrompts = append([]string(nil), prompts...)
	mu.Unlock()
	if nCalls != 4 {
		t.Fatalf("model calls %d phase %s", nCalls, s.Progress().Phase)
	}
	events = promptSoulContents(t, sqldb, s.ID)
	want = fileSoulEvent(t, store.SoulPath(), "loop")
	if len(events) != 2 || events[1] != want {
		t.Fatalf("events %#v want second %q", events, want)
	}
	if strings.Contains(events[1], "SOUL-BETA") || strings.Contains(events[1], "SOUL-ALPHA") {
		t.Fatalf("event logged soul text: %#v", events)
	}
	if strings.Contains(gotPrompts[2], "SOUL-ALPHA-7f3a") || !strings.Contains(gotPrompts[2], "SOUL-BETA-91c2") {
		t.Fatal("second turn kept the previous soul")
	}
	if !strings.Contains(gotPrompts[3], "SOUL-BETA-91c2") {
		t.Fatal("second model call of the second turn missed the new soul")
	}
}

func TestPromptSoulSystemSessionRecordsEmpty(t *testing.T) {
	var prompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req model.ChatRequest
		_ = json.Unmarshal(body, &req)
		var b strings.Builder
		for _, msg := range req.Messages {
			b.WriteString(msg.Content.PlainText())
			b.WriteByte('\n')
		}
		prompts = append(prompts, b.String())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`))
	}))
	defer srv.Close()

	root := t.TempDir()
	sqldb, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	store, err := memory.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSoul("SOUL-SYSTEM-SHOULD-SKIP"); err != nil {
		t.Fatal(err)
	}
	runner := overageRunner(srv.URL, 100000, 1000, 0)
	reg := NewRegistry(runner, store, sqldb, root, runner.Cfg.Model)
	runner.Reg = reg
	s := newSession("soulsys1", "sys")
	s.Kind = "system"
	s.history = append(s.history, model.Message{Role: "user", Content: model.ContentFromText("hello")})
	s.ui = []Message{{ID: "m-user", Role: "user", Content: "hello", CreatedAt: time.Now()}}
	runner.runTurn(s)
	events := promptSoulContents(t, sqldb, s.ID)
	want := "soul_sha=e3b0c44298fc chars=0 source=loop path=soul.md"
	if len(events) != 1 || events[0] != want {
		t.Fatalf("events %#v", events)
	}
	if len(prompts) != 1 || strings.Contains(prompts[0], "SOUL-SYSTEM-SHOULD-SKIP") {
		t.Fatalf("system session was given soul:\n%s", strings.Join(prompts, "\n"))
	}
}
