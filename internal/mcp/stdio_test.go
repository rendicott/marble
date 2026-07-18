package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mock MCP server script: newline-delimited JSON (modern MCP SDK framing)
func writeMockServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mock_mcp.py")
	script := `
import sys, json

def read_msg():
    line = sys.stdin.buffer.readline()
    if not line:
        return None
    line = line.strip()
    if not line:
        return read_msg()
    return json.loads(line)

def write_msg(obj):
    raw = json.dumps(obj).encode()
    sys.stdout.buffer.write(raw + b"\n")
    sys.stdout.buffer.flush()

while True:
    msg = read_msg()
    if msg is None:
        break
    method = msg.get("method")
    mid = msg.get("id")
    if method == "initialize":
        write_msg({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"mock","version":"0"}}} )
    elif method == "notifications/initialized":
        pass
    elif method == "tools/list":
        write_msg({"jsonrpc":"2.0","id":mid,"result":{"tools":[{"name":"echo","description":"echo args","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}}}]}})
    elif method == "tools/call":
        args = (msg.get("params") or {}).get("arguments") or {}
        text = args.get("text","")
        write_msg({"jsonrpc":"2.0","id":mid,"result":{"content":[{"type":"text","text":"echo:"+text}],"isError":False}})
    elif method == "resources/list":
        write_msg({"jsonrpc":"2.0","id":mid,"result":{"resources":[{"uri":"mock://doc","name":"doc","description":"d"}]}})
    elif method == "resources/read":
        write_msg({"jsonrpc":"2.0","id":mid,"result":{"contents":[{"uri":"mock://doc","text":"resource-body"}]}})
    elif method == "prompts/list":
        write_msg({"jsonrpc":"2.0","id":mid,"result":{"prompts":[{"name":"hi","description":"say hi"}]}})
    elif method == "prompts/get":
        write_msg({"jsonrpc":"2.0","id":mid,"result":{"description":"hi","messages":[{"role":"user","content":{"type":"text","text":"Hello"}}]}})
    else:
        if mid is not None:
            write_msg({"jsonrpc":"2.0","id":mid,"error":{"code":-32601,"message":"unknown"}})
`
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStdioToolsRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required for mock server")
	}
	script := writeMockServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sc := ServerConfig{Command: "python3", Args: []string{script}}
	sess, err := ConnectStdio(ctx, "mock", sc, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	tools, err := sess.ListTools(ctx)
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools: %+v err=%v", tools, err)
	}
	out, err := sess.CallTool(ctx, "echo", map[string]interface{}{"text": "hi"})
	if err != nil || out != "echo:hi" {
		t.Fatalf("call: %q err=%v", out, err)
	}
	res, err := sess.ListResources(ctx)
	if err != nil || len(res) != 1 {
		t.Fatalf("resources: %v %v", res, err)
	}
	body, err := sess.ReadResource(ctx, "mock://doc")
	if err != nil || body != "resource-body" {
		t.Fatalf("read: %q %v", body, err)
	}
	pr, err := sess.ListPrompts(ctx)
	if err != nil || len(pr) != 1 {
		t.Fatalf("prompts: %v %v", pr, err)
	}
	got, err := sess.GetPrompt(ctx, "hi", nil)
	if err != nil || !contains(got, "Hello") {
		t.Fatalf("get prompt: %q %v", got, err)
	}
}

func TestManagerMerge(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	script := writeMockServer(t)
	m := NewManager(5*time.Second, false)
	defer m.Close()
	enabled := true
	fc := FileConfig{MCPServers: map[string]ServerConfig{
		"mock": {Command: "python3", Args: []string{script}, Enabled: &enabled},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m.Start(ctx, fc)
	if m.ToolCount() < 1 {
		t.Fatal("expected tools")
	}
	specs := m.Specs()
	if len(specs) < 1 {
		t.Fatal("expected specs")
	}
	// call via marble name
	var marble string
	for _, s := range specs {
		if s.Function.Name == "mcp_mock_echo" {
			marble = s.Function.Name
		}
	}
	if marble == "" {
		// still find echo
		for _, s := range specs {
			if contains(s.Function.Name, "echo") {
				marble = s.Function.Name
				break
			}
		}
	}
	if marble == "" {
		var ns []string
		for _, s := range specs {
			ns = append(ns, s.Function.Name)
		}
		t.Fatalf("no echo tool in %v", ns)
	}
	args, _ := json.Marshal(map[string]string{"text": "x"})
	out := m.Execute(ctx, marble, string(args))
	if out != "echo:x" {
		t.Fatalf("execute: %q", out)
	}
	// bridge list_resources
	out = m.Execute(ctx, "mcp_mock_list_resources", `{}`)
	if !contains(out, "mock://doc") {
		t.Fatalf("list resources: %s", out)
	}
	if m.ServerOKCount() < 1 {
		t.Fatalf("health: %#v", m.Health())
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
