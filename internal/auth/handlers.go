package auth

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// RegisterRoutes mounts /auth/* on mux.
func (m *Manager) RegisterRoutes(mux *http.ServeMux) {
	if m == nil {
		return
	}
	mux.HandleFunc("/auth/login", m.handleLogin)
	mux.HandleFunc("/auth/callback", m.handleCallback)
	mux.HandleFunc("/auth/logout", m.handleLogout)
	mux.HandleFunc("/auth/me", m.handleMe)
}

func (m *Manager) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !m.Enabled() {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if !m.Store.AllowLoginStart(clientKey(r)) {
		http.Error(w, "too many login attempts; try again later", http.StatusTooManyRequests)
		return
	}
	next := SafeNext(r.URL.Query().Get("next"))
	state, err := GenerateState()
	if err != nil {
		http.Error(w, "state error", http.StatusInternalServerError)
		return
	}
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		http.Error(w, "pkce error", http.StatusInternalServerError)
		return
	}
	if !m.Store.PutPending(state, verifier, next) {
		http.Error(w, "login temporarily unavailable; try again later", http.StatusServiceUnavailable)
		return
	}
	loc := m.Google.AuthCodeURL(state, challenge)
	http.Redirect(w, r, loc, http.StatusFound)
}

// clientKey is a coarse rate-limit key (host from RemoteAddr).
func clientKey(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	return host
}

func (m *Manager) handleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !m.Enabled() {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		http.Error(w, "oauth error: "+errParam, http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Error(w, "missing code/state", http.StatusBadRequest)
		return
	}
	verifier, next, ok := m.Store.TakePending(state)
	if !ok {
		http.Error(w, "invalid or expired oauth state", http.StatusBadRequest)
		return
	}
	user, err := m.Google.ExchangeCode(r.Context(), code, verifier)
	if err != nil {
		log.Printf("auth: callback reject: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body style="font-family:system-ui;padding:2rem">
<h1>Access denied</h1>
<p>Your Google account is not on the Marble allowlist, or sign-in failed.</p>
<p><a href="/auth/login">Try again</a></p>
</body></html>`))
		return
	}
	sid, _ := m.Store.Create(*user)
	m.SetSessionCookie(w, sid)
	log.Printf("auth: login email=%s sub=%s", user.Email, user.Sub)
	http.Redirect(w, r, next, http.StatusFound)
}

func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c, err := r.Cookie(CookieName); err == nil {
		m.Store.Delete(c.Value)
	}
	m.ClearSessionCookie(w)
	if u := UserFromContext(r.Context()); u != nil {
		log.Printf("auth: logout email=%s", u.Email)
	}
	// Prefer JSON for SPA POST
	if r.Method == http.MethodPost || strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func (m *Manager) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if !m.Enabled() {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"auth_mode": "open",
			"user":      nil,
		})
		return
	}
	u := m.userFromRequest(r)
	if u == nil {
		// also try context
		u = UserFromContext(r.Context())
	}
	if u == nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"auth_mode": "google",
			"error":     "auth_required",
			"user":      nil,
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"auth_mode": "google",
		"user":      u,
	})
}

// LoginURL builds /auth/login?next=…
func LoginURL(next string) string {
	return "/auth/login?next=" + url.QueryEscape(SafeNext(next))
}
