package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rendicott/marble/internal/memsearch"
	"github.com/rendicott/marble/internal/tools"
)

func TestMemoryIndexEndpoints(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "knowledge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "knowledge", "a.md"), []byte("# A\n\nkestrel"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := &tools.Registry{Memory: root}
	t.Cleanup(func() {
		if reg.MemIndex != nil {
			_ = reg.MemIndex.Close()
		}
	})
	s := &Server{Tools: reg}

	w := httptest.NewRecorder()
	s.handleMemoryIndex(w, httptest.NewRequest(http.MethodGet, "/api/memory-index/reindex", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET reindex %d", w.Code)
	}

	w = httptest.NewRecorder()
	s.handleMemoryIndex(w, httptest.NewRequest(http.MethodPost, "/api/memory-index/reindex", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("reindex %d %s", w.Code, w.Body.String())
	}
	var rep memsearch.ReindexReport
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil || rep.Added != 1 || rep.Partial {
		t.Fatalf("report %+v %v", rep, err)
	}

	w = httptest.NewRecorder()
	s.handleMemoryIndex(w, httptest.NewRequest(http.MethodGet, "/api/memory-index", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("stats %d %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"docs", "by_kind", "bytes", "last_reindexed_at", "age_sec"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("missing %s in %s", k, w.Body.String())
		}
	}
	if body["docs"].(float64) != 1 {
		t.Fatalf("docs %v", body["docs"])
	}
}

func TestMemoryIndexUnavailable(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.handleMemoryIndex(w, httptest.NewRequest(http.MethodGet, "/api/memory-index", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", w.Code)
	}
}
