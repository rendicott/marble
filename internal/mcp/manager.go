package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/rendicott/marble/internal/model"
)

const (
	// MaxTools is the soft cap for registered MCP tools (ADR-0006 Q9).
	MaxTools = 64
	// DefaultCallTimeout for tools/resources/prompts (Q10).
	DefaultCallTimeout = 60 * time.Second
)

// ServerStatus is health for one server.
type ServerStatus struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"` // stdio | http
	OK          bool     `json:"ok"`
	Error       string   `json:"error,omitempty"`
	Tools       int      `json:"tools"`
	ToolNames   []string `json:"tool_names,omitempty"`   // original MCP names
	MarbleNames []string `json:"marble_names,omitempty"` // mcp_<server>_<tool> as model sees them
	Resources   int      `json:"resources"`
	Prompts     int      `json:"prompts"`
}

// Manager holds process-global MCP connections and catalogs.
type Manager struct {
	mu        sync.RWMutex
	sessions  map[string]Session
	tools     []ToolDef
	toolIndex map[string]ToolDef // marble name → def
	resources []ResourceDef
	prompts   []PromptDef
	status    []ServerStatus
	timeout   time.Duration
	disabled  bool
}

// NewManager creates an empty manager.
func NewManager(timeout time.Duration, disabled bool) *Manager {
	if timeout <= 0 {
		timeout = DefaultCallTimeout
	}
	return &Manager{
		sessions:  make(map[string]Session),
		toolIndex: make(map[string]ToolDef),
		timeout:   timeout,
		disabled:  disabled,
	}
}

// Start connects enabled servers from config (non-fatal per server).
func (m *Manager) Start(ctx context.Context, fc FileConfig) {
	if m.disabled {
		m.mu.Lock()
		m.status = []ServerStatus{{Name: "*", OK: false, Error: "mcp disabled via --mcp-disable"}}
		m.mu.Unlock()
		return
	}
	for name, sc := range fc.MCPServers {
		if !sc.IsEnabled() {
			continue
		}
		kind := sc.Kind()
		if kind == "" {
			m.addStatus(ServerStatus{Name: name, OK: false, Error: "need command (stdio) or url (http)"})
			continue
		}
		var sess Session
		var err error
		switch kind {
		case "stdio":
			sess, err = ConnectStdio(ctx, name, sc, m.timeout)
		case "http":
			sess, err = ConnectHTTP(ctx, name, sc, m.timeout)
		default:
			err = fmt.Errorf("unknown transport")
		}
		if err != nil {
			log.Printf("mcp: server %q failed: %v", name, err)
			m.addStatus(ServerStatus{Name: name, Kind: kind, OK: false, Error: err.Error()})
			continue
		}
		st := ServerStatus{Name: name, Kind: kind, OK: true}
		// discover
		if tools, err := sess.ListTools(ctx); err == nil {
			m.mu.Lock()
			for _, t := range tools {
				if len(m.tools) >= MaxTools {
					log.Printf("mcp: tool soft cap %d reached; skipping further tools", MaxTools)
					break
				}
				// unique marble name
				mn := t.MarbleName
				base := mn
				i := 2
				for {
					if _, ok := m.toolIndex[mn]; !ok {
						break
					}
					mn = fmt.Sprintf("%s_%d", base, i)
					if len(mn) > 64 {
						mn = mn[:64]
					}
					i++
				}
				t.MarbleName = mn
				m.tools = append(m.tools, t)
				m.toolIndex[mn] = t
				st.ToolNames = append(st.ToolNames, t.Name)
				st.MarbleNames = append(st.MarbleNames, mn)
			}
			st.Tools = len(st.ToolNames)
			m.mu.Unlock()
		} else {
			log.Printf("mcp: %s tools/list: %v", name, err)
		}
		if res, err := sess.ListResources(ctx); err == nil {
			st.Resources = len(res)
			m.mu.Lock()
			m.resources = append(m.resources, res...)
			m.mu.Unlock()
		}
		if pr, err := sess.ListPrompts(ctx); err == nil {
			st.Prompts = len(pr)
			m.mu.Lock()
			m.prompts = append(m.prompts, pr...)
			m.mu.Unlock()
		}
		m.mu.Lock()
		m.sessions[name] = sess
		m.mu.Unlock()
		m.addStatus(st)
		log.Printf("mcp: connected %s (%s) tools=%d resources=%d prompts=%d", name, kind, st.Tools, st.Resources, st.Prompts)
	}
}

func (m *Manager) addStatus(st ServerStatus) {
	m.mu.Lock()
	m.status = append(m.status, st)
	m.mu.Unlock()
}

