package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyReasoningOptsSkipsChatTemplateKwargsForGemini(t *testing.T) {
	req := &ChatRequest{Model: "gemini-3.6-flash"}
	applyReasoningOpts(req, "none", "https://generativelanguage.googleapis.com/v1beta/openai", "gemini-3.6-flash")
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
	applyReasoningOpts(req, "high", "http://127.0.0.1:8000/v1", "Qwen/Qwen3.5")
	if req.ReasoningEffort != "high" {
		t.Fatalf("effort=%q", req.ReasoningEffort)
	}
	if req.ChatTemplateKwargs == nil || req.ChatTemplateKwargs["enable_thinking"] != true {
		t.Fatalf("kwargs=%#v", req.ChatTemplateKwargs)
	}
}

func TestApplyReasoningOptsSkipsChatTemplateKwargsForMistral(t *testing.T) {
	req := &ChatRequest{Model: "mistral-small-4"}
	applyReasoningOpts(req, "high", "http://spark-be0f.tail6f1a62.ts.net:8000/v1", "mistral-small-4")
	if req.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort=%q", req.ReasoningEffort)
	}
	if req.ChatTemplateKwargs != nil {
		t.Fatalf("chat_template_kwargs must be omitted for Mistral, got %#v", req.ChatTemplateKwargs)
	}
	raw, _ := json.Marshal(req)
	if strings.Contains(string(raw), "chat_template_kwargs") {
		t.Fatalf("JSON must not include chat_template_kwargs: %s", raw)
	}
}

func TestApplyReasoningOptsClampsMistralEffort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"none", "none"},
		{"low", "high"},
		{"medium", "high"},
		{"high", "high"},
	}
	for _, tc := range cases {
		req := &ChatRequest{Model: "mistral-small-4"}
		applyReasoningOpts(req, tc.in, "http://spark-be0f.tail6f1a62.ts.net:8000/v1", "mistral-small-4")
		if req.ReasoningEffort != tc.want {
			t.Fatalf("effort %q -> %q, want %q", tc.in, req.ReasoningEffort, tc.want)
		}
		if req.ChatTemplateKwargs != nil {
			t.Fatalf("chat_template_kwargs must be omitted for Mistral")
		}
	}
}

func TestSupportsChatTemplateKwargs(t *testing.T) {
	cases := []struct {
		url, model string
		want       bool
	}{
		{"https://generativelanguage.googleapis.com/v1beta/openai", "qwen3.6-35b", false},
		{"https://api.openai.com/v1", "qwen3.6-35b", false},
		{"https://api.x.ai/v1", "qwen3.6-35b", false},
		{"http://127.0.0.1:8000/v1", "Qwen/Qwen3.5", true},
		{"http://spark.tailnet:8000/v1", "qwen3.6-35b", true},
		{"", "Qwen/Qwen3.5-122B-A10B-FP8", true},
		{"http://127.0.0.1:8000/v1", "mistral-small-4", false},
		{"http://spark-be0f.tail6f1a62.ts.net:8000/v1", "mistral-small-4", false},
	}
	for _, tc := range cases {
		if got := supportsChatTemplateKwargs(tc.url, tc.model); got != tc.want {
			t.Fatalf("%s / %s: got %v want %v", tc.url, tc.model, got, tc.want)
		}
	}
}
