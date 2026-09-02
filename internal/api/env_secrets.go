package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/rendicott/marble/internal/auth"
	"github.com/rendicott/marble/internal/config"
)

// handleSettingsEnv serves /api/settings/env — operator secret file CRUD.
// Pure HTTP ↔ disk; never goes through the model / agent tool path.
func (s *Server) handleSettingsEnv(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.envSecretsList(w, r)
	case http.MethodPut, http.MethodPost:
		s.envSecretsUpsert(w, r)
	case http.MethodDelete:
		s.envSecretsDelete(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) envSecretsList(w http.ResponseWriter, r *http.Request) {
	path, readPaths, entries, err := config.ListManagedEnv()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	u := auth.UserFromContext(r.Context())
	auth.LogAction("settings_env_list", "count="+itoa(len(entries)), u)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"managed_path": path,
		"read_paths":   readPaths,
		"entries":      entries,
		"security": map[string]interface{}{
			"model_bypass": true,
			"note": "This API writes KEY=value to the managed env file only. Values are never sent to the LLM, never stored in SQLite settings, and are not exposed via agent tools. Catalog/MCP entries reference env *names* only.",
			"file_mode": "0600",
		},
	})
}

func (s *Server) envSecretsUpsert(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	path, err := config.UpsertManagedEnv(body.Name, body.Value)
	if err != nil {
		if strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "required") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	u := auth.UserFromContext(r.Context())
	auth.LogAction("settings_env_upsert", "name="+strings.TrimSpace(body.Name), u)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":           true,
		"managed_path": path,
		"name":         strings.TrimSpace(body.Name),
	})
}

func (s *Server) envSecretsDelete(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		name = strings.TrimSpace(body.Name)
	}
	path, err := config.DeleteManagedEnv(name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "required") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	u := auth.UserFromContext(r.Context())
	auth.LogAction("settings_env_delete", "name="+name, u)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":           true,
		"managed_path": path,
		"name":         name,
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
