package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is an OpenAI-compatible chat message.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	// Reasoning is populated by some local Qwen/vLLM builds instead of content.
	Reasoning string `json:"reasoning,omitempty"`
}

// ToolCall is a model-requested function invocation.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the tool name and JSON arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolSpec is the OpenAI tools[] entry.
type ToolSpec struct {
	Type     string             `json:"type"`
	Function ToolFunctionSchema `json:"function"`
}

// ToolFunctionSchema describes a callable tool.
type ToolFunctionSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ChatRequest is the completion request body.
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []ToolSpec `json:"tools,omitempty"`
	ToolChoice  string    `json:"tool_choice,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

// Usage is token usage from the provider when present.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResult is a completion plus optional usage and latency.
type ChatResult struct {
	Message      Message
	FinishReason string
	Usage        *Usage
	LatencyMs    int
}

// ChatResponse is a non-streaming completion response.
type ChatResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Index   int     `json:"index"`
		Message Message `json:"message"`
		Finish  string  `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage,omitempty"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Client talks to an OpenAI-compatible chat completions endpoint.
type Client struct {
	BaseURL    string
	Model      string
	MaxTokens  int
	// APIKey when non-empty sets Authorization: Bearer <key> (ADR-0016).
	// Empty → no Authorization header (local/open endpoints).
	APIKey     string
	HTTPClient *http.Client
}

// New creates a Client. baseURL should include /v1 (no trailing slash required).
// apiKey may be empty for unauthenticated local models (ADR-0016).
func New(baseURL, model string, maxTokens int, apiKey string) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		Model:     model,
		MaxTokens: maxTokens,
		APIKey:    strings.TrimSpace(apiKey),
		HTTPClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
	}
}

// setAuth applies Bearer auth when APIKey is set; otherwise omits Authorization.
func (c *Client) setAuth(req *http.Request) {
	if c == nil || req == nil {
		return
	}
	key := strings.TrimSpace(c.APIKey)
	if key == "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
}

// Chat performs a non-streaming chat completion.
func (c *Client) Chat(ctx context.Context, messages []Message, tools []ToolSpec) (ChatResult, error) {
	reqBody := ChatRequest{
		Model:     c.Model,
		Messages:  messages,
		Tools:     tools,
		MaxTokens: c.MaxTokens,
		Stream:    false,
	}
	if len(tools) > 0 {
		reqBody.ToolChoice = "auto"
	}
	t := 0.2
	reqBody.Temperature = &t

	raw, err := json.Marshal(reqBody)
	if err != nil {
		return ChatResult{}, err
	}
	url := c.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return ChatResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	start := time.Now()
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return ChatResult{}, fmt.Errorf("model request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatResult{}, err
	}
	latency := int(time.Since(start).Milliseconds())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ChatResult{}, fmt.Errorf("model HTTP %d: %s", resp.StatusCode, truncate(string(body), 800))
	}

	var cr ChatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return ChatResult{}, fmt.Errorf("decode response: %w", err)
	}
	if cr.Error != nil && cr.Error.Message != "" {
		return ChatResult{}, fmt.Errorf("model error: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return ChatResult{}, fmt.Errorf("model returned no choices")
	}
	msg := normalizeMessage(cr.Choices[0].Message)
	return ChatResult{
		Message:      msg,
		FinishReason: cr.Choices[0].Finish,
		Usage:        cr.Usage,
		LatencyMs:    latency,
	}, nil
}

// Health hits /models to verify connectivity.
func (c *Client) Health(ctx context.Context) error {
	url := c.BaseURL + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("health HTTP %d: %s", resp.StatusCode, truncate(string(b), 400))
	}
	return nil
}

func normalizeMessage(m Message) Message {
	if m.Role == "" {
		m.Role = "assistant"
	}
	// Some local builds put the answer in reasoning with content null/empty.
	if strings.TrimSpace(m.Content) == "" && strings.TrimSpace(m.Reasoning) != "" && len(m.ToolCalls) == 0 {
		m.Content = strings.TrimSpace(m.Reasoning)
		m.Reasoning = ""
	}
	// Ensure tool call type is set.
	for i := range m.ToolCalls {
		if m.ToolCalls[i].Type == "" {
			m.ToolCalls[i].Type = "function"
		}
		if m.ToolCalls[i].ID == "" {
			m.ToolCalls[i].ID = fmt.Sprintf("call_%d", i)
		}
	}
	return m
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
