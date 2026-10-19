package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/search"
	"github.com/rendicott/marble/internal/session"
)

func TestSessionSearchUnavailableWhenLimp(t *testing.T) {
	s := &Server{Registry: session.NewRegistry(nil, nil, &db.DB{Mode: db.ModeLimp}, "", "")}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/search?q=android", nil)
	w := httptest.NewRecorder()
	s.handleSessionSub(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "search unavailable") {
		t.Fatalf("body %s", w.Body.String())
	}
}

func TestSessionSearchResponseShape(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	terms := search.Terms([]search.MessageText{
		{Role: "tool", Content: "apk gradle build log"},
	})
	if err := d.ReplaceSessionSearch(db.SessionSearchRow{
		SessionID:   "sess1",
		Title:       "Build session",
		Description: "Build session",
		UpdatedAt:   "2026-10-18T15:04:05Z",
	}, terms, false); err != nil {
		t.Fatal(err)
	}
	s := &Server{Registry: session.NewRegistry(nil, nil, d, "", "")}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/search?q=android", nil)
	w := httptest.NewRecorder()
	s.handleSessionSub(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		Hits []map[string]interface{} `json:"hits"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Hits) != 1 {
		t.Fatalf("hits %+v", body.Hits)
	}
	h := body.Hits[0]
	for _, k := range []string{"id", "description", "updated_at"} {
		if _, ok := h[k]; !ok {
			t.Fatalf("missing %s in %+v", k, h)
		}
	}
	if len(h) != 3 {
		t.Fatalf("extra fields %+v", h)
	}
	if strings.Contains(w.Body.String(), "gradle") || strings.Contains(w.Body.String(), "apk") {
		t.Fatalf("response leaked indexed text: %s", w.Body.String())
	}
	if h["description"] != "Build session" || h["id"] != "sess1" {
		t.Fatalf("hit %+v", h)
	}
}
