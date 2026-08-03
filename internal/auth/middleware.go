package auth

import (
	"log"
	"net/http"
	"net/url"
	"strings"
)

// Manager wires OAuth + sessions + middleware (ADR-0017).
type Manager struct {
	Mode   string // open | google
	Secure bool   // cookie Secure
	Google *Google
	Store  *SessionStore
}

// Enabled is true when google mode is active.
func (m *Manager) Enabled() bool {
	return m != nil && m.Mode == "google" && m.Google != nil && m.Store != nil
}

// Middleware enforces auth for protected paths when google mode is on.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	if !m.Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if isPublicPath(path) {
			// Still attach user if cookie present (optional for /auth/me).
			if u := m.userFromRequest(r); u != nil {
				r = r.WithContext(WithUser(r.Context(), u))
			}
			next.ServeHTTP(w, r)
			return
		}
		u := m.userFromRequest(r)
		if u == nil {
			m.reject(w, r)
			return
		}
		// CSRF: mutating methods on API/SPA need custom header (Q12).
		if isMutating(r.Method) && strings.HasPrefix(path, "/api/") {
			if r.Header.Get(CSRFHeader) != CSRFValue {
				http.Error(w, `{"error":"csrf_required"}`, http.StatusForbidden)
				return
			}
		}
		r = r.WithContext(WithUser(r.Context(), u))
		next.ServeHTTP(w, r)
	})
}

func isPublicPath(path string) bool {
	if path == "/mpub" || strings.HasPrefix(path, "/mpub/") {
		return true
	}
	if path == "/api/health" {
		return true
	}
	// Desktop peer pairing + WebSocket (ADR-0020) — auth via H-code / device token.
	if path == "/api/computers/pair/join" || path == "/api/computers/pair/status" || path == "/api/computers/ws" {
		return true
	}
	if strings.HasPrefix(path, "/auth/") {
		return true
	}
	return false
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func (m *Manager) userFromRequest(r *http.Request) *User {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	return m.Store.Get(c.Value)
}

func (m *Manager) reject(w http.ResponseWriter, r *http.Request) {
	// API / fetch → 401 JSON; navigation → redirect to login
	accept := r.Header.Get("Accept")
	xhr := r.Header.Get(CSRFHeader) != "" || strings.Contains(accept, "application/json") ||
		strings.HasPrefix(r.URL.Path, "/api/")
	if xhr {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"auth_required"}`))
		return
	}
	next := r.URL.RequestURI()
	http.Redirect(w, r, "/auth/login?next="+url.QueryEscape(next), http.StatusFound)
}

// SetSessionCookie writes the session cookie.
func (m *Manager) SetSessionCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   m.Secure,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

// ClearSessionCookie clears the cookie.
func (m *Manager) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   m.Secure,
		MaxAge:   -1,
	})
}

// LogAction logs an attributed action (ADR-0017 Q17).
func LogAction(action, detail string, u *User) {
	who := "anonymous"
	if u != nil && u.Email != "" {
		who = u.Email
	}
	log.Printf("auth action=%s by=%s %s", action, who, detail)
}
