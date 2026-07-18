package bgtask

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/rendicott/marble/internal/memory"
	"github.com/rendicott/marble/internal/shellpolicy"
)

// Status of a background task.
type Status string

const (
	StatusRunning Status = "running"
	StatusExited  Status = "exited"
	StatusKilled  Status = "killed"
	StatusFailed  Status = "failed"
)

// Task is a long-running session-scoped command.
type Task struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Label     string    `json:"label,omitempty"`
	Command   string    `json:"command"`
	CWD       string    `json:"cwd"`
	Status    Status    `json:"status"`
	PID       int       `json:"pid,omitempty"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Stdout    string    `json:"-"`
	Stderr    string    `json:"-"`
	Error     string    `json:"error,omitempty"`

	cmd    *exec.Cmd
	cancel context.CancelFunc
}

// Manager tracks background tasks per session.
type Manager struct {
	mu       sync.Mutex
	tasks    map[string]*Task
	bySess   map[string]map[string]struct{}
	policy   *shellpolicy.Policy
	maxPerS  int
	maxOut   int
}

// New creates a manager (max 8 concurrent per session per ADR-0005).
func New(policy *shellpolicy.Policy) *Manager {
	return &Manager{
		tasks:   make(map[string]*Task),
		bySess:  make(map[string]map[string]struct{}),
		policy:  policy,
		maxPerS: 8,
		maxOut:  512 * 1024,
	}
}

// Start launches a background command.
func (m *Manager) Start(sessionID, command, cwdRel, label string) (*Task, error) {
	if m.policy != nil {
		if err := m.policy.Check(command, cwdRel); err != nil {
			return nil, err
		}
	}
	cwd := ""
	var err error
	if m.policy != nil {
		cwd, err = m.policy.ResolveCWD(cwdRel)
		if err != nil {
			return nil, err
		}
	} else {
		cwd = cwdRel
	}

	m.mu.Lock()
	if m.bySess[sessionID] == nil {
		m.bySess[sessionID] = make(map[string]struct{})
	}
	if len(m.bySess[sessionID]) >= m.maxPerS {
		m.mu.Unlock()
		return nil, fmt.Errorf("max concurrent background tasks (%d) for session", m.maxPerS)
	}
	id := memory.NewSessionID() // short id
	bin, flag := shellpolicy.ShellBinary()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, flag, command)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{buf: &stdout, max: m.maxOut}
	cmd.Stderr = &limitedWriter{buf: &stderr, max: m.maxOut}

	t := &Task{
		ID:        id,
		SessionID: sessionID,
		Label:     label,
		Command:   command,
		CWD:       cwd,
		Status:    StatusRunning,
		StartedAt: time.Now(),
		cmd:       cmd,
		cancel:    cancel,
	}
	m.tasks[id] = t
	m.bySess[sessionID][id] = struct{}{}
	m.mu.Unlock()

	if err := cmd.Start(); err != nil {
		m.finish(t, StatusFailed, nil, err.Error())
		return t, err
	}
	t.PID = cmd.Process.Pid

	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				m.finish(t, StatusFailed, nil, err.Error())
				return
			}
		}
		st := StatusExited
		// if cancelled, mark killed
		select {
		case <-ctx.Done():
			st = StatusKilled
		default:
		}
		m.mu.Lock()
		t.Stdout = stdout.String()
		t.Stderr = stderr.String()
		m.mu.Unlock()
		m.finish(t, st, &code, "")
	}()

	return t, nil
}

func (m *Manager) finish(t *Task, st Status, code *int, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t.Status != StatusRunning {
		return
	}
	t.Status = st
	t.ExitCode = code
	t.Error = errMsg
	now := time.Now()
	t.EndedAt = &now
	t.cancel = nil
}

// Kill sends signal to task process group.
func (m *Manager) Kill(taskID string, force bool) error {
	m.mu.Lock()
	t, ok := m.tasks[taskID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("task not found")
	}
	if t.Status != StatusRunning || t.cmd == nil || t.cmd.Process == nil {
		return fmt.Errorf("task not running")
	}
	pgid := t.cmd.Process.Pid
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(-pgid, sig)
	if !force {
		go func() {
			time.Sleep(5 * time.Second)
			m.mu.Lock()
			still := t.Status == StatusRunning
			m.mu.Unlock()
			if still {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
		}()
	}
	return nil
}

// Get returns a task snapshot.
func (m *Manager) Get(taskID string) (*Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return nil, false
	}
	cp := *t
	return &cp, true
}

// List session tasks.
func (m *Manager) List(sessionID string) []*Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Task
	for id := range m.bySess[sessionID] {
		if t, ok := m.tasks[id]; ok {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out
}

// KillSession terminates all running tasks for a session.
func (m *Manager) KillSession(sessionID string) {
	for _, t := range m.List(sessionID) {
		if t.Status == StatusRunning {
			_ = m.Kill(t.ID, false)
		}
	}
}

// MarkOrphansOnBoot marks all running as failed (no restart survival).
func (m *Manager) MarkOrphansOnBoot() {
	// in-memory only; nothing to recover
}

type limitedWriter struct {
	buf *bytes.Buffer
	max int
	n   int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
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

// Ensure we don't leave zombies: on process exit Wait is called in Start.
var _ = os.DevNull
