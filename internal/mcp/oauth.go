package mcp

// OAuth 2.1 support for remote (HTTP) MCP servers, per the MCP authorization spec:
//
//   - discovery: 401 WWW-Authenticate resource_metadata (RFC 9728) with
//     /.well-known/oauth-protected-resource fallbacks, then authorization server
//     metadata (RFC 8414 / OpenID Connect discovery)
//   - dynamic client registration (RFC 7591) as a public client
//   - authorization code + PKCE S256 (RFC 7636) with resource indicators (RFC 8707)
//   - loopback redirect listener plus manual completion by pasting the redirect URL
//   - token store at $MEMORY/mcp-oauth.json (0600), refresh before expiry and on 401
//
// Out of scope for v1: client ID metadata documents (CIMD), step-up on 403
// insufficient_scope, and private_key_jwt client authentication.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// OAuthStoreFile is the token store file name under the memory root.
	OAuthStoreFile = "mcp-oauth.json"

	oauthFlowTTL     = 5 * time.Minute
	oauthRefreshSkew = 60 * time.Second
	oauthMaxBody     = 1 << 20
	oauthHTTPTimeout = 20 * time.Second
)

var (
	// ErrLoginPending means a login flow for the server is already in progress.
	ErrLoginPending = errors.New("oauth login already pending for this server")
	// ErrUnknownServer means the named server is not in the loaded MCP config.
	ErrUnknownServer = errors.New("unknown mcp server")
	// ErrNoLogin means there is no login flow for the server.
	ErrNoLogin = errors.New("no oauth login flow for this server")
)

// OAuthConfig is the optional Cursor-compatible "auth" block of a server entry.
type OAuthConfig struct {
	ClientID     string   `json:"CLIENT_ID,omitempty"`
	ClientSecret string   `json:"CLIENT_SECRET,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

// OAuthStorePath returns $MEMORY/mcp-oauth.json.
func OAuthStorePath(memoryRoot string) string {
	if strings.TrimSpace(memoryRoot) == "" {
		return ""
	}
	return filepath.Join(memoryRoot, OAuthStoreFile)
}

// AuthRequiredError is returned by the HTTP transport when the server demands
// OAuth and no usable token exists (or refresh failed).
type AuthRequiredError struct {
	Server  string
	URL     string
	AuthURL string // set when a login flow is pending for this server
	Detail  string
}

func (e *AuthRequiredError) Error() string {
	msg := "oauth authorization required"
	if e.Detail != "" {
		msg += " (" + e.Detail + ")"
	}
	if e.AuthURL != "" {
		return msg + "; authorize at " + e.AuthURL
	}
	return msg + "; use Settings → MCP → Connect (OAuth)"
}

// NormalizeServerURL canonicalizes an MCP server URL for use as a token-store key:
// lowercase scheme/host, default port dropped, no userinfo/fragment, no trailing slash.
func NormalizeServerURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	switch {
	case port != "":
		u.Host = net.JoinHostPort(host, port)
	case strings.Contains(host, ":"):
		u.Host = "[" + host + "]"
	default:
		u.Host = host
	}
	u.User = nil
	u.Fragment = ""
	u.RawFragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String()
}

// ---------------------------------------------------------------------------
// WWW-Authenticate parsing

type authChallenge struct {
	Scheme string
	Params map[string]string
}

// parseWWWAuthenticate parses RFC 9110 challenges, e.g.
// `Bearer realm="x", resource_metadata="https://…", scope="a b"`.
func parseWWWAuthenticate(h string) []authChallenge {
	var out []authChallenge
	i := 0
	n := len(h)
	isSep := func(c byte) bool { return c == ' ' || c == '\t' || c == ',' || c == '=' || c == '"' }
	skipWS := func() {
		for i < n && (h[i] == ' ' || h[i] == '\t') {
			i++
		}
	}
	token := func() string {
		start := i
		for i < n && !isSep(h[i]) {
			i++
		}
		return h[start:i]
	}
	quoted := func() string {
		i++ // opening quote
		var b strings.Builder
		for i < n && h[i] != '"' {
			if h[i] == '\\' && i+1 < n {
				i++
			}
			b.WriteByte(h[i])
			i++
		}
		if i < n {
			i++ // closing quote
		}
		return b.String()
	}
	cur := -1
	for i < n {
		for i < n && (h[i] == ' ' || h[i] == '\t' || h[i] == ',') {
			i++
		}
		if i >= n {
			break
		}
		tok := token()
		if tok == "" {
			i++ // stray '=' or '"'
			continue
		}
		skipWS()
		if i < n && h[i] == '=' {
			i++
			skipWS()
			var val string
			if i < n && h[i] == '"' {
				val = quoted()
			} else {
				val = token()
			}
			if cur >= 0 {
				out[cur].Params[strings.ToLower(tok)] = val
			}
			continue
		}
		out = append(out, authChallenge{Scheme: tok, Params: map[string]string{}})
		cur = len(out) - 1
	}
	return out
}

// bearerChallengeParams returns params of the Bearer challenge (or of any challenge
// carrying resource_metadata) from a WWW-Authenticate value.
func bearerChallengeParams(h string) map[string]string {
	chs := parseWWWAuthenticate(h)
	for _, c := range chs {
		if strings.EqualFold(c.Scheme, "bearer") {
			return c.Params
		}
	}
	for _, c := range chs {
		if c.Params["resource_metadata"] != "" {
			return c.Params
		}
	}
	return map[string]string{}
}

// ---------------------------------------------------------------------------
// Discovery

// ProtectedResourceMetadata is RFC 9728 metadata.
type ProtectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// AuthServerMetadata is the subset of RFC 8414 / OIDC discovery we use.
type AuthServerMetadata struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	ScopesSupported               []string `json:"scopes_supported"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
}

