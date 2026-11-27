package api

import (
	"net/http"
	"strconv"

	"github.com/rendicott/marble/internal/db"
)

// handleToolLog serves GET /api/tool-log and GET /api/tool-log/stats.
// Operator auth only: neither path is in isPublicPath.
func (s *Server) handleToolLog(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/tool-log/stats" {
		s.handleToolLogStats(w, r)
		return
	}
	if r.URL.Path != "/api/tool-log" && r.URL.Path != "/api/tool-log/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	database, ok := s.toolLogDB(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit := 0
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			http.Error(w, "bad limit", http.StatusBadRequest)
			return
		}
		limit = n
	}
	rows, err := database.ListToolCallLogs(q.Get("session_id"), q.Get("tool"), q.Get("since"), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

func (s *Server) handleToolLogStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	database, ok := s.toolLogDB(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	st, err := database.ToolCallLogStats(q.Get("tool"), q.Get("since"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) toolLogDB(w http.ResponseWriter) (*db.DB, bool) {
	if s.Registry == nil || s.Registry.DB() == nil || !s.Registry.DB().Writable() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "tool log unavailable"})
		return nil, false
	}
	return s.Registry.DB(), true
}
