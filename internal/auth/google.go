package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Google handles OAuth authorization code + PKCE against Google.
type Google struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Allowlist    map[string]struct{} // lowercase emails
	HTTPClient   *http.Client
}

func (g *Google) oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     g.ClientID,
		ClientSecret: g.ClientSecret,
		RedirectURL:  g.RedirectURL,
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     google.Endpoint,
	}
}

// AuthCodeURL builds the Google authorize URL with PKCE.
func (g *Google) AuthCodeURL(state, codeChallenge string) string {
	cfg := g.oauthConfig()
	return cfg.AuthCodeURL(state,
		oauth2.AccessTypeOnline,
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.SetAuthURLParam("prompt", "select_account"),
	)
}

// ExchangeCode trades code+verifier for tokens and returns the allowlisted user.
func (g *Google) ExchangeCode(ctx context.Context, code, codeVerifier string) (*User, error) {
	cfg := g.oauthConfig()
	tok, err := cfg.Exchange(ctx, code,
		oauth2.SetAuthURLParam("code_verifier", codeVerifier),
	)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	// Prefer userinfo endpoint with access token (verified by Google).
	client := g.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://openidconnect.googleapis.com/v1/userinfo", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("userinfo HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var info struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("userinfo decode: %w", err)
	}
	email := strings.ToLower(strings.TrimSpace(info.Email))
	if email == "" {
		return nil, fmt.Errorf("userinfo: missing email")
	}
	if !info.EmailVerified {
		return nil, fmt.Errorf("email not verified by Google")
	}
	if !g.Allowed(email) {
		return nil, fmt.Errorf("email %s is not on the allowlist", email)
	}
	return &User{
		Email: email,
		Name:  strings.TrimSpace(info.Name),
		Sub:   strings.TrimSpace(info.Sub),
	}, nil
}

// Allowed reports whether email is on the allowlist.
func (g *Google) Allowed(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if g.Allowlist == nil {
		return false
	}
	_, ok := g.Allowlist[email]
	return ok
}

// SafeNext returns a relative path for post-login redirect (open redirect safe).
func SafeNext(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "/"
	}
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		// disallow protocol-relative
		if u, err := url.Parse(raw); err == nil && u.Host == "" {
			return raw
		}
	}
	return "/"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
