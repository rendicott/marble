package api

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

// pendingView is GET /api/sessions/{id}/pending: what this session is waiting on.
// The UI renders it as chips (transcript rows + a pending bar) and refetches on the
// session's "pending" SSE event. Now lets the client correct countdowns for skew.
type pendingView struct {
	Now           time.Time          `json:"now"`
	Continuations []pendingCont      `json:"continuations"`
	BgTasks       []pendingTask      `json:"bg_tasks"`
	AgentTasks    []pendingAgentTask `json:"agent_tasks"`
}

type pendingCont struct {
	ID          string     `json:"id"`
	Label       string     `json:"label,omitempty"`
	Prompt      string     `json:"prompt"`
	State       string     `json:"state"` // pending | fired | cancelled
	CreatedAt   time.Time  `json:"created_at"`
	FireAt      time.Time  `json:"fire_at"`
	WaitForTask string     `json:"wait_for_task,omitempty"`
	FiredAt     *time.Time `json:"fired_at,omitempty"`
	Reason      string     `json:"reason,omitempty"`  // delay | task_done
	Outcome     string     `json:"outcome,omitempty"` // started | waiting: … | dropped: … | cancelled
}

type pendingTask struct {
	ID        string     `json:"id"`
	Label     string     `json:"label,omitempty"`
	Command   string     `json:"command"`
	Status    string     `json:"status"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	ExitCode  *int       `json:"exit_code,omitempty"`
	Error     string     `json:"error,omitempty"`
}

type pendingAgentTask struct {
	ID          string     `json:"id"`
	Format      string     `json:"format"`
	Prompt      string     `json:"prompt"`
	Status      string     `json:"status"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	Phase       string     `json:"phase,omitempty"`
	Alive       bool       `json:"alive"`
	AliveAgeSec int        `json:"alive_age_sec"`
	StuckHint   bool       `json:"stuck_hint,omitempty"`
	Error       string     `json:"error,omitempty"`
}

func (s *Server) handleSessionPending(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.pendingFor(id))
}

func (s *Server) pendingFor(id string) pendingView {
	out := pendingView{
		Now:           time.Now().UTC(),
		Continuations: []pendingCont{},
		BgTasks:       []pendingTask{},
		AgentTasks:    []pendingAgentTask{},
	}
	if s.Tools == nil {
		return out
	}
	if s.Tools.Cont != nil {
		for _, j := range s.Tools.Cont.List(id) {
			st := "pending"
			switch {
			case j.Cancelled:
				st = "cancelled"
			case j.Fired:
				st = "fired"
			}
			out.Continuations = append(out.Continuations, pendingCont{
				ID: j.ID, Label: j.Label, Prompt: preview(j.Prompt, 160), State: st,
				CreatedAt: j.CreatedAt, FireAt: j.FireAt, WaitForTask: j.WaitTask,
				FiredAt: j.FiredAt, Reason: j.Reason, Outcome: j.Outcome,
			})
		}
		sort.Slice(out.Continuations, func(a, b int) bool {
			return out.Continuations[a].CreatedAt.Before(out.Continuations[b].CreatedAt)
		})
	}
	if s.Tools.BG != nil {
		for _, t := range s.Tools.BG.List(id) {
			out.BgTasks = append(out.BgTasks, pendingTask{
				ID: t.ID, Label: t.Label, Command: preview(t.Command, 160), Status: string(t.Status),
				StartedAt: t.StartedAt, EndedAt: t.EndedAt, ExitCode: t.ExitCode, Error: t.Error,
			})
		}
		sort.Slice(out.BgTasks, func(a, b int) bool { return out.BgTasks[a].StartedAt.Before(out.BgTasks[b].StartedAt) })
	}
	if s.Tools.Agents != nil {
		for _, t := range s.Tools.Agents.List(id) {
			v := pendingAgentTask{
				ID: t.ID, Format: t.Format, Prompt: preview(t.Prompt, 160), Status: string(t.Status),
				StartedAt: t.StartedAt, EndedAt: t.EndedAt, ExitCode: t.ExitCode, Error: t.Error,
			}
			if p := t.Progress; p != nil {
				v.Phase, v.Alive, v.AliveAgeSec, v.StuckHint = p.Phase, p.Alive, p.AliveAgeSec, p.StuckHint
			}
			out.AgentTasks = append(out.AgentTasks, v)
		}
		sort.Slice(out.AgentTasks, func(a, b int) bool { return out.AgentTasks[a].StartedAt.Before(out.AgentTasks[b].StartedAt) })
	}
	return out
}

func preview(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
