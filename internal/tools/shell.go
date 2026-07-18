package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/rendicott/marble/internal/shellpolicy"
)

type shellArgs struct {
	Command    string `json:"command"`
	CWD        string `json:"cwd"`
	TimeoutSec int    `json:"timeout_sec"`
}

func (r *Registry) shellExecute(argsJSON string, tc *TurnContext) (string, error) {
	var a shellArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Command) == "" {
		return "", fmt.Errorf("command is required")
	}
	pol := r.Policy
	if pol == nil {
		return "", fmt.Errorf("shell policy not configured")
	}
	if err := pol.Check(a.Command, a.CWD); err != nil {
		return "", err
	}
	cwd, err := pol.ResolveCWD(a.CWD)
	if err != nil {
		return "", err
	}
	timeout, hint := pol.ClampTimeout(a.TimeoutSec)

	parent := context.Background()
	if tc != nil && tc.Ctx != nil {
		parent = tc.Ctx
	}

	bin, flag := shellpolicy.ShellBinary()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, flag, a.Command)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr bytes.Buffer
	maxOut := pol.MaxOutput
	if maxOut <= 0 {
		maxOut = 512 * 1024
	}
	cmd.Stdout = &capWriter{buf: &stdout, max: maxOut}
	cmd.Stderr = &capWriter{buf: &stderr, max: maxOut}

	start := time.Now()
	err = cmd.Run()
	dur := time.Since(start)

	exit := 0
	killed := false
	killedStop := false
	if ctx.Err() != nil {
		killed = true
		// ADR-0010 Q9: kill process group SIGTERM then SIGKILL (best-effort)
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			time.Sleep(150 * time.Millisecond)
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		exit = -1
		if parent.Err() != nil && parent.Err() != context.DeadlineExceeded {
			killedStop = true
		}
	} else if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
			err = nil
		} else {
			return "", err
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "exit=%d duration=%s killed_timeout=%v killed_stop=%v\n",
		exit, dur.Round(time.Millisecond), killed && !killedStop, killedStop)
	if hint != "" {
		fmt.Fprintf(&b, "note: %s\n", hint)
	}
	fmt.Fprintf(&b, "--- stdout ---\n%s\n", stdout.String())
	if stderr.Len() > 0 {
		fmt.Fprintf(&b, "--- stderr ---\n%s\n", stderr.String())
	}
	return b.String(), nil
}

type capWriter struct {
	buf *bytes.Buffer
	max int
	n   int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.n >= w.max {
		return len(p), nil
	}
	remain := w.max - w.n
	if len(p) > remain {
		_, _ = w.buf.Write(p[:remain])
		w.n = w.max
		return len(p), nil
	}
	n, err := w.buf.Write(p)
	w.n += n
	return n, err
}

type bgStartArgs struct {
	Command string `json:"command"`
	CWD     string `json:"cwd"`
	Label   string `json:"label"`
}

func (r *Registry) startBG(argsJSON string, tc *TurnContext) (string, error) {
	if r.BG == nil {
		return "", fmt.Errorf("background tasks not configured")
	}
	if tc == nil || tc.SessionID == "" {
		return "", fmt.Errorf("session context required")
	}
	var a bgStartArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	t, err := r.BG.Start(tc.SessionID, a.Command, a.CWD, a.Label)
	if err != nil {
		return "", err
	}
	return mustJSON(map[string]interface{}{
		"task_id":    t.ID,
		"status":     t.Status,
		"pid":        t.PID,
		"started_at": t.StartedAt,
		"command":    t.Command,
	}), nil
}

type bgKillArgs struct {
	TaskID string `json:"task_id"`
	Signal string `json:"signal"`
}

func (r *Registry) killBG(argsJSON string) (string, error) {
	if r.BG == nil {
		return "", fmt.Errorf("background tasks not configured")
	}
	var a bgKillArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	force := strings.EqualFold(a.Signal, "kill")
	if err := r.BG.Kill(a.TaskID, force); err != nil {
		return "", err
	}
	return fmt.Sprintf("signal sent to task %s", a.TaskID), nil
}

type bgCheckArgs struct {
	TaskID    string `json:"task_id"`
	TailLines int    `json:"tail_lines"`
}

func (r *Registry) checkBG(argsJSON string, tc *TurnContext) (string, error) {
	if r.BG == nil {
		return "", fmt.Errorf("background tasks not configured")
	}
	var a bgCheckArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.TaskID == "" {
		if tc == nil {
			return "", fmt.Errorf("session context required to list tasks")
		}
		list := r.BG.List(tc.SessionID)
		type row struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Command  string `json:"command"`
			ExitCode *int   `json:"exit_code,omitempty"`
			Label    string `json:"label,omitempty"`
		}
		var rows []row
		for _, t := range list {
			rows = append(rows, row{ID: t.ID, Status: string(t.Status), Command: t.Command, ExitCode: t.ExitCode, Label: t.Label})
		}
		return mustJSON(rows), nil
	}
	t, ok := r.BG.Get(a.TaskID)
	if !ok {
		return "", fmt.Errorf("task not found")
	}
	out := map[string]interface{}{
		"task_id":    t.ID,
		"session_id": t.SessionID,
		"status":     t.Status,
		"command":    t.Command,
		"pid":        t.PID,
		"exit_code":  t.ExitCode,
		"started_at": t.StartedAt,
		"ended_at":   t.EndedAt,
		"error":      t.Error,
	}
	if a.TailLines > 0 {
		out["stdout_tail"] = tailLines(t.Stdout, a.TailLines)
		out["stderr_tail"] = tailLines(t.Stderr, a.TailLines)
	}
	return mustJSON(out), nil
}

func tailLines(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

type contArgs struct {
	Prompt      string `json:"prompt"`
	DelaySec    int    `json:"delay_sec"`
	WaitForTask string `json:"wait_for_task"`
	Label       string `json:"label"`
}

func (r *Registry) scheduleContinuation(argsJSON string, tc *TurnContext) (string, error) {
	if r.Cont == nil {
		return "", fmt.Errorf("continuations not configured")
	}
	if tc == nil || tc.SessionID == "" {
		return "", fmt.Errorf("session context required")
	}
	var a contArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	j, err := r.Cont.Schedule(tc.SessionID, a.Prompt, a.DelaySec, a.WaitForTask, a.Label)
	if err != nil {
		return "", err
	}
	return mustJSON(map[string]interface{}{
		"continuation_id": j.ID,
		"fire_at":         j.FireAt,
		"wait_for_task":   j.WaitTask,
		"prompt":          j.Prompt,
	}), nil
}
