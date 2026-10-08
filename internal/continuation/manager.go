package continuation

import (
	"fmt"
	"sync"
	"time"

	"github.com/rendicott/marble/internal/memory"
)

// Job is a scheduled session resume.
type Job struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Prompt    string    `json:"prompt"`
	Label     string    `json:"label,omitempty"`
	FireAt    time.Time `json:"fire_at"`
	WaitTask  string    `json:"wait_for_task,omitempty"`
	Cancelled bool      `json:"cancelled"`
	Fired     bool      `json:"fired"`
	CreatedAt time.Time `json:"created_at"`
	// FiredAt / Reason: when the job became due and why (delay | task_done).
	FiredAt *time.Time `json:"fired_at,omitempty"`
	Reason  string     `json:"reason,omitempty"`
	// Outcome after firing or cancelling: started | dropped | cancelled (+ detail).
	Outcome string `json:"outcome,omitempty"`
}

// FireFunc is invoked when a job should run (session may be busy — caller handles
// and reports the result with SetOutcome).
type FireFunc func(job Job)

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
	// Notify (optional) is called with the session id whenever a job is scheduled,
	// fires, changes outcome or is cancelled (live UI chips). Set before first use.
	Notify func(sessionID string)
}

func (m *Manager) notify(sessionID string) {
	if m != nil && m.Notify != nil && sessionID != "" {
		m.Notify(sessionID)
	}
}

// SetOutcome records what happened when a fired job was delivered.
func (m *Manager) SetOutcome(jobID, outcome string) {
	m.mu.Lock()
	j, ok := m.jobs[jobID]
	if ok {
		j.Outcome = outcome
	}
	m.mu.Unlock()
	if ok {
		m.notify(j.SessionID)
	}
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
	cp := *j
	m.mu.Unlock()
	m.notify(sessionID)
	return &cp, nil
}

// CancelSession cancels all pending jobs for a session (Q20).
func (m *Manager) CancelSession(sessionID string) {
	m.mu.Lock()
	changed := false
	for id := range m.bySess[sessionID] {
		if j, ok := m.jobs[id]; ok && !j.Fired && !j.Cancelled {
			j.Cancelled = true
			j.Outcome = "cancelled"
			changed = true
		}
	}
	m.mu.Unlock()
	if changed {
		m.notify(sessionID)
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
		reason := ""
		if j.WaitTask != "" && m.taskDone != nil && m.taskDone(j.WaitTask) {
			ready, reason = true, "task_done"
		}
		// Whichever comes first: the awaited task finishing, or fire_at (the delay, or
		// the max-delay ceiling for wait-only jobs).
		if !ready && !now.Before(j.FireAt) {
			ready, reason = true, "delay"
		}
		if ready {
			j.Fired = true
			fired := now
			j.FiredAt = &fired
			j.Reason = reason
			cp := *j
			due = append(due, &cp)
		}
	}
	m.mu.Unlock()
	for _, j := range due {
		m.notify(j.SessionID)
		if m.onFire != nil {
			m.onFire(*j)
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
