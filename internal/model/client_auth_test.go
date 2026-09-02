package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSetAuthOmitsWhenEmpty(t *testing.T) {
	c := New("http://example/v1", "m", 100, "", 0)
	req, _ := http.NewRequest(http.MethodGet, "http://example/v1/models", nil)
	c.setAuth(req)
	if req.Header.Get("Authorization") != "" {
		t.Fatalf("expected no Authorization, got %q", req.Header.Get("Authorization"))
	}
}

func TestSetAuthBearerWhenSet(t *testing.T) {
	c := New("http://example/v1", "m", 100, "sk-test", 0)
	req, _ := http.NewRequest(http.MethodGet, "http://example/v1/models", nil)
	c.setAuth(req)
	if got := req.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Fatalf("got %q", got)
	}
}

func TestHealthAuthHeader(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/v1", "m", 100, "sk-live", 0)
	if err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sawAuth != "Bearer sk-live" {
		t.Fatalf("auth %q", sawAuth)
	}

	sawAuth = "unset"
	c2 := New(srv.URL+"/v1", "m", 100, "", 0)
	if err := c2.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sawAuth != "" {
		t.Fatalf("expected empty auth, got %q", sawAuth)
	}
}

func TestDefaultHTTPTimeout(t *testing.T) {
	c := New("http://example/v1", "m", 100, "", 0)
	if c.HTTPTimeout() != DefaultHTTPTimeout {
		t.Fatalf("got %v want %v", c.HTTPTimeout(), DefaultHTTPTimeout)
	}
	c2 := New("http://example/v1", "m", 100, "", 45*time.Minute)
	if c2.HTTPTimeout() != 45*time.Minute {
		t.Fatalf("got %v", c2.HTTPTimeout())
	}
}
