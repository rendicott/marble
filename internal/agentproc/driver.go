package agentproc

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Request is a normalized call from the Marble tool.
type Request struct {
	Format       string // grok | claude
	Prompt       string
	CWD          string // absolute path already jailed
	OutputFormat string // plain | json
	TimeoutSec   int
	Model        string
	ExtraArgs    []string
	Background   bool
	// Preset overlays (ADR-0030): optional command/args from a named preset.
	CommandOverride     string
	ArgsOverride        []string
	IgnoreDriverEnabled bool
	// ContextBlock is prepended to Prompt in prepare (ADR-0031). Empty = no inject.
	ContextBlock  string
	ContextMarker string
}

// Result is returned to the Marble model (and stored on BG completion).
type Result struct {
	Format     string      `json:"format"`
	OK         bool        `json:"ok"`
	ExitCode   int         `json:"exit_code"`
	DurationMs int64       `json:"duration_ms"`
	CWD        string      `json:"cwd"`
	Summary    string      `json:"summary"`
	Raw        interface{} `json:"raw,omitempty"`
	StderrTail string      `json:"stderr_tail,omitempty"`
	Command    []string    `json:"command,omitempty"`
	Error      string      `json:"error,omitempty"`
}

// Driver builds argv and parses vendor output.
type Driver interface {
	Name() string
	SupportsJSON() bool
	BuildArgv(req Request, cfg DriverConfig) (argv []string, err error)
	Parse(stdout, stderr string, exitCode int) Result
}

var driverRegistry = map[string]func() Driver{
	"grok":     func() Driver { return grokDriver{} },
	"claude":   func() Driver { return claudeDriver{} },
	"opencode": func() Driver { return opencodeDriver{} },
}

// RegisterDriver adds or replaces a driver factory (tests / future adapters).
func RegisterDriver(name string, fn func() Driver) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || fn == nil {
		return
	}
	driverRegistry[name] = fn
}

// KnownDrivers returns registered driver names.
func KnownDrivers() []string {
	out := make([]string, 0, len(driverRegistry))
	for k := range driverRegistry {
		out = append(out, k)
	}
	return out
}

func driverFor(format string) (Driver, error) {
	key := strings.ToLower(strings.TrimSpace(format))
	fn, ok := driverRegistry[key]
	if !ok || fn == nil {
		return nil, fmt.Errorf("unknown format %q (supported: grok, claude, opencode)", format)
	}
	return fn(), nil
}

type grokDriver struct{}

func (grokDriver) Name() string       { return "grok" }
func (grokDriver) SupportsJSON() bool { return true }

func (grokDriver) BuildArgv(req Request, cfg DriverConfig) ([]string, error) {
	cmd := cfg.Command
	if cmd == "" {
		cmd = "grok"
	}
	path, err := exec.LookPath(cmd)
	if err != nil {
		return nil, fmt.Errorf("grok binary %q not found on PATH: %w", cmd, err)
	}
	argv := []string{path, "-p", req.Prompt}
	of := req.OutputFormat
	if of == "" {
		of = cfg.DefaultOutputFormat
	}
	if of == "" {
		of = "json"
	}
	if of == "json" || of == "plain" || of == "streaming-json" {
		argv = append(argv, "--output-format", of)
	}
	if req.CWD != "" {
		argv = append(argv, "--cwd", req.CWD)
	}
	if req.Model != "" {
		argv = append(argv, "-m", req.Model)
	}
	if cfg.AutoApproveEnabled() {
		argv = append(argv, "--always-approve")
	}
	argv = append(argv, cfg.DefaultArgs...)
	// Extra args override defaults for the same flag (avoid "cannot be used multiple times").
	argv = append(argv, filterExtra(req.ExtraArgs, grokExtraAllow)...)
	argv = dedupeFlagsLastWins(argv)
	return argv, nil
}

func (grokDriver) Parse(stdout, stderr string, exitCode int) Result {
	r := Result{
		Format:     "grok",
		OK:         exitCode == 0,
		ExitCode:   exitCode,
		StderrTail: tail(stderr, 2000),
	}
	s := strings.TrimSpace(stdout)
	// Try JSON (grok --output-format json)
	var raw interface{}
	if err := json.Unmarshal([]byte(s), &raw); err == nil {
		r.Raw = raw
		r.Summary = extractSummary(raw, s)
		return r
	}
	// NDJSON / streaming-json: last object or join text fields
	if lines := strings.Split(s, "\n"); len(lines) > 1 {
		var texts []string
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var obj map[string]interface{}
			if json.Unmarshal([]byte(line), &obj) == nil {
				if t := firstString(obj, "result", "text", "content", "message", "response"); t != "" {
					texts = append(texts, t)
				}
				r.Raw = obj
			}
		}
		if len(texts) > 0 {
			r.Summary = strings.Join(texts, "\n")
			return r
		}
	}
	r.Summary = s
	return r
}

type claudeDriver struct{}

func (claudeDriver) Name() string       { return "claude" }
func (claudeDriver) SupportsJSON() bool { return true }

