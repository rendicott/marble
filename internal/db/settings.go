package db

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/rendicott/marble/internal/shellpolicy"
)

// KnownSettings is the allow-list for PUT /api/settings (ADR-0007 Q10).
var KnownSettings = map[string]bool{
	"blob_max_age_days":           true,
	"closed_session_max_age_days": true,
	"db_inline_max_bytes":         true,
	"shell_enabled":               true,
	"shell_mode":                  true,
	"shell_allow_sudo":            true,
	"shell_default_timeout_sec":   true,
	"shell_max_timeout_sec":       true,
	"shell_max_output_bytes":      true,
	"shell_cwd_strict":            true,
	"shell_block_memory_paths":    true,
	"shell_allow_patterns":        true,
	"shell_deny_patterns":         true,
	"tool_round_soft":             true,
	"tool_round_hard":             true,
}

// DefaultSettings returns factory defaults for known keys.
func DefaultSettings() map[string]string {
	deny, _ := json.Marshal(shellpolicy.DefaultDenyPatterns)
	return map[string]string{
		"blob_max_age_days":           "4",
		"closed_session_max_age_days": "4",
		"db_inline_max_bytes":         "32768",
		"shell_enabled":               "true",
		"shell_mode":                  "deny_list",
		"shell_allow_sudo":            "false",
		"shell_default_timeout_sec":   "60",
		"shell_max_timeout_sec":       "300",
		"shell_max_output_bytes":      "524288",
		"shell_cwd_strict":            "true",
		"shell_block_memory_paths":    "true",
		"shell_allow_patterns":        "[]",
		"shell_deny_patterns":         string(deny),
		"tool_round_soft":             "65",
		"tool_round_hard":             "80",
	}
}

// MemorySectionKeys for reset.
var MemorySectionKeys = []string{
	"blob_max_age_days",
	"closed_session_max_age_days",
	"db_inline_max_bytes",
}

// ShellSectionKeys for reset.
var ShellSectionKeys = []string{
	"shell_enabled",
	"shell_mode",
	"shell_allow_sudo",
	"shell_default_timeout_sec",
	"shell_max_timeout_sec",
	"shell_max_output_bytes",
	"shell_cwd_strict",
	"shell_block_memory_paths",
	"shell_allow_patterns",
	"shell_deny_patterns",
}

// ListSettings returns all known keys with current or default values.
func (d *DB) ListSettings() (map[string]string, error) {
	out := DefaultSettings()
	if d == nil || !d.Writable() {
		return out, nil
	}
	rows, err := d.SQL.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return out, err
		}
		if KnownSettings[k] {
			out[k] = v
		}
	}
	// ensure deny patterns default if empty
	if strings.TrimSpace(out["shell_deny_patterns"]) == "" {
		out["shell_deny_patterns"] = DefaultSettings()["shell_deny_patterns"]
	}
	return out, rows.Err()
}

// SetSettings updates known keys only. Returns error if any unknown or invalid.
func (d *DB) SetSettings(updates map[string]string) error {
	if d == nil || !d.Writable() {
		return fmt.Errorf("database not writable")
	}
	var unknown []string
	for k := range updates {
		if !KnownSettings[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("unknown keys: %s", strings.Join(unknown, ", "))
	}
	// validate all first
	for k, v := range updates {
		if err := ValidateSetting(k, v); err != nil {
			return err
		}
	}
	now := UTCNow()
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range updates {
		_, err := tx.Exec(`
			INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at
		`, k, v, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ResetSection resets Memory or Shell keys to factory defaults.
func (d *DB) ResetSection(section string) error {
	defs := DefaultSettings()
	var keys []string
	switch strings.ToLower(section) {
	case "memory", "memory_db", "db":
		keys = MemorySectionKeys
	case "shell":
		keys = ShellSectionKeys
	default:
		return fmt.Errorf("unknown section %q (use memory or shell)", section)
	}
	up := make(map[string]string, len(keys))
	for _, k := range keys {
		up[k] = defs[k]
	}
	return d.SetSettings(up)
}

// ValidateSetting checks a single key/value.
func ValidateSetting(key, value string) error {
	v := strings.TrimSpace(value)
	switch key {
	case "blob_max_age_days", "closed_session_max_age_days":
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 3650 {
			return fmt.Errorf("%s must be integer 0–3650", key)
		}
	case "db_inline_max_bytes", "shell_max_output_bytes":
		n, err := strconv.Atoi(v)
		if err != nil || n < 1024 || n > 50<<20 {
			return fmt.Errorf("%s must be integer 1024–52428800", key)
		}
	case "shell_default_timeout_sec", "shell_max_timeout_sec":
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 86400 {
			return fmt.Errorf("%s must be integer 1–86400", key)
		}
	case "tool_round_soft", "tool_round_hard":
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			return fmt.Errorf("%s must be integer 1–500", key)
		}
	case "shell_enabled", "shell_allow_sudo", "shell_cwd_strict", "shell_block_memory_paths":
		s := strings.ToLower(v)
		if s != "true" && s != "false" && s != "1" && s != "0" && s != "yes" && s != "no" {
			return fmt.Errorf("%s must be boolean", key)
		}
	case "shell_mode":
		if v != "deny_list" && v != "allow_list" {
			return fmt.Errorf("shell_mode must be deny_list or allow_list")
		}
	case "shell_allow_patterns", "shell_deny_patterns":
		var pats []string
		if err := json.Unmarshal([]byte(v), &pats); err != nil {
			return fmt.Errorf("%s must be a JSON string array: %w", key, err)
		}
		if len(v) > 256*1024 {
			return fmt.Errorf("%s too large", key)
		}
		for _, p := range pats {
			if err := validateRegexp(p); err != nil {
				return fmt.Errorf("%s: invalid regex %q: %w", key, p, err)
			}
		}
	default:
		if !KnownSettings[key] {
			return fmt.Errorf("unknown key %s", key)
		}
	}
	return nil
}

func validateRegexp(p string) error {
	if p == "" {
		return fmt.Errorf("empty pattern")
	}
	return reCompile(p)
}
