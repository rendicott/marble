package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// httpSession talks MCP over Streamable HTTP / simple JSON-RPC POST.
// Spec variants exist; we support:
//   - application/json JSON-RPC response
//   - text/event-stream with data: JSON-RPC lines
type httpSession struct {
	name    string
	url     string
	headers map[string]string
	client  *http.Client
	timeout time.Duration
	nextID  int64
	// session id from server if provided
	sessionID string
}

// ConnectHTTP initializes an HTTP MCP session.
func ConnectHTTP(ctx context.Context, name string, sc ServerConfig, timeout time.Duration) (Session, error) {
	if sc.URL == "" {
		return nil, fmt.Errorf("http server %q: url required", name)
	}
	to := defaultTimeout(timeout)
	s := &httpSession{
		name:    name,
		url:     sc.URL,
		headers: sc.Headers,
		client: &http.Client{
			Timeout: to + 5*time.Second,
		},
		timeout: to,
	}
	initCtx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	params := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "marble-harness",
			"version": "0.1.0",
		},
	}
	var result map[string]interface{}
	if err := s.call(initCtx, "initialize", params, &result); err != nil {
		return nil, fmt.Errorf("initialize %s: %w", name, err)
	}
	// notifications/initialized (best-effort)
	_ = s.notify(initCtx, "notifications/initialized", map[string]interface{}{})
	return s, nil
}

func (s *httpSession) Name() string { return s.name }

func (s *httpSession) Close() error { return nil }

func (s *httpSession) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.timeout)
}

func (s *httpSession) call(ctx context.Context, method string, params, result interface{}) error {
	id := atomic.AddInt64(&s.nextID, 1)
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if s.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", s.sessionID)
	}
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		s.sessionID = sid
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("http %d: %s", resp.StatusCode, string(b))
	}
	ct := resp.Header.Get("Content-Type")
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	var rpcResult json.RawMessage
	var rpcErr *rpcError
	if strings.Contains(ct, "text/event-stream") {
		rpcResult, rpcErr, err = parseSSEJSONRPC(body, id)
		if err != nil {
			return err
		}
	} else {
		var respObj struct {
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
			ID     interface{}     `json:"id"`
		}
		if err := json.Unmarshal(body, &respObj); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		rpcResult = respObj.Result
		rpcErr = respObj.Error
	}
	if rpcErr != nil {
		return fmt.Errorf("mcp %s: %s (%d)", s.name, rpcErr.Message, rpcErr.Code)
	}
	if result != nil && len(rpcResult) > 0 {
		return json.Unmarshal(rpcResult, result)
	}
	return nil
}

func (s *httpSession) notify(ctx context.Context, method string, params interface{}) error {
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	raw, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if s.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", s.sessionID)
	}
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return nil
}

func parseSSEJSONRPC(body []byte, wantID int64) (json.RawMessage, *rpcError, error) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	var dataLines []string
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if line == "" && len(dataLines) > 0 {
			payload := strings.Join(dataLines, "\n")
			dataLines = dataLines[:0]
			var respObj struct {
				Result json.RawMessage `json:"result"`
				Error  *rpcError       `json:"error"`
				ID     interface{}     `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal([]byte(payload), &respObj); err != nil {
				continue
			}
			if respObj.Method != "" {
				continue
			}
			// match id loosely
			if respObj.ID != nil {
				switch v := respObj.ID.(type) {
				case float64:
					if int64(v) != wantID {
						continue
					}
				case string:
					if v != strconv.FormatInt(wantID, 10) {
						continue
					}
				}
			}
			return respObj.Result, respObj.Error, nil
		}
	}
	// leftover
	if len(dataLines) > 0 {
		payload := strings.Join(dataLines, "\n")
		var respObj struct {
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &respObj); err == nil {
			return respObj.Result, respObj.Error, nil
		}
	}
	return nil, nil, fmt.Errorf("no JSON-RPC result in SSE stream")
}

func (s *httpSession) ListTools(ctx context.Context) ([]ToolDef, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	var result struct {
		Tools []struct {
			Name        string                 `json:"name"`
			Description string                 `json:"description"`
			InputSchema map[string]interface{} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := s.call(ctx, "tools/list", map[string]interface{}{}, &result); err != nil {
		return nil, err
	}
	out := make([]ToolDef, 0, len(result.Tools))
	for _, t := range result.Tools {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		out = append(out, ToolDef{
			Server: s.name, Name: t.Name, MarbleName: ToolName(s.name, t.Name),
			Description: t.Description, InputSchema: schema,
		})
	}
	return out, nil
}

func (s *httpSession) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	if args == nil {
		args = map[string]interface{}{}
	}
	params := map[string]interface{}{"name": name, "arguments": args}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := s.call(ctx, "tools/call", params, &result); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range result.Content {
		if c.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(c.Text)
		}
	}
	out := b.String()
	if result.IsError {
		return "", fmt.Errorf("%s", out)
	}
	if out == "" {
		raw, _ := json.Marshal(result)
		return string(raw), nil
	}
	return out, nil
}

func (s *httpSession) ListResources(ctx context.Context) ([]ResourceDef, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	var result struct {
		Resources []struct {
			URI         string `json:"uri"`
			Name        string `json:"name"`
			Description string `json:"description"`
			MimeType    string `json:"mimeType"`
		} `json:"resources"`
	}
	if err := s.call(ctx, "resources/list", map[string]interface{}{}, &result); err != nil {
		return nil, nil
	}
	out := make([]ResourceDef, 0, len(result.Resources))
	for _, r := range result.Resources {
		out = append(out, ResourceDef{Server: s.name, URI: r.URI, Name: r.Name, Description: r.Description, MimeType: r.MimeType})
	}
	return out, nil
}

func (s *httpSession) ReadResource(ctx context.Context, uri string) (string, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	var result struct {
		Contents []struct {
			Text     string `json:"text"`
			Blob     string `json:"blob"`
			MimeType string `json:"mimeType"`
		} `json:"contents"`
	}
	if err := s.call(ctx, "resources/read", map[string]interface{}{"uri": uri}, &result); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range result.Contents {
		if c.Text != "" {
			b.WriteString(c.Text)
		} else if c.Blob != "" {
			fmt.Fprintf(&b, "[blob mime=%s]", c.MimeType)
		}
	}
	return b.String(), nil
}

func (s *httpSession) ListPrompts(ctx context.Context) ([]PromptDef, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	var result struct {
		Prompts []struct {
			Name        string      `json:"name"`
			Description string      `json:"description"`
			Arguments   []PromptArg `json:"arguments"`
		} `json:"prompts"`
	}
	if err := s.call(ctx, "prompts/list", map[string]interface{}{}, &result); err != nil {
		return nil, nil
	}
	out := make([]PromptDef, 0, len(result.Prompts))
	for _, p := range result.Prompts {
		out = append(out, PromptDef{Server: s.name, Name: p.Name, Description: p.Description, Arguments: p.Arguments})
	}
	return out, nil
}

func (s *httpSession) GetPrompt(ctx context.Context, name string, args map[string]string) (string, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	params := map[string]interface{}{"name": name}
	if args != nil {
		params["arguments"] = args
	}
	var result struct {
		Description string `json:"description"`
		Messages    []struct {
			Role    string `json:"role"`
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := s.call(ctx, "prompts/get", params, &result); err != nil {
		return "", err
	}
	var b strings.Builder
	if result.Description != "" {
		fmt.Fprintf(&b, "description: %s\n", result.Description)
	}
	for _, m := range result.Messages {
		fmt.Fprintf(&b, "[%s] %s\n", m.Role, m.Content.Text)
	}
	return b.String(), nil
}
