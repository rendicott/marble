package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"context"

	"github.com/rendicott/marble/internal/agentproc"
	"github.com/rendicott/marble/internal/bgtask"
	"github.com/rendicott/marble/internal/continuation"
	"github.com/rendicott/marble/internal/cron"
	"github.com/rendicott/marble/internal/mcp"
	"github.com/rendicott/marble/internal/model"
	"github.com/rendicott/marble/internal/shellpolicy"
)

// Attachment is emitted for UI rendering (attach_file).
type Attachment struct {
	Path     string `json:"path"`
	Name     string `json:"name,omitempty"`
	Inline   bool   `json:"inline"`
	Mime     string `json:"mime,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Preview  string `json:"preview,omitempty"`
}

// TurnContext is per-agent-turn state for tools that need session scope.
type TurnContext struct {
	SessionID   string
	SessionKind string // user | system
	ReadPaths   map[string]bool
	// Ctx is the turn cancel context (ADR-0010); shell/MCP should honor it.
	Ctx context.Context
	// callbacks set by session loop
	GetUsage       func() map[string]interface{}
	Compact        func(style string, keepLast int) (string, error)
	OnAttachment   func(Attachment) // attach_file — ephemeral SSE (ADR-0005)
	// OnChatAttachment is durable chat attach (ADR-0019 message_attach); loop appendUI+message.
	OnChatAttachment func(Attachment)
	OnHarnessNote  func(string) // optional
	HistorySnippet func() string
}

// Registry holds tool implementations and shared deps.
type Registry struct {
	Workspace      string
	Memory         string
	Addr           string // --addr for mpub public URLs
	MaxResultChars int

	Policy *shellpolicy.Policy
	BG     *bgtask.Manager
	Cont   *continuation.Manager
	Cron   *cron.Manager
	MCP    *mcp.Manager
	Agents *agentproc.Manager

	// Shell timeouts from config (fallback if policy unset)
	ShellDefault time.Duration
	ShellMax     time.Duration

	// Model catalog helpers (ADR-0018) — wired from main
	ListModels     func() ([]map[string]interface{}, error)
	SetSessionModel func(sessionID, modelID string) (map[string]interface{}, error)

	// StageChatAttachment stores bytes and returns id,mime,kind (ADR-0019).
	StageChatAttachment func(sessionID, name string, data []byte) (id, mime, kind string, err error)

	mu sync.Mutex
}

// Specs returns OpenAI tool definitions for built-ins + MCP (ADR-0005/0006).
func (r *Registry) Specs() []model.ToolSpec {
	out := allSpecs()
	if r.MCP != nil {
		out = append(out, r.MCP.Specs()...)
	}
	return out
}

// Execute runs a named tool. tc may be nil for simple tools (reads still work).
func (r *Registry) Execute(name, argsJSON string, tc *TurnContext) string {
	max := r.MaxResultChars
	if max <= 0 {
		max = 32000
	}
	if tc == nil {
		tc = &TurnContext{ReadPaths: map[string]bool{}}
	}
	if tc.ReadPaths == nil {
		tc.ReadPaths = map[string]bool{}
	}

	parent := context.Background()
	if tc.Ctx != nil {
		parent = tc.Ctx
	}

	// MCP tools (namespaced mcp_*)
	if r.MCP != nil && r.MCP.IsMCPTool(name) {
		// timeout enforced inside MCP sessions; outer bound 2m; honor turn cancel
		ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
		defer cancel()
		return clamp(r.MCP.Execute(ctx, name, argsJSON), max)
	}

	var out string
	var err error
	switch name {
	case "file_read":
		out, err = r.fileRead(argsJSON, tc)
	case "file_write":
		out, err = r.fileWrite(argsJSON)
	case "list_files":
		out, err = r.listFiles(argsJSON)
	case "codebase_summary":
		out, err = r.codebaseSummary(argsJSON)
	case "grep":
		out, err = r.grep(argsJSON)
	case "glob":
		out, err = r.glob(argsJSON)
	case "edit_file":
		out, err = r.editFile(argsJSON, tc)
	case "apply_patch":
		out, err = r.applyPatch(argsJSON, tc)
	case "shell_execute":
		out, err = r.shellExecute(argsJSON, tc)
	case "start_background_task":
		out, err = r.startBG(argsJSON, tc)
	case "kill_background_task":
		out, err = r.killBG(argsJSON)
	case "check_background_task":
		out, err = r.checkBG(argsJSON, tc)
	case "schedule_continuation":
		out, err = r.scheduleContinuation(argsJSON, tc)
	case "cron_list":
		out, err = r.cronList(argsJSON)
	case "cron_get":
		out, err = r.cronGet(argsJSON)
	case "cron_create":
		out, err = r.cronCreate(argsJSON)
	case "cron_update":
		out, err = r.cronUpdate(argsJSON)
	case "cron_delete":
		out, err = r.cronDelete(argsJSON)
	case "cron_run":
		out, err = r.cronRun(argsJSON)
	case "model_list":
		out, err = r.modelList(argsJSON)
	case "session_set_model":
		out, err = r.sessionSetModel(argsJSON, tc)
	case "get_context_usage":
		out, err = r.getContextUsage(tc)
	case "session_compact":
		out, err = r.sessionCompact(argsJSON, tc)
	case "memory_search":
		out, err = r.memorySearch(argsJSON)
	case "memory_fetch":
		out, err = r.memoryFetch(argsJSON)
	case "memory_write":
		out, err = r.memoryWrite(argsJSON)
	case "skill_search":
		out, err = r.skillSearch(argsJSON)
	case "skill_load":
		out, err = r.skillLoad(argsJSON)
	case "attach_file":
		out, err = r.attachFile(argsJSON, tc)
	case "message_attach":
		out, err = r.messageAttach(argsJSON, tc)
	case "web_fetch":
		out, err = r.webFetch(argsJSON, tc)
	case "call_agent_process":
		out, err = r.callAgentProcess(argsJSON, tc)
	case "mpub_publish":
		out, err = r.mpubPublish(argsJSON, tc)
	case "mpub_list":
		out, err = r.mpubList(argsJSON)
	case "mpub_get":
		out, err = r.mpubGet(argsJSON)
	case "mpub_unpublish":
		out, err = r.mpubUnpublish(argsJSON)
	case "mpub_set_visibility":
		out, err = r.mpubSetVisibility(argsJSON)
	default:
		return fmt.Sprintf("error: unknown tool %q", name)
	}
	if err != nil {
		return "error: " + err.Error()
	}
	return clamp(out, max)
}

func clamp(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n\n…[truncated %d of %d chars]", max, len(s))
}

// resolve maps a path into the workspace jail.
// Relative paths are joined to the workspace root.
// Absolute paths are allowed when they resolve under the workspace
// (agents often pass abs paths from list_files / prior tool output).
func (r *Registry) resolve(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	absWS, err := filepath.Abs(r.Workspace)
	if err != nil {
		return "", err
	}
	var absFull string
	if filepath.IsAbs(rel) {
		absFull, err = filepath.Abs(filepath.Clean(rel))
		if err != nil {
			return "", err
		}
	} else {
		clean := filepath.Clean(rel)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return "", fmt.Errorf("path escapes workspace")
		}
		absFull, err = filepath.Abs(filepath.Join(r.Workspace, clean))
		if err != nil {
			return "", err
		}
	}
	if absFull != absWS && !strings.HasPrefix(absFull, absWS+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return absFull, nil
}

func (r *Registry) resolveMemory(rel string) (string, error) {
	if r.Memory == "" {
		return "", fmt.Errorf("memory root not configured")
	}
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) {
		abs, err := filepath.Abs(clean)
		if err != nil {
			return "", err
		}
		if abs != r.Memory && !strings.HasPrefix(abs, r.Memory+string(os.PathSeparator)) {
			return "", fmt.Errorf("path escapes memory root")
		}
		return abs, nil
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes memory root")
	}
	full := filepath.Join(r.Memory, clean)
	abs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if abs != r.Memory && !strings.HasPrefix(abs, r.Memory+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes memory root")
	}
	return abs, nil
}

func mustJSON(v interface{}) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func parseArgs(argsJSON string, dest interface{}) error {
	if argsJSON == "" || argsJSON == "null" {
		return nil
	}
	return json.Unmarshal([]byte(argsJSON), dest)
}
