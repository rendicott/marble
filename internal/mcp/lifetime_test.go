package mcp

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Regression: a stdio server must outlive the dial context. Both callers (startup in
// cmd/marble-harness/main.go and the settings reload in internal/api/settings.go)
// cancel their ctx immediately after Start/Reload returns. When the child was started
// with exec.CommandContext, that cancel killed every stdio server seconds after
// "connected", and every later tool call failed with "write |1: broken pipe".
func TestStdioSurvivesDialContextCancel(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required for mock server")
	}
	script := writeMockServer(t)
	enabled := true
	sc := ServerConfig{Command: "python3", Args: []string{script}, Enabled: &enabled}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	sess, err := ConnectStdio(ctx, "mock", sc, 5*time.Second)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	cancel() // emulate startup / settings-reload cancelling the dial ctx
	time.Sleep(400 * time.Millisecond)

	out, err := sess.CallTool(context.Background(), "echo", map[string]interface{}{"text": "survived"})
	if err != nil {
		t.Fatalf("call after dial-ctx cancel: %v", err)
	}
	if out != "echo:survived" {
		t.Fatalf("got %q", out)
	}
}

// Self-heal: if a child dies anyway (OOM, crash), the next tool call respawns it
// instead of failing the turn with a broken pipe.
func TestManagerRespawnsDeadChild(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required for mock server")
	}
	script := writeMockServer(t)
	enabled := true
	fc := FileConfig{MCPServers: map[string]ServerConfig{
		"mock": {Command: "python3", Args: []string{script}, Enabled: &enabled},
	}}
	m := NewManager(5*time.Second, false)
	defer m.Close()
	m.Start(context.Background(), fc)

	if got := m.Execute(context.Background(), "mcp_mock_echo", `{"text":"one"}`); !strings.Contains(got, "echo:one") {
		t.Fatalf("first call: %q", got)
	}

	// Murder the child behind the manager's back.
	m.mu.RLock()
	ss, ok := m.sessions["mock"].(*stdioSession)
	m.mu.RUnlock()
	if !ok || ss == nil {
		t.Fatal("no stdio session")
	}
	_ = ss.cmd.Process.Kill()
	_, _ = ss.cmd.Process.Wait()

	if got := m.Execute(context.Background(), "mcp_mock_echo", `{"text":"two"}`); !strings.Contains(got, "echo:two") {
		t.Fatalf("call after child death should respawn, got %q", got)
	}
}
