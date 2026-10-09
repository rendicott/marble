package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/agentproc"
)

// NormalizeAgentCWD cleans a project directory relative to the workspace.
// Empty, ".", and a trailing slash mean the workspace root. Absolute paths
// and ".." that leave the workspace are rejected.
func NormalizeAgentCWD(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	rel = strings.ReplaceAll(rel, "\\", "/")
	if rel == "" || rel == "." || rel == "/" {
		return "", nil
	}
	if strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("agent_cwd contains a NUL")
	}
	// Check before trimming slashes, or "/Users/rini" becomes the relative path "Users/rini".
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "~") {
		return "", fmt.Errorf("agent_cwd must be a path relative to the workspace")
	}
	rel = strings.Trim(rel, "/")
	if rel == "" || rel == "." {
		return "", nil
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("agent_cwd escapes the workspace")
	}
	return clean, nil
}

// resolveRoutedDir picks the directory a routed subprocess runs in.
// An empty rel uses the workspace. That is refused when the workspace is not
// inside a git work tree, so a fresh agent lock on a home directory does not
// start. An explicit relative directory is used even when it is not a git repo;
// gitTop is set when rev-parse succeeds. A subdirectory of a repo is valid.
func resolveRoutedDir(workspace, rel string) (abs, gitTop, refusal string, err error) {
	rel, err = NormalizeAgentCWD(rel)
	if err != nil {
		return "", "", "", err
	}
	workspace = strings.TrimSpace(workspace)
	if rel == "" {
		abs = workspace
	} else {
		if workspace == "" {
			return "", "", "", fmt.Errorf("project directory %q set but no workspace is configured", rel)
		}
		abs, err = agentproc.JoinUnderWorkspace(workspace, rel)
		if err != nil {
			return "", "", "", err
		}
	}
	if abs == "" {
		return "", "", "", fmt.Errorf("no workspace is configured for this agent")
	}
	st, statErr := os.Stat(abs)
	if statErr != nil {
		if rel == "" {
			return "", "", "", fmt.Errorf("workspace %s: %w", abs, statErr)
		}
		return "", "", "", fmt.Errorf("project directory %q (%s): %w", rel, abs, statErr)
	}
	if !st.IsDir() {
		return "", "", "", fmt.Errorf("working directory %s is not a directory", abs)
	}
	top, ok := gitToplevel(abs)
	if rel == "" && !ok {
		return abs, "", routedWorkspaceRefusal(abs), nil
	}
	if ok {
		gitTop = top
	}
	return abs, gitTop, "", nil
}

func routedWorkspaceRefusal(workspace string) string {
	if strings.TrimSpace(workspace) == "" {
		workspace = "(none)"
	}
	return "This session's working directory is " + workspace + ", which is not inside a git work tree. Set a project directory relative to the workspace in the field under the composer, then send again. The agent was not started."
}

// gitToplevel runs `git rev-parse --show-toplevel` in dir.
// GIT_DIR and GIT_WORK_TREE from the harness environment are ignored so a
// checkout is detected from dir itself. Failure, including git missing, is
// "not a work tree".
func gitToplevel(dir string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel")
	cmd.Env = gitProbeEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return "", false
	}
	return top, true
}

func gitProbeEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(src)+1)
	for _, e := range src {
		if strings.HasPrefix(e, "GIT_DIR=") || strings.HasPrefix(e, "GIT_WORK_TREE=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0")
}

// routedFirstTurn is true when the transcript has no assistant reply yet.
// The current user message is already stored when a turn starts.
func routedFirstTurn(s *Session) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	users, assistants := 0, 0
	for _, m := range s.ui {
		switch m.Role {
		case "user":
			users++
		case "assistant":
			assistants++
		}
	}
	return users <= 1 && assistants == 0
}
