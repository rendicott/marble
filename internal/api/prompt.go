package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"unicode/utf8"

	"github.com/rendicott/marble/internal/memory"
	"github.com/rendicott/marble/internal/session"
)

// handlePrompt serves GET/PUT /api/prompt (ADR-0013).
func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.promptGet(w, r)
	case http.MethodPut:
		s.promptPut(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) promptGet(w http.ResponseWriter, r *http.Request) {
	soul := ""
	soulPath := "soul.md"
	soulAbs := ""
	if store := s.Registry.Store(); store != nil {
		soulPath = "soul.md"
		soulAbs = store.SoulPath()
		if t, err := store.ReadSoul(); err == nil {
			soul = t
		}
	} else if s.Cfg.Memory != "" {
		soulAbs = filepath.Join(s.Cfg.Memory, "soul.md")
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"system_prompt":  session.SystemPrompt(),
		"soul":           soul,
		"soul_path":      soulPath,
		"soul_path_abs":  soulAbs,
		"soul_max_chars": memory.SoulMaxBytes,
		"immutable":      true,
	})
}

func (s *Server) promptPut(w http.ResponseWriter, r *http.Request) {
	store := s.Registry.Store()
	if store == nil {
		http.Error(w, "memory store not configured", http.StatusServiceUnavailable)
		return
	}
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	// Reject attempts to set system prompt or unknown keys
	if _, ok := body["system_prompt"]; ok {
		http.Error(w, "system_prompt is immutable", http.StatusBadRequest)
		return
	}
	for k := range body {
		if k != "soul" {
			http.Error(w, "unknown field: "+k+" (only soul is writable)", http.StatusBadRequest)
			return
		}
	}
	soulVal, ok := body["soul"]
	if !ok {
		http.Error(w, "soul required", http.StatusBadRequest)
		return
	}
	soul, ok := soulVal.(string)
	if !ok {
		http.Error(w, "soul must be a string", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(soul) > memory.SoulMaxBytes || len(soul) > memory.SoulMaxBytes {
		http.Error(w, "soul exceeds max size", http.StatusBadRequest)
		return
	}
	if err := store.WriteSoul(soul); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":             true,
		"soul":           soul,
		"soul_path":      "soul.md",
		"soul_path_abs":  store.SoulPath(),
		"soul_max_chars": memory.SoulMaxBytes,
	})
}
