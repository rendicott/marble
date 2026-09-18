package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/rendicott/marble/internal/agentproc"
	"github.com/rendicott/marble/internal/auth"
	"github.com/rendicott/marble/internal/db"
)

func (s *Server) handleAgentsSettings(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/settings/")
	path = strings.Trim(path, "/")
	switch {
	case path == "agents" && r.Method == http.MethodGet:
		s.agentsGet(w, r)
	case path == "agents/detect" && r.Method == http.MethodPost:
		s.agentsDetect(w, r)
	case path == "agents/context" && r.Method == http.MethodPut:
		s.agentsPutContext(w, r)
	case path == "agents/presets" && r.Method == http.MethodPost:
		s.agentsCreatePreset(w, r)
	case strings.HasPrefix(path, "agents/presets/"):
		id := strings.TrimPrefix(path, "agents/presets/")
		id = strings.Trim(id, "/")
		switch r.Method {
		case http.MethodPut:
			s.agentsUpdatePreset(w, r, id)
		case http.MethodDelete:
			s.agentsDeletePreset(w, r, id)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleAgentPresetsPublic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	d := s.db()
	out := []map[string]interface{}{}
	if d != nil && d.Writable() {
		rows, err := d.ListAgentPresets()
		if err == nil {
			detectedOnly := r.URL.Query().Get("detected") == "1"
			for _, row := range rows {
				if !row.Enabled {
					continue
				}
				if detectedOnly && !row.Detected {
					continue
				}
				out = append(out, row.Public())
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"presets": out,
		"drivers": db.KnownAgentDrivers,
	})
}

func (s *Server) agentsGet(w http.ResponseWriter, r *http.Request) {
	d := s.db()
	presets := []map[string]interface{}{}
	if d != nil && d.Writable() {
		rows, err := d.ListAgentPresets()
		if err == nil {
			for _, row := range rows {
				presets = append(presets, row.Public())
			}
		}
	}
	caps := map[string]interface{}{}
	if s.Tools != nil && s.Tools.Agents != nil {
		cfg := s.Tools.Agents.Config()
		g := cfg.GlobalContextSpec()
		caps = map[string]interface{}{
			"default_timeout_sec":   cfg.DefaultTimeoutSec,
			"max_timeout_sec":       cfg.MaxTimeoutSec,
			"max_per_session":       cfg.MaxPerSession,
			"max_output_bytes":      cfg.MaxOutputBytes,
			"system_agents_enabled": cfg.SystemAgentsEnabled,
			"stuck_after_sec":       cfg.StuckAfterSec,
			"config_path":           agentproc.ConfigPath(s.Cfg.Memory),
			"context_default":       g.Sources,
			"context_max_chars":     g.MaxChars,
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"presets": presets,
		"drivers": db.KnownAgentDrivers,
		"caps":    caps,
	})
}

func (s *Server) agentsPutContext(w http.ResponseWriter, r *http.Request) {
	if s.Tools == nil || s.Tools.Agents == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	var body agentproc.ContextConfig
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := s.Tools.Agents.SetContext(body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	u := auth.UserFromContext(r.Context())
	auth.LogAction("settings_agents_context", fmt.Sprintf("default=%v max=%d", body.Default, body.MaxChars), u)
	s.agentsGet(w, r)
}

func (s *Server) agentsDetect(w http.ResponseWriter, r *http.Request) {
	d := s.db()
	if d == nil || !d.Writable() {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	n := detectAgentPresets(d)
	u := auth.UserFromContext(r.Context())
	auth.LogAction("settings_agents_detect", "n="+itoa(n), u)
	s.agentsGet(w, r)
}

func detectAgentPresets(d *db.DB) int {
	rows, err := d.ListAgentPresets()
	if err != nil {
		return 0
	}
	n := 0
	for _, row := range rows {
		p := agentproc.ProbeCommand(row.Command)
		_ = d.UpdateAgentPresetDetection(row.ID, p.Detected, p.Path, p.Version)
		if p.Detected {
			n++
		}
	}
	return n
}

func (s *Server) agentsCreatePreset(w http.ResponseWriter, r *http.Request) {
	d := s.db()
	if d == nil || !d.Writable() {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	row, err := decodePreset(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := d.InsertAgentPreset(row); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p := agentproc.ProbeCommand(row.Command)
	_ = d.UpdateAgentPresetDetection(row.ID, p.Detected, p.Path, p.Version)
	got, _ := d.GetAgentPreset(row.ID)
	u := auth.UserFromContext(r.Context())
	auth.LogAction("settings_agents_create", "id="+row.ID, u)
	writeJSON(w, http.StatusCreated, got.Public())
}

func (s *Server) agentsUpdatePreset(w http.ResponseWriter, r *http.Request, id string) {
	d := s.db()
	if d == nil || !d.Writable() {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	existing, err := d.GetAgentPreset(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	row, err := decodePreset(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	row.ID = existing.ID
	if err := d.UpdateAgentPreset(row); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p := agentproc.ProbeCommand(row.Command)
	_ = d.UpdateAgentPresetDetection(row.ID, p.Detected, p.Path, p.Version)
	got, _ := d.GetAgentPreset(row.ID)
	u := auth.UserFromContext(r.Context())
	auth.LogAction("settings_agents_update", "id="+row.ID, u)
	writeJSON(w, http.StatusOK, got.Public())
}

func (s *Server) agentsDeletePreset(w http.ResponseWriter, r *http.Request, id string) {
	d := s.db()
	if d == nil || !d.Writable() {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := d.DeleteAgentPreset(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	u := auth.UserFromContext(r.Context())
	auth.LogAction("settings_agents_delete", "id="+id, u)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "deleted", "id": id})
}

func decodePreset(r *http.Request) (db.AgentPresetRow, error) {
	var raw map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return db.AgentPresetRow{}, err
	}
	row := db.AgentPresetRow{
		ID:          strField(raw, "id"),
		Driver:      strField(raw, "driver"),
		DisplayName: strField(raw, "display_name"),
		Command:     strField(raw, "command"),
		Model:       strField(raw, "model"),
		Notes:       strField(raw, "notes"),
		Enabled:     true,
	}
	if v, ok := raw["enabled"].(bool); ok {
		row.Enabled = v
	}
	if v, ok := raw["timeout_sec"].(float64); ok {
		row.TimeoutSec = int(v)
	}
	if v, ok := raw["sort_order"].(float64); ok {
		row.SortOrder = int(v)
	}
	row.DefaultArgs = stringSliceField(raw, "default_args")
	if v, ok := raw["context_max_chars"].(float64); ok {
		row.ContextMaxChars = int(v)
	}
	switch v := raw["context"].(type) {
	case []interface{}:
		src := stringSliceField(raw, "context")
		b, _ := json.Marshal(src)
		row.Context = string(b)
	case string:
		spec, err := parseContextField(v)
		if err == nil {
			b, _ := json.Marshal(spec)
			row.Context = string(b)
		}
	}
	return row, db.ValidateAgentPreset(&row)
}

func parseContextField(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if strings.HasPrefix(s, "[") {
		var out []string
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			return nil, err
		}
		return out, nil
	}
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }), nil
}

func strField(m map[string]interface{}, k string) string {
	v, _ := m[k].(string)
	return strings.TrimSpace(v)
}

func stringSliceField(m map[string]interface{}, k string) []string {
	switch v := m[k].(type) {
	case []interface{}:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		return strings.Fields(v)
	}
	return nil
}
