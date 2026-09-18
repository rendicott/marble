package agentproc

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// Probe is the result of a cheap host detection (ADR-0030 Q3).
type Probe struct {
	Detected bool
	Path     string
	Version  string
}

// ProbeCommand resolves command on PATH and runs `<cmd> --version` (~3s).
// LookPath success is enough to mark detected even if --version fails.
func ProbeCommand(command string) Probe {
	command = strings.TrimSpace(command)
	if command == "" {
		return Probe{}
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return Probe{}
	}
	p := Probe{Detected: true, Path: path}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	_ = cmd.Run()
	p.Version = firstVersionLine(buf.String())
	return p
}

func firstVersionLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 120 {
				line = line[:120]
			}
			return line
		}
	}
	return ""
}
