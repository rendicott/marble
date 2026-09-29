package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestChatRetriesWithoutTemperature(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		_, hasTemp := req["temperature"]
		if n == 1 {
			if !hasTemp {
				t.Errorf("first call should send temperature")
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_request_error","message":"temperature is deprecated for this model.","type":"invalid_request_error","param":null}}`))
			return
		}
		if hasTemp {
			t.Errorf("call %d should omit temperature", n)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "claude-fable-5-1", 100, "", 0)
	msgs := []Message{{Role: "user", Content: Content{Text: "hi"}}}
	if _, err := c.Chat(context.Background(), msgs, nil); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if _, err := c.Chat(context.Background(), msgs, nil); err != nil {
		t.Fatalf("second chat: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls=%d want 3", got)
	}
}