// OAuthDiscovery is the outcome of discovery for one MCP server.
type OAuthDiscovery struct {
	ServerURL           string
	ResourceMetadataURL string // PRM URL that answered ("" if legacy fallback)
	Resource            string // RFC 8707 resource ("" when unknown)
	Scope               string // challenge scope, else PRM scopes_supported
	AuthServer          string // issuer / AS base URL
	Metadata            AuthServerMetadata
}

func urlOrigin(u *url.URL) string { return u.Scheme + "://" + u.Host }

// protectedResourceMetadataURLs lists RFC 9728 well-known candidates for serverURL.
func protectedResourceMetadataURLs(serverURL string) []string {
	u, err := url.Parse(serverURL)
	if err != nil || u.Host == "" {
		return nil
	}
	origin := urlOrigin(u)
	p := strings.TrimRight(u.Path, "/")
	var out []string
	if p != "" {
		out = append(out, origin+"/.well-known/oauth-protected-resource"+p)
	}
	return append(out, origin+"/.well-known/oauth-protected-resource")
}

// authServerMetadataURLs lists RFC 8414 + OIDC discovery candidates for an issuer.
func authServerMetadataURLs(issuer string) []string {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		return nil
	}
	origin := urlOrigin(u)
	p := strings.TrimRight(u.Path, "/")
	if p == "" {
		return []string{
			origin + "/.well-known/oauth-authorization-server",
			origin + "/.well-known/openid-configuration",
		}
	}
	return []string{
		origin + "/.well-known/oauth-authorization-server" + p,
		origin + "/.well-known/openid-configuration" + p,
		origin + p + "/.well-known/openid-configuration",
	}
}

// secureURL requires https, except plain http to loopback hosts.
func secureURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid url %q", raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		h := u.Hostname()
		if h == "localhost" {
			return nil
		}
		if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("refusing non-https url %q", raw)
}

func fetchJSON(ctx context.Context, hc *http.Client, u string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, oauthMaxBody))
		return fmt.Errorf("GET %s: http %d", u, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, oauthMaxBody)).Decode(out); err != nil {
		return fmt.Errorf("GET %s: decode: %w", u, err)
	}
	return nil
}