// Close shuts down all sessions.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, s := range m.sessions {
		if err := s.Close(); err != nil {
			log.Printf("mcp: close %s: %v", name, err)
		}
		delete(m.sessions, name)
	}
	m.tools = nil
	m.toolIndex = make(map[string]ToolDef)
	m.resources = nil
	m.prompts = nil
	m.status = nil
}

// Reload closes existing sessions and reconnects from fc.
func (m *Manager) Reload(ctx context.Context, fc FileConfig) {
	if m == nil {
		return
	}
	m.Close()
	m.mu.Lock()
	m.sessions = make(map[string]Session)
	m.toolIndex = make(map[string]ToolDef)
	m.mu.Unlock()
	m.Start(ctx, fc)
}

// CatalogTool is an MCP-exposed tool for UI/API listings.
type CatalogTool struct {
	Name         string `json:"name"`          // marble name (mcp_server_tool)
	Server       string `json:"server"`        // MCP server key
	Description  string `json:"description,omitempty"`
	OriginalName string `json:"original_name,omitempty"` // name on the MCP server
	Bridge       bool   `json:"bridge,omitempty"`        // list_resources / get_prompt helpers
}

// ToolCatalog returns discovered MCP tools and bridge helpers with server attribution.
func (m *Manager) ToolCatalog() []CatalogTool {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]CatalogTool, 0, len(m.tools)+8)
	for _, t := range m.tools {
		out = append(out, CatalogTool{
			Name:         t.MarbleName,
			Server:       t.Server,
			Description:  t.Description,
			OriginalName: t.Name,
		})
	}
	// Bridge tools (same as Specs)
	serversWithRes := map[string]bool{}
	for _, r := range m.resources {
		serversWithRes[r.Server] = true
	}
	serversWithPrompt := map[string]bool{}
	for _, p := range m.prompts {
		serversWithPrompt[p.Server] = true
	}
	for srv := range serversWithRes {
		out = append(out,
			CatalogTool{Name: ToolName(srv, "list_resources"), Server: srv, Description: "List MCP resources", Bridge: true},
			CatalogTool{Name: ToolName(srv, "read_resource"), Server: srv, Description: "Read an MCP resource by URI", Bridge: true},
		)
	}
	for srv := range serversWithPrompt {
		out = append(out,
			CatalogTool{Name: ToolName(srv, "list_prompts"), Server: srv, Description: "List MCP prompts", Bridge: true},
			CatalogTool{Name: ToolName(srv, "get_prompt"), Server: srv, Description: "Get an MCP prompt by name", Bridge: true},
		)
	}
	return out
}

// Specs returns OpenAI tool specs for MCP tools + resource/prompt bridge tools.
func (m *Manager) Specs() []model.ToolSpec {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []model.ToolSpec
	for _, t := range m.tools {
		desc := t.Description
		if desc == "" {
			desc = "MCP tool " + t.Name + " from server " + t.Server
		} else {
			desc = fmt.Sprintf("[mcp:%s] %s", t.Server, desc)
		}
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		out = append(out, model.ToolSpec{
			Type: "function",
			Function: model.ToolFunctionSchema{
				Name:        t.MarbleName,
				Description: desc,
				Parameters:  schema,
			},
		})
	}
	// Bridge tools for resources/prompts (per server that has any)
	serversWithRes := map[string]bool{}
	for _, r := range m.resources {
		serversWithRes[r.Server] = true
	}
	serversWithPrompt := map[string]bool{}
	for _, p := range m.prompts {
		serversWithPrompt[p.Server] = true
	}
	// also servers that connected even if empty lists — skip empty
	for srv := range serversWithRes {
		out = append(out, bridgeSpec(
			ToolName(srv, "list_resources"),
			fmt.Sprintf("[mcp:%s] List MCP resources on this server", srv),
			map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		))
		out = append(out, bridgeSpec(
			ToolName(srv, "read_resource"),
			fmt.Sprintf("[mcp:%s] Read an MCP resource by URI", srv),
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"uri": map[string]interface{}{"type": "string", "description": "Resource URI"},
				},
				"required": []string{"uri"},
			},
		))
	}
	for srv := range serversWithPrompt {
		out = append(out, bridgeSpec(
			ToolName(srv, "list_prompts"),
			fmt.Sprintf("[mcp:%s] List MCP prompts on this server", srv),
			map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		))
		out = append(out, bridgeSpec(
			ToolName(srv, "get_prompt"),
			fmt.Sprintf("[mcp:%s] Get an MCP prompt by name (optional args object)", srv),
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{"type": "string"},
					"arguments": map[string]interface{}{
						"type": "object",
						"additionalProperties": map[string]interface{}{"type": "string"},
					},
				},
				"required": []string{"name"},
			},
		))
	}
	return out
}

func bridgeSpec(name, desc string, params map[string]interface{}) model.ToolSpec {
	return model.ToolSpec{
		Type: "function",
		Function: model.ToolFunctionSchema{
			Name:        name,
			Description: desc,
			Parameters:  params,
		},
	}
}

