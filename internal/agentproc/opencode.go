package agentproc

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type opencodeDriver struct{}

func (opencodeDriver) Name() string       { return "opencode" }
func (opencodeDriver) SupportsJSON() bool { return true }

func (opencodeDriver) BuildArgv(req Request, cfg DriverConfig) ([]string, error) {
	cmd := cfg.Command
	if cmd == "" {
		cmd = "opencode"
	}
	path, err := exec.LookPath(cmd)
	if err != nil {
		return nil, fmt.Errorf("opencode binary %q not found on PATH: %w", cmd, err)
	}
	argv := []string{path, "run"}
	of := req.OutputFormat
	if of == "" {
		of = cfg.DefaultOutputFormat
	}
	if of == "" {
		of = "json"
	}
	if of == "json" {
		argv = append(argv, "--format", "json")
	}
	if req.CWD != "" {
		argv = append(argv, "--dir", req.CWD)
	}
	if req.Model != "" {
		argv = append(argv, "-m", req.Model)
	}
	if cfg.AutoApproveEnabled() {
		argv = append(argv, "--dangerously-skip-permissions")
	}
	argv = append(argv, cfg.DefaultArgs...)
	argv = append(argv, filterExtra(req.ExtraArgs, opencodeExtraAllow)...)
	argv = dedupeFlagsLastWins(argv)
	argv = append(argv, req.Prompt)
	return argv, nil
}

func (opencodeDriver) Parse(stdout, stderr string, exitCode int) Result {
	r := Result{
		Format:     "opencode",
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
	if lines := strings.Split(s, "\n"); len(lines) > 1 {
		var texts []string
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var obj map[string]interface{}
			if json.Unmarshal([]byte(line), &obj) == nil {
				if t := firstString(obj, "result", "text", "content", "message", "response", "part"); t != "" {
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

var opencodeExtraAllow = map[string]bool{
	"--model": true, "-m": true, "--agent": true, "--format": true,
	"--dir": true, "--title": true, "--file": true, "-f": true,
	"--dangerously-skip-permissions": true,
}