// discoverOAuth resolves the authorization server for serverURL. wwwAuth is the
// WWW-Authenticate header from a 401 (may be empty).
func discoverOAuth(ctx context.Context, hc *http.Client, serverURL, wwwAuth string) (*OAuthDiscovery, error) {
	su, err := url.Parse(serverURL)
	if err != nil || su.Host == "" {
		return nil, fmt.Errorf("invalid server url %q", serverURL)
	}
	params := bearerChallengeParams(wwwAuth)
	d := &OAuthDiscovery{ServerURL: serverURL, Scope: params["scope"]}

	var candidates []string
	if rm := strings.TrimSpace(params["resource_metadata"]); rm != "" {
		if ru, err := su.Parse(rm); err == nil {
			candidates = append(candidates, ru.String())
		}
	}
	for _, c := range protectedResourceMetadataURLs(serverURL) {
		if len(candidates) == 0 || candidates[0] != c {
			candidates = append(candidates, c)
		}
	}
	var prm ProtectedResourceMetadata
	var prmErrs []string
	for _, c := range candidates {
		if err := secureURL(c); err != nil {
			prmErrs = append(prmErrs, err.Error())
			continue
		}
		var m ProtectedResourceMetadata
		if err := fetchJSON(ctx, hc, c, &m); err != nil {
			prmErrs = append(prmErrs, err.Error())
			continue
		}
		if len(m.AuthorizationServers) == 0 {
			prmErrs = append(prmErrs, c+": no authorization_servers")
			continue
		}
		prm = m
		d.ResourceMetadataURL = c
		break
	}
	if d.ResourceMetadataURL != "" {
		d.AuthServer = prm.AuthorizationServers[0]
		d.Resource = strings.TrimSpace(prm.Resource)
		if d.Resource == "" {
			d.Resource = NormalizeServerURL(serverURL)
		}
		if d.Scope == "" && len(prm.ScopesSupported) > 0 {
			d.Scope = strings.Join(prm.ScopesSupported, " ")
		}
	} else {
		// Legacy (2025-03-26) servers: the AS lives at the MCP server's origin.
		d.AuthServer = urlOrigin(su)
	}
	if err := secureURL(d.AuthServer); err != nil {
		return nil, fmt.Errorf("authorization server: %w", err)
	}

	var asErrs []string
	found := false
	for _, c := range authServerMetadataURLs(d.AuthServer) {
		var m AuthServerMetadata
		if err := fetchJSON(ctx, hc, c, &m); err != nil {
			asErrs = append(asErrs, err.Error())
			continue
		}
		if m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" {
			asErrs = append(asErrs, c+": missing authorization_endpoint/token_endpoint")
			continue
		}
		d.Metadata = m
		found = true
		break
	}
	if !found {
		all := append(prmErrs, asErrs...)
		return nil, fmt.Errorf("oauth discovery failed for %s: %s", serverURL, strings.Join(all, "; "))
	}
	for _, e := range []string{d.Metadata.AuthorizationEndpoint, d.Metadata.TokenEndpoint} {
		if err := secureURL(e); err != nil {
			return nil, err
		}
	}
	if d.Metadata.RegistrationEndpoint != "" {
		if err := secureURL(d.Metadata.RegistrationEndpoint); err != nil {
			return nil, err
		}
	}
	if ms := d.Metadata.CodeChallengeMethodsSupported; len(ms) > 0 {
		ok := false
		for _, m := range ms {
			if m == "S256" {
				ok = true
			}
		}
		if !ok {
			return nil, fmt.Errorf("authorization server does not support PKCE S256")
		}
	}
	return d, nil
}

// ---------------------------------------------------------------------------
// Dynamic client registration (RFC 7591)

