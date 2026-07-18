package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandEnv(t *testing.T) {
	os.Setenv("MARBLE_TEST_KEY", "secret123")
	defer os.Unsetenv("MARBLE_TEST_KEY")
	got := ExpandEnv("Bearer ${MARBLE_TEST_KEY}")
	if got != "Bearer secret123" {
		t.Fatalf("got %q", got)
	}
	if ExpandEnv("${MISSING_VAR_XYZ}") != "" {
		t.Fatal("missing should be empty")
	}
}

func TestToolName(t *testing.T) {
	n := ToolName("Tavily!", "tavily-search")
	if n != "mcp_tavily_tavily_search" {
		t.Fatalf("got %q", n)
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	body := `{
	  "mcpServers": {
	    "demo": {
	      "command": "echo",
	      "args": ["hi"],
	      "enabled": false
	    }
	  }
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	fc, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sc, ok := fc.MCPServers["demo"]
	if !ok {
		t.Fatal("missing demo")
	}
	if sc.IsEnabled() {
		t.Fatal("expected disabled")
	}
	if sc.Kind() != "stdio" {
		t.Fatalf("kind %s", sc.Kind())
	}
}

func TestResolveConfigPath(t *testing.T) {
	p := ResolveConfigPath("", "/mem")
	if p != "/mem/mcp.json" {
		t.Fatalf("got %s", p)
	}
}
