package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/rendicott/marble/internal/auth"
	"github.com/rendicott/marble/internal/mpub"
)

// handleMpub serves GET /mpub and GET /mpub/{slug}[/raw] (ADR-0009 + visibility).
// Public pages are always reachable. Private pages require an allowlisted admin
// when Google auth is on; in open mode everyone is treated as admin.
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
	admin := s.mpubViewerIsAdmin(r)

	if path == "" {
		list, err := s.Mpub.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		shown := make([]mpub.Meta, 0, len(list))
		for _, m := range list {
			if mpub.EffectiveVisibility(m) == mpub.VisibilityPublic || admin {
				shown = append(shown, m)
			}
		}
		html := mpub.IndexHTML(shown)
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
		// Do not leak existence of private docs to anonymous viewers.
		if !admin {
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}

	if mpub.EffectiveVisibility(doc.Meta) == mpub.VisibilityPrivate && !admin {
		s.rejectPrivateMpub(w, r)
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

// mpubViewerIsAdmin is true when the viewer may see private mpub pages.
// Open mode (no Google auth): all local operators. Google mode: logged-in allowlisted user.
func (s *Server) mpubViewerIsAdmin(r *http.Request) bool {
	if s.Auth == nil || !s.Auth.Enabled() {
		return true
	}
	return auth.UserFromContext(r.Context()) != nil
}

// rejectPrivateMpub asks for login (browser) or 401 JSON (API-style Accept).
func (s *Server) rejectPrivateMpub(w http.ResponseWriter, r *http.Request) {
	accept := r.Header.Get("Accept")
	xhr := strings.Contains(accept, "application/json") ||
		r.Header.Get(auth.CSRFHeader) != ""
	if xhr {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"auth_required","detail":"private mpub page"}`))
		return
	}
	next := r.URL.RequestURI()
	http.Redirect(w, r, "/auth/login?next="+url.QueryEscape(next), http.StatusFound)
}