func registerClient(ctx context.Context, hc *http.Client, endpoint, redirectURI, scope string) (clientID, clientSecret string, err error) {
	body := map[string]interface{}{
		"client_name":                "Marble",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
	if scope != "" {
		body["scope"] = scope
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("client registration: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, oauthMaxBody))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", "", fmt.Errorf("client registration: http %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", "", fmt.Errorf("client registration: decode: %w", err)
	}
	if out.ClientID == "" {
		return "", "", fmt.Errorf("client registration: no client_id in response")
	}
	return out.ClientID, out.ClientSecret, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------------------------------------------------------------------------
// PKCE + redirect parsing

func randomURLToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pkceS256 returns the RFC 7636 S256 code_challenge for verifier.
func pkceS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func buildAuthorizeURL(endpoint, clientID, redirectURI, state, challenge, scope, resource string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if scope != "" {
		q.Set("scope", scope)
	}
	if resource != "" {
		q.Set("resource", resource)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// parseRedirectURL extracts code/state from a pasted redirect URL (or a bare
// query string). An OAuth error in the redirect is returned as err.
func parseRedirectURL(raw string) (code, state string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("redirect url empty")
	}
	var q url.Values
	if u, perr := url.Parse(raw); perr == nil && (u.RawQuery != "" || u.Scheme != "") {
		q = u.Query()
		if len(q) == 0 && u.Fragment != "" {
			q, _ = url.ParseQuery(u.Fragment)
		}
	} else {
		q, err = url.ParseQuery(strings.TrimPrefix(raw, "?"))
		if err != nil {
			return "", "", fmt.Errorf("cannot parse redirect url: %w", err)
		}
	}
	if e := q.Get("error"); e != "" {
		if d := q.Get("error_description"); d != "" {
			e += ": " + d
		}
		return "", q.Get("state"), fmt.Errorf("authorization denied: %s", e)
	}
	code, state = q.Get("code"), q.Get("state")
	if code == "" {
		return "", state, fmt.Errorf("no code in redirect url")
	}
	if state == "" {
		return code, "", fmt.Errorf("no state in redirect url")
	}
	return code, state, nil
}

// ---------------------------------------------------------------------------
// Token endpoint

type tokenResponse struct {
	AccessToken  string          `json:"access_token"`
	TokenType    string          `json:"token_type"`
	RefreshToken string          `json:"refresh_token"`
	Scope        string          `json:"scope"`
	ExpiresIn    json.RawMessage `json:"expires_in"`
	Error        string          `json:"error"`
	ErrorDesc    string          `json:"error_description"`
}

func (t tokenResponse) expiresIn() int64 {
	s := strings.Trim(strings.TrimSpace(string(t.ExpiresIn)), `"`)
	if s == "" {
		return 0
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return int64(n)
}

func tokenRequest(ctx context.Context, hc *http.Client, endpoint string, form url.Values, clientID, clientSecret string) (*tokenResponse, error) {
	if clientID != "" {
		form.Set("client_id", clientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if clientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(clientSecret))
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, oauthMaxBody))
	var tr tokenResponse
	jerr := json.Unmarshal(data, &tr)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || tr.Error != "" {
		if tr.Error != "" {
			msg := tr.Error
			if tr.ErrorDesc != "" {
				msg += ": " + tr.ErrorDesc
			}
			return nil, fmt.Errorf("token endpoint: %s", msg)
		}
		return nil, fmt.Errorf("token endpoint: http %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	if jerr != nil {
		return nil, fmt.Errorf("token endpoint: decode: %w", jerr)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("token endpoint: no access_token in response")
	}
	return &tr, nil
}

// ---------------------------------------------------------------------------
// Token store

// OAuthToken is one stored credential, keyed by normalized server URL.
type OAuthToken struct {
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	TokenType     string `json:"token_type,omitempty"`
	Scope         string `json:"scope,omitempty"`
	ExpiresAt     int64  `json:"expires_at,omitempty"` // unix seconds; 0 = unknown
	ClientID      string `json:"client_id,omitempty"`
	ClientSecret  string `json:"client_secret,omitempty"`
	TokenEndpoint string `json:"token_endpoint,omitempty"`
	Resource      string `json:"resource,omitempty"`
	UpdatedAt     int64  `json:"updated_at,omitempty"`
}

// Expired reports whether the access token is past its expiry.
func (t OAuthToken) Expired(now time.Time) bool {
	return t.ExpiresAt > 0 && !now.Before(time.Unix(t.ExpiresAt, 0))
}

// NeedsRefresh reports whether the token expires within the refresh skew.
func (t OAuthToken) NeedsRefresh(now time.Time) bool {
	return t.ExpiresAt > 0 && !now.Add(oauthRefreshSkew).Before(time.Unix(t.ExpiresAt, 0))
}

// CanRefresh reports whether a refresh grant is possible.
func (t OAuthToken) CanRefresh() bool {
	return t.RefreshToken != "" && t.TokenEndpoint != ""
}

type tokenStoreFile struct {
	Version int                   `json:"version"`
	Tokens  map[string]OAuthToken `json:"tokens"`
}

// TokenStore persists OAuth tokens as JSON (mode 0600, atomic replace).
type TokenStore struct {
	mu     sync.Mutex
	path   string
	loaded bool
	tokens map[string]OAuthToken
}

// NewTokenStore returns a store backed by path (loaded lazily).
func NewTokenStore(path string) *TokenStore {
	return &TokenStore{path: path}
}

// Path returns the backing file.
func (s *TokenStore) Path() string { return s.path }

func (s *TokenStore) loadLocked() error {
	if s.loaded {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.tokens = map[string]OAuthToken{}
			s.loaded = true
			return nil
		}
		return err
	}
	var f tokenStoreFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("%s: %w", s.path, err)
	}
	if f.Tokens == nil {
		f.Tokens = map[string]OAuthToken{}
	}
	s.tokens = f.Tokens
	s.loaded = true
	return nil
}

func (s *TokenStore) saveLocked() error {
	data, err := json.MarshalIndent(tokenStoreFile{Version: 1, Tokens: s.tokens}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".mcp-oauth-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

// Get returns the token for serverURL.
func (s *TokenStore) Get(serverURL string) (OAuthToken, bool) {
	if s == nil {
		return OAuthToken{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		log.Printf("mcp oauth: token store: %v", err)
		return OAuthToken{}, false
	}
	t, ok := s.tokens[NormalizeServerURL(serverURL)]
	return t, ok
}

// Put stores tok for serverURL and persists.
func (s *TokenStore) Put(serverURL string, tok OAuthToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	if tok.UpdatedAt == 0 {
		tok.UpdatedAt = time.Now().Unix()
	}
	s.tokens[NormalizeServerURL(serverURL)] = tok
	return s.saveLocked()
}

// Delete removes the token for serverURL. Reports whether one existed.
func (s *TokenStore) Delete(serverURL string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return false, err
	}
	key := NormalizeServerURL(serverURL)
	if _, ok := s.tokens[key]; !ok {
		return false, nil
	}
	delete(s.tokens, key)
	return true, s.saveLocked()
}

func tokenFromResponse(tr *tokenResponse, now time.Time, prev OAuthToken) OAuthToken {
	t := prev
	t.AccessToken = tr.AccessToken
	if tr.TokenType != "" {
		t.TokenType = tr.TokenType
	}
	if tr.RefreshToken != "" {
		t.RefreshToken = tr.RefreshToken
	}
	if tr.Scope != "" {
		t.Scope = tr.Scope
	}
	t.ExpiresAt = 0
	if s := tr.expiresIn(); s > 0 {
		t.ExpiresAt = now.Unix() + s
	}
	t.UpdatedAt = now.Unix()
	return t
}

// ---------------------------------------------------------------------------
// Coordinator: refresh + login flows

// LoginStatus is the API view of a login flow.
type LoginStatus struct {
	Status    string `json:"status"` // pending | done | error
	AuthURL   string `json:"auth_url,omitempty"`
	Error     string `json:"error,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}

type loginFlow struct {
	name, serverURL        string
	state, verifier        string
	redirectURI            string
	authURL                string
	clientID, clientSecret string
	tokenEndpoint          string
	resource               string
	expires                time.Time
	status                 string // pending | done | error
	err                    string
	completing             bool
	srv                    *http.Server
	timer                  *time.Timer
	stopOnce               sync.Once
	onDone                 func()
}

func (f *loginFlow) snapshot() LoginStatus {
	st := LoginStatus{Status: f.status, AuthURL: f.authURL, Error: f.err}
	if !f.expires.IsZero() {
		st.ExpiresAt = f.expires.Unix()
	}
	if f.status != "pending" {
		st.AuthURL = ""
	}
	return st
}

func (f *loginFlow) stop() {
	f.stopOnce.Do(func() {
		if f.timer != nil {
			f.timer.Stop()
		}
		if f.srv != nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = f.srv.Shutdown(ctx)
			}()
		}
	})
}

type oauthCoordinator struct {
	mu        sync.Mutex
	store     *TokenStore
	flows     map[string]*loginFlow
	refreshMu sync.Mutex
	hc        *http.Client
	now       func() time.Time
	listen    func() (net.Listener, error)
	ttl       time.Duration
}

func newOAuthCoordinator() *oauthCoordinator {
	return &oauthCoordinator{
		flows:  map[string]*loginFlow{},
		hc:     &http.Client{Timeout: oauthHTTPTimeout},
		now:    time.Now,
		listen: func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") },
		ttl:    oauthFlowTTL,
	}
}

func (c *oauthCoordinator) setStorePath(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if path == "" || (c.store != nil && c.store.path == path) {
		return
	}
	c.store = NewTokenStore(path)
}

// storeFor returns the configured store, defaulting to a sibling of cfgPath.
func (c *oauthCoordinator) storeFor(cfgPath string) *TokenStore {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil && cfgPath != "" {
		c.store = NewTokenStore(filepath.Join(filepath.Dir(cfgPath), OAuthStoreFile))
	}
	return c.store
}

func (c *oauthCoordinator) pendingAuthURL(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if f := c.flows[name]; f != nil && f.status == "pending" && c.now().Before(f.expires) {
		return f.authURL
	}
	return ""
}

// refresh runs a refresh grant for key unless the stored access token already
// changed from failedAccess and is fresh (another caller refreshed it).
func (c *oauthCoordinator) refresh(ctx context.Context, store *TokenStore, key, failedAccess string) (OAuthToken, error) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	tok, ok := store.Get(key)
	if !ok {
		return OAuthToken{}, fmt.Errorf("no stored token")
	}
	now := c.now()
	if tok.AccessToken != failedAccess && tok.AccessToken != "" && !tok.NeedsRefresh(now) {
		return tok, nil
	}
	if !tok.CanRefresh() {
		return OAuthToken{}, fmt.Errorf("no refresh token")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", tok.RefreshToken)
	if tok.Resource != "" {
		form.Set("resource", tok.Resource)
	}
	rctx, cancel := context.WithTimeout(ctx, oauthHTTPTimeout)
	defer cancel()
	tr, err := tokenRequest(rctx, c.hc, tok.TokenEndpoint, form, tok.ClientID, tok.ClientSecret)
	if err != nil {
		return OAuthToken{}, err
	}
	nt := tokenFromResponse(tr, now, tok)
	if err := store.Put(key, nt); err != nil {
		log.Printf("mcp oauth: persist refreshed token: %v", err)
	}
	return nt, nil
}

// serverAuth is the per-session token source used by httpSession.
type serverAuth struct {
	c     *oauthCoordinator
	store *TokenStore
	name  string
	url   string
}

// bearer returns the current access token (refreshing shortly before expiry), or "".
func (a *serverAuth) bearer(ctx context.Context) string {
	tok, ok := a.store.Get(a.url)
	if !ok || tok.AccessToken == "" {
		return ""
	}
	if tok.NeedsRefresh(a.c.now()) && tok.CanRefresh() {
		nt, err := a.c.refresh(ctx, a.store, a.url, tok.AccessToken)
		if err == nil {
			return nt.AccessToken
		}
		log.Printf("mcp oauth: %s: refresh before expiry: %v", a.name, err)
	}
	return tok.AccessToken
}

// onUnauthorized tries once to obtain a different, valid token after a 401.
func (a *serverAuth) onUnauthorized(ctx context.Context, failed string) (string, bool) {
	tok, ok := a.store.Get(a.url)
	if !ok {
		return "", false
	}
	if tok.AccessToken != "" && tok.AccessToken != failed && !tok.Expired(a.c.now()) {
		return tok.AccessToken, true
	}
	if !tok.CanRefresh() {
		return "", false
	}
	nt, err := a.c.refresh(ctx, a.store, a.url, failed)
	if err != nil {
		log.Printf("mcp oauth: %s: refresh after 401: %v", a.name, err)
		return "", false
	}
	return nt.AccessToken, nt.AccessToken != "" && nt.AccessToken != failed
}

func (a *serverAuth) authRequired(wwwAuth string) error {
	detail := "http 401"
	if p := bearerChallengeParams(wwwAuth); p["error"] != "" {
		detail += ": " + p["error"]
		if p["error_description"] != "" {
			detail += " — " + p["error_description"]
		}
	}
	return &AuthRequiredError{Server: a.name, URL: a.url, AuthURL: a.c.pendingAuthURL(a.name), Detail: detail}
}

// probeChallenge sends an unauthenticated initialize to read the 401 challenge.
func probeChallenge(ctx context.Context, hc *http.Client, sc ServerConfig) string {
	body := []byte(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"marble-harness","version":"0.1.0"}}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sc.URL, bytes.NewReader(body))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range sc.Headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.Header.Get("WWW-Authenticate")
}

func hasAuthorizationHeader(h map[string]string) bool {
	for k := range h {
		if strings.EqualFold(k, "Authorization") {
			return true
		}
	}
	return false
}

func (c *oauthCoordinator) status(name string) (LoginStatus, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.flows[name]
	if f == nil {
		return LoginStatus{}, false
	}
	if f.status == "pending" && !f.completing && !c.now().Before(f.expires) {
		f.status, f.err = "error", "login expired; start again"
		f.stop()
	}
	return f.snapshot(), true
}

func (c *oauthCoordinator) cancel(name, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if f := c.flows[name]; f != nil {
		if f.status == "pending" && !f.completing {
			f.status, f.err = "error", reason
		}
		f.stop()
	}
}

// startLogin begins an authorization-code flow for one server.
func (c *oauthCoordinator) startLogin(ctx context.Context, name string, sc ServerConfig, store *TokenStore, onDone func()) (LoginStatus, error) {
	if hasAuthorizationHeader(sc.Headers) {
		return LoginStatus{}, fmt.Errorf("server %q has a static Authorization header; remove it to use OAuth", name)
	}
	c.mu.Lock()
	if f := c.flows[name]; f != nil && f.status == "pending" && (f.completing || c.now().Before(f.expires)) {
		st := f.snapshot()
		c.mu.Unlock()
		return st, ErrLoginPending
	}
	if old := c.flows[name]; old != nil {
		old.stop()
	}
	f := &loginFlow{name: name, serverURL: sc.URL, status: "pending", expires: c.now().Add(c.ttl), onDone: onDone}
	c.flows[name] = f
	c.mu.Unlock()

	fail := func(err error) (LoginStatus, error) {
		c.mu.Lock()
		f.status, f.err = "error", err.Error()
		f.stop()
		st := f.snapshot()
		c.mu.Unlock()
		return st, err
	}

	sctx, cancel := context.WithTimeout(ctx, 2*oauthHTTPTimeout)
	defer cancel()
	www := probeChallenge(sctx, c.hc, sc)
	disc, err := discoverOAuth(sctx, c.hc, sc.URL, www)
	if err != nil {
		return fail(err)
	}
	ln, err := c.listen()
	if err != nil {
		return fail(fmt.Errorf("loopback listener: %w", err))
	}
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	scope := disc.Scope
	clientID, clientSecret := "", ""
	if sc.Auth != nil {
		if len(sc.Auth.Scopes) > 0 {
			scope = strings.Join(sc.Auth.Scopes, " ")
		}
		clientID, clientSecret = sc.Auth.ClientID, sc.Auth.ClientSecret
	}
	if clientID == "" {
		if disc.Metadata.RegistrationEndpoint == "" {
			ln.Close()
			return fail(fmt.Errorf("authorization server has no registration_endpoint; set auth.CLIENT_ID in mcp.json"))
		}
		clientID, clientSecret, err = registerClient(sctx, c.hc, disc.Metadata.RegistrationEndpoint, redirectURI, scope)
		if err != nil {
			ln.Close()
			return fail(err)
		}
	}
	verifier, err := randomURLToken(32)
	if err != nil {
		ln.Close()
		return fail(err)
	}
	state, err := randomURLToken(24)
	if err != nil {
		ln.Close()
		return fail(err)
	}
	authURL, err := buildAuthorizeURL(disc.Metadata.AuthorizationEndpoint, clientID, redirectURI, state, pkceS256(verifier), scope, disc.Resource)
	if err != nil {
		ln.Close()
		return fail(err)
	}

	mux := http.NewServeMux()
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		err := c.complete(f, store, r.URL.String())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<!doctype html><title>Marble</title><p>Marble: authorization for <b>%s</b> failed: %s</p>", html.EscapeString(name), html.EscapeString(err.Error()))
			return
		}
		fmt.Fprintf(w, "<!doctype html><title>Marble</title><p>Marble: <b>%s</b> authorized. You can close this tab.</p>", html.EscapeString(name))
	})

	c.mu.Lock()
	if c.flows[name] != f || f.status != "pending" {
		c.mu.Unlock()
		ln.Close()
		return LoginStatus{Status: "error", Error: "login cancelled"}, fmt.Errorf("login cancelled")
	}
	f.redirectURI, f.authURL = redirectURI, authURL
	f.state, f.verifier = state, verifier
	f.clientID, f.clientSecret = clientID, clientSecret
	f.tokenEndpoint, f.resource = disc.Metadata.TokenEndpoint, disc.Resource
	f.expires = c.now().Add(c.ttl)
	f.srv = srv
	f.timer = time.AfterFunc(c.ttl, func() {
		c.mu.Lock()
		if f.status == "pending" && !f.completing {
			f.status, f.err = "error", "login expired; start again"
		}
		c.mu.Unlock()
		f.stop()
	})
	st := f.snapshot()
	c.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("mcp oauth: %s: callback listener: %v", name, err)
		}
	}()
	log.Printf("mcp oauth: %s: login started (redirect %s, as %s)", name, redirectURI, disc.AuthServer)
	return st, nil
}

// completeManual finishes the pending flow for name from a pasted redirect URL.
func (c *oauthCoordinator) completeManual(name string, store *TokenStore, redirectURL string) (LoginStatus, error) {
	c.mu.Lock()
	f := c.flows[name]
	c.mu.Unlock()
	if f == nil {
		return LoginStatus{}, ErrNoLogin
	}
	err := c.complete(f, store, redirectURL)
	c.mu.Lock()
	st := f.snapshot()
	c.mu.Unlock()
	return st, err
}

// complete validates state, exchanges the code, stores the token and then
// (asynchronously) reconnects the server before marking the flow done.
func (c *oauthCoordinator) complete(f *loginFlow, store *TokenStore, redirectURL string) error {
	code, state, perr := parseRedirectURL(redirectURL)
	c.mu.Lock()
	switch {
	case f.status != "pending":
		c.mu.Unlock()
		return fmt.Errorf("login is not pending (status %s)", f.status)
	case f.completing:
		c.mu.Unlock()
		return fmt.Errorf("login is already completing")
	case f.state == "":
		c.mu.Unlock()
		return fmt.Errorf("login is still starting")
	case !c.now().Before(f.expires):
		f.status, f.err = "error", "login expired; start again"
		c.mu.Unlock()
		f.stop()
		return fmt.Errorf("login expired")
	case state != f.state:
		// Do not kill the flow: a stale or forged redirect must not cancel the real one.
		c.mu.Unlock()
		if perr != nil && state == "" {
			return perr
		}
		return fmt.Errorf("state mismatch (stale or foreign redirect URL)")
	}
	finish := func(err error) error {
		c.mu.Lock()
		f.completing = false
		f.status, f.err = "error", err.Error()
		c.mu.Unlock()
		f.stop()
		return err
	}
	if perr != nil {
		f.completing = true
		c.mu.Unlock()
		return finish(perr)
	}
	f.completing = true
	c.mu.Unlock()

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", f.redirectURI)
	form.Set("code_verifier", f.verifier)
	if f.resource != "" {
		form.Set("resource", f.resource)
	}
	ctx, cancel := context.WithTimeout(context.Background(), oauthHTTPTimeout)
	defer cancel()
	tr, err := tokenRequest(ctx, c.hc, f.tokenEndpoint, form, f.clientID, f.clientSecret)
	if err != nil {
		return finish(err)
	}
	tok := tokenFromResponse(tr, c.now(), OAuthToken{
		ClientID: f.clientID, ClientSecret: f.clientSecret,
		TokenEndpoint: f.tokenEndpoint, Resource: f.resource,
	})
	if err := store.Put(f.serverURL, tok); err != nil {
		return finish(fmt.Errorf("save token: %w", err))
	}
	log.Printf("mcp oauth: %s: token stored", f.name)
	f.stop()
	go func() {
		if f.onDone != nil {
			f.onDone()
		}
		c.mu.Lock()
		f.completing = false
		f.status = "done"
		c.mu.Unlock()
	}()
	return nil
}

// ---------------------------------------------------------------------------
// Manager integration

// OAuthServerInfo is the per-server OAuth summary for the settings UI.
type OAuthServerInfo struct {
	HasToken     bool         `json:"has_token"`
	ExpiresAt    int64        `json:"expires_at,omitempty"`
	CanRefresh   bool         `json:"can_refresh,omitempty"`
	StaticHeader bool         `json:"static_auth_header,omitempty"`
	Login        *LoginStatus `json:"login,omitempty"`
}

// SetOAuthStorePath pins the token store file (normally $MEMORY/mcp-oauth.json).
// Without it the store defaults to mcp-oauth.json next to the loaded mcp.json.
func (m *Manager) SetOAuthStorePath(path string) {
	if m == nil {
		return
	}
	m.oauth.setStorePath(path)
}

func (m *Manager) tokenStore() *TokenStore {
	m.mu.RLock()
	p := m.cfg.path
	m.mu.RUnlock()
	return m.oauth.storeFor(p)
}

// serverAuthFor returns the OAuth token source for an HTTP server, or nil when a
// static Authorization header is configured or no store is available.
func (m *Manager) serverAuthFor(name string, sc ServerConfig) *serverAuth {
	if hasAuthorizationHeader(sc.Headers) {
		return nil
	}
	store := m.tokenStore()
	if store == nil {
		return nil
	}
	return &serverAuth{c: m.oauth, store: store, name: name, url: sc.URL}
}

func (m *Manager) httpServerConfig(name string) (ServerConfig, error) {
	if m == nil || m.disabled {
		return ServerConfig{}, fmt.Errorf("mcp disabled")
	}
	m.mu.RLock()
	sc, ok := m.cfg.MCPServers[name]
	m.mu.RUnlock()
	if !ok {
		return ServerConfig{}, ErrUnknownServer
	}
	if sc.Kind() != "http" {
		return ServerConfig{}, fmt.Errorf("server %q is not an http server", name)
	}
	return sc, nil
}

// StartOAuthLogin begins an OAuth login for the named HTTP server.
func (m *Manager) StartOAuthLogin(ctx context.Context, name string) (LoginStatus, error) {
	sc, err := m.httpServerConfig(name)
	if err != nil {
		return LoginStatus{}, err
	}
	store := m.tokenStore()
	if store == nil {
		return LoginStatus{}, fmt.Errorf("oauth token store not configured")
	}
	return m.oauth.startLogin(ctx, name, sc, store, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		m.refreshServer(ctx, name)
	})
}

// CompleteOAuthLogin finishes a pending login from a pasted redirect URL.
func (m *Manager) CompleteOAuthLogin(name, redirectURL string) (LoginStatus, error) {
	if _, err := m.httpServerConfig(name); err != nil {
		return LoginStatus{}, err
	}
	store := m.tokenStore()
	if store == nil {
		return LoginStatus{}, fmt.Errorf("oauth token store not configured")
	}
	return m.oauth.completeManual(name, store, redirectURL)
}

// OAuthLoginStatus reports the latest login flow for name.
func (m *Manager) OAuthLoginStatus(name string) (LoginStatus, error) {
	if m == nil {
		return LoginStatus{}, ErrNoLogin
	}
	st, ok := m.oauth.status(name)
	if !ok {
		return LoginStatus{}, ErrNoLogin
	}
	return st, nil
}

// ForgetOAuthToken drops the stored token, cancels any pending login and
// reconnects the server (which then reports auth required).
func (m *Manager) ForgetOAuthToken(ctx context.Context, name string) (bool, error) {
	sc, err := m.httpServerConfig(name)
	if err != nil {
		return false, err
	}
	m.oauth.cancel(name, "cancelled (token forgotten)")
	store := m.tokenStore()
	if store == nil {
		return false, nil
	}
	existed, err := store.Delete(sc.URL)
	if err != nil {
		return false, err
	}
	m.refreshServer(ctx, name)
	return existed, nil
}

// OAuthInfo summarizes token/login state for every configured HTTP server.
func (m *Manager) OAuthInfo() map[string]OAuthServerInfo {
	out := map[string]OAuthServerInfo{}
	if m == nil {
		return out
	}
	m.mu.RLock()
	names := make([]string, 0, len(m.cfg.MCPServers))
	cfgs := map[string]ServerConfig{}
	for n, sc := range m.cfg.MCPServers {
		if sc.Kind() == "http" {
			names = append(names, n)
			cfgs[n] = sc
		}
	}
	m.mu.RUnlock()
	sort.Strings(names)
	store := m.tokenStore()
	for _, n := range names {
		sc := cfgs[n]
		info := OAuthServerInfo{StaticHeader: hasAuthorizationHeader(sc.Headers)}
		if tok, ok := store.Get(sc.URL); ok && tok.AccessToken != "" {
			info.HasToken = true
			info.ExpiresAt = tok.ExpiresAt
			info.CanRefresh = tok.CanRefresh()
		}
		if st, ok := m.oauth.status(n); ok {
			info.Login = &st
		}
		out[n] = info
	}
	return out
}
