package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyReasoningOptsSkipsChatTemplateKwargsForGemini(t *testing.T) {
	req := &ChatRequest{Model: "gemini-3.6-flash"}
	applyReasoningOpts(req, "none", "https://generativelanguage.googleapis.com/v1beta/openai")
	if req.ReasoningEffort != "none" {
		t.Fatalf("reasoning_effort=%q", req.ReasoningEffort)
	}
	if req.ChatTemplateKwargs != nil {
		t.Fatalf("chat_template_kwargs must be omitted for Gemini, got %#v", req.ChatTemplateKwargs)
	}
	raw, _ := json.Marshal(req)
	if strings.Contains(string(raw), "chat_template_kwargs") {
		t.Fatalf("JSON must not include chat_template_kwargs: %s", raw)
	}
}

func TestApplyReasoningOptsIncludesChatTemplateKwargsForLocal(t *testing.T) {
	req := &ChatRequest{Model: "Qwen/Qwen3.5"}
	applyReasoningOpts(req, "high", "http://127.0.0.1:8000/v1")
	if req.ReasoningEffort != "high" {
		t.Fatalf("effort=%q", req.ReasoningEffort)
	}
	if req.ChatTemplateKwargs == nil || req.ChatTemplateKwargs["enable_thinking"] != true {
		t.Fatalf("kwargs=%#v", req.ChatTemplateKwargs)
	}
}

func TestSupportsChatTemplateKwargs(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://generativelanguage.googleapis.com/v1beta/openai", false},
		{"https://api.openai.com/v1", false},
		{"https://api.x.ai/v1", false},
		{"http://127.0.0.1:8000/v1", true},
		{"http://spark.tailnet:8000/v1", true},
		{"", true},
	}
	for _, tc := range cases {
		if got := supportsChatTemplateKwargs(tc.url); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.url, got, tc.want)
		}
	}
}
