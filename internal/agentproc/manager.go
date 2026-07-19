package agentproc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rendicott/marble/internal/memory"
)

// Status of a background agent process.
type Status string

const (
	StatusRunning Status = "running"
	StatusExited  Status = "exited"
	StatusKilled  Status = "killed"
	StatusFailed  Status = "failed"
)

// Task tracks one external agent invocation.
type Task struct {
	ID        string     `json:"id"`
	SessionID string     `json:"session_id"`
	Format    string     `json:"format"`
	Prompt    string     `json:"prompt"`
	CWD       string     `json:"cwd"`
	Status    Status     `json:"status"`
	PID       int        `json:"pid,omitempty"`
	ExitCode  *int       `json:"exit_code,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Result    *Result    `json:"result,omitempty"`
	Error     string     `json:"error,omitempty"`
	Command   []string   `json:"command,omitempty"`

	cmd    *exec.Cmd
	cancel context.CancelFunc
}

// Manager runs and tracks call_agent_process invocations (ADR-0014).
type Manager struct {
	mu       sync.Mutex
	cfg      Config
	tasks    map[string]*Task
	bySess   map[string]map[string]struct{}
	active   map[string]int // sessionID → running count (sync + bg)
	wsRoot   string
	memRoot  string
}

// New loads config from memory root and binds workspace jail.
func New(memoryRoot, workspace string) (*Manager, error) {
	cfg, err := Load(ConfigPath(memoryRoot))
	if err != nil {
		return nil, err
	}
	absWS, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	absMem, _ := filepath.Abs(memoryRoot)
	return &Manager{
		cfg:     cfg,
		tasks:   make(map[string]*Task),
		bySess:  make(map[string]map[string]struct{}),
		active:  make(map[string]int),
		wsRoot:  absWS,
		memRoot: absMem,
	}, nil
}

// Config returns loaded config.
func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// SystemAgentsEnabled reports whether system sessions may call agents.
func (m *Manager) SystemAgentsEnabled() bool {
	return m.Config().SystemAgentsEnabled
}

// ResolveCWD resolves cwd/workdir under workspace; creates workdir if set.
func (m *Manager) ResolveCWD(cwdRel, workdir string) (string, error) {
	base := m.wsRoot
	if cwdRel != "" && cwdRel != "." {
		p, err := jailJoin(m.wsRoot, cwdRel)
		if err != nil {
			return "", err
		}
		base = p
	}
	if workdir != "" {
		p, err := jailJoin(base, workdir)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			return "", fmt.Errorf("workdir: %w", err)
		}
		return p, nil
	}
	st, err := os.Stat(base)
	if err != nil {
		return "", fmt.Errorf("cwd: %w", err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("cwd is not a directory")
	}
	return base, nil
}

// RunSync executes the agent and waits (honors ctx cancel / turn Stop).
func (m *Manager) RunSync(ctx context.Context, sessionID string, req Request) (Result, error) {
	argv, cwd, dcfg, err := m.prepare(req)
	if err != nil {
		return Result{}, err
	}
	if err := m.acquire(sessionID); err != nil {
		return Result{}, err
	}
	defer m.release(sessionID)

	timeout := m.clampTimeout(req.TimeoutSec)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = mergeEnv(dcfg.Env)

	var stdout, stderr bytes.Buffer
	maxOut := m.maxOut()
	cmd.Stdout = &limitedBuf{buf: &stdout, max: maxOut}
	cmd.Stderr = &limitedBuf{buf: &stderr, max: maxOut}

	err = cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
			err = nil
		} else {
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
				time.Sleep(150 * time.Millisecond)
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			drv, _ := driverFor(req.Format)
			res := drv.Parse(stdout.String(), stderr.String(), -1)
			res.DurationMs = time.Since(start).Milliseconds()
			res.CWD = cwd
			res.Command = argv
			res.Format = req.Format
			res.OK = false
			if runCtx.Err() != nil {
				res.Error = runCtx.Err().Error()
			} else {
				res.Error = err.Error()
			}
			return res, nil
		}
	}

	drv, _ := driverFor(req.Format)
	res := drv.Parse(stdout.String(), stderr.String(), code)
	res.DurationMs = time.Since(start).Milliseconds()
	res.CWD = cwd
	res.Command = argv
	res.Format = req.Format
	return res, nil
}

// StartBackground launches without waiting; returns task id for polling.
func (m *Manager) StartBackground(sessionID string, req Request) (*Task, error) {
	argv, cwd, dcfg, err := m.prepare(req)
	if err != nil {
		return nil, err
	}
	if err := m.acquire(sessionID); err != nil {
		return nil, err
	}

	timeout := m.clampTimeout(req.TimeoutSec)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = mergeEnv(dcfg.Env)

	var stdout, stderr bytes.Buffer
	maxOut := m.maxOut()
	cmd.Stdout = &limitedBuf{buf: &stdout, max: maxOut}
	cmd.Stderr = &limitedBuf{buf: &stderr, max: maxOut}

	id := memory.NewSessionID()
	t := &Task{
		ID:        id,
		SessionID: sessionID,
		Format:    req.Format,
		Prompt:    req.Prompt,
		CWD:       cwd,
		Status:    StatusRunning,
		StartedAt: time.Now(),
		Command:   argv,
		cmd:       cmd,
		cancel:    cancel,
	}

	m.mu.Lock()
	m.tasks[id] = t
	if m.bySess[sessionID] == nil {
		m.bySess[sessionID] = make(map[string]struct{})
	}
	m.bySess[sessionID][id] = struct{}{}
	m.mu.Unlock()

	if err := cmd.Start(); err != nil {
		m.finishTask(t, StatusFailed, nil, nil, err.Error())
		m.release(sessionID)
		return t, err
	}
	t.PID = cmd.Process.Pid

	go func() {
		waitErr := cmd.Wait()
		code := 0
		st := StatusExited
		if waitErr != nil {
			if ee, ok := waitErr.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				m.finishTask(t, StatusFailed, nil, nil, waitErr.Error())
				m.release(sessionID)
				return
			}
		}
		select {
		case <-ctx.Done():
			st = StatusKilled
		default:
		}
		drv, _ := driverFor(req.Format)
		res := drv.Parse(stdout.String(), stderr.String(), code)
		res.DurationMs = time.Since(t.StartedAt).Milliseconds()
		res.CWD = cwd
		res.Command = argv
		res.Format = req.Format
		m.finishTask(t, st, &code, &res, "")
		m.release(sessionID)
	}()

	return t, nil
}

// Get returns a task snapshot.
func (m *Manager) Get(id string) (*Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return nil, false
	}
	cp := *t
	if t.Result != nil {
		r := *t.Result
		cp.Result = &r
	}
	return &cp, true
}

// List session agent tasks.
func (m *Manager) List(sessionID string) []*Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Task
	for id := range m.bySess[sessionID] {
		if t, ok := m.tasks[id]; ok {
			cp := *t
			if t.Result != nil {
				r := *t.Result
				cp.Result = &r
			}
			out = append(out, &cp)
		}
	}
	return out
}

// Kill terminates a background agent task.
func (m *Manager) Kill(taskID string, force bool) error {
	m.mu.Lock()
	t, ok := m.tasks[taskID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("agent task not found")
	}
	if t.Status != StatusRunning || t.cmd == nil || t.cmd.Process == nil {
		return fmt.Errorf("agent task not running")
	}
	if t.cancel != nil {
		t.cancel()
	}
	pgid := t.cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	if force {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	} else {
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

// KillSession kills all running agent tasks for a session.
func (m *Manager) KillSession(sessionID string) {
	for _, t := range m.List(sessionID) {
		if t.Status == StatusRunning {
			_ = m.Kill(t.ID, true)
		}
	}
}

func (m *Manager) prepare(req Request) (argv []string, cwd string, dcfg DriverConfig, err error) {
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		return nil, "", dcfg, fmt.Errorf("format is required (grok|claude)")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, "", dcfg, fmt.Errorf("prompt is required")
	}
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()

	dcfg, ok := cfg.Drivers[format]
	if !ok {
		return nil, "", dcfg, fmt.Errorf("driver %q not configured", format)
	}
	if !dcfg.Enabled {
		return nil, "", dcfg, fmt.Errorf("driver %q is disabled in agent_process.json", format)
	}

	drv, err := driverFor(format)
	if err != nil {
		return nil, "", dcfg, err
	}
	if req.OutputFormat == "" {
		req.OutputFormat = "json"
		if dcfg.DefaultOutputFormat != "" {
			req.OutputFormat = dcfg.DefaultOutputFormat
		} else if !drv.SupportsJSON() {
			req.OutputFormat = "plain"
		}
	}
	cwd = req.CWD
	if cwd == "" {
		cwd = m.wsRoot
	}
	req.CWD = cwd
	argv, err = drv.BuildArgv(req, dcfg)
	if err != nil {
		return nil, "", dcfg, err
	}
	return argv, cwd, dcfg, nil
}

func (m *Manager) clampTimeout(sec int) time.Duration {
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	timeout := cfg.DefaultTimeout()
	if sec > 0 {
		timeout = time.Duration(sec) * time.Second
	}
	if max := cfg.MaxTimeout(); timeout > max {
		timeout = max
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return timeout
}

func (m *Manager) maxOut() int {
	m.mu.Lock()
	n := m.cfg.MaxOutputBytes
	m.mu.Unlock()
	if n <= 0 {
		return 1 << 20
	}
	return n
}

func (m *Manager) acquire(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	max := m.cfg.MaxPerSession
	if max <= 0 {
		max = 10
	}
	if m.active[sessionID] >= max {
		return fmt.Errorf("max concurrent agent processes (%d) for session", max)
	}
	m.active[sessionID]++
	return nil
}

func (m *Manager) release(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active[sessionID] > 0 {
		m.active[sessionID]--
	}
}

func (m *Manager) finishTask(t *Task, st Status, code *int, res *Result, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t.Status != StatusRunning {
		return
	}
	t.Status = st
	t.ExitCode = code
	t.Result = res
	t.Error = errMsg
	now := time.Now()
	t.EndedAt = &now
	t.cancel = nil
}

func mergeEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func jailJoin(root, rel string) (string, error) {
	if rel == "" || rel == "." {
		return root, nil
	}
	var abs string
	if filepath.IsAbs(rel) {
		var err error
		abs, err = filepath.Abs(rel)
		if err != nil {
			return "", err
		}
	} else {
		abs = filepath.Clean(filepath.Join(root, rel))
	}
	relTo, err := filepath.Rel(root, abs)
	if err != nil || relTo == ".." || strings.HasPrefix(relTo, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return abs, nil
}

type limitedBuf struct {
	buf *bytes.Buffer
	max int
	n   int
}

func (w *limitedBuf) Write(p []byte) (int, error) {
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
