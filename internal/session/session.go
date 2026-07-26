package session

import (
	"sync"
	"time"

	"github.com/rendicott/marble/internal/memory"
	"github.com/rendicott/marble/internal/model"
)

// Event is a streamable update for the UI.
type Event struct {
	Type       string                 `json:"type"`
	SessionID  string                 `json:"session_id"`
	Message    *Message               `json:"message,omitempty"`
	Tool       *ToolInfo              `json:"tool,omitempty"`
	Attachment *AttachmentInfo        `json:"attachment,omitempty"`
	Turn       *TurnProgress          `json:"turn,omitempty"`
	Error      string                 `json:"error,omitempty"`
	Status     string                 `json:"status,omitempty"`
	ModelID    string                 `json:"model_id,omitempty"`
	Model      string                 `json:"model,omitempty"`
	ModelEff   map[string]interface{} `json:"model_effective,omitempty"`
	At         time.Time              `json:"at"`
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
	Role       string    `json:"role"` // user | assistant | tool | system | attachment
	Content    string    `json:"content"`
	ToolName   string    `json:"tool_name,omitempty"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	// Actor identity for human user messages (ADR-0017); never injected into model history.
	UserEmail string `json:"user_email,omitempty"`
	UserName  string `json:"user_name,omitempty"`
	UserSub   string `json:"user_sub,omitempty"`
	// Attachments are durable chat chips (ADR-0019).
	Attachments []UIAttachment `json:"attachments,omitempty"`
}

// Actor is optional identity for a human-authored message.
type Actor struct {
	Email string
	Name  string
	Sub   string
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
	// Cron is true when this session is a target of a durable cron job (ADR-0015),
	// or was auto-created for cron (title prefix "cron:"). Enriched by the API layer.
	Cron     bool     `json:"cron,omitempty"`
	CronJobs []string `json:"cron_jobs,omitempty"` // job names when known
	// Model selection (ADR-0018)
	ModelID string `json:"model_id,omitempty"`
	Model   string `json:"model,omitempty"` // last effective provider string
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

	// ModelID is durable catalog slug ("" = process default). ADR-0018.
	ModelID string
	// ProviderModel is last effective provider model string (KD12).
	ProviderModel string

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
			{Role: "system", Content: model.ContentFromText(defaultSystemPrompt)},
		},
		ui:   nil,
		subs: make(map[chan Event]struct{}),
	}
}

// SystemPrompt returns the immutable harness system prompt (ADR-0013).
func SystemPrompt() string {
	return defaultSystemPrompt
}

const defaultSystemPrompt = `You are Marble, a general-purpose agent harness for sysadmin tasks, personal automation, and learning over time.
You work inside a single workspace directory (tool jail). Memory is separate.

Tools: filesystem (file_read/write, list_files, grep, glob, codebase_summary), surgical edits (edit_file requires prior file_read in the same turn; apply_patch is atomic), shell_execute (policy-limited; prefer start_background_task for jobs >60s), background tasks, schedule_continuation, get_context_usage, session_compact when context is high, memory_* and skill_* for long-term knowledge, attach_file for UI-only file chips, web_fetch for HTTP(S) page retrieval.
Cron: use cron_list/get/create/update/delete/run for durable recurring schedules (SQLite, survive restarts). schedule_continuation is one-shot delay or wait-for-background-task only. Prefer interval ≥ 60s; target a session_id for a known thread, or omit session_id so the first fire creates a session. Keep cron prompts short.
mpub_publish / mpub_list / mpub_get / mpub_unpublish / mpub_set_visibility: publish human-facing pages under $MEMORY/mpub, served at /mpub/{slug}. Default visibility is private (allowlisted admins only when OAuth is on). Set visibility=public only when the user explicitly asks to share openly. Use mpub_set_visibility to promote/demote without rewriting the body. Primary content_type text/html; markdown also supported. Use for research notes and shareable results — not for project source files (use workspace tools) and not for agent memory_write knowledge.
MCP tools (if configured in mcp.json) appear as mcp_<server>_<tool> plus resource/prompt helpers — use them for web search (e.g. Tavily MCP) and other integrations.

Web research: use web search if available (e.g. mcp_tavily_tavily_search) to discover real URLs, then use the web_fetch native tool for deeper analysis of chosen pages. Do not invent URLs. Prefer web_fetch over Tavily extract/crawl when a simple page fetch works; use extract/crawl/research only if fetch fails or multi-page crawl is needed.

Memory: when unsure about prior decisions, operator preferences, project facts, or “have we done this before?”, check durable memory before guessing or re-deriving from scratch. Use memory_search (keywords/time/tags; scope session|daily|knowledge|all) then memory_fetch for full text. Prefer knowledge/ for intentional long-term facts; use skill_search/skill_load for procedural playbooks. After learning something durable the operator would want next time, memory_write it under knowledge/. Do not invent past work that is not in memory or the current transcript.

External agents: use call_agent_process(format=grok|claude, prompt=…) for large multi-file coding better suited to Grok Build or Claude Code. For multi-minute jobs set background=true (or poll via task_id) so the Marble turn is not blocked — do not wait synchronously on long agent runs. Use workdir for a dedicated subfolder under the workspace. Child auto-approve is on; scope the prompt and workdir carefully. Prefer Marble tools for simple reads/edits/shell. Summarize the external result for the user.

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
		ModelID:      s.ModelID,
		Model:        s.ProviderModel,
	}
}

