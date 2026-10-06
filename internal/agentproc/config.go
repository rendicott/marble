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
	// StuckAfterSec: while status=running, if no liveness signal (session state,
	// process I/O/CPU, child processes, output, cwd mtime) has advanced for this many
	// seconds, poll reports stuck_hint (default 480 = 8m). 0 → default. ADR-0035.
	StuckAfterSec int `json:"stuck_after_sec"`
	// AliveWindowSec: a signal advance younger than this reports progress.alive
	// (default 120). Advisory; does not gate stuck_hint.
	AliveWindowSec int `json:"alive_window_sec"`
	// Liveness tunes the generic process signals (ADR-0035).
	Liveness LivenessConfig `json:"liveness"`
	// StuckKill: if true, auto-kill process group when stuck_hint would fire.
	StuckKill bool `json:"stuck_kill"`
	// Context is the global default source-list for subprocess injection (ADR-0031).
	Context ContextConfig `json:"context"`
}

// LivenessConfig holds rate thresholds for process-tree liveness signals.
// Idle CLIs tick counters slightly (timers, event loops), so a signal counts as
// an advance only above these rates.
type LivenessConfig struct {
	MinIOBytesPerSec int     `json:"min_io_bytes_per_sec"` // rchar+wchar across the tree; default 4096
	MinCPUPercent    float64 `json:"min_cpu_percent"`      // utime+stime across the tree; default 25 (real compute only)
	SampleSec        int     `json:"sample_sec"`           // background sample interval; default 5
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
	// SessionIDFlag pins the child's session id (e.g. "-s", "--session-id") so its
	// state files can be found. "" → built-in default for known drivers; "none" disables.
	SessionIDFlag string `json:"session_id_flag,omitempty"`
	// StateGlobs locate the child's per-session state (files or dirs). "{session_id}"
	// is substituted; "~/" and $VARS expand (a pattern naming an unset var is skipped).
	StateGlobs []string `json:"state_globs,omitempty"`
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
				AutoApprove:   &t,
				SessionIDFlag: "-s",
				StateGlobs: []string{
					"$GROK_HOME/sessions/*/{session_id}",
					"~/.grok/sessions/*/{session_id}",
				},
			},
			"claude": {
				Enabled:             true,
				Command:             "claude",
				DefaultOutputFormat: "json",
				DefaultArgs:         nil,
				AutoApprove:         &t,
				SessionIDFlag:       "--session-id",
				StateGlobs: []string{
					"$CLAUDE_CONFIG_DIR/projects/*/{session_id}.jsonl",
					"~/.claude/projects/*/{session_id}.jsonl",
				},
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
		StuckAfterSec:       480, // 8m with no liveness advance → stuck_hint
		AliveWindowSec:      120,
		StuckKill:           false,
		Liveness:            DefaultLivenessConfig(),
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
	if cfg.AliveWindowSec <= 0 {
		cfg.AliveWindowSec = def.AliveWindowSec
	}
	cfg.Liveness = cfg.Liveness.withDefaults()
	if cfg.Drivers == nil {
		cfg.Drivers = def.Drivers
	}
	for name, d := range def.Drivers {
		cur, ok := cfg.Drivers[name]
		if !ok {
			cfg.Drivers[name] = d
			continue
		}
		// Older agent_process.json files predate session pinning; inherit defaults.
		if cur.SessionIDFlag == "" && len(cur.StateGlobs) == 0 {
			cur.SessionIDFlag = d.SessionIDFlag
			cur.StateGlobs = d.StateGlobs
			cfg.Drivers[name] = cur
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

// AliveWindow returns the window within which a signal advance reports alive.
func (c Config) AliveWindow() time.Duration {
	sec := c.AliveWindowSec
	if sec <= 0 {
		sec = 120
	}
	return time.Duration(sec) * time.Second
}

// DefaultLivenessConfig returns thresholds above observed idle noise (ADR-0035):
// idle I/O ~125 B/s (grok) to ~675 B/s (claude); active ≥ 8.8 KB/s. Idle claude
// CPU spikes to ~8.5% — as high as when streaming — so CPU only counts real compute.
func DefaultLivenessConfig() LivenessConfig {
	return LivenessConfig{MinIOBytesPerSec: 4096, MinCPUPercent: 25, SampleSec: 5}
}

func (l LivenessConfig) withDefaults() LivenessConfig {
	def := DefaultLivenessConfig()
	if l.MinIOBytesPerSec <= 0 {
		l.MinIOBytesPerSec = def.MinIOBytesPerSec
	}
	if l.MinCPUPercent <= 0 {
		l.MinCPUPercent = def.MinCPUPercent
	}
	if l.SampleSec <= 0 {
		l.SampleSec = def.SampleSec
	}
	return l
}

// SessionPinned reports whether this driver should get a pinned session id.
func (d DriverConfig) SessionPinned() bool {
	f := strings.TrimSpace(d.SessionIDFlag)
	return f != "" && f != "none"
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
