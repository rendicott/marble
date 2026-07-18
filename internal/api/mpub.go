package api

import (
	"net/http"
	"strings"

	"github.com/rendicott/marble/internal/mpub"
)

// handleMpub serves GET /mpub and GET /mpub/{slug}[/raw] (ADR-0009).
func (s *Server) handleMpub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Mpub == nil {
		http.Error(w, "mpub not configured", http.StatusServiceUnavailable)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/mpub")
	path = strings.Trim(path, "/")

	if path == "" {
		list, err := s.Mpub.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		html := mpub.IndexHTML(list)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write([]byte(html))
		return
	}

	parts := strings.Split(path, "/")
	slug := parts[0]
	raw := len(parts) > 1 && parts[1] == "raw"
	if len(parts) > 2 || (len(parts) == 2 && !raw) {
		http.NotFound(w, r)
		return
	}

	doc, err := s.Mpub.Get(slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if raw {
		ct := doc.Meta.ContentType
		if ct == "" {
			ct = "text/plain"
		}
		w.Header().Set("Content-Type", ct+"; charset=utf-8")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write([]byte(doc.Content))
		return
	}

	ct, body := mpub.ServeBody(doc)
	w.Header().Set("Content-Type", ct)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}
