package shellpolicy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// SettingsSource reads string settings (DB or in-memory map).
type SettingsSource interface {
	SettingString(key, def string) string
	SettingInt(key string, def int) int
	SettingBool(key string, def bool) bool
}

// Policy enforces configurable shell allow/deny rules (ADR-0005).
type Policy struct {
	mu           sync.RWMutex
	Enabled      bool
	Mode         string // deny_list | allow_list
	Deny         []*regexp.Regexp
	Allow        []*regexp.Regexp
	AllowSudo    bool
	DefaultTO    time.Duration
	MaxTO        time.Duration
	MaxOutput    int
	CwdStrict    bool
	BlockMemory  bool
	MemoryRoot   string // absolute --memory path
	Workspace    string
	DisableCLI   bool
	src          SettingsSource
}

// DefaultDenyPatterns are seed deny regexes (catastrophic + privilege + power).
// Normal workspace file deletion (e.g. rm file) is intentionally NOT blocked.
var DefaultDenyPatterns = []string{
	`(?i)\brm\s+(-[a-zA-Z]*f[a-zA-Z]*\s+)?/\s*$`,
	`(?i)\brm\s+-[a-zA-Z]*r[a-zA-Z]*f[a-zA-Z]*\s+/`,
	`(?i)\brm\s+-[a-zA-Z]*f[a-zA-Z]*r[a-zA-Z]*\s+/`,
	`(?i)\bmkfs(\.\w+)?\b`,
	`(?i)\bdd\b.*\bof=/dev/`,
	`(?i)\b(sudo|pkexec|doas)\b`,
	`(?i)(^|[;&|` + "`" + `])\s*su\b`,
	`(?i)\b(shutdown|reboot|poweroff|halt)\b`,
	`(?i)\bsystemctl\s+(poweroff|reboot|halt)\b`,
	`(?i)\binit\s+[06]\b`,
}

// New builds a policy from CLI-ish defaults; call Reload to pull DB settings.
func New(workspace, memoryRoot string, disableCLI bool, defaultTO, maxTO time.Duration) *Policy {
	p := &Policy{
		Enabled:     !disableCLI,
		Mode:        "deny_list",
		AllowSudo:   false,
		DefaultTO:   defaultTO,
		MaxTO:       maxTO,
		MaxOutput:   512 * 1024,
		CwdStrict:   true,
		BlockMemory: true,
		MemoryRoot:  memoryRoot,
		Workspace:   workspace,
		DisableCLI:  disableCLI,
	}
	_ = p.setDenyPatterns(DefaultDenyPatterns)
	return p
}

// BindSettings attaches a settings source and reloads.
func (p *Policy) BindSettings(src SettingsSource) {
	p.mu.Lock()
	p.src = src
	p.mu.Unlock()
	p.Reload()
}

// Reload refreshes from settings source when present.
func (p *Policy) Reload() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.DisableCLI {
		p.Enabled = false
	}
	if p.src == nil {
		return
	}
	if p.DisableCLI {
		p.Enabled = false
	} else {
		p.Enabled = p.src.SettingBool("shell_enabled", true)
	}
	p.Mode = p.src.SettingString("shell_mode", "deny_list")
	if p.Mode != "allow_list" {
		p.Mode = "deny_list"
	}
	p.AllowSudo = p.src.SettingBool("shell_allow_sudo", false)
	p.DefaultTO = time.Duration(p.src.SettingInt("shell_default_timeout_sec", int(p.DefaultTO.Seconds()))) * time.Second
	p.MaxTO = time.Duration(p.src.SettingInt("shell_max_timeout_sec", int(p.MaxTO.Seconds()))) * time.Second
	p.MaxOutput = p.src.SettingInt("shell_max_output_bytes", p.MaxOutput)
	p.CwdStrict = p.src.SettingBool("shell_cwd_strict", true)
	p.BlockMemory = p.src.SettingBool("shell_block_memory_paths", true)

	denyJSON := p.src.SettingString("shell_deny_patterns", "")
	if denyJSON != "" {
		var pats []string
		if err := json.Unmarshal([]byte(denyJSON), &pats); err == nil && len(pats) > 0 {
			_ = p.setDenyPatterns(pats)
		}
	}
	allowJSON := p.src.SettingString("shell_allow_patterns", "[]")
	var allows []string
	_ = json.Unmarshal([]byte(allowJSON), &allows)
	p.Allow = nil
	for _, s := range allows {
		re, err := regexp.Compile(s)
		if err == nil {
			p.Allow = append(p.Allow, re)
		}
	}
}

