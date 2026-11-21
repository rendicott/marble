package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/auth"
	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/mcp"
	"github.com/rendicott/marble/internal/shellpolicy"
)

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/settings")
	path = strings.Trim(path, "/")

	switch {
	case path == "" && r.Method == http.MethodGet:
		s.settingsGet(w, r)
	case path == "" && r.Method == http.MethodPut:
		s.settingsPut(w, r)
	case path == "reset" && r.Method == http.MethodPost:
		s.settingsReset(w, r)
	case path == "mcp" && r.Method == http.MethodGet:
		s.mcpGet(w, r)
	case path == "mcp" && r.Method == http.MethodPut:
		s.mcpPut(w, r)
	case path == "mcp/login":
		s.mcpLogin(w, r)
	case path == "mcp/logout" && r.Method == http.MethodPost:
		s.mcpLogout(w, r)
	case path == "env":
		// Secrets / env file manager — never routed through the agent loop.
		s.handleSettingsEnv(w, r)
	case path == "tts":
		s.handleTTSSettings(w, r)
	case path == "sinks" || strings.HasPrefix(path, "sinks/"):
		s.handleSinksSettings(w, r)
	case path == "agents" || strings.HasPrefix(path, "agents/"):
		s.handleAgentsSettings(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) settingsGet(w http.ResponseWriter, r *http.Request) {
	sqldb := s.Registry.DB()
	editable := sqldb != nil && sqldb.Writable()
	mode := "none"
	limpReason := ""
	if sqldb != nil {
		mode = string(sqldb.Mode)
		limpReason = sqldb.Reason
	}

	persistent := map[string]string{}
	if sqldb != nil {
		var err error
		persistent, err = sqldb.ListSettings()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		persistent = db.DefaultSettings()
	}

	// Ensure deny patterns present for UI
	if strings.TrimSpace(persistent["shell_deny_patterns"]) == "" {
		b, _ := json.Marshal(shellpolicy.DefaultDenyPatterns)
		persistent["shell_deny_patterns"] = string(b)
	}

	runtime := map[string]interface{}{
		"model":                 s.Cfg.Model,
		"base_url":              s.Cfg.BaseURL,
		"workspace":             s.Cfg.Workspace,
		"memory":                s.Cfg.Memory,
		"addr":                  s.Cfg.Addr,
		"context_limit":         s.Cfg.ContextLimit,
		"max_output":            s.Cfg.MaxOutput,
		"context_reserve":       s.Cfg.ContextReserve,
		"budget":                s.Cfg.Budget(),
		"persist_interval_sec":  int(s.Cfg.PersistEvery.Seconds()),
		"disable_shell":         s.Cfg.DisableShell,
		"max_tool_iters":        s.Cfg.MaxToolIters,
		"tool_round_soft":       s.Cfg.ToolRoundSoft,
		"soft_wall_sec":         int(s.Cfg.SoftWall.Seconds()),
		"hard_wall_sec":         int(s.Cfg.HardWall.Seconds()),
		"auto_continue_reserve": s.Cfg.AutoContinueReserve,
		"anti_repeat_n":         s.Cfg.AntiRepeatN,
		"stuck_escalate_k":      s.Cfg.StuckEscalateK,
		"block_sleep_shell":     s.Cfg.BlockSleepShell,
		"eval_mutate_max":       s.Cfg.EvalMutateMax,
		"schema_version_binary": db.CurrentSchemaVersion,
		"mcp_config_path":       mcp.ResolveConfigPath(s.Cfg.MCPConfig, s.Cfg.Memory),
		"mcp_disabled_cli":      s.Cfg.MCPDisable,
		"note":                  "from launch flags — restart to change",
	}
	for k, v := range s.Cfg.ModelAuthPublic() {
		runtime[k] = v
	}
	for k, v := range s.Cfg.AuthPublicSettings() {
		runtime[k] = v
	}
	if u := auth.UserFromContext(r.Context()); u != nil {
		runtime["current_user"] = u
	}
	if sqldb != nil && sqldb.Writable() {
		if ver, err := s.readSchemaVer(sqldb); err == nil {
			runtime["schema_version_db"] = ver
		}
	}

	shellEff := s.shellEffective(persistent)

	mcpSummary := map[string]interface{}{}
	if s.MCP != nil {
		mcpSummary = s.MCP.Health()
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"mode":            mode,
		"limp_reason":     limpReason,
		"editable":        editable,
		"runtime":         runtime,
		"persistent":      persistent,
		"shell_effective": shellEff,
		"mcp":             mcpSummary,
	})
}

