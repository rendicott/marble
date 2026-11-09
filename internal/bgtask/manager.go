package bgtask

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/rendicott/marble/internal/config"
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
	// Combined is the end of stdout and stderr interleaved in arrival order.
	Combined string `json:"-"`
	Error     string    `json:"error,omitempty"`

	cmd    *exec.Cmd
	cancel context.CancelFunc
	// Live capture; nil once the task finishes and Stdout/Stderr/Combined are final.
	stdoutBuf, stderrBuf, combinedBuf *outputBuffer
}

// copyLiveOutput fills dst's output fields from the live buffers. Call with Manager.mu held.
func (t *Task) copyLiveOutput(dst *Task) {
	if t.stdoutBuf == nil {
		return
	}
	dst.Stdout = t.stdoutBuf.String()
	dst.Stderr = t.stderrBuf.String()
	dst.Combined = t.combinedBuf.String()
}

// Manager tracks background tasks per session.
type Manager struct {
	mu       sync.Mutex
	tasks    map[string]*Task
	bySess   map[string]map[string]struct{}
	policy   *shellpolicy.Policy
	maxPerS  int
	maxOut   int
	// Notify (optional) is called with the session id after a task starts or
	// finishes, outside the lock (live UI chips). Set before first use.
	Notify func(sessionID string)
}

// New creates a manager (max 8 running per session per ADR-0005).
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
	running := 0
	for tid := range m.bySess[sessionID] {
		if t, ok := m.tasks[tid]; ok && t.Status == StatusRunning {
			running++
		}
	}
	if running >= m.maxPerS {
		m.mu.Unlock()
		return nil, fmt.Errorf("max concurrent background tasks (%d) for session", m.maxPerS)
	}
	id := memory.NewSessionID() // short id
	bin, flag := shellpolicy.ShellBinary()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, flag, command)
	cmd.Dir = cwd
	cmd.Env = config.ChildEnv(nil) // Settings → Secrets as $NAME, live
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout := newOutputBuffer(m.maxOut/4, m.maxOut-m.maxOut/4)
	stderr := newOutputBuffer(m.maxOut/4, m.maxOut-m.maxOut/4)
	combined := newOutputBuffer(0, combinedTailBytes)
	cmd.Stdout = io.MultiWriter(stdout, combined)
	cmd.Stderr = io.MultiWriter(stderr, combined)

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

		stdoutBuf:   stdout,
		stderrBuf:   stderr,
		combinedBuf: combined,
	}
	m.tasks[id] = t
	m.bySess[sessionID][id] = struct{}{}
	m.mu.Unlock()

	if err := cmd.Start(); err != nil {
		m.finish(t, StatusFailed, nil, err.Error())
		return t, err
	}
	m.mu.Lock()
	t.PID = cmd.Process.Pid
	started := *t
	m.mu.Unlock()
	m.notify(sessionID)

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
		m.finish(t, st, &code, "")
	}()

	return &started, nil
}

func (m *Manager) finish(t *Task, st Status, code *int, errMsg string) {
	m.mu.Lock()
	if t.Status != StatusRunning {
		m.mu.Unlock()
		return
	}
	t.copyLiveOutput(t)
	t.stdoutBuf, t.stderrBuf, t.combinedBuf = nil, nil, nil
	t.Status = st
	t.ExitCode = code
	t.Error = errMsg
	now := time.Now()
	t.EndedAt = &now
	t.cancel = nil
	sid := t.SessionID
	m.mu.Unlock()
	m.notify(sid)
}

func (m *Manager) notify(sessionID string) {
	if m != nil && m.Notify != nil && sessionID != "" {
		m.Notify(sessionID)
	}
}

// Kill sends signal to task process group.
func (m *Manager) Kill(taskID string, force bool) error {
	m.mu.Lock()
	t, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("task not found")
	}
	if t.Status != StatusRunning || t.PID == 0 {
		m.mu.Unlock()
		return fmt.Errorf("task not running")
	}
	pgid := t.PID
	m.mu.Unlock()
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

