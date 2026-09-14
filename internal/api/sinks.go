package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/rendicott/marble/internal/auth"
	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/sink"
)

func envNamesForSinks() []string {
	_, _, entries, err := config.ListManagedEnv()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Name != "" {
			out = append(out, e.Name)
		}
	}
	return out
}

// handleSinksSettings serves GET/PUT /api/settings/sinks and POST /api/settings/sinks/test.
func (s *Server) handleSinksSettings(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/settings/")
	path = strings.Trim(path, "/")
	if path == "sinks/test" {
		s.handleSinksTest(w, r)
		return
	}
	if path != "sinks" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg := sink.DefaultConfig()
		path := ""
		st := map[string]interface{}{"configured": false, "sinks": 0}
		hist := []sink.DeliveryRecord{}
		fallback := s.PublicOrigin(r)
		if s.Sinks != nil {
			cfg = s.Sinks.ConfigSnapshot()
			path = s.Sinks.ConfigPath()
			st = s.Sinks.Status()
			hist = s.Sinks.History()
			if b := s.Sinks.DeepLinkBaseResolved(); b != "" {
				fallback = b
			}
		} else if s.Cfg.Memory != "" {
			path = sink.ResolveConfigPath(s.Cfg.SinksConfig, s.Cfg.Memory)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"config":                  cfg,
			"config_path":             path,
			"status":                  st,
			"history":                 hist,
			"env_names":               envNamesForSinks(),
			"fallback_deep_link_base": fallback,
			"types":                   sink.KnownTypes,
			"kinds":                   sink.KnownKinds,
		})
	case http.MethodPut:
		if s.Sinks == nil {
			http.Error(w, "sinks unavailable", http.StatusServiceUnavailable)
			return
		}
		var body sink.Config
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if err := s.Sinks.ApplyConfig(body, true); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		u := auth.UserFromContext(r.Context())
		auth.LogAction("settings_sinks_put", "count="+itoa(len(body.Sinks)), u)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":          true,
			"config":      s.Sinks.ConfigSnapshot(),
			"config_path": s.Sinks.ConfigPath(),
			"status":      s.Sinks.Status(),
			"history":     s.Sinks.History(),
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSinksTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Sinks == nil {
		http.Error(w, "sinks unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	rec, err := s.Sinks.TestSink(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	u := auth.UserFromContext(r.Context())
	auth.LogAction("settings_sinks_test", "id="+id+" status="+rec.Status, u)
	writeJSON(w, http.StatusOK, rec)
}