// IsMCPTool reports whether name is handled by MCP.
func (m *Manager) IsMCPTool(name string) bool {
	if m == nil {
		return false
	}
	if strings.HasPrefix(name, "mcp_") {
		return true
	}
	return false
}

// Execute runs an MCP tool or bridge tool by marble name.
func (m *Manager) Execute(ctx context.Context, name, argsJSON string) string {
	if m == nil {
		return "error: mcp not configured"
	}
	var args map[string]interface{}
	if argsJSON != "" && argsJSON != "null" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "error: invalid args: " + err.Error()
		}
	}
	if args == nil {
		args = map[string]interface{}{}
	}

	m.mu.RLock()
	// direct tool
	if td, ok := m.toolIndex[name]; ok {
		sess := m.sessions[td.Server]
		m.mu.RUnlock()
		if sess == nil {
			return "error: mcp server not connected"
		}
		out, err := sess.CallTool(ctx, td.Name, args)
		if err != nil {
			return "error: " + err.Error()
		}
		return out
	}
	m.mu.RUnlock()

	// bridge: mcp_<server>_list_resources etc.
	server, action, ok := parseBridge(name)
	if !ok {
		return "error: unknown mcp tool " + name
	}
	m.mu.RLock()
	sess := m.sessions[server]
	m.mu.RUnlock()
	if sess == nil {
		// try sanitized reverse: find session by SanitizeName match
		m.mu.RLock()
		for n, s := range m.sessions {
			if SanitizeName(n) == server {
				sess = s
				server = n
				break
			}
		}
		m.mu.RUnlock()
	}
	if sess == nil {
		return "error: mcp server not connected for " + name
	}

	switch action {
	case "list_resources":
		m.mu.RLock()
		var list []ResourceDef
		for _, r := range m.resources {
			if r.Server == server || SanitizeName(r.Server) == SanitizeName(server) {
				list = append(list, r)
			}
		}
		m.mu.RUnlock()
		b, _ := json.MarshalIndent(list, "", "  ")
		return string(b)
	case "read_resource":
		uri, _ := args["uri"].(string)
		if uri == "" {
			return "error: uri required"
		}
		out, err := sess.ReadResource(ctx, uri)
		if err != nil {
			return "error: " + err.Error()
		}
		return out
	case "list_prompts":
		m.mu.RLock()
		var list []PromptDef
		for _, p := range m.prompts {
			if p.Server == server || SanitizeName(p.Server) == SanitizeName(server) {
				list = append(list, p)
			}
		}
		m.mu.RUnlock()
		b, _ := json.MarshalIndent(list, "", "  ")
		return string(b)
	case "get_prompt":
		pname, _ := args["name"].(string)
		if pname == "" {
			return "error: name required"
		}
		strArgs := map[string]string{}
		if raw, ok := args["arguments"].(map[string]interface{}); ok {
			for k, v := range raw {
				strArgs[k] = fmt.Sprint(v)
			}
		}
		out, err := sess.GetPrompt(ctx, pname, strArgs)
		if err != nil {
			return "error: " + err.Error()
		}
		return out
	default:
		return "error: unknown mcp bridge action " + action
	}
}

func parseBridge(marbleName string) (server, action string, ok bool) {
	// mcp_<server>_list_resources | read_resource | list_prompts | get_prompt
	if !strings.HasPrefix(marbleName, "mcp_") {
		return "", "", false
	}
	rest := strings.TrimPrefix(marbleName, "mcp_")
	for _, act := range []string{"list_resources", "read_resource", "list_prompts", "get_prompt"} {
		suffix := "_" + act
		if strings.HasSuffix(rest, suffix) {
			server = strings.TrimSuffix(rest, suffix)
			return server, act, server != ""
		}
	}
	return "", "", false
}

// Health returns fields for /api/health.
func (m *Manager) Health() map[string]interface{} {
	if m == nil {
		return map[string]interface{}{"mcp_enabled": false}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	okN := 0
	for _, s := range m.status {
		if s.OK {
			okN++
		}
	}
	return map[string]interface{}{
		"mcp_enabled":       !m.disabled,
		"mcp_servers":       len(m.sessions),
		"mcp_servers_ok":    okN,
		"mcp_tools":         len(m.tools),
		"mcp_resources":     len(m.resources),
		"mcp_prompts":       len(m.prompts),
		"mcp_server_status": m.status,
	}
}

// ToolCount returns number of MCP tools registered.
func (m *Manager) ToolCount() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tools)
}

// ServerOKCount returns connected OK servers.
func (m *Manager) ServerOKCount() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, s := range m.status {
		if s.OK {
			n++
		}
	}
	return n
}
