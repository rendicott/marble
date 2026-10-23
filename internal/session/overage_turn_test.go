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

	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/memory"
	"github.com/rendicott/marble/internal/model"
	"github.com/rendicott/marble/internal/token"
)

const incident400Body = `{"message":"This model's maximum context length is 1048576 tokens. However, you requested 1048656 tokens (655440 in the messages, 393216 in the completion)."}`

func TestIncidentClampRetriesWithSlack(t *testing.T) {
	const (
		limit  = 1048576
		maxOut = 393216
	)
	var mu sync.Mutex
	var calls []model.ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req model.ChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		n := len(calls)
		calls = append(calls, req)
		mu.Unlock()
		if n == 0 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(incident400Body))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok-fit"},"finish_reason":"stop"}],"usage":{"prompt_tokens":655440,"completion_tokens":8,"total_tokens":655448}}`))
	}))
	defer srv.Close()

	budget := config.BudgetTokens(limit, maxOut, 0)
	available := budget - estimateTools(nil)
	if available < 256 {
		available = 256
	}
	hist := historyAtEstimate(t, available)
	est := estimateAll(hist)
	if est != available {
		t.Fatalf("estimate %d available %d", est, available)
	}

	s := newSession("incident", "incident")
	s.history = hist
	s.ui = []Message{{ID: "m-user", Role: "user", Content: "pad", CreatedAt: time.Now()}}
	r := overageRunner(srv.URL, limit, maxOut, 0)
	r.runTurn(s)

	mu.Lock()
	got := append([]model.ChatRequest(nil), calls...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("provider calls=%d", len(got))
	}
	want := ClampMaxTokens(limit, maxOut, est, defaultObsRatio)
	if !want.OK || got[0].MaxTokens != want.MaxTokens || got[0].MaxTokens >= maxOut {
		t.Fatalf("first max_tokens=%d clamp=%+v", got[0].MaxTokens, want)
	}
	retry, ok := RetryMaxTokens(limit, 655440, maxOut, want.Margin, got[0].MaxTokens)
	if !ok {
		t.Fatal("retry decision declined")
	}
	if got[1].MaxTokens != retry {
		t.Fatalf("second max_tokens=%d want %d", got[1].MaxTokens, retry)
	}
	slack := limit - (655440 + got[1].MaxTokens)
	if slack <= 0 {
		t.Fatalf("slack %d (max_tokens %d)", slack, got[1].MaxTokens)
	}
	if len(got[0].Messages) != len(got[1].Messages) {
		t.Fatalf("message count %d vs %d", len(got[0].Messages), len(got[1].Messages))
	}
	for i := range got[0].Messages {
		if got[0].Messages[i].Role != got[1].Messages[i].Role || got[0].Messages[i].Content.PlainText() != got[1].Messages[i].Content.PlainText() {
			t.Fatalf("message %d changed on retry", i)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ui) != 2 {
		t.Fatalf("ui messages=%d", len(s.ui))
	}
	if s.ui[1].Role != "assistant" || !strings.Contains(s.ui[1].Content, "ok-fit") {
		t.Fatalf("assistant %q", s.ui[1].Content)
	}
	if strings.Contains(s.ui[1].Content, "Send another message") {
		t.Fatalf("success bubble offered continue: %s", s.ui[1].Content)
	}
	for _, m := range s.ui {
		if strings.Contains(m.Content, "overage-rescue") || m.Role == "harness" {
			t.Fatalf("rescue rewrote the transcript: %+v", m)
		}
	}
}

