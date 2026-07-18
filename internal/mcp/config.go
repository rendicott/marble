package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// FileConfig is the on-disk mcp.json shape (Cursor-compatible).
type FileConfig struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// ServerConfig describes one MCP server (stdio and/or HTTP).
type ServerConfig struct {
	// Stdio
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`

	// HTTP / SSE
	URL       string            `json:"url,omitempty"`
	Transport string            `json:"transport,omitempty"` // http | sse | streamable-http (normalized to http)
	Headers   map[string]string `json:"headers,omitempty"`

	Enabled *bool `json:"enabled,omitempty"` // default true if omitted
}

// IsEnabled reports whether the server should connect.
func (s ServerConfig) IsEnabled() bool {
	if s.Enabled == nil {
		return true
	}
	return *s.Enabled
}

// Kind returns "stdio", "http", or "".
func (s ServerConfig) Kind() string {
	if strings.TrimSpace(s.URL) != "" {
		return "http"
	}
	if strings.TrimSpace(s.Command) != "" {
		return "stdio"
	}
	return ""
}

// LoadFile reads and parses an mcp.json file. Missing file returns empty config (not error).
func LoadFile(path string) (FileConfig, error) {
	var fc FileConfig
	if path == "" {
		return fc, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fc, nil
		}
		return fc, err
	}
	if err := json.Unmarshal(data, &fc); err != nil {
		return fc, fmt.Errorf("mcp config: %w", err)
	}
	if fc.MCPServers == nil {
		fc.MCPServers = map[string]ServerConfig{}
	}
	// Expand env in place
	for name, sc := range fc.MCPServers {
		sc.Command = ExpandEnv(sc.Command)
		sc.Cwd = ExpandEnv(sc.Cwd)
		sc.URL = ExpandEnv(sc.URL)
		for i := range sc.Args {
			sc.Args[i] = ExpandEnv(sc.Args[i])
		}
		for k, v := range sc.Env {
			sc.Env[k] = ExpandEnv(v)
		}
		for k, v := range sc.Headers {
			sc.Headers[k] = ExpandEnv(v)
		}
		fc.MCPServers[name] = sc
	}
	return fc, nil
}

// LoadFileRaw reads mcp.json without expanding environment variables (for Settings UI).
func LoadFileRaw(path string) (FileConfig, error) {
	var fc FileConfig
	if path == "" {
		return FileConfig{MCPServers: map[string]ServerConfig{}}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return FileConfig{MCPServers: map[string]ServerConfig{}}, nil
		}
		return fc, err
	}
	if err := json.Unmarshal(data, &fc); err != nil {
		return fc, fmt.Errorf("mcp config: %w", err)
	}
	if fc.MCPServers == nil {
		fc.MCPServers = map[string]ServerConfig{}
	}
	return fc, nil
}

// SaveFile writes mcp.json atomically (pretty JSON, no env expansion).
func SaveFile(path string, fc FileConfig) error {
	if path == "" {
		return fmt.Errorf("mcp config path empty")
	}
	if fc.MCPServers == nil {
		fc.MCPServers = map[string]ServerConfig{}
	}
	data, err := json.MarshalIndent(fc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ResolveConfigPath picks --mcp-config or $MEMORY/mcp.json.
func ResolveConfigPath(explicit, memoryRoot string) string {
	if strings.TrimSpace(explicit) != "" {
		abs, err := filepath.Abs(explicit)
		if err == nil {
			return abs
		}
		return explicit
	}
	if memoryRoot == "" {
		return ""
	}
	return filepath.Join(memoryRoot, "mcp.json")
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandEnv replaces ${VAR} from the process environment (no shell).
func ExpandEnv(s string) string {
	return envPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := envPattern.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		if v, ok := os.LookupEnv(sub[1]); ok {
			return v
		}
		return "" // missing → empty (fail closed for secrets)
	})
}

// SanitizeName makes an OpenAI-safe tool fragment.
func SanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	if out == "" {
		out = "x"
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

// ToolName builds mcp_<server>_<tool>.
func ToolName(server, tool string) string {
	n := "mcp_" + SanitizeName(server) + "_" + SanitizeName(tool)
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}
