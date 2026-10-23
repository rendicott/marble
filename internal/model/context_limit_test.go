package model

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const incidentContextBody = `{"message":"This model's maximum context length is 1048576 tokens. However, you requested 1048656 tokens (655440 in the messages, 393216 in the completion)."}`

func TestParseContextLimitIncident(t *testing.T) {
	cle := ParseContextLimit(400, incidentContextBody)
	if cle == nil {
		t.Fatal("expected ContextLimitError")
	}
	if cle.Limit != 1048576 || cle.Messages != 655440 || cle.Completion != 393216 {
		t.Fatalf("parsed %+v", cle)
	}
	if !strings.HasPrefix(cle.Error(), "model HTTP 400:") {
		t.Fatalf("error %q", cle.Error())
	}
	if !strings.Contains(cle.Error(), "655440") {
		t.Fatalf("error dropped body: %q", cle.Error())
	}
}

func TestParseContextLimitTable(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantNil    bool
		limit      int
		messages   int
		completion int
	}{
		{
			name:       "incident",
			body:       incidentContextBody,
			limit:      1048576,
			messages:   655440,
			completion: 393216,
		},
		{
			name:       "json type with numbers in message",
			body:       `{"error":{"message":"maximum context length is 8192 tokens (100 in the messages, 50 in the completion)","type":"context_length_exceeded"}}`,
			limit:      8192,
			messages:   100,
			completion: 50,
		},
		{
			name:     "type only",
			body:     `{"error":{"message":"too long","type":"context_length_exceeded"}}`,
			limit:    0,
			messages: 0,
		},
		{
			name:    "temperature",
			body:    `{"error":{"code":"invalid_request_error","message":"temperature is deprecated for this model.","type":"invalid_request_error","param":null}}`,
			wantNil: true,
		},
		{
			name:    "reasoning",
			body:    `{"error":{"message":"Invalid value 'none' for reasoning_effort; expected low|medium|high"}}`,
			wantNil: true,
		},
		{
			name:    "context phrase without the counted sentence",
			body:    `model HTTP 400: context length exceeded`,
			wantNil: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseContextLimit(400, tc.body)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("nil")
			}
			if got.Limit != tc.limit || got.Messages != tc.messages || got.Completion != tc.completion {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestChatOnceReturnsContextLimitError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(incidentContextBody))
	}))
	defer srv.Close()

	c := New(srv.URL, "deepseek", 393216, "", 0)
	_, err := c.Chat(context.Background(), []Message{{Role: "user", Content: ContentFromText("hi")}}, nil)
	var cle *ContextLimitError
	if !errors.As(err, &cle) {
		t.Fatalf("err=%v", err)
	}
	if cle.Limit != 1048576 || cle.Messages != 655440 || cle.Completion != 393216 {
		t.Fatalf("%+v", cle)
	}
	if !strings.Contains(err.Error(), "model HTTP 400:") {
		t.Fatalf("error %q", err.Error())
	}
}
