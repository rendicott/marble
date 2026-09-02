package api

import (
	"fmt"
	"html"
	"net/http"
	"strings"
)

// handleConfirmPage serves GET/POST /confirm/{id} — Tailscale-reachable Accept/Deny.
// Prefer this over peer loopback mini-UI links in notifications.
func (s *Server) handleConfirmPage(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/confirm/")
	path = strings.Trim(path, "/")
	if path == "" || strings.Contains(path, "/") {
		http.Error(w, "missing confirm id", http.StatusBadRequest)
		return
	}
	id := path

	switch r.Method {
	case http.MethodGet:
		s.renderConfirmPage(w, r, id, "")
	case http.MethodPost:
		accept := r.FormValue("accept") == "1" || r.FormValue("accept") == "true"
		if s.PeerHub == nil {
			s.renderConfirmPage(w, r, id, "Peer hub unavailable")
			return
		}
		if err := s.PeerHub.ResolveConfirmFromHarness(id, accept); err != nil {
			s.renderConfirmPage(w, r, id, err.Error())
			return
		}
		msg := "Denied — action will not proceed."
		if accept {
			msg = "Accepted — the agent may continue."
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"/><meta name="viewport" content="width=device-width,initial-scale=1"/>
<title>Confirm resolved</title>
<style>body{font-family:system-ui,sans-serif;background:#0f1115;color:#e8ecf4;padding:2rem;max-width:36rem;margin:auto}
.ok{color:#86efac}.bad{color:#fecaca}a{color:#93c5fd}</style></head><body>
<h1 class="%s">%s</h1>
<p>You can close this tab and return to Marble.</p>
<p><a href="/">Open Marble</a></p>
</body></html>`, map[bool]string{true: "ok", false: "bad"}[accept], html.EscapeString(msg))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) renderConfirmPage(w http.ResponseWriter, r *http.Request, id, errMsg string) {
	var prompt, risk, computer, session string
	expired := false
	if s.PeerHub != nil {
		for _, p := range s.PeerHub.ListConfirms("") {
			if p.ID == id {
				prompt = p.Prompt
				risk = p.Risk
				computer = p.ComputerID
				session = p.SessionID
				expired = p.Expired
				break
			}
		}
	}
	if prompt == "" && errMsg == "" {
		errMsg = "This confirmation is unknown or already resolved. If a desktop notification remains, dismiss it."
	}
	if risk == "" {
		risk = "high"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"/><meta name="viewport" content="width=device-width,initial-scale=1"/>
<title>Marble confirm</title>
<style>
body{font-family:system-ui,sans-serif;background:#0f1115;color:#e8ecf4;padding:1.5rem;max-width:36rem;margin:auto;line-height:1.45}
.card{background:#422006;border:1px solid #f59e0b;border-radius:12px;padding:1.1rem 1.2rem}
.meta{color:#d6d3d1;font-size:0.85rem;margin-bottom:0.75rem}
.prompt{white-space:pre-wrap;background:rgba(0,0,0,.3);border-radius:8px;padding:0.75rem;margin:0.75rem 0 1rem}
.row{display:flex;gap:0.6rem}
button{flex:1;border:0;border-radius:8px;padding:0.85rem;font-size:1.05rem;font-weight:650;cursor:pointer;color:#fff}
.accept{background:#16a34a}.deny{background:#dc2626}
.err{color:#fecaca;margin-top:1rem}
.warn{color:#fde68a;font-size:0.9rem}
a{color:#93c5fd}
</style></head><body>
<div class="card">
  <div class="meta">⚠️ Marble confirmation · %s · risk %s%s</div>
  <div class="prompt">%s</div>
  %s
  <form method="POST" class="row">
    <button class="accept" name="accept" value="1" type="submit">Accept</button>
    <button class="deny" name="accept" value="0" type="submit">Deny / Dismiss</button>
  </form>
  <p class="warn" style="margin-top:0.85rem">Deny also clears a stale card if the peer already timed out. Prefer this page over peer loopback links (127.0.0.1) when using Tailscale.</p>
</div>
%s
<p style="margin-top:1.25rem"><a href="/">← Marble</a>%s</p>
</body></html>`,
		html.EscapeString(computer),
		html.EscapeString(risk),
		map[bool]string{true: " · expired/stale", false: ""}[expired],
		html.EscapeString(prompt),
		map[bool]string{true: `<p class="warn">This confirm has expired on the peer (default deny). You can still Dismiss to clear it from Marble.</p>`, false: ""}[expired],
		map[bool]string{true: `<p class="err">` + html.EscapeString(errMsg) + `</p>`, false: ""}[errMsg != ""],
		map[bool]string{true: ` · <a href="/?session=` + html.EscapeString(session) + `">Open session</a>`, false: ""}[session != ""],
	)
}

// PublicOrigin returns a browser-reachable origin for this harness (Tailscale-friendly).
// Order: --public-url → OAuth redirect host → request Host → listen addr as http://127.0.0.1:port.
func (s *Server) PublicOrigin(r *http.Request) string {
	if s != nil {
		if u := strings.TrimRight(strings.TrimSpace(s.Cfg.PublicURL), "/"); u != "" {
			return u
		}
		if u := strings.TrimSpace(s.Cfg.OAuthRedirectURL); u != "" {
			// https://host:8080/auth/callback → https://host:8080
			if i := strings.Index(u, "://"); i >= 0 {
				rest := u[i+3:]
				if j := strings.Index(rest, "/"); j >= 0 {
					return u[:i+3] + rest[:j]
				}
				return u
			}
		}
	}
	if r != nil {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if xf := r.Header.Get("X-Forwarded-Proto"); xf != "" {
			scheme = strings.TrimSpace(strings.Split(xf, ",")[0])
		}
		host := r.Header.Get("X-Forwarded-Host")
		if host == "" {
			host = r.Host
		}
		host = strings.TrimSpace(strings.Split(host, ",")[0])
		if host != "" {
			return scheme + "://" + host
		}
	}
	if s != nil {
		addr := strings.TrimSpace(s.Cfg.Addr)
		if addr == "" {
			addr = ":8080"
		}
		if strings.HasPrefix(addr, ":") {
			return "http://127.0.0.1" + addr
		}
		if !strings.Contains(addr, "://") {
			return "http://" + addr
		}
		return addr
	}
	return ""
}

// ConfirmPageURL builds http(s)://harness/confirm/{id}.
func (s *Server) ConfirmPageURL(r *http.Request, id string) string {
	base := s.PublicOrigin(r)
	if base == "" {
		// Process listen addr fallback (may be :8080 only — still better than peer loopback for local).
		addr := strings.TrimSpace(s.Cfg.Addr)
		if addr == "" {
			addr = ":8080"
		}
		if strings.HasPrefix(addr, ":") {
			base = "http://127.0.0.1" + addr
		} else {
			base = "http://" + addr
		}
	}
	return strings.TrimRight(base, "/") + "/confirm/" + id
}
