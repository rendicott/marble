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
	meta, err := s.Publish("foo-research", "Foo", "<h1>Hi</h1>", "text/html", "sess1", []string{"x"}, false, "", nil)
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
	if _, err := s.Publish("foo-research", "x", "y", "text/html", "", nil, true, "", nil); err == nil {
		t.Fatal("expected fail")
	}
	// overwrite keeps visibility when omitted
	meta2, err := s.Publish("foo-research", "Foo2", "<p>v2</p>", "text/html", "sess1", nil, false, "", nil)
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
	pub, err := s.Publish("open-page", "Open", "hi", "text/plain", "", nil, false, VisibilityPublic, nil)
	if err != nil {
		t.Fatal(err)
	}
	if EffectiveVisibility(*pub) != VisibilityPublic {
		t.Fatalf("%+v", pub)
	}
	priv, err := s.Publish("secret-page", "Secret", "nope", "text/plain", "", nil, false, VisibilityPrivate, nil)
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
	if _, err := s.Publish("big", "", big, "text/plain", "", nil, false, "", nil); err == nil {
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

func TestPublishAssetsRewriteAndKeep(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG-fake")
	body := `<p><img src="shot.png"> <a href='./shot.png'>full</a> <img src="other.png"></p>`
	if _, err := s.Publish("pg", "P", body, "text/html", "", nil, false, "", []Asset{{Name: "shot.png", Data: png}}); err != nil {
		t.Fatal(err)
	}
	// text-only republish keeps the asset
	if _, err := s.Publish("pg", "P", body, "text/html", "", nil, false, "", nil); err != nil {
		t.Fatal(err)
	}
	doc, err := s.Get("pg")
	if err != nil || len(doc.Assets) != 1 || doc.Assets[0].Name != "shot.png" || doc.Assets[0].Bytes != int64(len(png)) {
		t.Fatalf("assets=%+v err=%v", doc.Assets, err)
	}
	_, out := ServeBody(doc)
	got := string(out)
	if !strings.Contains(got, `src="/mpub/pg/shot.png"`) || !strings.Contains(got, `href='/mpub/pg/shot.png'`) {
		t.Fatalf("known refs not rewritten:\n%s", got)
	}
	if !strings.Contains(got, `src="other.png"`) {
		t.Fatalf("unknown ref must be untouched:\n%s", got)
	}
	data, ct, err := s.ReadAsset("pg", "shot.png")
	if err != nil || ct != "image/png" || string(data) != string(png) {
		t.Fatalf("%q %q %v", data, ct, err)
	}
	if _, _, err := s.ReadAsset("pg", "../meta.json"); err == nil {
		t.Fatal("expected traversal rejection")
	}
	if err := s.Unpublish("pg"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReadAsset("pg", "shot.png"); err == nil {
		t.Fatal("assets should be removed with the page")
	}
}

func TestPublishAssetValidation(t *testing.T) {
	s, _ := New(t.TempDir())
	cases := map[string][]Asset{
		"space":     {{Name: "my shot.png", Data: []byte("x")}},
		"traversal": {{Name: "../x.png", Data: []byte("x")}},
		"type":      {{Name: "run.js", Data: []byte("x")}},
		"dup":       {{Name: "a.png", Data: []byte("x")}, {Name: "a.png", Data: []byte("y")}},
		"big":       {{Name: "a.png", Data: make([]byte, MaxAssetBytes+1)}},
	}
	for name, assets := range cases {
		if _, err := s.Publish("v-"+name, "", "<p>x</p>", "text/html", "", nil, false, "", assets); err == nil {
			t.Errorf("%s: expected error", name)
		}
		// nothing written when validation fails
		if _, err := s.ReadMeta("v-" + name); err == nil {
			t.Errorf("%s: page created despite invalid assets", name)
		}
	}
}

func TestMarkdownImageRewrite(t *testing.T) {
	doc := &Doc{
		Meta:    Meta{Slug: "md", Title: "T", ContentType: "text/markdown"},
		Content: "![shot](a.png)\n\n![bad](javascript:alert(1))",
		Assets:  []AssetInfo{{Name: "a.png"}},
	}
	_, out := ServeBody(doc)
	got := string(out)
	if !strings.Contains(got, `<img src="/mpub/md/a.png" alt="shot">`) {
		t.Fatalf("md image: %s", got)
	}
	if strings.Contains(got, "javascript:") {
		t.Fatalf("unsafe image src leaked: %s", got)
	}
}

func TestLint(t *testing.T) {
	assets := []AssetInfo{{Name: "ok.png"}}
	w := Lint("text/html", `<img src="ok.png"><img src="missing.png"><img src="https://x/y.png"><img src="data:image/png;base64,AA"><img src="/abs.png"><a href="page.html">l</a><p>__INBOX__</p>`, assets)
	joined := strings.Join(w, "\n")
	if !strings.Contains(joined, `"missing.png"`) || !strings.Contains(joined, "__INBOX__") {
		t.Fatalf("warnings: %v", w)
	}
	if strings.Contains(joined, "ok.png") || strings.Contains(joined, "page.html") || strings.Contains(joined, "abs.png") || strings.Contains(joined, "x/y.png") || strings.Contains(joined, "base64") {
		t.Fatalf("false positive: %v", w)
	}
	if w := Lint("text/markdown", "![a](nope.png) ![b](ok.png)", assets); len(w) != 1 || !strings.Contains(w[0], "nope.png") {
		t.Fatalf("md warnings: %v", w)
	}
	if w := Lint("text/html", `<img src="ok.png">`, assets); len(w) != 0 {
		t.Fatalf("clean page warned: %v", w)
	}
}
