package mpub

import (
	"encoding/json"
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
	meta, err := s.Publish("foo-research", "Foo", "<h1>Hi</h1>", "text/html", "sess1", []string{"x"}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Slug != "foo-research" || meta.SessionID != "sess1" {
		t.Fatalf("%+v", meta)
	}
	if EffectiveVisibility(*meta) != VisibilityPrivate {
		t.Fatalf("new publish default want private got %q", meta.Visibility)
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
	if _, err := s.Publish("foo-research", "x", "y", "text/html", "", nil, true, ""); err == nil {
		t.Fatal("expected fail")
	}
	// overwrite keeps visibility when omitted
	meta2, err := s.Publish("foo-research", "Foo2", "<p>v2</p>", "text/html", "sess1", nil, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if EffectiveVisibility(*meta2) != VisibilityPrivate {
		t.Fatalf("overwrite keep vis: %+v", meta2)
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

func TestVisibilityPublicPrivate(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pub, err := s.Publish("open-page", "Open", "hi", "text/plain", "", nil, false, VisibilityPublic)
	if err != nil {
		t.Fatal(err)
	}
	if EffectiveVisibility(*pub) != VisibilityPublic {
		t.Fatalf("%+v", pub)
	}
	priv, err := s.Publish("secret-page", "Secret", "nope", "text/plain", "", nil, false, VisibilityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if EffectiveVisibility(*priv) != VisibilityPrivate {
		t.Fatalf("%+v", priv)
	}
	// promote
	meta, err := s.SetVisibility("secret-page", VisibilityPublic)
	if err != nil {
		t.Fatal(err)
	}
	if EffectiveVisibility(*meta) != VisibilityPublic {
		t.Fatalf("%+v", meta)
	}
	// demote
	meta, err = s.SetVisibility("open-page", "private")
	if err != nil {
		t.Fatal(err)
	}
	if EffectiveVisibility(*meta) != VisibilityPrivate {
		t.Fatalf("%+v", meta)
	}
	if _, err := s.SetVisibility("missing", "public"); err == nil {
		t.Fatal("expected missing")
	}
	if _, err := s.SetVisibility("open-page", "friends"); err == nil {
		t.Fatal("expected bad visibility")
	}
}

func TestLegacyEmptyVisibilityIsPublic(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// write legacy meta without visibility field
	dir := filepath.Join(s.Root, "legacy-doc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "content.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]interface{}{
		"slug": "legacy-doc", "title": "Legacy", "content_type": "text/plain",
		"created_at": "2020-01-01T00:00:00Z", "updated_at": "2020-01-01T00:00:00Z",
		"bytes": 3, "filename": "content.txt",
	}
	b, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := s.ReadMeta("legacy-doc")
	if err != nil {
		t.Fatal(err)
	}
	if EffectiveVisibility(*m) != VisibilityPublic {
		t.Fatalf("legacy empty → public, got %q", m.Visibility)
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
	if _, err := s.Publish("big", "", big, "text/plain", "", nil, false, ""); err == nil {
		t.Fatal("expected size error")
	}
}

func TestIndexHTMLBadge(t *testing.T) {
	html := IndexHTML([]Meta{
		{Slug: "a", Title: "A", ContentType: "text/html", Visibility: VisibilityPublic, UpdatedAt: "t"},
		{Slug: "b", Title: "B", ContentType: "text/html", Visibility: VisibilityPrivate, UpdatedAt: "t"},
	})
	if !strings.Contains(html, "badge-public") || !strings.Contains(html, "badge-private") {
		t.Fatal(html)
	}
}
