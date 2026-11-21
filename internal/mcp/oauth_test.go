package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseWWWAuthenticate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []authChallenge
	}{
		{"empty", "", nil},
		{"bare bearer", "Bearer", []authChallenge{{Scheme: "Bearer", Params: map[string]string{}}}},
		{
			"mcp style",
			`Bearer realm="mcp", resource_metadata="https://api.example.com/.well-known/oauth-protected-resource/mcp", scope="read write"`,
			[]authChallenge{{Scheme: "Bearer", Params: map[string]string{
				"realm":             "mcp",
				"resource_metadata": "https://api.example.com/.well-known/oauth-protected-resource/mcp",
				"scope":             "read write",
			}}},
		},
		{
			"unquoted and escaped",
			`Bearer error=invalid_token, error_description="say \"hi\", ok"`,
			[]authChallenge{{Scheme: "Bearer", Params: map[string]string{
				"error":             "invalid_token",
				"error_description": `say "hi", ok`,
			}}},
		},
		{
			"multiple challenges, case-insensitive param names",
			`Basic realm="x", Bearer Resource_Metadata="/prm"`,
			[]authChallenge{
				{Scheme: "Basic", Params: map[string]string{"realm": "x"}},
				{Scheme: "Bearer", Params: map[string]string{"resource_metadata": "/prm"}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseWWWAuthenticate(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v want %#v", got, tc.want)
			}
		})
	}
	if p := bearerChallengeParams(`Basic realm="x", Bearer scope="a"`); p["scope"] != "a" {
		t.Fatalf("bearer params: %v", p)
	}
}

