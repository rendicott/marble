package tts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Default ElevenLabs Flash model (ADR-0027 Q9).
const DefaultElevenLabsModel = "eleven_flash_v2_5"

// Config is $MEMORY/tts.json (ADR-0027).
type Config struct {
	Enabled                bool   `json:"enabled"`
	Provider               string `json:"provider"` // none | elevenlabs | openai
	APIKeyEnv              string `json:"api_key_env"`
	DefaultVoice           string `json:"default_voice"`
	DefaultModel           string `json:"default_model"`
	Format                 string `json:"format"` // mp3
	Cache                  bool   `json:"cache"`
	MaxCharsPerRequest     int    `json:"max_chars_per_request"`
	MaxConcurrent          int    `json:"max_concurrent"`
	EagerOnWonderstandTurn bool   `json:"eager_on_wonderstand_turn"`
}

// DefaultConfig is off — $0 spend until the operator enables TTS.
func DefaultConfig() Config {
	return Config{
		Enabled:                false,
		Provider:               "none",
		APIKeyEnv:              "ELEVENLABS_API_KEY",
		DefaultVoice:           "",
		DefaultModel:           DefaultElevenLabsModel,
		Format:                 "mp3",
		Cache:                  true,
		MaxCharsPerRequest:     4000,
		MaxConcurrent:          2,
		EagerOnWonderstandTurn: false,
	}
}

// ResolveConfigPath picks --tts-config or $MEMORY/tts.json.
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
	return filepath.Join(memoryRoot, "tts.json")
}

// Load reads a TTS config file. Missing file → DefaultConfig (disabled).
func Load(path string) (Config, error) {
	cfg := DefaultConfig()
	path = strings.TrimSpace(path)
	if path == "" {
		return cfg, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return DefaultConfig(), fmt.Errorf("tts config: %w", err)
	}
	cfg = applyDefaults(cfg)
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func applyDefaults(c Config) Config {
	d := DefaultConfig()
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	if c.Provider == "" {
		c.Provider = d.Provider
	}
	if strings.TrimSpace(c.APIKeyEnv) == "" {
		c.APIKeyEnv = d.APIKeyEnv
	}
	if strings.TrimSpace(c.Format) == "" {
		c.Format = d.Format
	}
	if strings.TrimSpace(c.DefaultModel) == "" {
		switch c.Provider {
		case "elevenlabs":
			c.DefaultModel = DefaultElevenLabsModel
		case "openai":
			c.DefaultModel = DefaultOpenAIModel
		}
	}
	if strings.TrimSpace(c.APIKeyEnv) == "" && c.Provider == "openai" {
		c.APIKeyEnv = "OPENAI_API_KEY"
	}
	if c.MaxCharsPerRequest <= 0 {
		c.MaxCharsPerRequest = d.MaxCharsPerRequest
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = d.MaxConcurrent
	}
	return c
}

// Validate checks provider / format when enabled.
func (c Config) Validate() error {
	switch c.Provider {
	case "none", "elevenlabs", "openai":
	default:
		return fmt.Errorf("tts: unknown provider %q", c.Provider)
	}
	format := strings.ToLower(strings.TrimSpace(c.Format))
	if format != "" && format != "mp3" && format != "wav" && format != "ogg" {
		return fmt.Errorf("tts: unsupported format %q", c.Format)
	}
	if c.Enabled && c.Provider == "none" {
		return fmt.Errorf("tts: enabled but provider is none")
	}
	if c.Enabled && strings.TrimSpace(c.APIKeyEnv) == "" {
		return fmt.Errorf("tts: enabled but api_key_env empty")
	}
	return nil
}

// Save writes cfg to path (mode 0600). Creates parent dir if needed.
func Save(path string, cfg Config) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("tts config path empty")
	}
	cfg = applyDefaults(cfg)
	if err := cfg.Validate(); err != nil {
		return err
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