func (s *Server) readSchemaVer(d *db.DB) (int, error) {
	h := d.Health()
	if v, ok := h["schema_version_db"]; ok {
		switch n := v.(type) {
		case int:
			return n, nil
		case int64:
			return int(n), nil
		}
	}
	return db.CurrentSchemaVersion, nil
}

func (s *Server) shellEffective(persistent map[string]string) map[string]interface{} {
	enabled := true
	reason := ""
	if s.Cfg.DisableShell {
		enabled = false
		reason = "disabled by CLI (--disable-shell)"
	} else {
		v := strings.ToLower(persistent["shell_enabled"])
		if v == "false" || v == "0" || v == "no" || v == "off" {
			enabled = false
			reason = "disabled in settings"
		}
	}
	mode := persistent["shell_mode"]
	if mode == "" {
		mode = "deny_list"
	}
	label := "disabled"
	if enabled {
		label = "enabled (" + mode + ")"
	}
	return map[string]interface{}{
		"enabled": enabled,
		"mode":    mode,
		"label":   label,
		"reason":  reason,
	}
}

func (s *Server) settingsPut(w http.ResponseWriter, r *http.Request) {
	sqldb := s.Registry.DB()
	if sqldb == nil || !sqldb.Writable() {
		http.Error(w, "database not writable (limp mode or unavailable)", http.StatusServiceUnavailable)
		return
	}
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	// Normalize multi-line pattern fields if client sent lines instead of JSON
	for _, k := range []string{"shell_allow_patterns", "shell_deny_patterns"} {
		if v, ok := body[k]; ok {
			nv, err := normalizePatternField(v)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			body[k] = nv
		}
	}
	if err := sqldb.SetSettings(body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// live-apply shell policy
	if s.Policy != nil {
		s.Policy.Reload()
	}
	persistent, _ := sqldb.ListSettings()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":          "ok",
		"persistent":      persistent,
		"shell_effective": s.shellEffective(persistent),
	})
}

func (s *Server) settingsReset(w http.ResponseWriter, r *http.Request) {
	sqldb := s.Registry.DB()
	if sqldb == nil || !sqldb.Writable() {
		http.Error(w, "database not writable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Section string `json:"section"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := sqldb.ResetSection(body.Section); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.Policy != nil {
		s.Policy.Reload()
	}
	persistent, _ := sqldb.ListSettings()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":     "ok",
		"persistent": persistent,
	})
}

func normalizePatternField(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "[]", nil
	}
	// already JSON array?
	if strings.HasPrefix(v, "[") {
		var pats []string
		if err := json.Unmarshal([]byte(v), &pats); err != nil {
			return "", err
		}
		b, _ := json.Marshal(pats)
		return string(b), nil
	}
	// lines
	var pats []string
	for _, line := range strings.Split(v, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pats = append(pats, line)
	}
	b, err := json.Marshal(pats)
	return string(b), err
}

func (s *Server) mcpGet(w http.ResponseWriter, r *http.Request) {
	path := mcp.ResolveConfigPath(s.Cfg.MCPConfig, s.Cfg.Memory)
	fc, err := mcp.LoadFileRaw(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// redact nothing; env placeholders preserved in raw
	health := map[string]interface{}{}
	oauth := map[string]mcp.OAuthServerInfo{}
	if s.MCP != nil {
		s.pinMCPOAuthStore()
		health = s.MCP.Health()
		oauth = s.MCP.OAuthInfo()
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"path":     path,
		"config":   fc,
		"health":   health,
		"oauth":    oauth,
		"disabled": s.Cfg.MCPDisable,
	})
}

