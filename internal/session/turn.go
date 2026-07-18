package session

import (
	"context"
	"strings"
	"time"
)

const (
	maxTurnSteps     = 100
	argsPreviewMax   = 200
	resultTailMax    = 120
)

// ToolProg is current/last tool info for the progress card.
type ToolProg struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	ArgsPreview string `json:"args_preview,omitempty"`
	ResultTail  string `json:"result_tail,omitempty"`
	Phase       string `json:"phase"` // start | result
}

// TurnStep is one entry in the collapsible step log (ADR-0010 Q3).
type TurnStep struct {
	At      string `json:"at"`
	Kind    string `json:"kind"` // model_call | tool_start | tool_result | advisory | stop | error | done | starting
	Iter    int    `json:"iter,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Latency *int   `json:"latency_ms,omitempty"`
	Tool    string `json:"tool,omitempty"`
}

// TurnProgress is the live/last-turn snapshot for UI + GET /progress.
type TurnProgress struct {
	SessionID          string     `json:"session_id"`
	Active             bool       `json:"active"`
	Phase              string     `json:"phase"` // starting | calling_model | running_tool | finishing | stopping | idle | error | complete
	Iter               int        `json:"iter"`
	IterHard           int        `json:"iter_hard"`
	ToolRounds         int        `json:"tool_rounds"`
	ToolSoft           int        `json:"tool_soft"`
	TurnStartedAt      time.Time  `json:"turn_started_at"`
	PhaseStartedAt     time.Time  `json:"phase_started_at"`
	LastEventAt        time.Time  `json:"last_event_at"`
	TurnEndedAt        *time.Time `json:"turn_ended_at,omitempty"`
	ContextUsage       *float64   `json:"context_usage,omitempty"`
	LastModelLatencyMs *int       `json:"last_model_latency_ms,omitempty"`
	CurrentTool        *ToolProg  `json:"current_tool,omitempty"`
	LastTool           *ToolProg  `json:"last_tool,omitempty"`
	Steps              []TurnStep `json:"steps,omitempty"`
	Collapsed          bool       `json:"collapsed"`
	StopRequested      bool       `json:"stop_requested"`
	Message            string     `json:"message,omitempty"`
}

// Snapshot of turn cancel + progress (under session lock).
type turnControl struct {
	cancel context.CancelFunc
	prog   TurnProgress
}

func (s *Session) initTurnProgress(hard, soft int) {
	now := time.Now()
	s.mu.Lock()
	s.turn = turnControl{
		prog: TurnProgress{
			SessionID:      s.ID,
			Active:         true,
			Phase:          "starting",
			Iter:           0,
			IterHard:       hard,
			ToolRounds:     0,
			ToolSoft:       soft,
			TurnStartedAt:  now,
			PhaseStartedAt: now,
			LastEventAt:    now,
			Collapsed:      false,
			Steps:          nil,
		},
	}
	s.mu.Unlock()
}

func (s *Session) setTurnCancel(cancel context.CancelFunc) {
	s.mu.Lock()
	s.turn.cancel = cancel
	s.mu.Unlock()
}

// RequestStop cancels the in-flight turn if busy. Returns true if stop was accepted.
func (s *Session) RequestStop() bool {
	s.mu.Lock()
	if !s.busy {
		s.mu.Unlock()
		return false
	}
	s.turn.prog.StopRequested = true
	s.turn.prog.Phase = "stopping"
	s.turn.prog.PhaseStartedAt = time.Now()
	s.turn.prog.LastEventAt = time.Now()
	s.turn.prog.Message = "Stop requested"
	s.appendStepLocked(TurnStep{
		At:     time.Now().UTC().Format(time.RFC3339),
		Kind:   "stop",
		Detail: "operator stop",
	})
	cancel := s.turn.cancel
	prog := s.copyProgressLocked()
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	s.publish(Event{Type: "status", Status: "stopping"})
	s.publish(Event{Type: "turn", Turn: &prog})
	return true
}

// Progress returns a copy of current or last turn progress.
func (s *Session) Progress() TurnProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.copyProgressLocked()
}

func (s *Session) copyProgressLocked() TurnProgress {
	p := s.turn.prog
	if p.SessionID == "" {
		p.SessionID = s.ID
		p.Phase = "idle"
		p.Active = false
	}
	if len(p.Steps) > 0 {
		steps := make([]TurnStep, len(p.Steps))
		copy(steps, p.Steps)
		p.Steps = steps
	}
	if p.CurrentTool != nil {
		ct := *p.CurrentTool
		p.CurrentTool = &ct
	}
	if p.LastTool != nil {
		lt := *p.LastTool
		p.LastTool = &lt
	}
	if p.ContextUsage != nil {
		v := *p.ContextUsage
		p.ContextUsage = &v
	}
	if p.LastModelLatencyMs != nil {
		v := *p.LastModelLatencyMs
		p.LastModelLatencyMs = &v
	}
	if p.TurnEndedAt != nil {
		t := *p.TurnEndedAt
		p.TurnEndedAt = &t
	}
	return p
}

func (s *Session) publishTurnProgress() {
	s.mu.Lock()
	s.turn.prog.LastEventAt = time.Now()
	prog := s.copyProgressLocked()
	s.mu.Unlock()
	s.publish(Event{Type: "turn", Turn: &prog})
}

func (s *Session) setPhase(phase string) {
	s.mu.Lock()
	s.turn.prog.Phase = phase
	s.turn.prog.PhaseStartedAt = time.Now()
	s.turn.prog.LastEventAt = time.Now()
	s.mu.Unlock()
}

func (s *Session) setIter(iter int) {
	s.mu.Lock()
	s.turn.prog.Iter = iter
	s.turn.prog.LastEventAt = time.Now()
	s.mu.Unlock()
}

func (s *Session) setToolRounds(n int) {
	s.mu.Lock()
	s.turn.prog.ToolRounds = n
	s.mu.Unlock()
}

func (s *Session) setContextUsage(ratio float64) {
	s.mu.Lock()
	s.turn.prog.ContextUsage = &ratio
	s.mu.Unlock()
}

func (s *Session) setLastModelLatency(ms int) {
	s.mu.Lock()
	s.turn.prog.LastModelLatencyMs = &ms
	s.mu.Unlock()
}

func (s *Session) setCurrentTool(id, name, args, phase string) {
	s.mu.Lock()
	tp := &ToolProg{
		ID:          id,
		Name:        name,
		ArgsPreview: truncateOneLine(args, argsPreviewMax),
		Phase:       phase,
	}
	s.turn.prog.CurrentTool = tp
	s.turn.prog.LastEventAt = time.Now()
	s.mu.Unlock()
}

func (s *Session) finishCurrentTool(result string) {
	s.mu.Lock()
	if s.turn.prog.CurrentTool != nil {
		lt := *s.turn.prog.CurrentTool
		lt.Phase = "result"
		lt.ResultTail = truncateOneLine(result, resultTailMax)
		s.turn.prog.LastTool = &lt
		s.turn.prog.CurrentTool = nil
	}
	s.turn.prog.LastEventAt = time.Now()
	s.mu.Unlock()
}

func (s *Session) appendStep(step TurnStep) {
	s.mu.Lock()
	s.appendStepLocked(step)
	s.mu.Unlock()
}

func (s *Session) appendStepLocked(step TurnStep) {
	if step.At == "" {
		step.At = time.Now().UTC().Format(time.RFC3339)
	}
	s.turn.prog.Steps = append(s.turn.prog.Steps, step)
	if len(s.turn.prog.Steps) > maxTurnSteps {
		s.turn.prog.Steps = s.turn.prog.Steps[len(s.turn.prog.Steps)-maxTurnSteps:]
	}
	s.turn.prog.LastEventAt = time.Now()
}

func (s *Session) setTurnMessage(msg string) {
	s.mu.Lock()
	s.turn.prog.Message = msg
	s.mu.Unlock()
}

func (s *Session) finalizeTurnProgress(phase, message string) {
	now := time.Now()
	s.mu.Lock()
	s.turn.prog.Active = false
	s.turn.prog.Phase = phase
	if message != "" {
		s.turn.prog.Message = message
	}
	s.turn.prog.Collapsed = true
	s.turn.prog.TurnEndedAt = &now
	s.turn.prog.LastEventAt = now
	s.turn.prog.CurrentTool = nil
	s.turn.cancel = nil
	s.appendStepLocked(TurnStep{
		At:     now.UTC().Format(time.RFC3339),
		Kind:   "done",
		Detail: phase + (func() string {
			if message != "" {
				return ": " + message
			}
			return ""
		})(),
	})
	prog := s.copyProgressLocked()
	s.mu.Unlock()
	s.publish(Event{Type: "turn", Turn: &prog})
}

func truncateOneLine(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	s = strings.Join(strings.Fields(s), " ")
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 2 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
