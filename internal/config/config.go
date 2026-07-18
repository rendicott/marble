package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config holds runtime settings for marble-harness.
type Config struct {
	BaseURL        string
	Model          string
	ContextLimit   int
	MaxOutput      int
	ContextReserve int
	Workspace      string
	Memory         string
	// MemoryCreated is true when --memory did not exist and was created at launch.
	MemoryCreated bool
	PersistEvery  time.Duration
	Addr          string
	// MaxToolIters is the hard stop for tool rounds per user turn (ADR-0005 Q1/Q30).
	MaxToolIters int
	// ToolRoundSoft is the soft advisory threshold (ADR-0005 Q1).
	ToolRoundSoft int
	// SoftWall is continuous tool-round wall clock before advisory (ADR-0005 Q2).
	SoftWall time.Duration
	// ContextWarnRatio soft-warns when usage_ratio >= this (default 0.60).
	ContextWarnRatio float64
	// ContextAutoCompactRatio auto-compact trigger (default 0.85).
	ContextAutoCompactRatio float64
	// ContextAutoCompactRounds consecutive high-usage rounds before auto compact.
	ContextAutoCompactRounds int
	MaxToolResult            int
	// DisableShell forces shell tools off regardless of DB settings.
	DisableShell bool
	// ShellDefaultTimeout / ShellMaxTimeout for shell_execute.
	ShellDefaultTimeout time.Duration
	ShellMaxTimeout     time.Duration
	// MCP (ADR-0006)
	MCPConfig  string // --mcp-config path (empty → $MEMORY/mcp.json)
	MCPDisable bool
	MCPTimeout time.Duration
}

// Budget returns the maximum estimated tokens allowed for prompt material
// (system + tools + messages) before a completion call.
func (c Config) Budget() int {
	b := c.ContextLimit - c.MaxOutput - c.ContextReserve
	if b < 1024 {
		return 1024
	}
	return b
}

// UsageRatio returns est_prompt / budget (ADR-0005 Q5).
func (c Config) UsageRatio(estPrompt int) float64 {
	b := c.Budget()
	if b <= 0 {
		return 1
	}
	return float64(estPrompt) / float64(b)
}

// ParseFlags reads CLI flags into Config.
func ParseFlags(args []string) (Config, error) {
	fs := flag.NewFlagSet("marble-harness", flag.ContinueOnError)
	cfg := Config{}
	home, _ := os.UserHomeDir()
	defaultMemory := filepath.Join(home, ".marble")

	fs.StringVar(&cfg.BaseURL, "base-url", "http://127.0.0.1:8000/v1", "OpenAI-compatible API base URL")
	fs.StringVar(&cfg.Model, "model", "Qwen/Qwen3.5-122B-A10B-FP8", "Model id")
	fs.IntVar(&cfg.ContextLimit, "context-limit", 262144, "Total model context window (tokens)")
	fs.IntVar(&cfg.MaxOutput, "max-output", 32768, "Max generation tokens per model call")
	fs.IntVar(&cfg.ContextReserve, "context-reserve", 8192, "Reserved tokens for tool schemas / formatting")
	fs.StringVar(&cfg.Workspace, "workspace", ".", "Filesystem root for tools")
	fs.StringVar(&cfg.Memory, "memory", defaultMemory, "Memory leaf root (session/ and daily/ live here; may be outside workspace)")
	fs.DurationVar(&cfg.PersistEvery, "persist-interval", 5*time.Minute, "Background session persist interval")
	fs.StringVar(&cfg.Addr, "addr", ":8080", "HTTP listen address")
	fs.IntVar(&cfg.MaxToolIters, "max-tool-iters", 80, "Hard max tool-call rounds per user turn")
	fs.IntVar(&cfg.ToolRoundSoft, "tool-round-soft", 65, "Soft advisory tool-round threshold")
	fs.DurationVar(&cfg.SoftWall, "soft-wall", 3*time.Minute, "Soft wall-clock for continuous tool rounds without final reply")
	fs.IntVar(&cfg.MaxToolResult, "max-tool-result", 32000, "Max characters returned from a single tool call")
	fs.BoolVar(&cfg.DisableShell, "disable-shell", false, "Disable shell_execute and background shell tasks")
	fs.DurationVar(&cfg.ShellDefaultTimeout, "shell-default-timeout", 60*time.Second, "Default shell_execute timeout")
	fs.DurationVar(&cfg.ShellMaxTimeout, "shell-max-timeout", 5*time.Minute, "Max shell_execute timeout")
	fs.StringVar(&cfg.MCPConfig, "mcp-config", "", "Path to mcp.json (default: $MEMORY/mcp.json)")
	fs.BoolVar(&cfg.MCPDisable, "mcp-disable", false, "Disable MCP client entirely")
	fs.DurationVar(&cfg.MCPTimeout, "mcp-timeout", 60*time.Second, "Default timeout for MCP tool/resource/prompt calls")

	// Defaults not exposed as flags (ADR-0005 locked ratios)
	cfg.ContextWarnRatio = 0.60
	cfg.ContextAutoCompactRatio = 0.85
	cfg.ContextAutoCompactRounds = 3

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if cfg.ContextLimit <= 0 || cfg.MaxOutput <= 0 {
		return Config{}, fmt.Errorf("context-limit and max-output must be positive")
	}
	if cfg.PersistEvery <= 0 {
		return Config{}, fmt.Errorf("persist-interval must be positive")
	}
	if cfg.MaxToolIters < 1 {
		return Config{}, fmt.Errorf("max-tool-iters must be positive")
	}
	if cfg.ToolRoundSoft < 1 {
		cfg.ToolRoundSoft = cfg.MaxToolIters
	}
	if cfg.ToolRoundSoft > cfg.MaxToolIters {
		cfg.ToolRoundSoft = cfg.MaxToolIters
	}

	absWS, err := filepath.Abs(cfg.Workspace)
	if err != nil {
		return Config{}, fmt.Errorf("workspace: %w", err)
	}
	info, err := os.Stat(absWS)
	if err != nil {
		return Config{}, fmt.Errorf("workspace: %w", err)
	}
	if !info.IsDir() {
		return Config{}, fmt.Errorf("workspace is not a directory: %s", absWS)
	}
	cfg.Workspace = absWS

	absMem, created, err := resolveMemoryDir(cfg.Memory)
	if err != nil {
		return Config{}, err
	}
	cfg.Memory = absMem
	cfg.MemoryCreated = created
	return cfg, nil
}

// resolveMemoryDir ensures --memory is an absolute directory.
// If it does not exist, it is created (caller should warn). If it exists but is
// not a directory, or creation fails, an error is returned.
func resolveMemoryDir(path string) (abs string, created bool, err error) {
	if strings.TrimSpace(path) == "" {
		return "", false, fmt.Errorf("memory: path is empty")
	}
	abs, err = filepath.Abs(path)
	if err != nil {
		return "", false, fmt.Errorf("memory: %w", err)
	}
	st, err := os.Stat(abs)
	if err == nil {
		if !st.IsDir() {
			return "", false, fmt.Errorf("memory: path exists but is not a directory: %s", abs)
		}
		return abs, false, nil
	}
	if !os.IsNotExist(err) {
		return "", false, fmt.Errorf("memory: cannot stat %s: %w", abs, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", false, fmt.Errorf("memory: directory does not exist and could not be created: %s: %w", abs, err)
	}
	st, err = os.Stat(abs)
	if err != nil || !st.IsDir() {
		return "", false, fmt.Errorf("memory: directory still missing after create: %s", abs)
	}
	return abs, true, nil
}
