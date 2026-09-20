package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mpubTestRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	ws := t.TempDir()
	return &Registry{Workspace: ws, Memory: t.TempDir(), Addr: ":8080"}, ws
}

func publishJSON(t *testing.T, r *Registry, args map[string]interface{}) (map[string]interface{}, error) {
	t.Helper()
	b, _ := json.Marshal(args)
	out, err := r.mpubPublish(string(b), nil)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return m, nil
}

func TestMpubPublishAssetsAndContentPath(t *testing.T) {
	r, ws := mpubTestRegistry(t)
	r.PublicBaseURL = func() string { return "http://host.ts.net:8080/" }
	if err := os.MkdirAll(filepath.Join(ws, "shots"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ws, "shots", "inbox.jpg"), []byte("jpgdata"), 0o644)
	page := `<img src="inbox.jpg"><img src="missing.jpg"> __INBOX__`
	os.WriteFile(filepath.Join(ws, "page.html"), []byte(page), 0o644)

	m, err := publishJSON(t, r, map[string]interface{}{
		"slug": "ref", "content_path": "page.html", "assets": []string{"shots/inbox.jpg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("published %d bytes, 1 assets", len(page)); m["summary"] != want {
		t.Fatalf("summary: %v", m["summary"])
	}
	if m["url"] != "http://host.ts.net:8080/mpub/ref" || m["local_url"] != "http://127.0.0.1:8080/mpub/ref" {
		t.Fatalf("urls: %v %v", m["url"], m["local_url"])
	}
	if len(m["sha256"].(string)) != 64 {
		t.Fatalf("sha: %v", m["sha256"])
	}
	warns, _ := m["warnings"].([]interface{})
	joined := ""
	for _, w := range warns {
		joined += w.(string) + "\n"
	}
	if !strings.Contains(joined, "missing.jpg") || !strings.Contains(joined, "__INBOX__") || strings.Contains(joined, "inbox.jpg\"") {
		t.Fatalf("warnings: %q", joined)
	}

	// mpub_get round-trips the same sha so callers can verify
	out, err := r.mpubGet(`{"slug":"ref"}`)
	if err != nil {
		t.Fatal(err)
	}
	var g map[string]interface{}
	json.Unmarshal([]byte(out), &g)
	if g["sha256"] != m["sha256"] || len(g["assets"].([]interface{})) != 1 {
		t.Fatalf("get: %v", g)
	}
}

func TestMpubPublishRejectsBadInput(t *testing.T) {
	r, ws := mpubTestRegistry(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "x.png"), []byte("x"), 0o644)
	os.Symlink(filepath.Join(outside, "x.png"), filepath.Join(ws, "link.png"))
	os.WriteFile(filepath.Join(ws, "ok.png"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(ws, "my shot.png"), []byte("x"), 0o644)

	cases := map[string]map[string]interface{}{
		"neither":     {"slug": "a"},
		"both":        {"slug": "a", "content": "x", "content_path": "ok.png"},
		"escape":      {"slug": "a", "content": "x", "assets": []string{"../x.png"}},
		"abs-outside": {"slug": "a", "content": "x", "assets": []string{filepath.Join(outside, "x.png")}},
		"symlink":     {"slug": "a", "content": "x", "assets": []string{"link.png"}},
		"missing":     {"slug": "a", "content": "x", "assets": []string{"nope.png"}},
		"space":       {"slug": "a", "content": "x", "assets": []string{"my shot.png"}},
		"body-escape": {"slug": "a", "content_path": "../etc/passwd"},
	}
	for name, args := range cases {
		if _, err := publishJSON(t, r, args); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	// inline content still works with no assets
	if _, err := publishJSON(t, r, map[string]interface{}{"slug": "a", "content": "<p>x</p>"}); err != nil {
		t.Fatal(err)
	}
}