// pinMCPOAuthStore points the MCP token store at $MEMORY/mcp-oauth.json.
func (s *Server) pinMCPOAuthStore() {
	if s.MCP != nil && strings.TrimSpace(s.Cfg.Memory) != "" {
		s.MCP.SetOAuthStorePath(mcp.OAuthStorePath(s.Cfg.Memory))
	}
}

func mcpOAuthErrStatus(err error) int {
	switch {
	case errors.Is(err, mcp.ErrUnknownServer), errors.Is(err, mcp.ErrNoLogin):
		return http.StatusNotFound
	case errors.Is(err, mcp.ErrLoginPending):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

// mcpLogin serves /api/settings/mcp/login:
//
//	POST {"name"}                 → start OAuth flow → {"status","auth_url"} (409 if pending)
//	POST {"name","redirect_url"}  → complete flow from a pasted redirect URL
//	GET  ?name=                   → {"status":"pending|done|error","auth_url","error"}
func (s *Server) mcpLogin(w http.ResponseWriter, r *http.Request) {
	if s.MCP == nil || s.Cfg.MCPDisable {
		http.Error(w, "MCP disabled", http.StatusBadRequest)
		return
	}
	s.pinMCPOAuthStore()
	switch r.Method {
	case http.MethodGet:
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		st, err := s.MCP.OAuthLoginStatus(name)
		if err != nil {
			http.Error(w, err.Error(), mcpOAuthErrStatus(err))
			return
		}
		writeJSON(w, http.StatusOK, st)
	case http.MethodPost:
		var body struct {
			Name        string `json:"name"`
			RedirectURL string `json:"redirect_url"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		body.Name = strings.TrimSpace(body.Name)
		if body.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		var st mcp.LoginStatus
		var err error
		if strings.TrimSpace(body.RedirectURL) != "" {
			st, err = s.MCP.CompleteOAuthLogin(body.Name, body.RedirectURL)
		} else {
			st, err = s.MCP.StartOAuthLogin(r.Context(), body.Name)
		}
		if err != nil {
			code := mcpOAuthErrStatus(err)
			if code == http.StatusConflict {
				writeJSON(w, code, map[string]interface{}{
					"status": st.Status, "auth_url": st.AuthURL, "error": err.Error(),
				})
				return
			}
			http.Error(w, err.Error(), code)
			return
		}
		writeJSON(w, http.StatusOK, st)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// mcpLogout serves POST /api/settings/mcp/logout {"name"} — drops the stored token.
func (s *Server) mcpLogout(w http.ResponseWriter, r *http.Request) {
	if s.MCP == nil || s.Cfg.MCPDisable {
		http.Error(w, "MCP disabled", http.StatusBadRequest)
		return
	}
	s.pinMCPOAuthStore()
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	existed, err := s.MCP.ForgetOAuthToken(ctx, name)
	if err != nil {
		http.Error(w, err.Error(), mcpOAuthErrStatus(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "removed": existed})
}

func (s *Server) mcpPut(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.MCPDisable {
		http.Error(w, "MCP disabled via --mcp-disable", http.StatusBadRequest)
		return
	}
	path := mcp.ResolveConfigPath(s.Cfg.MCPConfig, s.Cfg.Memory)
	var fc mcp.FileConfig
	if err := json.NewDecoder(r.Body).Decode(&fc); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if fc.MCPServers == nil {
		fc.MCPServers = map[string]mcp.ServerConfig{}
	}
	// basic validation
	for name, sc := range fc.MCPServers {
		if strings.TrimSpace(name) == "" {
			http.Error(w, "server name empty", http.StatusBadRequest)
			return
		}
		if sc.Kind() == "" {
			http.Error(w, "server "+name+": need command or url", http.StatusBadRequest)
			return
		}
	}
	if err := mcp.SaveFile(path, fc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// reload live connections (expand env for runtime)
	if s.MCP != nil {
		s.pinMCPOAuthStore()
		live, err := mcp.LoadFile(path)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			s.MCP.Reload(ctx, live)
			cancel()
		}
	}
	health := map[string]interface{}{}
	if s.MCP != nil {
		health = s.MCP.Health()
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "ok",
		"path":   path,
		"config": fc,
		"health": health,
	})
}