func TestContextOverageNoRetryWhenPromptExceedsWindow(t *testing.T) {
	const limit = 1048576
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"This model's maximum context length is 1048576 tokens. However, you requested 1441216 tokens (1048000 in the messages, 393216 in the completion)."}`))
	}))
	defer srv.Close()

	s := newSession("over", "over")
	s.history = append(s.history, model.Message{Role: "user", Content: model.ContentFromText("hello")})
	s.ui = []Message{{ID: "m-user", Role: "user", Content: "hello", CreatedAt: time.Now()}}
	overageRunner(srv.URL, limit, 393216, 0).runTurn(s)

	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	if s.Progress().Phase != "context_overage" {
		t.Fatalf("phase %q", s.Progress().Phase)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var bubble string
	for _, m := range s.ui {
		if m.Role == "assistant" {
			bubble = m.Content
		}
	}
	if !strings.Contains(bubble, "Use Rescue context.") {
		t.Fatalf("bubble %q", bubble)
	}
	if strings.Contains(bubble, "Send another message") {
		t.Fatalf("bubble still offers continue: %s", bubble)
	}
}

func TestContextOverageSecond400DoesNotRetryAgain(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(incident400Body))
	}))
	defer srv.Close()

	s := newSession("twice", "twice")
	s.history = append(s.history, model.Message{Role: "user", Content: model.ContentFromText("hello")})
	overageRunner(srv.URL, 1048576, 393216, 0).runTurn(s)
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if s.Progress().Phase != "context_overage" {
		t.Fatalf("phase %q", s.Progress().Phase)
	}
}

func TestLargerProviderLimitIsRecordedNotCataloged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req model.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.MaxTokens > 50000 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 1048576 tokens. However, you requested 999999 tokens (1000 in the messages, 393216 in the completion).","type":"context_length_exceeded"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

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
	r := overageRunner(srv.URL, 1000000, 393216, 8192)
	reg := NewRegistry(r, store, d, dir, r.Cfg.Model)
	r.Reg = reg

	s := newSession("limitgap", "limitgap")
	s.history = append(s.history, model.Message{Role: "user", Content: model.ContentFromText("hello")})
	reg.mu.Lock()
	reg.sessions[s.ID] = s
	reg.mu.Unlock()
	r.runTurn(s)

	if r.Cfg.ContextLimit != 1000000 {
		t.Fatalf("process limit rewritten to %d", r.Cfg.ContextLimit)
	}
	obs, ok := s.LastContextLimit()
	if !ok || obs.Limit != 1048576 || obs.Messages != 1000 {
		t.Fatalf("observation %+v ok=%v", obs, ok)
	}
	var n int
	if err := d.SQL.QueryRow(`SELECT COUNT(*) FROM session_events WHERE session_id=? AND kind=?`, s.ID, "context_limit_observed").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("context_limit_observed rows=%d", n)
	}
}

func TestGeneric400KeepsContinueSentence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid tool schema","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()
	s := newSession("schema", "schema")
	s.history = append(s.history, model.Message{Role: "user", Content: model.ContentFromText("hello")})
	overageRunner(srv.URL, 1048576, 393216, 0).runTurn(s)
	if s.Progress().Phase != "error" {
		t.Fatalf("phase %q", s.Progress().Phase)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var bubble string
	for _, m := range s.ui {
		if m.Role == "assistant" {
			bubble = m.Content
		}
	}
	if !strings.Contains(bubble, "Send another message") {
		t.Fatalf("generic 400 lost the continue sentence: %s", bubble)
	}
	if strings.Contains(bubble, "Use Rescue context.") {
		t.Fatalf("generic 400 offered rescue: %s", bubble)
	}
}

func overageRunner(baseURL string, limit, maxOut, reserve int) *Runner {
	cfg := config.Config{
		BaseURL:                  baseURL,
		Model:                    "deepseek-v4",
		ContextLimit:             limit,
		MaxOutput:                maxOut,
		ContextReserve:           reserve,
		ContextWarnRatio:         2,
		ContextAutoCompactRatio:  2,
		ContextAutoCompactRounds: 99,
		MaxToolIters:             4,
		ToolRoundSoft:            4,
		HardWall:                 time.Minute,
		ModelTimeout:             time.Minute,
	}
	return &Runner{
		Cfg:    cfg,
		Client: model.New(baseURL, cfg.Model, maxOut, "", time.Minute),
	}
}

func historyAtEstimate(t *testing.T, target int) []model.Message {
	t.Helper()
	sys := model.Message{Role: "system", Content: model.ContentFromText(defaultSystemPrompt)}
	empty := estimateAll([]model.Message{sys, {Role: "user", Content: model.ContentFromText("")}})
	need := target - empty
	if need < 1 {
		t.Fatalf("target %d is below the empty-user estimate %d", target, empty)
	}
	n := need*4 - 3
	if n < 1 {
		n = 1
	}
	text := strings.Repeat("a", n)
	if token.Estimate(text) != need {
		t.Fatalf("Estimate(%d)=%d want %d", n, token.Estimate(text), need)
	}
	msgs := []model.Message{sys, {Role: "user", Content: model.ContentFromText(text)}}
	if got := estimateAll(msgs); got != target {
		t.Fatalf("estimateAll=%d want %d", got, target)
	}
	return msgs
}
