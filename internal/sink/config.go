package sink

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// KnownTypes is the first-pass sink type set (ADR-0028 Q6).
var KnownTypes = []string{"orb", "webhook", "slack", "discord", "ntfy", "stdout"}

var sinkIDRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

// Config is $MEMORY/sinks.json (ADR-0028).
type Config struct {
	DeepLinkBase string     `json:"deep_link_base"`
	Sinks        []SinkSpec `json:"sinks"`
}

// SinkSpec is one configured destination. Type-specific fields share one struct
// so Settings can round-trip without a per-type schema fork.
type SinkSpec struct {
	ID           string  `json:"id"`
	Type         string  `json:"type"`
	Enabled      bool    `json:"enabled"`
	DeepLinkBase string  `json:"deep_link_base,omitempty"`
	Filters      Filters `json:"filters"`
	SecretEnv    string  `json:"secret_env,omitempty"`

	// orb
	TopicID     string `json:"topic_id,omitempty"`
	APIBase     string `json:"api_base,omitempty"`
	TitlePrefix string `json:"title_prefix,omitempty"`

	// webhook
	URL      string            `json:"url,omitempty"`
	Method   string            `json:"method,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Template string            `json:"template,omitempty"`

	// slack / discord
	Channel   string `json:"channel,omitempty"`
	Username  string `json:"username,omitempty"`
	IconEmoji string `json:"icon_emoji,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`

	// ntfy
	Server   string `json:"server,omitempty"`
	Topic    string `json:"topic,omitempty"`
	Priority int    `json:"priority,omitempty"`

	// stdout
	Format string `json:"format,omitempty"` // plain | json

	// ValidationNote is computed on load/apply; not an operator-authored field.
	ValidationNote string `json:"validation_note,omitempty"`
}

// DefaultConfig is empty — zero sinks, zero behavior change.
func DefaultConfig() Config {
	return Config{Sinks: []SinkSpec{}}
}

// ResolveConfigPath picks --sinks-config or $MEMORY/sinks.json.
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
	return filepath.Join(memoryRoot, "sinks.json")
}

// Load reads sinks.json. Missing file → DefaultConfig.
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
		return DefaultConfig(), fmt.Errorf("sinks config: %w", err)
	}
	cfg.Normalize()
	return cfg, nil
}

// Save writes cfg to path (mode 0600).
func Save(path string, cfg Config) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("sinks config path empty")
	}
	cfg.Normalize()
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

// Normalize trims fields and fills validation notes. Invalid sinks stay in the
// file but EffectiveEnabled() is false.
func (c *Config) Normalize() {
	if c == nil {
		return
	}
	c.DeepLinkBase = strings.TrimRight(strings.TrimSpace(c.DeepLinkBase), "/")
	if c.Sinks == nil {
		c.Sinks = []SinkSpec{}
	}
	seen := map[string]int{}
	for i := range c.Sinks {
		c.Sinks[i].normalize()
		id := c.Sinks[i].ID
		if id == "" {
			continue
		}
		if n, ok := seen[id]; ok {
			if c.Sinks[i].ValidationNote == "" {
				c.Sinks[i].ValidationNote = fmt.Sprintf("duplicate id %q (first at index %d)", id, n)
			}
		} else {
			seen[id] = i
		}
	}
}

func (s *SinkSpec) normalize() {
	s.ID = strings.TrimSpace(s.ID)
	s.Type = strings.ToLower(strings.TrimSpace(s.Type))
	s.DeepLinkBase = strings.TrimRight(strings.TrimSpace(s.DeepLinkBase), "/")
	s.SecretEnv = strings.TrimSpace(s.SecretEnv)
	s.TopicID = strings.TrimSpace(s.TopicID)
	s.APIBase = strings.TrimRight(strings.TrimSpace(s.APIBase), "/")
	s.URL = strings.TrimSpace(s.URL)
	s.Method = strings.ToUpper(strings.TrimSpace(s.Method))
	s.Channel = strings.TrimSpace(s.Channel)
	s.Username = strings.TrimSpace(s.Username)
	s.IconEmoji = strings.TrimSpace(s.IconEmoji)
	s.AvatarURL = strings.TrimSpace(s.AvatarURL)
	s.Server = strings.TrimRight(strings.TrimSpace(s.Server), "/")
	s.Topic = strings.TrimSpace(s.Topic)
	s.Format = strings.ToLower(strings.TrimSpace(s.Format))
	s.Filters.normalize()
	s.ValidationNote = ""
	if err := s.validate(); err != nil {
		s.ValidationNote = err.Error()
	}
}

func (s SinkSpec) validate() error {
	if s.ID == "" {
		return fmt.Errorf("id required")
	}
	if !sinkIDRe.MatchString(s.ID) {
		return fmt.Errorf("id %q must match %s", s.ID, sinkIDRe.String())
	}
	switch s.Type {
	case "orb", "webhook", "slack", "discord", "ntfy", "stdout":
	case "":
		return fmt.Errorf("type required")
	default:
		return fmt.Errorf("unknown type %q", s.Type)
	}
	if err := s.Filters.validate(); err != nil {
		return err
	}
	switch s.Type {
	case "orb":
		if s.TopicID == "" {
			return fmt.Errorf("orb: topic_id required")
		}
		if s.SecretEnv == "" {
			return fmt.Errorf("orb: secret_env required")
		}
	case "webhook":
		if s.URL == "" {
			return fmt.Errorf("webhook: url required")
		}
		if strings.TrimSpace(s.Template) == "" {
			return fmt.Errorf("webhook: template required")
		}
		if s.Method != "" && s.Method != "POST" && s.Method != "PUT" && s.Method != "PATCH" {
			return fmt.Errorf("webhook: method %q not supported", s.Method)
		}
	case "slack":
		if s.SecretEnv == "" {
			return fmt.Errorf("slack: secret_env required (webhook URL)")
		}
	case "discord":
		if s.SecretEnv == "" {
			return fmt.Errorf("discord: secret_env required (webhook URL)")
		}
	case "ntfy":
		if s.Topic == "" {
			return fmt.Errorf("ntfy: topic required")
		}
		if s.Priority != 0 && (s.Priority < 1 || s.Priority > 5) {
			return fmt.Errorf("ntfy: priority must be 1–5")
		}
	case "stdout":
		if s.Format != "" && s.Format != "plain" && s.Format != "json" {
			return fmt.Errorf("stdout: format must be plain or json")
		}
	}
	if s.Template != "" {
		if _, err := parseTemplate("sink-"+s.ID, s.Template); err != nil {
			return fmt.Errorf("template: %w", err)
		}
	}
	return nil
}

// EffectiveEnabled is the global default after validation.
func (s SinkSpec) EffectiveEnabled() bool {
	return s.Enabled && s.ValidationNote == ""
}

func boolPtr(v bool) *bool { return &v }

func maxHeaderRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return truncateRunes(s, n)
}
