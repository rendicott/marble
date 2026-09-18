package agentproc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is $MEMORY/agent_process.json (ADR-0014).
type Config struct {
	Drivers             map[string]DriverConfig `json:"drivers"`
	DefaultTimeoutSec   int                     `json:"default_timeout_sec"`
	MaxTimeoutSec       int                     `json:"max_timeout_sec"`
	MaxPerSession       int                     `json:"max_per_session"`
	MaxOutputBytes      int                     `json:"max_output_bytes"`
	SystemAgentsEnabled bool                    `json:"system_agents_enabled"`
	// StuckAfterSec: while status=running, if cwd has no mtime newer than start
	// for this many seconds, poll reports stuck_hint (default 480 = 8m). 0 → default.
	StuckAfterSec int `json:"stuck_after_sec"`
	// StuckKill: if true, auto-kill process group when stuck_hint would fire.
	StuckKill bool `json:"stuck_kill"`
	// Context is the global default source-list for subprocess injection (ADR-0031).
	Context ContextConfig `json:"context"`
}

// ContextConfig is agent_process.json `context` (ADR-0031).
type ContextConfig struct {
	Default  []string `json:"default"`
	MaxChars int      `json:"max_chars"`
}

// DriverConfig configures one harness CLI.
type DriverConfig struct {
	Enabled             bool              `json:"enabled"`
	Command             string            `json:"command"`
	DefaultOutputFormat string            `json:"default_output_format"`
	DefaultArgs         []string          `json:"default_args"`
	AutoApprove         *bool             `json:"auto_approve"` // nil = true
	Env                 map[string]string `json:"env"`
}

// DefaultConfig returns ADR-0014 defaults tuned for headless implement jobs
// (medium effort, turn cap, no-plan) so high-effort explore loops are not the default.
func DefaultConfig() Config {
	t := true
	return Config{
		Drivers: map[string]DriverConfig{
			"grok": {
				Enabled:             true,
				Command:             "grok",
				DefaultOutputFormat: "json",
				DefaultArgs: []string{
					"--no-plan",
					"--effort", "medium",
					"--max-turns", "40",
				},
				AutoApprove: &t,
			},
			"claude": {
				Enabled:             true,
				Command:             "claude",
				DefaultOutputFormat: "json",
				DefaultArgs:         nil,
				AutoApprove:         &t,
			},
			"opencode": {
				Enabled:             true,
				Command:             "opencode",
				DefaultOutputFormat: "json",
				DefaultArgs:         nil,
				AutoApprove:         &t,
			},
		},
		DefaultTimeoutSec:   900,  // 15m
		MaxTimeoutSec:       1800, // 30m
		MaxPerSession:       10,
		MaxOutputBytes:      1 << 20,
		SystemAgentsEnabled: false,
		StuckAfterSec:       480, // 8m with no cwd mtime change → stuck_hint
		StuckKill:           false,
		Context: ContextConfig{
			Default:  DefaultContextSources(),
			MaxChars: DefaultContextMaxChars,
		},
	}
}

// Load reads path or returns defaults if missing.
func Load(path string) (Config, error) {
	cfg := DefaultConfig()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return DefaultConfig(), err
	}
	// merge defaults for zero values
	def := DefaultConfig()
	if cfg.DefaultTimeoutSec <= 0 {
		cfg.DefaultTimeoutSec = def.DefaultTimeoutSec
	}
	if cfg.MaxTimeoutSec <= 0 {
		cfg.MaxTimeoutSec = def.MaxTimeoutSec
	}
	if cfg.MaxPerSession <= 0 {
		cfg.MaxPerSession = def.MaxPerSession
	}
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = def.MaxOutputBytes
	}
	if cfg.StuckAfterSec < 0 {
		cfg.StuckAfterSec = def.StuckAfterSec
	}
	// 0 means "use default" for stuck window (explicit disable: very large value)
	if cfg.StuckAfterSec == 0 {
		cfg.StuckAfterSec = def.StuckAfterSec
	}
	if cfg.Drivers == nil {
		cfg.Drivers = def.Drivers
	}
	for name, d := range def.Drivers {
		if _, ok := cfg.Drivers[name]; !ok {
			cfg.Drivers[name] = d
		}
	}
	if len(cfg.Context.Default) == 0 && cfg.Context.MaxChars == 0 {
		cfg.Context = def.Context
	}
	if cfg.Context.MaxChars <= 0 {
		cfg.Context.MaxChars = DefaultContextMaxChars
	}
	if cfg.Context.Default == nil {
		cfg.Context.Default = DefaultContextSources()
	}
	return cfg, nil
}

// GlobalContextSpec is the process default (Q12 full+memory).
func (c Config) GlobalContextSpec() ContextSpec {
	src := c.Context.Default
	if src == nil {
		src = DefaultContextSources()
	}
	max := c.Context.MaxChars
	if max <= 0 {
		max = DefaultContextMaxChars
	}
	return ContextSpec{Sources: src, MaxChars: max, Set: true}
}

// StuckAfter returns the stuck-detection window.
func (c Config) StuckAfter() time.Duration {
	sec := c.StuckAfterSec
	if sec <= 0 {
		sec = 480
	}
	return time.Duration(sec) * time.Second
}

// ConfigPath returns $MEMORY/agent_process.json
func ConfigPath(memoryRoot string) string {
	return filepath.Join(memoryRoot, "agent_process.json")
}

// Save writes cfg to path (mode 0600).
func Save(path string, cfg Config) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("agent_process.json path empty")
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c Config) DefaultTimeout() time.Duration {
	return time.Duration(c.DefaultTimeoutSec) * time.Second
}

func (c Config) MaxTimeout() time.Duration {
	return time.Duration(c.MaxTimeoutSec) * time.Second
}

func (d DriverConfig) AutoApproveEnabled() bool {
	if d.AutoApprove == nil {
		return true
	}
	return *d.AutoApprove
}
