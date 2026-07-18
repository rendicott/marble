package session

import (
	"sync"
	"time"

	"github.com/rendicott/marble/internal/memory"
	"github.com/rendicott/marble/internal/model"
)

// Event is a streamable update for the UI.
type Event struct {
	Type       string          `json:"type"`
	SessionID  string          `json:"session_id"`
	Message    *Message        `json:"message,omitempty"`
	Tool       *ToolInfo       `json:"tool,omitempty"`
	Attachment *AttachmentInfo `json:"attachment,omitempty"`
	Turn       *TurnProgress   `json:"turn,omitempty"`
	Error      string          `json:"error,omitempty"`
	Status     string          `json:"status,omitempty"`
	At         time.Time       `json:"at"`
}

// AttachmentInfo is a UI attachment (attach_file tool).
type AttachmentInfo struct {
	Path    string `json:"path"`
	Name    string `json:"name,omitempty"`
	Inline  bool   `json:"inline"`
	Mime    string `json:"mime,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Preview string `json:"preview,omitempty"`
}

// ToolInfo describes a tool call for the UI.
type ToolInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Phase  string `json:"phase"` // start | result
}

// Message is a transcript entry shown in the UI / stored in history.
type Message struct {
	ID         string    `json:"id"`
	Role       string    `json:"role"` // user | assistant | tool | system
	Content    string    `json:"content"`
	ToolName   string    `json:"tool_name,omitempty"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Summary is a list-view row.
type Summary struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Kind         string     `json:"kind"` // user | system
	ParentID     string     `json:"parent_session_id,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ClosedAt     *time.Time `json:"closed_at,omitempty"`
	Status       string     `json:"status"`
	MessageCount int        `json:"message_count"`
	Busy         bool       `json:"busy"`
	Loaded       bool       `json:"loaded"`
	Dirty        bool       `json:"dirty"`
}

// Session is one independent conversation.
type Session struct {
	ID        string
	Title     string
	Kind      string // user | system
	ParentID  string // optional parent user session for system agents
	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  *time.Time
	Status    string // active | closed

	mu      sync.Mutex
	history []model.Message // model-facing history
	ui      []Message       // UI transcript
	busy    bool
	dirty   bool
	subs    map[chan Event]struct{}
	seq     int
	turn    turnControl // ADR-0010 live / last-turn progress
}

func newSession(id, title string) *Session {
	now := time.Now()
	if title == "" {
		title = "New session"
	}
	return &Session{
		ID:        id,
		Title:     title,
		Kind:      "user",
		CreatedAt: now,
		UpdatedAt: now,
		Status:    "active",
		history: []model.Message{
			{Role: "system", Content: defaultSystemPrompt},
		},
		ui:   nil,
		subs: make(map[chan Event]struct{}),
	}
}

const defaultSystemPrompt = `You are Marble, a general-purpose agent harness for sysadmin tasks, personal automation, and learning over time.
You work inside a single workspace directory (tool jail). Memory is separate.

Tools: filesystem (file_read/write, list_files, grep, glob, codebase_summary), surgical edits (edit_file requires prior file_read in the same turn; apply_patch is atomic), shell_execute (policy-limited; prefer start_background_task for jobs >60s), background tasks, schedule_continuation, get_context_usage, session_compact when context is high, memory_* and skill_* for long-term knowledge, attach_file for UI-only file chips.
mpub_publish / mpub_list / mpub_get / mpub_unpublish: publish human-facing pages under $MEMORY/mpub, served at /mpub/{slug} on this harness (primary content_type text/html; markdown also supported). Use for research notes and shareable results — not for project source files (use workspace tools) and not for agent memory_write knowledge.
MCP tools (if configured in mcp.json) appear as mcp_<server>_<tool> plus resource/prompt helpers — use them for web search (e.g. Tavily MCP) and other integrations.

Prefer edit_file/apply_patch over full file_write for existing files. Read before edit. Be concise in final answers.`

func (s *Session) Summary() Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.summaryLocked(true)
}

func (s *Session) summaryLocked(loaded bool) Summary {
	st := s.Status
	if st == "" {
		st = "active"
	}
	kind := s.Kind
	if kind == "" {
		kind = "user"
	}
	return Summary{
		ID:           s.ID,
		Title:        s.Title,
		Kind:         kind,
		ParentID:     s.ParentID,
		CreatedAt:    s.CreatedAt,
		UpdatedAt:    s.UpdatedAt,
		ClosedAt:     s.ClosedAt,
		Status:       st,
		MessageCount: len(s.ui),
		Busy:         s.busy,
		Loaded:       loaded,
		Dirty:        s.dirty,
	}
}

func (s *Session) UIMessages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Message, len(s.ui))
	copy(out, s.ui)
	return out
}

func (s *Session) Subscribe() chan Event {
	ch := make(chan Event, 32)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch
}

func (s *Session) Unsubscribe(ch chan Event) {
	s.mu.Lock()
	delete(s.subs, ch)
	s.mu.Unlock()
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func (s *Session) publish(ev Event) {
	ev.SessionID = s.ID
	ev.At = time.Now()
	s.mu.Lock()
	subs := make([]chan Event, 0, len(s.subs))
	for ch := range s.subs {
		subs = append(subs, ch)
	}
	s.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (s *Session) nextID(prefix string) string {
	s.seq++
	return fmtID(prefix, s.ID, s.seq)
}

func fmtID(prefix, sid string, n int) string {
	head := sid
	if len(head) > 8 {
		head = head[:8]
	}
	return prefix + "-" + head + "-" + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func (s *Session) appendUI(m Message) {
	s.ui = append(s.ui, m)
	s.UpdatedAt = time.Now()
	s.dirty = true
}

func (s *Session) markDirty() {
	s.dirty = true
	s.UpdatedAt = time.Now()
}

func (s *Session) tryBeginTurn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy || s.Status == "closed" {
		return false
	}
	s.busy = true
	return true
}

func (s *Session) endTurn() {
	// Finalize progress if still active (normal completion path).
	s.mu.Lock()
	stillActive := s.turn.prog.Active
	s.mu.Unlock()
	if stillActive {
		s.finalizeTurnProgress("complete", "")
	}

	s.mu.Lock()
	s.busy = false
	s.UpdatedAt = time.Now()
	s.dirty = true
	s.turn.cancel = nil
	s.mu.Unlock()
	s.publish(Event{Type: "status", Status: "idle"})
}

// SnapshotDoc builds a memory.SessionDoc under lock.
func (s *Session) SnapshotDoc(workspace, modelName string) *memory.SessionDoc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotDocLocked(workspace, modelName)
}

func (s *Session) snapshotDocLocked(workspace, modelName string) *memory.SessionDoc {
	msgs := make([]memory.TranscriptMessage, len(s.ui))
	for i, m := range s.ui {
		msgs[i] = memory.TranscriptMessage{
			ID:         m.ID,
			Role:       m.Role,
			Content:    m.Content,
			ToolName:   m.ToolName,
			ToolCallID: m.ToolCallID,
			CreatedAt:  m.CreatedAt,
		}
	}
	st := s.Status
	if st == "" {
		st = "active"
	}
	kind := s.Kind
	if kind == "" {
		kind = "user"
	}
	return &memory.SessionDoc{
		SessionMeta: memory.SessionMeta{
			ID:           s.ID,
			Title:        s.Title,
			Kind:         kind,
			ParentID:     s.ParentID,
			CreatedAt:    s.CreatedAt,
			UpdatedAt:    s.UpdatedAt,
			ClosedAt:     s.ClosedAt,
			Status:       st,
			MessageCount: len(msgs),
			Workspace:    workspace,
			Model:        modelName,
		},
		Messages: msgs,
	}
}

// IsDirty reports unpersisted changes.
func (s *Session) IsDirty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dirty
}

func (s *Session) clearDirty() {
	s.mu.Lock()
	s.dirty = false
	s.mu.Unlock()
}

// LoadFromDoc replaces in-memory state from a disk document.
func (s *Session) LoadFromDoc(doc *memory.SessionDoc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ID = doc.ID
	s.Title = doc.Title
	s.Kind = doc.Kind
	if s.Kind == "" {
		s.Kind = "user"
	}
	s.ParentID = doc.ParentID
	s.CreatedAt = doc.CreatedAt
	s.UpdatedAt = doc.UpdatedAt
	s.ClosedAt = doc.ClosedAt
	s.Status = doc.Status
	if s.Status == "" {
		s.Status = "active"
	}
	s.ui = make([]Message, 0, len(doc.Messages))
	s.history = []model.Message{{Role: "system", Content: defaultSystemPrompt}}
	s.seq = 0
	for _, m := range doc.Messages {
		s.ui = append(s.ui, Message{
			ID:         m.ID,
			Role:       m.Role,
			Content:    m.Content,
			ToolName:   m.ToolName,
			ToolCallID: m.ToolCallID,
			CreatedAt:  m.CreatedAt,
		})
		switch m.Role {
		case "user":
			s.history = append(s.history, model.Message{Role: "user", Content: m.Content})
		case "assistant":
			s.history = append(s.history, model.Message{Role: "assistant", Content: m.Content})
		case "tool":
			s.history = append(s.history, model.Message{
				Role:       "tool",
				Content:    m.Content,
				Name:       m.ToolName,
				ToolCallID: m.ToolCallID,
			})
		}
		// bump seq from message ids when possible
		s.seq++
	}
	s.dirty = false
	s.busy = false
}
