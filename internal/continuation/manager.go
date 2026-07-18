package continuation

import (
	"fmt"
	"sync"
	"time"

	"github.com/rendicott/marble/internal/memory"
)

// Job is a scheduled session resume.
type Job struct {
	ID        string     `json:"id"`
	SessionID string     `json:"session_id"`
	Prompt    string     `json:"prompt"`
	Label     string     `json:"label,omitempty"`
	FireAt    time.Time  `json:"fire_at"`
	WaitTask  string     `json:"wait_for_task,omitempty"`
	Cancelled bool       `json:"cancelled"`
	Fired     bool       `json:"fired"`
	CreatedAt time.Time  `json:"created_at"`
}

// FireFunc is invoked when a job should run (session may be busy — caller handles).
type FireFunc func(sessionID, prompt string)

// TaskDoneFunc reports whether a background task has finished.
type TaskDoneFunc func(taskID string) bool

// Manager schedules delayed continuations (in-memory; DB persist optional later).
type Manager struct {
	mu       sync.Mutex
	jobs     map[string]*Job
	bySess   map[string]map[string]struct{}
	maxDelay time.Duration
	onFire   FireFunc
	taskDone TaskDoneFunc
	stop     chan struct{}
}

// New creates a continuation manager.
func New(onFire FireFunc, taskDone TaskDoneFunc) *Manager {
	m := &Manager{
		jobs:     make(map[string]*Job),
		bySess:   make(map[string]map[string]struct{}),
		maxDelay: 24 * time.Hour,
		onFire:   onFire,
		taskDone: taskDone,
		stop:     make(chan struct{}),
	}
	go m.loop()
	return m
}

// Stop shuts down the ticker loop.
func (m *Manager) Stop() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
}

// Schedule creates a job. Requires delaySec > 0 and/or waitTask.
func (m *Manager) Schedule(sessionID, prompt string, delaySec int, waitTask, label string) (*Job, error) {
	prompt = trim(prompt)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if delaySec <= 0 && waitTask == "" {
		return nil, fmt.Errorf("require delay_sec and/or wait_for_task")
	}
	if delaySec > int(m.maxDelay.Seconds()) {
		return nil, fmt.Errorf("delay_sec exceeds max %s", m.maxDelay)
	}
	fireAt := time.Now()
	if delaySec > 0 {
		fireAt = fireAt.Add(time.Duration(delaySec) * time.Second)
	} else {
		// wait-only: poll until task done, with maxDelay ceiling
		fireAt = fireAt.Add(m.maxDelay)
	}
	id := memory.NewSessionID()
	j := &Job{
		ID:        id,
		SessionID: sessionID,
		Prompt:    prompt,
		Label:     label,
		FireAt:    fireAt,
		WaitTask:  waitTask,
		CreatedAt: time.Now(),
	}
	m.mu.Lock()
	m.jobs[id] = j
	if m.bySess[sessionID] == nil {
		m.bySess[sessionID] = make(map[string]struct{})
	}
	m.bySess[sessionID][id] = struct{}{}
	m.mu.Unlock()
	return j, nil
}

// CancelSession cancels all pending jobs for a session (Q20).
func (m *Manager) CancelSession(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.bySess[sessionID] {
		if j, ok := m.jobs[id]; ok && !j.Fired {
			j.Cancelled = true
		}
	}
}

// List pending/recent jobs for a session.
func (m *Manager) List(sessionID string) []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Job
	for id := range m.bySess[sessionID] {
		if j, ok := m.jobs[id]; ok {
			cp := *j
			out = append(out, &cp)
		}
	}
	return out
}

func (m *Manager) loop() {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.tick()
		}
	}
}

func (m *Manager) tick() {
	now := time.Now()
	var due []*Job
	m.mu.Lock()
	for _, j := range m.jobs {
		if j.Cancelled || j.Fired {
			continue
		}
		ready := false
		if j.WaitTask != "" && m.taskDone != nil && m.taskDone(j.WaitTask) {
			ready = true
		}
		if now.After(j.FireAt) || now.Equal(j.FireAt) {
			ready = true
		}
		// If both set: fire on whichever first (task done OR delay)
		if j.WaitTask != "" && now.Before(j.FireAt) {
			if m.taskDone != nil && m.taskDone(j.WaitTask) {
				ready = true
			} else {
				// only delay path later
				if !ready {
					continue
				}
			}
		}
		if ready {
			j.Fired = true
			cp := *j
			due = append(due, &cp)
		}
	}
	m.mu.Unlock()
	for _, j := range due {
		if m.onFire != nil {
			m.onFire(j.SessionID, j.Prompt)
		}
	}
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\n' && c != '\t' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