func (p *Policy) setDenyPatterns(pats []string) error {
	p.Deny = p.Deny[:0]
	for _, s := range pats {
		re, err := regexp.Compile(s)
		if err != nil {
			return err
		}
		p.Deny = append(p.Deny, re)
	}
	return nil
}

// Check returns nil if command may run.
func (p *Policy) Check(command, cwdRel string) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.Enabled {
		return fmt.Errorf("shell is disabled")
	}
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return fmt.Errorf("command is empty")
	}
	if p.Mode == "allow_list" {
		ok := false
		for _, re := range p.Allow {
			if re.MatchString(cmd) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("command not on shell allow list")
		}
	} else {
		for _, re := range p.Deny {
			if re.MatchString(cmd) {
				// sudo special-case
				if p.AllowSudo && regexp.MustCompile(`(?i)\bsudo\b`).MatchString(cmd) &&
					strings.Contains(re.String(), "sudo") {
					continue
				}
				return fmt.Errorf("command blocked by shell deny policy")
			}
		}
	}
	if p.BlockMemory && p.MemoryRoot != "" {
		if strings.Contains(cmd, p.MemoryRoot) {
			return fmt.Errorf("command appears to target memory root (blocked)")
		}
	}
	if p.CwdStrict && cwdRel != "" && cwdRel != "." {
		// path escape checked by resolve in tools
	}
	return nil
}

// ClampTimeout applies default/max rules. Returns timeout and optional hint.
func (p *Policy) ClampTimeout(requestedSec int) (time.Duration, string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	def := p.DefaultTO
	if def <= 0 {
		def = 60 * time.Second
	}
	max := p.MaxTO
	if max <= 0 {
		max = 5 * time.Minute
	}
	if requestedSec <= 0 {
		return def, ""
	}
	d := time.Duration(requestedSec) * time.Second
	hint := ""
	if d > def {
		hint = "consider start_background_task for long-running jobs"
	}
	if d > max {
		return max, fmt.Sprintf("timeout clamped to %s; use start_background_task for longer jobs", max)
	}
	return d, hint
}

// ResolveCWD resolves cwd under workspace.
func (p *Policy) ResolveCWD(cwdRel string) (string, error) {
	p.mu.RLock()
	ws := p.Workspace
	strict := p.CwdStrict
	p.mu.RUnlock()
	if cwdRel == "" || cwdRel == "." {
		return ws, nil
	}
	if filepath.IsAbs(cwdRel) {
		if strict {
			abs, err := filepath.Abs(cwdRel)
			if err != nil {
				return "", err
			}
			if abs != ws && !strings.HasPrefix(abs, ws+string(os.PathSeparator)) {
				return "", fmt.Errorf("cwd outside workspace")
			}
			return abs, nil
		}
		return cwdRel, nil
	}
	clean := filepath.Clean(cwdRel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("cwd escapes workspace")
	}
	full := filepath.Join(ws, clean)
	abs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if abs != ws && !strings.HasPrefix(abs, ws+string(os.PathSeparator)) {
		return "", fmt.Errorf("cwd escapes workspace")
	}
	return abs, nil
}

// ShellBinary returns bash -lc or sh -c argv prefix.
func ShellBinary() (bin string, arg string) {
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash", "-lc"
	}
	return "/bin/sh", "-c"
}
