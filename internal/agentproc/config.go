package agentproc

import (
	"encoding/json"
	"os"
	"path/filepath"
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
}

// DriverConfig configures one harness CLI.
type DriverConfig struct {
	Enabled              bool     `json:"enabled"`
	Command              string   `json:"command"`
	DefaultOutputFormat  string   `json:"default_output_format"`
	DefaultArgs          []string `json:"default_args"`
	AutoApprove          *bool    `json:"auto_approve"` // nil = true
	Env                  map[string]string `json:"env"`
}

// DefaultConfig returns ADR-0014 defaults.
func DefaultConfig() Config {
	t := true
	return Config{
		Drivers: map[string]DriverConfig{
			"grok": {
				Enabled:             true,
				Command:             "grok",
				DefaultOutputFormat: "json",
				DefaultArgs:         nil,
				AutoApprove:         &t,
			},
			"claude": {
				Enabled:             true,
				Command:             "claude",
				DefaultOutputFormat: "json",
				DefaultArgs:         nil,
				AutoApprove:         &t,
			},
		},
		DefaultTimeoutSec:   1800, // 30m
		MaxTimeoutSec:       7200, // 2h
		MaxPerSession:       10,
		MaxOutputBytes:      1 << 20,
		SystemAgentsEnabled: false,
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
	if cfg.Drivers == nil {
		cfg.Drivers = def.Drivers
	}
	for name, d := range def.Drivers {
		if _, ok := cfg.Drivers[name]; !ok {
			cfg.Drivers[name] = d
		}
	}
	return cfg, nil
}

// ConfigPath returns $MEMORY/agent_process.json
func ConfigPath(memoryRoot string) string {
	return filepath.Join(memoryRoot, "agent_process.json")
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
