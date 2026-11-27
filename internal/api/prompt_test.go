package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rendicott/marble/internal/memory"
	"github.com/rendicott/marble/internal/session"
)

func TestPromptGetSoulVersion(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Registry: session.NewRegistry(nil, store, nil, dir, "")}

	req := httptest.NewRequest(http.MethodGet, "/api/prompt", nil)
	w := httptest.NewRecorder()
	srv.handlePrompt(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	var empty map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"system_prompt", "soul", "soul_path", "soul_path_abs", "soul_max_chars", "immutable", "soul_sha", "soul_chars"} {
		if _, ok := empty[k]; !ok {
			t.Fatalf("missing %s in %s", k, w.Body.String())
		}
	}
	if empty["soul"] != "" || empty["soul_sha"] != "e3b0c44298fc" || empty["soul_chars"] != float64(0) {
		t.Fatalf("empty soul version: %s", w.Body.String())
	}
	if empty["immutable"] != true || empty["soul_path"] != "soul.md" {
		t.Fatalf("existing fields: %s", w.Body.String())
	}

	const text = "café rules"
	if err := store.WriteSoul(text); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	srv.handlePrompt(w, req)
	var body struct {
		Soul      string `json:"soul"`
		SoulSHA   string `json:"soul_sha"`
		SoulChars int    `json:"soul_chars"`
		System    string `json:"system_prompt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	sha, chars := memory.SoulVersion(text)
	if body.Soul != text || body.SoulSHA != sha || body.SoulChars != chars || body.System == "" {
		t.Fatalf("body %+v sha %s chars %d", body, sha, chars)
	}
	if chars != 10 {
		t.Fatalf("chars %d", chars)
	}
}