func (claudeDriver) BuildArgv(req Request, cfg DriverConfig) ([]string, error) {
	cmd := cfg.Command
	if cmd == "" {
		cmd = "claude"
	}
	path, err := exec.LookPath(cmd)
	if err != nil {
		return nil, fmt.Errorf("claude binary %q not found on PATH: %w", cmd, err)
	}
	// claude -p is the common print/headless mode
	argv := []string{path, "-p", req.Prompt}
	of := req.OutputFormat
	if of == "" {
		of = cfg.DefaultOutputFormat
	}
	if of == "json" {
		// Claude Code often supports --output-format json in print mode
		argv = append(argv, "--output-format", "json")
	}
	if req.Model != "" {
		argv = append(argv, "--model", req.Model)
	}
	if cfg.AutoApproveEnabled() {
		// Non-interactive: avoid permission prompts (flags vary by version)
		argv = append(argv, "--dangerously-skip-permissions")
	}
	argv = append(argv, cfg.DefaultArgs...)
	argv = append(argv, filterExtra(req.ExtraArgs, claudeExtraAllow)...)
	argv = dedupeFlagsLastWins(argv)
	return argv, nil
}

func (claudeDriver) Parse(stdout, stderr string, exitCode int) Result {
	r := Result{
		Format:     "claude",
		OK:         exitCode == 0,
		ExitCode:   exitCode,
		StderrTail: tail(stderr, 2000),
	}
	s := strings.TrimSpace(stdout)
	var raw interface{}
	if err := json.Unmarshal([]byte(s), &raw); err == nil {
		r.Raw = raw
		r.Summary = extractSummary(raw, s)
		return r
	}
	r.Summary = s
	return r
}

var grokExtraAllow = map[string]bool{
	"--max-turns": true, "--check": true, "--disable-web-search": true,
	"--no-subagents": true, "--worktree": true, "--worktree-ref": true,
	"--ref": true, "--rules": true, "--verbatim": true, "--tools": true,
	"--sandbox": true, "--reasoning-effort": true, "--effort": true,
	"--no-plan": true, "--permission-mode": true, "--debug-file": true,
}

var claudeExtraAllow = map[string]bool{
	"--max-turns": true, "--allowedTools": true, "--disallowedTools": true,
	"--append-system-prompt": true,
}

func filterExtra(extra []string, allow map[string]bool) []string {
	if len(extra) == 0 {
		return nil
	}
	var out []string
	for i := 0; i < len(extra); i++ {
		a := extra[i]
		key := a
		if strings.Contains(a, "=") {
			key = strings.SplitN(a, "=", 2)[0]
		}
		if !allow[key] {
			// skip unknown flags for safety
			continue
		}
		out = append(out, a)
		// value as next arg for --flag value form
		if !strings.Contains(a, "=") && i+1 < len(extra) && !strings.HasPrefix(extra[i+1], "-") {
			out = append(out, extra[i+1])
			i++
		}
	}
	return out
}

// dedupeFlagsLastWins collapses repeated CLI flags so DefaultArgs + ExtraArgs
// don't pass e.g. --max-turns twice (grok exits 2: "cannot be used multiple times").
// Keeps first non-flag tokens (binary, -p, prompt) in order; for each flag key,
// the last occurrence (and its value) wins.
func dedupeFlagsLastWins(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	type item struct {
		flag string
		val  string // empty if boolean flag
		hasV bool
	}
	var head []string
	var flags []item
	seen := map[string]int{} // flag key -> index in flags

	i := 0
	// Keep program path and everything until we've passed -p <prompt> if present.
	for i < len(argv) {
		a := argv[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			break
		}
		head = append(head, a)
		i++
	}
	for i < len(argv) {
		a := argv[i]
		if a == "-p" || a == "--" {
			head = append(head, a)
			i++
			if i < len(argv) {
				head = append(head, argv[i])
				i++
			}
			continue
		}
		if !strings.HasPrefix(a, "-") {
			head = append(head, a)
			i++
			continue
		}
		key := a
		val := ""
		hasV := false
		if strings.Contains(a, "=") {
			parts := strings.SplitN(a, "=", 2)
			key = parts[0]
			val = parts[1]
			hasV = true
			// store as separate flag+value for clarity
			a = key
		} else if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
			// value-taking flags we know about
			switch key {
			case "--output-format", "--cwd", "-m", "--model",
				"--max-turns", "--effort", "--reasoning-effort",
				"--worktree", "--worktree-ref", "--ref", "--rules",
				"--tools", "--sandbox", "--permission-mode", "--debug-file",
				"--allowedTools", "--disallowedTools", "--append-system-prompt",
				"--format", "--dir", "--agent", "--title":
				hasV = true
				val = argv[i+1]
				i++
			}
		}
		it := item{flag: a, val: val, hasV: hasV}
		if idx, ok := seen[key]; ok {
			flags[idx] = it
		} else {
			seen[key] = len(flags)
			flags = append(flags, it)
		}
		i++
	}
	out := append([]string{}, head...)
	for _, it := range flags {
		out = append(out, it.flag)
		if it.hasV {
			out = append(out, it.val)
		}
	}
	return out
}

func extractSummary(raw interface{}, fallback string) string {
	switch v := raw.(type) {
	case string:
		return v
	case map[string]interface{}:
		if t := firstString(v, "result", "text", "content", "message", "response", "output"); t != "" {
			return t
		}
		// nested message.content
		if m, ok := v["message"].(map[string]interface{}); ok {
			if t := firstString(m, "content", "text"); t != "" {
				return t
			}
		}
		b, _ := json.MarshalIndent(v, "", "  ")
		return string(b)
	default:
		b, _ := json.MarshalIndent(v, "", "  ")
		if len(b) > 0 {
			return string(b)
		}
	}
	return fallback
}

func firstString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				if strings.TrimSpace(t) != "" {
					return t
				}
			}
		}
	}
	return ""
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
