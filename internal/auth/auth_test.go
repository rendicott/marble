package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSafeNext(t *testing.T) {
	if SafeNext("") != "/" {
		t.Fatal("empty")
	}
	if SafeNext("/s/abc") != "/s/abc" {
		t.Fatal("relative")
	}
	if SafeNext("https://evil.com") != "/" {
		t.Fatal("absolute should reject")
	}
	if SafeNext("//evil.com") != "/" {
		t.Fatal("protocol-relative")
	}
}

func TestPKCE(t *testing.T) {
	v, c, err := GeneratePKCE()
	if err != nil || v == "" || c == "" {
		t.Fatal(err, v, c)
	}
	if v == c {
		t.Fatal("verifier should differ from challenge")
	}
}

func TestSessionStore(t *testing.T) {
	s := NewSessionStore()
	id, _ := s.Create(User{Email: "a@b.com", Name: "A", Sub: "1"})
	u := s.Get(id)
	if u == nil || u.Email != "a@b.com" {
		t.Fatalf("%+v", u)
	}
	s.Delete(id)
	if s.Get(id) != nil {
		t.Fatal("expected deleted")
	}
}

func TestMiddlewareOpen(t *testing.T) {
	m := &Manager{Mode: "open"}
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatal(rr.Code)
	}
}

func TestMiddlewareGoogleRequiresAuth(t *testing.T) {
	store := NewSessionStore()
	m := &Manager{
		Mode:  "google",
		Store: store,
		Google: &Google{
			Allowlist: map[string]struct{}{"a@b.com": {}},
		},
	}
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("Accept", "application/json")
	h.ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("want 401 got %d", rr.Code)
	}

	// mpub public
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/mpub/foo", nil)
	h.ServeHTTP(rr2, req2)
	if rr2.Code != 200 {
		t.Fatalf("mpub want 200 got %d", rr2.Code)
	}

	// health public
	rr3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	h.ServeHTTP(rr3, req3)
	if rr3.Code != 200 {
		t.Fatalf("health want 200 got %d", rr3.Code)
	}

	// with session + CSRF on POST
	sid, _ := store.Create(User{Email: "a@b.com"})
	rr4 := httptest.NewRecorder()
	req4 := httptest.NewRequest(http.MethodPost, "/api/sessions/x/messages", nil)
	req4.AddCookie(&http.Cookie{Name: CookieName, Value: sid})
	// missing CSRF
	h.ServeHTTP(rr4, req4)
	if rr4.Code != 403 {
		t.Fatalf("csrf want 403 got %d", rr4.Code)
	}
	rr5 := httptest.NewRecorder()
	req5 := httptest.NewRequest(http.MethodPost, "/api/sessions/x/messages", nil)
	req5.AddCookie(&http.Cookie{Name: CookieName, Value: sid})
	req5.Header.Set(CSRFHeader, CSRFValue)
	h.ServeHTTP(rr5, req5)
	if rr5.Code != 200 {
		t.Fatalf("authed want 200 got %d", rr5.Code)
	}
}

func TestAllowlist(t *testing.T) {
	g := &Google{Allowlist: map[string]struct{}{"alice@x.com": {}}}
	if !g.Allowed("Alice@X.com") {
		t.Fatal("case")
	}
	if g.Allowed("bob@x.com") {
		t.Fatal("deny")
	}
}
