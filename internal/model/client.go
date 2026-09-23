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
	Content    Content    `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	// Reasoning is populated by some local Qwen/vLLM builds (chain-of-thought).
	Reasoning string `json:"reasoning,omitempty"`
	// ReasoningContent is the OpenAI-style field used by some reasoning models.
	// Normalized into Reasoning in normalizeMessage.
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// ToolCall is a model-requested function invocation.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
	// ExtraContent preserves provider extensions. Gemini OpenAI-compat attaches
	// extra_content.google.thought_signature on tool_calls; that signature MUST be
	// echoed on the next request after tool results or Gemini returns HTTP 400.
	// See https://ai.google.dev/gemini-api/docs/thought-signatures
	ExtraContent json.RawMessage `json:"extra_content,omitempty"`
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
	Model       string     `json:"model"`
	Messages    []Message  `json:"messages"`
	Tools       []ToolSpec `json:"tools,omitempty"`
	ToolChoice  string     `json:"tool_choice,omitempty"`
	MaxTokens   int        `json:"max_tokens,omitempty"`
	Temperature *float64   `json:"temperature,omitempty"`
	Stream      bool       `json:"stream,omitempty"`
	// ReasoningEffort is OpenAI-style (none|low|medium|high). Providers ignore if unsupported.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// ChatTemplateKwargs is used by vLLM/Qwen-style servers (e.g. enable_thinking).
	ChatTemplateKwargs map[string]interface{} `json:"chat_template_kwargs,omitempty"`
}

// ChatOpts carries optional per-call parameters (reasoning effort, etc.).
type ChatOpts struct {
	// ReasoningEffort: none | low | medium | high (empty = omit / provider default).
	ReasoningEffort string
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
	// ReasoningEffortApplied is the effort actually sent (may differ after auto-upgrade).
	ReasoningEffortApplied string
	// ReasoningEffortNote is a short operator-facing note when effort was auto-changed.
	ReasoningEffortNote string
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

// DefaultHTTPTimeout is the default HTTP client timeout for a single
// chat/completions request (headers + full body). Long thinking models
// (e.g. Qwen3.x) can exceed 10m waiting on headers alone.
const DefaultHTTPTimeout = 30 * time.Minute

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
// httpTimeout ≤ 0 uses DefaultHTTPTimeout (30m).
func New(baseURL, model string, maxTokens int, apiKey string, httpTimeout time.Duration) *Client {
	if httpTimeout <= 0 {
		httpTimeout = DefaultHTTPTimeout
	}
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		Model:     model,
		MaxTokens: maxTokens,
		APIKey:    strings.TrimSpace(apiKey),
		HTTPClient: &http.Client{
			Timeout: httpTimeout,
		},
	}
}

// HTTPTimeout returns the configured client timeout (0 if unset).
func (c *Client) HTTPTimeout() time.Duration {
	if c == nil || c.HTTPClient == nil {
		return 0
	}
	return c.HTTPClient.Timeout
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
	return c.ChatWithOpts(ctx, messages, tools, ChatOpts{})
}

// ChatWithOpts is Chat plus optional reasoning effort / thinking controls.
// If the provider rejects reasoning_effort=none, automatically retries with low,
// then medium, and sets ReasoningEffortNote when an upgrade happened.
func (c *Client) ChatWithOpts(ctx context.Context, messages []Message, tools []ToolSpec, opts ChatOpts) (ChatResult, error) {
	requested := NormalizeReasoningEffort(opts.ReasoningEffort)
	efforts := []string{requested}
	if requested == "none" {
		efforts = []string{"none", "low", "medium"}
	}

	var lastErr error
	var totalLatency int
	for i, effort := range efforts {
		if i > 0 && !looksLikeReasoningNoneRejected(lastErr) {
			break
		}
		res, err := c.chatOnce(ctx, messages, tools, effort)
		totalLatency += res.LatencyMs
		if err == nil {
			res.LatencyMs = totalLatency
			res.ReasoningEffortApplied = effort
			if i > 0 {
				res.ReasoningEffortNote = fmt.Sprintf(
					"model rejected reasoning_effort=none; retried with %s", effort)
			}
			return res, nil
		}
		lastErr = err
		if requested != "none" || !looksLikeReasoningNoneRejected(err) {
			return ChatResult{LatencyMs: totalLatency}, err
		}
		// else: continue ladder none → low → medium
	}
	return ChatResult{LatencyMs: totalLatency}, lastErr
}

func (c *Client) chatOnce(ctx context.Context, messages []Message, tools []ToolSpec, effort string) (ChatResult, error) {
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
	applyReasoningOpts(&reqBody, effort, c.BaseURL, c.Model)

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
		return ChatResult{LatencyMs: latency}, fmt.Errorf("model HTTP %d: %s", resp.StatusCode, truncate(string(body), 800))
	}

	var cr ChatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return ChatResult{LatencyMs: latency}, fmt.Errorf("decode response: %w", err)
	}
	if cr.Error != nil && cr.Error.Message != "" {
		return ChatResult{LatencyMs: latency}, fmt.Errorf("model error: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return ChatResult{LatencyMs: latency}, fmt.Errorf("model returned no choices")
	}
	msg := normalizeMessage(cr.Choices[0].Message)
	return ChatResult{
		Message:                msg,
		FinishReason:           cr.Choices[0].Finish,
		Usage:                  cr.Usage,
		LatencyMs:              latency,
		ReasoningEffortApplied: NormalizeReasoningEffort(effort),
	}, nil
}

// looksLikeReasoningNoneRejected detects provider errors that typically mean
// reasoning_effort=none (or disable-thinking) is not an accepted value.
func looksLikeReasoningNoneRejected(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	// Must look like an effort/thinking parameter problem, not auth/network.
	mentionsEffort := strings.Contains(low, "reasoning_effort") ||
		strings.Contains(low, "reasoning") ||
		strings.Contains(low, "thinking") ||
		strings.Contains(low, "enable_thinking") ||
		strings.Contains(low, "chat_template_kwargs")
	if !mentionsEffort {
		return false
	}
	mentionsReject := strings.Contains(low, "none") ||
		strings.Contains(low, "invalid") ||
		strings.Contains(low, "unsupported") ||
		strings.Contains(low, "not support") ||
		strings.Contains(low, "not allowed") ||
		strings.Contains(low, "must be") ||
		strings.Contains(low, "unknown") ||
		strings.Contains(low, "expected") ||
		strings.Contains(low, "400")
	return mentionsReject
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
		msg := fmt.Sprintf("health HTTP %d: %s", resp.StatusCode, truncate(string(b), 400))
		if hint := healthAuthHint(c.BaseURL, resp.StatusCode, string(b)); hint != "" {
			msg += "\n\n" + hint
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// healthAuthHint adds operator guidance for common misconfigured endpoints.
func healthAuthHint(baseURL string, status int, body string) string {
	lowBase := strings.ToLower(baseURL)
	lowBody := strings.ToLower(body)
	// Google Gemini OpenAI-compat path (works with Bearer + Marble).
	const geminiOpenAI = "https://generativelanguage.googleapis.com/v1beta/openai"
	if strings.Contains(lowBase, "generativelanguage.googleapis.com") {
		if strings.Contains(lowBase, "/interactions") || !strings.Contains(lowBase, "/openai") {
			return "Hint: this base URL is not Marble’s OpenAI-compatible path. " +
				"Use " + geminiOpenAI + " (not /v1beta/interactions). " +
				"Keep model id e.g. gemini-3.6-flash and api_key_env=GEMINI_API_KEY. " +
				"Native Google Interactions API uses a different request shape and auth (x-goog-api-key)."
		}
		if status == 401 || strings.Contains(lowBody, "unauthenticated") || strings.Contains(lowBody, "oauth") {
			return "Hint: key may be wrong/revoked, or base URL should be " + geminiOpenAI + ". " +
				"Confirm GEMINI_API_KEY is a Google AI Studio API key (not an OAuth access token)."
		}
	}
	if status == 401 || status == 403 {
		return "Hint: api_key_env is set but the provider rejected credentials. " +
			"Check the secret in $MEMORY/env (Settings → Secrets) or process env, and that base_url matches an OpenAI-compatible endpoint."
	}
	return ""
}

func normalizeMessage(m Message) Message {
	if m.Role == "" {
		m.Role = "assistant"
	}
	// Unify provider-specific reasoning fields.
	if strings.TrimSpace(m.Reasoning) == "" && strings.TrimSpace(m.ReasoningContent) != "" {
		m.Reasoning = strings.TrimSpace(m.ReasoningContent)
	}
	m.ReasoningContent = ""
	// Some local builds put the *final* answer only in reasoning with content empty
	// and no tool calls. Keep Reasoning when tool_calls are present so the harness
	// can surface chain-of-thought in the UI (ADR-0026 thinking rows).
	if m.Content.IsEmpty() && strings.TrimSpace(m.Reasoning) != "" && len(m.ToolCalls) == 0 {
		m.Content = ContentFromText(strings.TrimSpace(m.Reasoning))
		m.Reasoning = ""
	} else if len(m.Content.Parts) > 0 {
		// Collapse assistant/tool responses to plain text for history simplicity.
		m.Content = ContentFromText(m.Content.PlainText())
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

// NormalizeReasoningEffort maps free text to none|low|medium|high or "".
func NormalizeReasoningEffort(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "off", "0":
		return "none"
	case "low", "1":
		return "low"
	case "medium", "med", "2":
		return "medium"
	case "high", "3":
		return "high"
	default:
		return ""
	}
}

func applyReasoningOpts(req *ChatRequest, effort, baseURL, model string) {
	if req == nil {
		return
	}
	e := NormalizeReasoningEffort(effort)
	if e == "" {
		return
	}
	e = clampReasoningEffortForModel(model, e)
	// OpenAI-style field (many cloud providers ignore unknown optional fields).
	req.ReasoningEffort = e
	// vLLM / Qwen chat template toggle — NOT part of OpenAI or Gemini OpenAI-compat.
	// Google returns HTTP 400: Unknown name "chat_template_kwargs" (session 0wd3247f02).
	if supportsChatTemplateKwargs(baseURL, model) {
		req.ChatTemplateKwargs = map[string]interface{}{
			"enable_thinking": e != "none",
		}
	}
}

// supportsChatTemplateKwargs reports whether baseURL is a self-hosted / vLLM-style
// endpoint that understands chat_template_kwargs. Strict cloud OpenAI-compat APIs
// (Gemini, OpenAI, Azure, Anthropic gateways) reject unknown top-level fields.
func supportsChatTemplateKwargs(baseURL, model string) bool {
	// chat_template_kwargs.enable_thinking is a Qwen-only toggle. Other local
	// models (e.g. Mistral's Tekken tokenizer) reject chat_template_kwargs with
	// HTTP 400 ("chat_template is not supported for Mistral tokenizers").
	if !isQwenModel(model) {
		return false
	}
	u := strings.ToLower(strings.TrimSpace(baseURL))
	if u == "" {
		return true // process-local default: allow (Qwen/vLLM)
	}
	deny := []string{
		"generativelanguage.googleapis.com",
		"googleapis.com/v1beta/openai",
		"api.openai.com",
		"openai.azure.com",
		"api.anthropic.com",
		"api.x.ai",
		"api.groq.com",
		"api.mistral.ai",
		"api.together.xyz",
		"openrouter.ai",
	}
	for _, d := range deny {
		if strings.Contains(u, d) {
			return false
		}
	}
	return true
}

// isQwenModel reports whether the served model is a Qwen-family model. Qwen's chat
// template reads enable_thinking from chat_template_kwargs; no other local model does.
func isQwenModel(model string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(model)), "qwen")
}

// isMistralModel reports whether the served model is a Mistral-family model.
func isMistralModel(model string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(model)), "mistral")
}

// clampReasoningEffortForModel maps a normalized reasoning effort onto the values a
// model actually accepts. Mistral's vLLM reasoning parser supports only none|high, so
// any non-"none" effort (low/medium/high) collapses to "high" (reasoning is binary).
func clampReasoningEffortForModel(model, effort string) string {
	if effort == "" || !isMistralModel(model) {
		return effort
	}
	if effort == "none" {
		return "none"
	}
	return "high"
}

// ThoughtText returns provider reasoning and/or interim content suitable for UI
// "thinking" rows. Empty when the model returned only tool_calls with no prose.
func ThoughtText(m Message) string {
	var parts []string
	if r := strings.TrimSpace(m.Reasoning); r != "" {
		parts = append(parts, r)
	}
	if c := strings.TrimSpace(m.Content.PlainText()); c != "" {
		// Avoid duplicating if content equals reasoning
		if len(parts) == 0 || c != parts[0] {
			parts = append(parts, c)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
