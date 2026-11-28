package api

import (
	"net/http"

	"github.com/rendicott/marble/internal/memsearch"
)

// handleMemoryIndex serves GET /api/memory-index and
// POST /api/memory-index/reindex. Operator auth only: neither path is in
// isPublicPath.
func (s *Server) handleMemoryIndex(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/memory-index", "/api/memory-index/":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ix, ok := s.memIndex(w)
		if !ok {
			return
		}
		st, err := ix.Stats()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, st)
	case "/api/memory-index/reindex":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ix, ok := s.memIndex(w)
		if !ok {
			return
		}
		rep, err := ix.ReindexFull()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rep)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) memIndex(w http.ResponseWriter) (*memsearch.Index, bool) {
	if s.Tools == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "memory index unavailable"})
		return nil, false
	}
	ix, err := s.Tools.MemoryIndex()
	if err != nil || ix == nil {
		msg := "memory index unavailable"
		if err != nil {
			msg += ": " + err.Error()
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": msg})
		return nil, false
	}
	return ix, true
}