func (s *Session) UIMessages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Message, len(s.ui))
	copy(out, s.ui)
	return out
}

// eventBufSize is large enough for a multi-tool turn's burst of progress
// events without dropping user-visible transcript updates under light lag.
const eventBufSize = 512

func (s *Session) Subscribe() chan Event {
	ch := make(chan Event, eventBufSize)
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

// droppableSSE is true for high-frequency progress noise that the UI can
// recover via GET /progress. Transcript and lifecycle events are not droppable.
func droppableSSE(typ string) bool {
	switch typ {
	case "turn", "tool":
		return true
	default:
		return false
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
			continue
		default:
		}
		// Buffer full. Drop progress noise; for critical events free one slot
		// then retry so final assistant/status/error still reach the UI.
		if droppableSSE(ev.Type) {
			continue
		}
		select {
		case <-ch: // drop oldest buffered event
		default:
		}
		select {
		case ch <- ev:
		default:
			// Still full of critical events — last resort non-block drop.
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

// IsBusy reports whether a turn is in progress.
func (s *Session) IsBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

// Reopen clears closed status so a cron/continuation can inject a turn.
func (s *Session) Reopen() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status == "closed" {
		s.Status = "active"
		s.ClosedAt = nil
		s.dirty = true
		s.UpdatedAt = time.Now()
	}
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
	s.turn.opts = TurnOpts{} // clear cron pin / turn-scoped opts (KD11)
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
			UserEmail:  m.UserEmail,
			UserName:   m.UserName,
			UserSub:    m.UserSub,
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
	model := s.ProviderModel
	if model == "" {
		model = modelName
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
			Model:        model,
			ModelID:      s.ModelID,
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
	s.ModelID = doc.ModelID
	s.ProviderModel = doc.Model
	s.ui = make([]Message, 0, len(doc.Messages))
	s.history = []model.Message{{Role: "system", Content: model.ContentFromText(defaultSystemPrompt)}}
	s.seq = 0
	for _, m := range doc.Messages {
		s.ui = append(s.ui, Message{
			ID:         m.ID,
			Role:       m.Role,
			Content:    m.Content,
			ToolName:   m.ToolName,
			ToolCallID: m.ToolCallID,
			CreatedAt:  m.CreatedAt,
			UserEmail:  m.UserEmail,
			UserName:   m.UserName,
			UserSub:    m.UserSub,
		})
		// Reload attachment markers into multimodal history when present (ADR-0019).
		histContent := historyContentFromUIMessage(m)
		switch m.Role {
		case "user":
			// Model history: content only (ADR-0017 Q13)
			s.history = append(s.history, model.Message{Role: "user", Content: histContent})
		case "assistant":
			s.history = append(s.history, model.Message{Role: "assistant", Content: histContent})
		case "tool":
			s.history = append(s.history, model.Message{
				Role:       "tool",
				Content:    histContent,
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

// historyContentFromUIMessage rebuilds model Content from UI/MD text, including
// attachment markers <!-- att:id name=… mime=… --> (ADR-0019).
func historyContentFromUIMessage(m memory.TranscriptMessage) model.Content {
	return model.ContentFromText(m.Content)
}
