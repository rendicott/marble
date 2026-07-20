package api

import (
	"net/http"
	"strings"

	"github.com/rendicott/marble/internal/auth"
	"github.com/rendicott/marble/internal/mpub"
)

// mpubCSP blocks scripts and most active content on published pages (same-origin XSS mitigation).
// Inline styles allowed for the simple mpub shell; images from https/data only.
const mpubCSP = "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; " +
	"img-src data: https: http:; style-src 'unsafe-inline'; font-src data:"

// handleMpub serves GET /mpub and GET /mpub/{slug}[/raw] (ADR-0009 + visibility).
// Public pages are always reachable. Private pages require an allowlisted admin
// when Google auth is on; in open mode everyone is treated as admin.
// Anonymous viewers get a uniform 404 for missing and private slugs (no existence oracle).
func (s *Server) handleMpub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Mpub == nil {
		http.Error(w, "mpub not configured", http.StatusServiceUnavailable)
		return
	}
	setMpubSecurityHeaders(w)

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
		http.NotFound(w, r)
		return
	}

	if mpub.EffectiveVisibility(doc.Meta) == mpub.VisibilityPrivate && !admin {
		// Same status as missing — do not leak private slug existence.
		http.NotFound(w, r)
		return
	}

	if raw {
		ct := doc.Meta.ContentType
		if ct == "" {
			ct = "text/plain"
		}
		// Avoid treating raw HTML as executable script surface without CSP (CSP already set).
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

func setMpubSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", mpubCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
}
