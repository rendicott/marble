package mpub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishGetListUnpublish(t *testing.T) {
	mem := t.TempDir()
	s, err := New(mem)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := s.Publish("foo-research", "Foo", "<h1>Hi</h1>", "text/html", "sess1", []string{"x"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Slug != "foo-research" || meta.SessionID != "sess1" {
		t.Fatalf("%+v", meta)
	}
	if _, err := os.Stat(filepath.Join(s.Root, "foo-research", "content.html")); err != nil {
		t.Fatal(err)
	}
	doc, err := s.Get("foo-research")
	if err != nil || !strings.Contains(doc.Content, "Hi") {
		t.Fatalf("%v %v", doc, err)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %v", list, err)
	}
	// if_exists fail
	if _, err := s.Publish("foo-research", "x", "y", "text/html", "", nil, true); err == nil {
		t.Fatal("expected fail")
	}
	// overwrite
	if _, err := s.Publish("foo-research", "Foo2", "<p>v2</p>", "text/html", "sess1", nil, false); err != nil {
		t.Fatal(err)
	}
	doc, _ = s.Get("foo-research")
	if !strings.Contains(doc.Content, "v2") {
		t.Fatal(doc.Content)
	}
	if err := s.Unpublish("foo-research"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("foo-research"); err == nil {
		t.Fatal("expected missing")
	}
}

func TestSlugValidation(t *testing.T) {
	if err := ValidateSlug("Foo"); err == nil {
		t.Fatal("uppercase")
	}
	if err := ValidateSlug("foo/bar"); err == nil {
		t.Fatal("nested")
	}
	if err := ValidateSlug("ok-slug-1"); err != nil {
		t.Fatal(err)
	}
}

func TestMarkdownRender(t *testing.T) {
	html := MarkdownToHTML("# Title\n\nHello **world**\n\n- a\n- b\n")
	if !strings.Contains(html, "<h1>") || !strings.Contains(html, "<strong>world</strong>") {
		t.Fatal(html)
	}
}

func TestPublicURL(t *testing.T) {
	u := PublicURL(":9090", "x")
	if u != "http://127.0.0.1:9090/mpub/x" {
		t.Fatal(u)
	}
}

func TestMaxBody(t *testing.T) {
	s, _ := New(t.TempDir())
	big := strings.Repeat("a", MaxBodyBytes+1)
	if _, err := s.Publish("big", "", big, "text/plain", "", nil, false); err == nil {
		t.Fatal("expected size error")
	}
}
