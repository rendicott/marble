package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// stdioSession is an MCP server over stdio.
type stdioSession struct {
	name    string
	cmd     *exec.Cmd
	conn    *jsonRPCConn
	timeout time.Duration
}

// ConnectStdio starts command and performs MCP initialize.
func ConnectStdio(ctx context.Context, name string, sc ServerConfig, timeout time.Duration) (Session, error) {
	if sc.Command == "" {
		return nil, fmt.Errorf("stdio server %q: command required", name)
	}
	cmd := exec.CommandContext(ctx, sc.Command, sc.Args...)
	if sc.Cwd != "" {
		cmd.Dir = sc.Cwd
	}
	// minimal env: optional PATH etc from parent + listed keys
	env := os.Environ()
	// filter? keep parent for node/npx; overlay sc.Env
	for k, v := range sc.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr // surface child errors to harness log
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	conn := newJSONRPCConn(name, stdout, stdin)
	to := defaultTimeout(timeout)
	initCtx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	if err := mcpInitialize(initCtx, conn, "marble-harness"); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = conn.Close()
		return nil, fmt.Errorf("initialize %s: %w", name, err)
	}
	return &stdioSession{name: name, cmd: cmd, conn: conn, timeout: to}, nil
}

func (s *stdioSession) Name() string { return s.name }

func (s *stdioSession) Close() error {
	_ = s.conn.Close()
	if s.cmd != nil && s.cmd.Process != nil {
		pgid := s.cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_ = s.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			_ = s.cmd.Wait()
		}
	}
	return nil
}

func (s *stdioSession) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.timeout)
}

func (s *stdioSession) ListTools(ctx context.Context) ([]ToolDef, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	var result struct {
		Tools []struct {
			Name        string                 `json:"name"`
			Description string                 `json:"description"`
			InputSchema map[string]interface{} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := s.conn.Call(ctx, "tools/list", map[string]interface{}{}, &result); err != nil {
		return nil, err
	}
	out := make([]ToolDef, 0, len(result.Tools))
	for _, t := range result.Tools {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		out = append(out, ToolDef{
			Server:      s.name,
			Name:        t.Name,
			MarbleName:  ToolName(s.name, t.Name),
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return out, nil
}

func (s *stdioSession) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	if args == nil {
		args = map[string]interface{}{}
	}
	params := map[string]interface{}{
		"name":      name,
		"arguments": args,
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := s.conn.Call(ctx, "tools/call", params, &result); err != nil {
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
		out = string(raw)
	}
	return out, nil
}

func (s *stdioSession) ListResources(ctx context.Context) ([]ResourceDef, error) {
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
	if err := s.conn.Call(ctx, "resources/list", map[string]interface{}{}, &result); err != nil {
		// optional capability
		return nil, nil
	}
	out := make([]ResourceDef, 0, len(result.Resources))
	for _, r := range result.Resources {
		out = append(out, ResourceDef{
			Server: s.name, URI: r.URI, Name: r.Name,
			Description: r.Description, MimeType: r.MimeType,
		})
	}
	return out, nil
}

func (s *stdioSession) ReadResource(ctx context.Context, uri string) (string, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	params := map[string]interface{}{"uri": uri}
	var result struct {
		Contents []struct {
			URI      string `json:"uri"`
			MimeType string `json:"mimeType"`
			Text     string `json:"text"`
			Blob     string `json:"blob"`
		} `json:"contents"`
	}
	if err := s.conn.Call(ctx, "resources/read", params, &result); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range result.Contents {
		if c.Text != "" {
			b.WriteString(c.Text)
		} else if c.Blob != "" {
			fmt.Fprintf(&b, "[blob mime=%s len=%d]", c.MimeType, len(c.Blob))
		}
	}
	return b.String(), nil
}

func (s *stdioSession) ListPrompts(ctx context.Context) ([]PromptDef, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	var result struct {
		Prompts []struct {
			Name        string      `json:"name"`
			Description string      `json:"description"`
			Arguments   []PromptArg `json:"arguments"`
		} `json:"prompts"`
	}
	if err := s.conn.Call(ctx, "prompts/list", map[string]interface{}{}, &result); err != nil {
		return nil, nil
	}
	out := make([]PromptDef, 0, len(result.Prompts))
	for _, p := range result.Prompts {
		out = append(out, PromptDef{
			Server: s.name, Name: p.Name, Description: p.Description, Arguments: p.Arguments,
		})
	}
	return out, nil
}

func (s *stdioSession) GetPrompt(ctx context.Context, name string, args map[string]string) (string, error) {
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
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := s.conn.Call(ctx, "prompts/get", params, &result); err != nil {
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
