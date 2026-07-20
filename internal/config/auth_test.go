package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAuthOpenDefault(t *testing.T) {
	c := &Config{}
	if err := c.resolveAuthAndTLS(); err != nil {
		t.Fatal(err)
	}
	if c.AuthMode != "open" {
		t.Fatal(c.AuthMode)
	}
}

func TestResolveAuthPartialFatal(t *testing.T) {
	c := &Config{OAuthClientID: "x.apps.googleusercontent.com"}
	if err := c.resolveAuthAndTLS(); err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveAuthGoogle(t *testing.T) {
	t.Setenv("TEST_OAUTH_SECRET", "sekrit")
	dir := t.TempDir()
	f := filepath.Join(dir, "allow.txt")
	if err := os.WriteFile(f, []byte("alice@example.com\n# comment\nbob@x.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Config{
		OAuthClientID:        "cid.apps.googleusercontent.com",
		OAuthClientSecretEnv: "TEST_OAUTH_SECRET",
		OAuthRedirectURL:     "https://host/auth/callback",
		OAuthAllowEmails:     "carol@y.com",
		OAuthAllowFile:       f,
	}
	if err := c.resolveAuthAndTLS(); err != nil {
		t.Fatal(err)
	}
	if c.AuthMode != "google" {
		t.Fatal(c.AuthMode)
	}
	if len(c.AuthAllowlist) != 3 {
		t.Fatalf("allowlist %v", c.AuthAllowlist)
	}
	h := c.AuthPublicHealth()
	if h["auth_accounts"] != 3 {
		t.Fatal(h)
	}
	// health must not embed email list
	for k := range h {
		if k == "oauth_allow_emails" {
			t.Fatal("emails leaked to health")
		}
	}
	st := c.AuthPublicSettings()
	emails, _ := st["oauth_allow_emails"].([]string)
	if len(emails) != 3 {
		t.Fatal(st)
	}
}

func TestTLSPartialFatal(t *testing.T) {
	c := &Config{TLSCertFile: "/tmp/x.pem"}
	if err := c.resolveAuthAndTLS(); err == nil {
		t.Fatal("expected tls error")
	}
}

func TestCookieSecure(t *testing.T) {
	c := Config{OAuthRedirectURL: "http://localhost:8080/auth/callback"}
	if c.CookieSecure() {
		t.Fatal("localhost should not force secure")
	}
	c.OAuthRedirectURL = "https://ex.com/auth/callback"
	if !c.CookieSecure() {
		t.Fatal("https should secure")
	}
}
