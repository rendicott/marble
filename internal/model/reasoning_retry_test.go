package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestChatWithOptsUpgradesNoneToLow(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		body, _ := io.ReadAll(r.Body)
		var req ChatRequest
		_ = json.Unmarshal(body, &req)
		if n == 1 {
			if req.ReasoningEffort != "none" {
				t.Errorf("first call effort=%q", req.ReasoningEffort)
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid value 'none' for reasoning_effort; expected low|medium|high"}}`))
			return
		}
		if req.ReasoningEffort != "low" {
			t.Errorf("second call effort=%q", req.ReasoningEffort)
		}
		_ = json.NewEncoder(w).Encode(ChatResponse{
			Choices: []struct {
				Index   int     `json:"index"`
				Message Message `json:"message"`
				Finish  string  `json:"finish_reason"`
			}{
				{Message: Message{Role: "assistant", Content: ContentFromText("ok")}, Finish: "stop"},
			},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-model", 128, "", 0)
	c.HTTPClient = srv.Client()
	res, err := c.ChatWithOpts(context.Background(), []Message{
		{Role: "user", Content: ContentFromText("hi")},
	}, nil, ChatOpts{ReasoningEffort: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("calls=%d", atomic.LoadInt32(&calls))
	}
	if res.ReasoningEffortApplied != "low" {
		t.Fatalf("applied=%q", res.ReasoningEffortApplied)
	}
	if !strings.Contains(res.ReasoningEffortNote, "none") || !strings.Contains(res.ReasoningEffortNote, "low") {
		t.Fatalf("note=%q", res.ReasoningEffortNote)
	}
	if res.Message.Content.PlainText() != "ok" {
		t.Fatalf("msg=%q", res.Message.Content.PlainText())
	}
}

func TestLooksLikeReasoningNoneRejected(t *testing.T) {
	cases := []struct {
		err  string
		want bool
	}{
		{"model HTTP 400: invalid reasoning_effort value none", true},
		{"model error: unsupported thinking level: none", true},
		{"model HTTP 401: unauthorized", false},
		{"model request: connection refused", false},
		{"model HTTP 400: context length exceeded", false},
	}
	for _, tc := range cases {
		got := looksLikeReasoningNoneRejected(errString(tc.err))
		if got != tc.want {
			t.Fatalf("%q: got %v want %v", tc.err, got, tc.want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
