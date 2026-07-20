package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rendicott/marble/internal/auth"
	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/mpub"
)

func testMpubServer(t *testing.T, google bool) (*Server, *auth.SessionStore, *mpub.Store) {
	t.Helper()
	store, err := mpub.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Mpub: store, Mux: http.NewServeMux(), Cfg: config.Config{}}
	sess := auth.NewSessionStore()
	if google {
		s.Auth = &auth.Manager{
			Mode:  "google",
			Store: sess,
			Google: &auth.Google{
				Allowlist: map[string]struct{}{"admin@x.com": {}},
			},
		}
	} else {
		s.Auth = &auth.Manager{Mode: "open"}
	}
	s.routes()
	return s, sess, store
}

func TestMpubOpenModeSeesPrivate(t *testing.T) {
	s, _, store := testMpubServer(t, false)
	if _, err := store.Publish("secret", "S", "body", "text/plain", "", nil, false, mpub.VisibilityPrivate); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/mpub/secret", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("open mode private want 200 got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestMpubGooglePrivateUniform404(t *testing.T) {
	s, sess, store := testMpubServer(t, true)
	if _, err := store.Publish("secret", "S", "body-private", "text/plain", "", nil, false, mpub.VisibilityPrivate); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish("open", "O", "body-public", "text/plain", "", nil, false, mpub.VisibilityPublic); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// public OK anonymous + CSP
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/mpub/open", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "body-public") {
		t.Fatalf("public: %d %s", rr.Code, rr.Body.String())
	}
	csp := rr.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("missing CSP: %q", csp)
	}
	if rr.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("X-Frame-Options %q", rr.Header().Get("X-Frame-Options"))
	}

	// private anonymous → 404 (same as missing; no existence oracle / no login redirect)
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/mpub/secret", nil)
	h.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("private anon want 404 got %d", rr2.Code)
	}

	// missing also 404
	rrMiss := httptest.NewRecorder()
	reqMiss := httptest.NewRequest(http.MethodGet, "/mpub/does-not-exist", nil)
	h.ServeHTTP(rrMiss, reqMiss)
	if rrMiss.Code != http.StatusNotFound {
		t.Fatalf("missing want 404 got %d", rrMiss.Code)
	}

	// private with Accept JSON still 404 (not 401)
	rr3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/mpub/secret", nil)
	req3.Header.Set("Accept", "application/json")
	h.ServeHTTP(rr3, req3)
	if rr3.Code != 404 {
		t.Fatalf("want 404 got %d", rr3.Code)
	}

	// private as admin OK
	sid, _ := sess.Create(auth.User{Email: "admin@x.com"})
	rr4 := httptest.NewRecorder()
	req4 := httptest.NewRequest(http.MethodGet, "/mpub/secret", nil)
	req4.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sid})
	h.ServeHTTP(rr4, req4)
	if rr4.Code != 200 || !strings.Contains(rr4.Body.String(), "body-private") {
		t.Fatalf("admin private: %d %s", rr4.Code, rr4.Body.String())
	}

	// index: anonymous only public
	rr5 := httptest.NewRecorder()
	req5 := httptest.NewRequest(http.MethodGet, "/mpub", nil)
	h.ServeHTTP(rr5, req5)
	if rr5.Code != 200 {
		t.Fatal(rr5.Code)
	}
	body := rr5.Body.String()
	if !strings.Contains(body, "open") || strings.Contains(body, "secret") {
		t.Fatalf("anon index should list public only: %s", body)
	}

	// index: admin sees both
	rr6 := httptest.NewRecorder()
	req6 := httptest.NewRequest(http.MethodGet, "/mpub", nil)
	req6.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sid})
	h.ServeHTTP(rr6, req6)
	body6 := rr6.Body.String()
	if !strings.Contains(body6, "open") || !strings.Contains(body6, "secret") {
		t.Fatalf("admin index: %s", body6)
	}
	if !strings.Contains(body6, "badge-private") {
		t.Fatalf("expected private badge: %s", body6)
	}
}

func TestMpubRawPrivateGated(t *testing.T) {
	s, _, store := testMpubServer(t, true)
	if _, err := store.Publish("rawpriv", "R", "raw-secret", "text/plain", "", nil, false, mpub.VisibilityPrivate); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/mpub/rawpriv/raw", nil)
	req.Header.Set("Accept", "application/json")
	h.ServeHTTP(rr, req)
	if rr.Code != 404 {
		t.Fatalf("want 404 got %d", rr.Code)
	}
}

func TestHealthPublicMinimalGoogle(t *testing.T) {
	s, sess, _ := testMpubServer(t, true)
	s.Cfg.AuthMode = "google"
	h := s.Handler()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatal(rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "workspace") || strings.Contains(body, "base_url") || strings.Contains(body, "memory") {
		t.Fatalf("public health leaked detail: %s", body)
	}
	if !strings.Contains(body, `"auth_mode": "google"`) && !strings.Contains(body, `"auth_mode":"google"`) {
		// indented JSON from encoder
		if !strings.Contains(body, "google") {
			t.Fatalf("want auth_mode google: %s", body)
		}
	}

	// authed gets full health (may lack client — still should not be minimal-only shape with only 3 fields)
	sid, _ := sess.Create(auth.User{Email: "admin@x.com"})
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req2.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sid})
	h.ServeHTTP(rr2, req2)
	if rr2.Code != 200 {
		t.Fatal(rr2.Code)
	}
	if !strings.Contains(rr2.Body.String(), "model_ok") {
		t.Fatalf("authed health should be full: %s", rr2.Body.String())
	}
}