// Get returns a task snapshot, including the output captured so far while it runs.
func (m *Manager) Get(taskID string) (*Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return nil, false
	}
	cp := *t
	t.copyLiveOutput(&cp)
	return &cp, true
}

// IsRunning reports a task's state without copying its output, for frequent pollers.
func (m *Manager) IsRunning(taskID string) (running, found bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return false, false
	}
	return t.Status == StatusRunning, true
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

// KillAllRunning signals every running background task's process group.
// Non-blocking: SIGTERM immediately, SIGKILL escalate in a short-lived goroutine.
// Intended for harness shutdown / restart so children are not left orphaned.
func (m *Manager) KillAllRunning() int {
	type target struct {
		task   *Task
		pid    int
		cancel context.CancelFunc
	}
	m.mu.Lock()
	var running []target
	for _, t := range m.tasks {
		if t.Status == StatusRunning && t.PID != 0 {
			running = append(running, target{t, t.PID, t.cancel})
		}
	}
	m.mu.Unlock()
	for _, r := range running {
		pgid := r.pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		// cancel context if present
		if r.cancel != nil {
			r.cancel()
		}
		go func(pid int, task *Task) {
			time.Sleep(2 * time.Second)
			m.mu.Lock()
			still := task.Status == StatusRunning
			m.mu.Unlock()
			if still {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}(pgid, r.task)
	}
	return len(running)
}

// MarkOrphansOnBoot marks all running as failed (no restart survival).
func (m *Manager) MarkOrphansOnBoot() {
	// in-memory only; nothing to recover
}

// combinedTailBytes bounds the interleaved tail; enough for "last N lines" of output.
const combinedTailBytes = 64 * 1024

// outputBuffer keeps the first maxHead and the last maxTail bytes of a stream, so
// the end of a noisy command's output is really its end. Safe for concurrent use.
type outputBuffer struct {
	mu      sync.Mutex
	maxHead int
	maxTail int
	head    []byte
	tail    []byte // ring once full: oldest byte at pos
	pos     int
	dropped int64
}

func newOutputBuffer(maxHead, maxTail int) *outputBuffer {
	return &outputBuffer{maxHead: maxHead, maxTail: maxTail}
}

func (b *outputBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.maxHead - len(b.head); room > 0 {
		k := min(room, len(p))
		b.head = append(b.head, p[:k]...)
		p = p[k:]
	}
	if len(p) >= b.maxTail {
		b.dropped += int64(len(b.tail)) + int64(len(p)-b.maxTail)
		b.tail = append(b.tail[:0], p[len(p)-b.maxTail:]...)
		b.pos = 0
		return n, nil
	}
	if room := b.maxTail - len(b.tail); room > 0 {
		k := min(room, len(p))
		b.tail = append(b.tail, p[:k]...)
		p = p[k:]
	}
	for len(p) > 0 {
		k := copy(b.tail[b.pos:], p)
		b.dropped += int64(k)
		b.pos = (b.pos + k) % b.maxTail
		p = p[k:]
	}
	return n, nil
}

// String returns head and tail, with a marker where bytes were discarded. The
// oldest tail line is dropped when partial, so a cut never starts mid-line.
func (b *outputBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	tail := append(append(make([]byte, 0, len(b.tail)), b.tail[b.pos:]...), b.tail[:b.pos]...)
	if b.dropped == 0 {
		return string(b.head) + string(tail)
	}
	if i := bytes.IndexByte(tail, '\n'); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
	}
	if len(b.head) == 0 {
		return string(tail)
	}
	return fmt.Sprintf("%s\n…[%d bytes omitted]…\n%s", b.head, b.dropped, tail)
}

// Ensure we don't leave zombies: on process exit Wait is called in Start.
var _ = os.DevNull
