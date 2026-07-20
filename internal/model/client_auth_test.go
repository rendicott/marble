package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetAuthOmitsWhenEmpty(t *testing.T) {
	c := New("http://example/v1", "m", 100, "")
	req, _ := http.NewRequest(http.MethodGet, "http://example/v1/models", nil)
	c.setAuth(req)
	if req.Header.Get("Authorization") != "" {
		t.Fatalf("expected no Authorization, got %q", req.Header.Get("Authorization"))
	}
}

func TestSetAuthBearerWhenSet(t *testing.T) {
	c := New("http://example/v1", "m", 100, "sk-test")
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

	c := New(srv.URL+"/v1", "m", 100, "sk-live")
	if err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sawAuth != "Bearer sk-live" {
		t.Fatalf("auth %q", sawAuth)
	}

	sawAuth = "unset"
	c2 := New(srv.URL+"/v1", "m", 100, "")
	if err := c2.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sawAuth != "" {
		t.Fatalf("expected empty auth, got %q", sawAuth)
	}
}