func TestWellKnownURLs(t *testing.T) {
	tests := []struct {
		name string
		fn   func(string) []string
		in   string
		want []string
	}{
		{"prm with path", protectedResourceMetadataURLs, "https://mcp.example.com/mcp/", []string{
			"https://mcp.example.com/.well-known/oauth-protected-resource/mcp",
			"https://mcp.example.com/.well-known/oauth-protected-resource",
		}},
		{"prm root", protectedResourceMetadataURLs, "https://mcp.example.com", []string{
			"https://mcp.example.com/.well-known/oauth-protected-resource",
		}},
		{"as root", authServerMetadataURLs, "https://auth.example.com/", []string{
			"https://auth.example.com/.well-known/oauth-authorization-server",
			"https://auth.example.com/.well-known/openid-configuration",
		}},
		{"as with path", authServerMetadataURLs, "https://auth.example.com/tenant1", []string{
			"https://auth.example.com/.well-known/oauth-authorization-server/tenant1",
			"https://auth.example.com/.well-known/openid-configuration/tenant1",
			"https://auth.example.com/tenant1/.well-known/openid-configuration",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.fn(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeServerURL(t *testing.T) {
	tests := map[string]string{
		"HTTPS://MCP.Example.com:443/mcp/":  "https://mcp.example.com/mcp",
		"http://127.0.0.1:80/x#frag":        "http://127.0.0.1/x",
		"https://u:p@host:8443/a?b=1":       "https://host:8443/a?b=1",
		" https://mcp.producthunt.com/mcp ": "https://mcp.producthunt.com/mcp",
	}
	for in, want := range tests {
		if got := NormalizeServerURL(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestPKCES256KnownVector(t *testing.T) {
	// RFC 7636 Appendix B.
	got := pkceS256("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
	if want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	v, err := randomURLToken(32)
	if err != nil || len(v) < 43 {
		t.Fatalf("verifier %q err %v", v, err)
	}
}

func TestParseRedirectURL(t *testing.T) {
	tests := []struct {
		name, in, code, state, errSub string
	}{
		{"full url", "http://127.0.0.1:53111/callback?code=abc&state=xyz", "abc", "xyz", ""},
		{"whitespace + encoded", "  http://127.0.0.1:1/callback?state=s%2B1&code=c%2F2 \n", "c/2", "s+1", ""},
		{"bare query", "?code=abc&state=xyz", "abc", "xyz", ""},
		{"bare query no ?", "code=abc&state=xyz", "abc", "xyz", ""},
		{"fragment response", "http://127.0.0.1:1/callback#code=abc&state=xyz", "abc", "xyz", ""},
		{"denied", "http://127.0.0.1:1/callback?error=access_denied&error_description=nope&state=xyz", "", "xyz", "access_denied: nope"},
		{"no code", "http://127.0.0.1:1/callback?state=xyz", "", "xyz", "no code"},
		{"no state", "http://127.0.0.1:1/callback?code=abc", "abc", "", "no state"},
		{"empty", "  ", "", "", "empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, state, err := parseRedirectURL(tc.in)
			if tc.errSub == "" && err != nil {
				t.Fatalf("unexpected err %v", err)
			}
			if tc.errSub != "" && (err == nil || !strings.Contains(err.Error(), tc.errSub)) {
				t.Fatalf("err %v, want substring %q", err, tc.errSub)
			}
			if code != tc.code || state != tc.state {
				t.Fatalf("got code=%q state=%q", code, state)
			}
		})
	}
}

func TestAuthorizeURL(t *testing.T) {
	raw, err := buildAuthorizeURL("https://auth.example.com/authorize?prompt=consent", "cid", "http://127.0.0.1:9/callback", "st", "ch", "read", "https://mcp.example.com/mcp")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	want := map[string]string{
		"prompt": "consent", "response_type": "code", "client_id": "cid",
		"redirect_uri": "http://127.0.0.1:9/callback", "state": "st", "code_challenge": "ch",
		"code_challenge_method": "S256", "scope": "read", "resource": "https://mcp.example.com/mcp",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s=%q want %q", k, q.Get(k), v)
		}
	}
	raw, _ = buildAuthorizeURL("https://a/authorize", "cid", "r", "s", "c", "", "")
	if strings.Contains(raw, "scope=") || strings.Contains(raw, "resource=") {
		t.Fatalf("unexpected optional params: %s", raw)
	}
}

func TestTokenExpiryDecisions(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tests := []struct {
		name                  string
		tok                   OAuthToken
		needsRefresh, expired bool
		canRefresh            bool
	}{
		{"no expiry", OAuthToken{AccessToken: "a"}, false, false, false},
		{"fresh", OAuthToken{AccessToken: "a", ExpiresAt: now.Unix() + 3600}, false, false, false},
		{"within skew", OAuthToken{AccessToken: "a", ExpiresAt: now.Unix() + 30, RefreshToken: "r", TokenEndpoint: "https://t"}, true, false, true},
		{"expired", OAuthToken{AccessToken: "a", ExpiresAt: now.Unix() - 1, RefreshToken: "r"}, true, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tok.NeedsRefresh(now); got != tc.needsRefresh {
				t.Errorf("NeedsRefresh=%v", got)
			}
			if got := tc.tok.Expired(now); got != tc.expired {
				t.Errorf("Expired=%v", got)
			}
			if got := tc.tok.CanRefresh(); got != tc.canRefresh {
				t.Errorf("CanRefresh=%v", got)
			}
		})
	}
	tr := &tokenResponse{AccessToken: "n", ExpiresIn: json.RawMessage(`"120"`)}
	nt := tokenFromResponse(tr, now, OAuthToken{RefreshToken: "keep", ClientID: "c"})
	if nt.ExpiresAt != now.Unix()+120 || nt.RefreshToken != "keep" || nt.ClientID != "c" {
		t.Fatalf("tokenFromResponse: %+v", nt)
	}
}

func TestTokenStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mem", OAuthStoreFile)
	s := NewTokenStore(path)
	if _, ok := s.Get("https://x/mcp"); ok {
		t.Fatal("empty store returned token")
	}
	tok := OAuthToken{
		AccessToken: "at", RefreshToken: "rt", TokenType: "Bearer", Scope: "read",
		ExpiresAt: 12345, ClientID: "cid", ClientSecret: "sec",
		TokenEndpoint: "https://auth/token", Resource: "https://x/mcp",
	}
	if err := s.Put("HTTPS://X:443/mcp/", tok); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	// fresh store reads from disk; lookup by un-normalized equivalent URL
	s2 := NewTokenStore(path)
	got, ok := s2.Get("https://x/mcp")
	if !ok {
		t.Fatal("token not found after reload")
	}
	got.UpdatedAt = 0
	if !reflect.DeepEqual(got, tok) {
		t.Fatalf("got %+v want %+v", got, tok)
	}
	var raw tokenStoreFile
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &raw); err != nil || raw.Tokens["https://x/mcp"].AccessToken != "at" {
		t.Fatalf("file shape: %s", data)
	}
	if existed, err := s2.Delete("https://x/mcp"); err != nil || !existed {
		t.Fatalf("delete: %v %v", existed, err)
	}
	if _, ok := NewTokenStore(path).Get("https://x/mcp"); ok {
		t.Fatal("token survived delete")
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".mcp-oauth-*"))
	if len(matches) != 0 {
		t.Fatalf("temp files left: %v", matches)
	}
}

// ---------------------------------------------------------------------------
// Fake OAuth-protected MCP deployment

type fakeOAuth struct {
	t          *testing.T
	mcp        *httptest.Server // resource server
	as         *httptest.Server // authorization server
	mu         sync.Mutex
	valid      map[string]bool // access tokens accepted by the MCP server
	refresh    map[string]bool
	codes      map[string]string // code → challenge
	clients    map[string]bool
	n          int
	refreshes  int
	tokenForms []url.Values
	// knobs
	prmMode     string // "header" | "path" | "root" | "none"
	asMode      string // "rfc8414" | "oidc"
	expiresIn   int
	noRegister  bool
	asHasS256   bool
	asOnlyPlain bool
}

func newFakeOAuth(t *testing.T, prmMode, asMode string) *fakeOAuth {
	f := &fakeOAuth{
		t: t, prmMode: prmMode, asMode: asMode, expiresIn: 3600, asHasS256: true,
		valid: map[string]bool{}, refresh: map[string]bool{}, codes: map[string]string{}, clients: map[string]bool{},
	}
	f.as = httptest.NewServer(http.HandlerFunc(f.serveAS))
	f.mcp = httptest.NewServer(http.HandlerFunc(f.serveMCP))
	t.Cleanup(func() { f.mcp.Close(); f.as.Close() })
	return f
}

func (f *fakeOAuth) serverURL() string { return f.mcp.URL + "/mcp" }

func (f *fakeOAuth) serveMCP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/oauth-protected-resource/mcp":
		if f.prmMode != "path" && f.prmMode != "header" {
			http.NotFound(w, r)
			return
		}
		f.writePRM(w)
		return
	case "/.well-known/oauth-protected-resource":
		if f.prmMode != "root" {
			http.NotFound(w, r)
			return
		}
		f.writePRM(w)
		return
	case "/.well-known/oauth-authorization-server":
		if f.prmMode == "none" {
			f.writeASMeta(w) // legacy: AS at the MCP origin
			return
		}
		http.NotFound(w, r)
		return
	case "/mcp":
	default:
		http.NotFound(w, r)
		return
	}
	auth := r.Header.Get("Authorization")
	f.mu.Lock()
	ok := strings.HasPrefix(auth, "Bearer ") && f.valid[strings.TrimPrefix(auth, "Bearer ")]
	f.mu.Unlock()
	if !ok {
		if f.prmMode == "header" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp", scope="tools:read"`, f.mcp.URL))
		} else {
			w.Header().Set("WWW-Authenticate", `Bearer realm="mcp"`)
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		ID     interface{} `json:"id"`
		Method string      `json:"method"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result interface{} = map[string]interface{}{}
	switch req.Method {
	case "tools/list":
		result = map[string]interface{}{"tools": []map[string]interface{}{{"name": "echo", "description": "echo"}}}
	case "tools/call":
		result = map[string]interface{}{"content": []map[string]interface{}{{"type": "text", "text": "pong"}}}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

func (f *fakeOAuth) writePRM(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"resource":              f.serverURL(),
		"authorization_servers": []string{f.as.URL + "/tenant"},
		"scopes_supported":      []string{"tools:read", "tools:write"},
	})
}

func (f *fakeOAuth) asBase() string {
	if f.prmMode == "none" {
		return f.mcp.URL
	}
	return f.as.URL
}

func (f *fakeOAuth) writeASMeta(w http.ResponseWriter) {
	base := f.asBase()
	m := map[string]interface{}{
		"issuer":                 base,
		"authorization_endpoint": f.as.URL + "/authorize",
		"token_endpoint":         f.as.URL + "/token",
	}
	if !f.noRegister {
		m["registration_endpoint"] = f.as.URL + "/register"
	}
	if f.asOnlyPlain {
		m["code_challenge_methods_supported"] = []string{"plain"}
	} else if f.asHasS256 {
		m["code_challenge_methods_supported"] = []string{"S256"}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(m)
}

func (f *fakeOAuth) serveAS(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/oauth-authorization-server/tenant":
		if f.asMode == "rfc8414" {
			f.writeASMeta(w)
			return
		}
		http.NotFound(w, r)
	case "/.well-known/openid-configuration/tenant", "/tenant/.well-known/openid-configuration":
		if f.asMode == "oidc" && r.URL.Path == "/tenant/.well-known/openid-configuration" {
			f.writeASMeta(w)
			return
		}
		http.NotFound(w, r)
	case "/register":
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["token_endpoint_auth_method"] != "none" {
			f.t.Errorf("registration auth method %v", body["token_endpoint_auth_method"])
		}
		uris, _ := body["redirect_uris"].([]interface{})
		if len(uris) != 1 || !strings.HasPrefix(uris[0].(string), "http://127.0.0.1:") {
			f.t.Errorf("redirect_uris %v", uris)
		}
		f.mu.Lock()
		f.n++
		id := fmt.Sprintf("client-%d", f.n)
		f.clients[id] = true
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{"client_id": id})
	case "/token":
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.tokenForms = append(f.tokenForms, r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		fail := func(code string) {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": code})
		}
		if !f.clients[r.PostForm.Get("client_id")] {
			fail("invalid_client")
			return
		}
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			ch, ok := f.codes[r.PostForm.Get("code")]
			if !ok || pkceS256(r.PostForm.Get("code_verifier")) != ch {
				fail("invalid_grant")
				return
			}
			delete(f.codes, r.PostForm.Get("code"))
		case "refresh_token":
			if !f.refresh[r.PostForm.Get("refresh_token")] {
				fail("invalid_grant")
				return
			}
			delete(f.refresh, r.PostForm.Get("refresh_token"))
			f.refreshes++
		default:
			fail("unsupported_grant_type")
			return
		}
		f.n++
		at, rt := fmt.Sprintf("at-%d", f.n), fmt.Sprintf("rt-%d", f.n)
		f.valid[at] = true
		f.refresh[rt] = true
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": at, "refresh_token": rt, "token_type": "Bearer", "expires_in": f.expiresIn,
		})
	default:
		http.NotFound(w, r)
	}
}

// authorize simulates the user approving in a browser: returns the redirect URL.
func (f *fakeOAuth) authorize(t *testing.T, authURL string) string {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("pkce params missing: %s", authURL)
	}
	f.mu.Lock()
	f.n++
	code := fmt.Sprintf("code-%d", f.n)
	f.codes[code] = q.Get("code_challenge")
	f.mu.Unlock()
	return q.Get("redirect_uri") + "?code=" + code + "&state=" + url.QueryEscape(q.Get("state"))
}

func (f *fakeOAuth) revokeAll() {
	f.mu.Lock()
	f.valid = map[string]bool{}
	f.mu.Unlock()
}

func TestDiscoverOAuth(t *testing.T) {
	tests := []struct {
		prm, as       string
		wantResource  bool
		wantScope     string
		wantPRMSuffix string
	}{
		{"header", "rfc8414", true, "tools:read", "/.well-known/oauth-protected-resource/mcp"},
		{"path", "rfc8414", true, "tools:read tools:write", "/.well-known/oauth-protected-resource/mcp"},
		{"root", "oidc", true, "tools:read tools:write", "/.well-known/oauth-protected-resource"},
		{"none", "", false, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.prm+"/"+tc.as, func(t *testing.T) {
			f := newFakeOAuth(t, tc.prm, tc.as)
			hc := &http.Client{Timeout: 5 * time.Second}
			www := probeChallenge(context.Background(), hc, ServerConfig{URL: f.serverURL()})
			d, err := discoverOAuth(context.Background(), hc, f.serverURL(), www)
			if err != nil {
				t.Fatal(err)
			}
			if d.Metadata.TokenEndpoint != f.as.URL+"/token" || d.Metadata.RegistrationEndpoint != f.as.URL+"/register" {
				t.Fatalf("metadata %+v", d.Metadata)
			}
			if (d.Resource != "") != tc.wantResource {
				t.Fatalf("resource %q", d.Resource)
			}
			if d.Scope != tc.wantScope {
				t.Fatalf("scope %q want %q", d.Scope, tc.wantScope)
			}
			if !strings.HasSuffix(d.ResourceMetadataURL, tc.wantPRMSuffix) || (tc.wantPRMSuffix == "") != (d.ResourceMetadataURL == "") {
				t.Fatalf("prm url %q", d.ResourceMetadataURL)
			}
		})
	}
	t.Run("rejects AS without S256", func(t *testing.T) {
		f := newFakeOAuth(t, "path", "rfc8414")
		f.asOnlyPlain = true
		_, err := discoverOAuth(context.Background(), http.DefaultClient, f.serverURL(), "")
		if err == nil || !strings.Contains(err.Error(), "S256") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("rejects non-https AS", func(t *testing.T) {
		_, err := discoverOAuth(context.Background(), http.DefaultClient, "http://mcp.example.invalid/mcp", "")
		if err == nil || !strings.Contains(err.Error(), "non-https") {
			t.Fatalf("err %v", err)
		}
	})
}

func waitLogin(t *testing.T, m *Manager, name string) LoginStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, err := m.OAuthLoginStatus(name)
		if err != nil {
			t.Fatal(err)
		}
		if st.Status != "pending" {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("login did not finish")
	return LoginStatus{}
}

func serverStatus(m *Manager, name string) ServerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.status {
		if s.Name == name {
			return s
		}
	}
	return ServerStatus{}
}

func TestOAuthEndToEnd(t *testing.T) {
	for _, mode := range []string{"loopback", "manual"} {
		t.Run(mode, func(t *testing.T) {
			f := newFakeOAuth(t, "header", "rfc8414")
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "mcp.json")
			if err := SaveFile(cfgPath, FileConfig{MCPServers: map[string]ServerConfig{"ph": {URL: f.serverURL()}}}); err != nil {
				t.Fatal(err)
			}
			fc, err := LoadFile(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			m := NewManager(5*time.Second, false)
			ctx := context.Background()
			m.Start(ctx, fc)
			defer m.Close()

			st := serverStatus(m, "ph")
			if st.OK || !st.AuthRequired {
				t.Fatalf("want auth required, got %+v", st)
			}

			ls, err := m.StartOAuthLogin(ctx, "ph")
			if err != nil {
				t.Fatal(err)
			}
			if ls.Status != "pending" || ls.AuthURL == "" {
				t.Fatalf("start: %+v", ls)
			}
			au, _ := url.Parse(ls.AuthURL)
			if au.Query().Get("resource") != f.serverURL() || au.Query().Get("scope") != "tools:read" {
				t.Fatalf("authorize url params: %s", ls.AuthURL)
			}
			if _, err := m.StartOAuthLogin(ctx, "ph"); !errors.Is(err, ErrLoginPending) {
				t.Fatalf("second start: %v", err)
			}
			// While pending, the typed error carries the authorize URL.
			sa := m.serverAuthFor("ph", fc.MCPServers["ph"])
			var are *AuthRequiredError
			if err := sa.authRequired(""); !errors.As(err, &are) || are.AuthURL != ls.AuthURL {
				t.Fatalf("auth required err: %v", err)
			}

			redirect := f.authorize(t, ls.AuthURL)
			// A forged state must not kill the pending flow.
			if _, err := m.CompleteOAuthLogin("ph", strings.Replace(redirect, "state=", "state=x", 1)); err == nil {
				t.Fatal("state mismatch accepted")
			}
			if mode == "loopback" {
				resp, err := http.Get(redirect)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 || !strings.Contains(string(body), "authorized") {
					t.Fatalf("callback %d: %s", resp.StatusCode, body)
				}
			} else {
				if _, err := m.CompleteOAuthLogin("ph", redirect); err != nil {
					t.Fatal(err)
				}
			}
			if ls := waitLogin(t, m, "ph"); ls.Status != "done" {
				t.Fatalf("login: %+v", ls)
			}
			st = serverStatus(m, "ph")
			if !st.OK || st.Tools != 1 {
				t.Fatalf("after login: %+v", st)
			}
			// Token store is a sibling of mcp.json, 0600.
			storePath := filepath.Join(dir, OAuthStoreFile)
			fi, err := os.Stat(storePath)
			if err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatalf("store: %v %v", fi, err)
			}
			tok, ok := NewTokenStore(storePath).Get(f.serverURL())
			if !ok || tok.ClientID == "" || tok.Resource != f.serverURL() || tok.TokenEndpoint != f.as.URL+"/token" {
				t.Fatalf("stored token %+v", tok)
			}
			f.mu.Lock()
			form := f.tokenForms[0]
			f.mu.Unlock()
			if form.Get("resource") != f.serverURL() || form.Get("code_verifier") == "" {
				t.Fatalf("token request form %v", form)
			}

			// 401 → refresh once → retry succeeds, refreshed token persisted.
			f.revokeAll()
			out := m.Execute(ctx, "mcp_ph_echo", `{}`)
			if out != "pong" {
				t.Fatalf("execute after revoke: %q", out)
			}
			tok2, _ := NewTokenStore(storePath).Get(f.serverURL())
			if tok2.AccessToken == tok.AccessToken || tok2.RefreshToken == tok.RefreshToken {
				t.Fatalf("refresh not persisted: %+v", tok2)
			}
			f.mu.Lock()
			lastForm := f.tokenForms[len(f.tokenForms)-1]
			f.mu.Unlock()
			if lastForm.Get("grant_type") != "refresh_token" || lastForm.Get("resource") != f.serverURL() {
				t.Fatalf("refresh form %v", lastForm)
			}

			// Near expiry → proactive refresh before the request.
			tok2.ExpiresAt = time.Now().Unix() + 10
			_ = m.tokenStore().Put(f.serverURL(), tok2)
			f.mu.Lock()
			before := f.refreshes
			f.mu.Unlock()
			if out := m.Execute(ctx, "mcp_ph_echo", `{}`); out != "pong" {
				t.Fatalf("execute near expiry: %q", out)
			}
			f.mu.Lock()
			after := f.refreshes
			f.mu.Unlock()
			if after != before+1 {
				t.Fatalf("expected proactive refresh, refreshes %d → %d", before, after)
			}

			// Refresh impossible → typed auth-required error.
			f.revokeAll()
			f.mu.Lock()
			f.refresh = map[string]bool{}
			f.mu.Unlock()
			if out := m.Execute(ctx, "mcp_ph_echo", `{}`); !strings.Contains(out, "oauth authorization required") {
				t.Fatalf("want auth required, got %q", out)
			}

			// Forget token → server reconnects as auth-required.
			existed, err := m.ForgetOAuthToken(ctx, "ph")
			if err != nil || !existed {
				t.Fatalf("forget: %v %v", existed, err)
			}
			if _, ok := NewTokenStore(storePath).Get(f.serverURL()); ok {
				t.Fatal("token still stored")
			}
			if st := serverStatus(m, "ph"); st.OK || !st.AuthRequired {
				t.Fatalf("after forget: %+v", st)
			}
			if info := m.OAuthInfo()["ph"]; info.HasToken {
				t.Fatalf("info %+v", info)
			}
		})
	}
}

func TestOAuthLoginConfiguredClientAndExpiry(t *testing.T) {
	f := newFakeOAuth(t, "path", "rfc8414")
	f.noRegister = true
	f.clients["static-client"] = true
	dir := t.TempDir()
	m := NewManager(5*time.Second, false)
	m.SetOAuthStorePath(filepath.Join(dir, OAuthStoreFile))
	m.oauth.ttl = 300 * time.Millisecond
	fc := FileConfig{MCPServers: map[string]ServerConfig{
		"a": {URL: f.serverURL(), Auth: &OAuthConfig{ClientID: "static-client", Scopes: []string{"x", "y"}}},
		"b": {URL: f.serverURL()},
	}}
	m.Start(context.Background(), fc)
	defer m.Close()

	ls, err := m.StartOAuthLogin(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	q, _ := url.Parse(ls.AuthURL)
	if q.Query().Get("client_id") != "static-client" || q.Query().Get("scope") != "x y" {
		t.Fatalf("authorize url %s", ls.AuthURL)
	}
	// no registration endpoint and no configured client → clear error
	if _, err := m.StartOAuthLogin(context.Background(), "b"); err == nil || !strings.Contains(err.Error(), "CLIENT_ID") {
		t.Fatalf("b: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	st, _ := m.OAuthLoginStatus("a")
	if st.Status != "error" || !strings.Contains(st.Error, "expired") {
		t.Fatalf("want expired, got %+v", st)
	}
	if _, err := m.CompleteOAuthLogin("a", f.authorize(t, ls.AuthURL)); err == nil {
		t.Fatal("completed expired login")
	}
	if _, err := m.StartOAuthLogin(context.Background(), "nope"); !errors.Is(err, ErrUnknownServer) {
		t.Fatalf("unknown: %v", err)
	}
}
