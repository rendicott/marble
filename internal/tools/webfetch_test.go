package tools

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestWebFetchHTMLMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>T</title>
<script>evil()</script></head><body>
<h1>Hello</h1><p>World <strong>bold</strong></p>
<a href="https://example.com/x">link</a>
<ul><li>one</li><li>two</li></ul>
</body></html>`)
	}))
	defer srv.Close()

	r := &Registry{MaxResultChars: 50000}
	out := r.Execute("web_fetch", fmt.Sprintf(`{"url":%q}`, srv.URL), nil)
	if strings.HasPrefix(out, "error:") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "format: markdown") {
		t.Fatalf("expected markdown format: %s", out)
	}
	if !strings.Contains(out, "# Hello") && !strings.Contains(out, "Hello") {
		t.Fatalf("missing heading text: %s", out)
	}
	if !strings.Contains(out, "World") || !strings.Contains(out, "bold") {
		t.Fatalf("missing body: %s", out)
	}
	if strings.Contains(out, "evil()") {
		t.Fatal("script leaked")
	}
}

func TestWebFetchJSONRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"a":1,"b":"x"}`)
	}))
	defer srv.Close()

	r := &Registry{MaxResultChars: 50000}
	out := r.Execute("web_fetch", fmt.Sprintf(`{"url":%q}`, srv.URL), nil)
	if strings.HasPrefix(out, "error:") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "format: raw") {
		t.Fatalf("expected raw: %s", out)
	}
	if !strings.Contains(out, `{"a":1,"b":"x"}`) {
		t.Fatalf("json not raw: %s", out)
	}
}

func TestValidateFetchURLMetadataBlocked(t *testing.T) {
	cases := []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://metadata.google.internal/",
		"https://metadata.google.internal/computeMetadata/v1/",
	}
	for _, raw := range cases {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateFetchURL(u); err == nil {
			t.Fatalf("expected block for %s", raw)
		}
	}
}

func TestValidateFetchURLPrivateAllowed(t *testing.T) {
	// IP literals in private ranges must not be rejected by policy (LAN allowed).
	// Note: LookupIP on 10.0.0.1 still works as parse.
	for _, raw := range []string{
		"http://10.0.0.1/docs",
		"http://192.168.1.1/",
		"http://172.16.0.5/api",
		"http://127.0.0.1:8080/health",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateFetchURL(u); err != nil {
			t.Fatalf("LAN should be allowed for %s: %v", raw, err)
		}
	}
}

func TestValidateFetchURLScheme(t *testing.T) {
	u, _ := url.Parse("file:///etc/passwd")
	if err := validateFetchURL(u); err == nil {
		t.Fatal("expected scheme error")
	}
}

func TestHTMLToMarkdownBasic(t *testing.T) {
	md := htmlToMarkdown(`<html><body><h2>Title</h2><p>Hi <em>there</em></p></body></html>`)
	if !strings.Contains(md, "Title") || !strings.Contains(md, "Hi") {
		t.Fatalf("%q", md)
	}
}

func TestWebFetchRedirectRevalidates(t *testing.T) {
	var final *httptest.Server
	final = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "ok-final")
	}))
	defer final.Close()

	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/x", http.StatusFound)
	}))
	defer redir.Close()

	r := &Registry{MaxResultChars: 50000}
	out := r.Execute("web_fetch", fmt.Sprintf(`{"url":%q}`, redir.URL), nil)
	if strings.HasPrefix(out, "error:") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "ok-final") {
		t.Fatalf("%s", out)
	}
	if !strings.Contains(out, "final_url:") {
		t.Fatalf("missing final_url: %s", out)
	}
}
