package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/session"
)

func TestToolLogUnavailableWhenLimp(t *testing.T) {
	s := &Server{Registry: session.NewRegistry(nil, nil, &db.DB{Mode: db.ModeLimp}, "", "")}
	for _, path := range []string{"/api/tool-log", "/api/tool-log/stats"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		s.handleToolLog(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status %d body %s", path, w.Code, w.Body.String())
		}
	}
}

func TestToolLogRowsAndStats(t *testing.T) {
	d, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	hits := 2
	if err := d.InsertToolCallLog(db.ToolCallLog{
		SessionID: "s1", TS: "2026-11-01T00:00:01Z", ToolName: "memory_search",
		ToolCallID: "c1", ArgsJSON: `{"query":"a"}`,
		ResultSummaryJSON: `{"hits":2,"empty":false,"data":{"docs":[{"path":"knowledge/a.md"}]}}`,
		OK:                1, LatencyMS: 12, ResultChars: 40, HitCount: &hits,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertToolCallLog(db.ToolCallLog{
		SessionID: "s1", TS: "2026-11-01T00:00:02Z", ToolName: "memory_search",
		ToolCallID: "c2", ArgsJSON: `{"query":"a"}`, OK: 0, Error: "query is required",
		LatencyMS: 4, ResultChars: 18,
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{Registry: session.NewRegistry(nil, nil, d, "", "")}
	req := httptest.NewRequest(http.MethodGet, "/api/tool-log?session_id=s1&tool=memory_search&limit=10", nil)
	w := httptest.NewRecorder()
	s.handleToolLog(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		Rows []db.ToolCallLog `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Rows) != 2 || body.Rows[0].ToolCallID != "c2" || body.Rows[1].HitCount == nil || *body.Rows[1].HitCount != 2 {
		t.Fatalf("rows %+v", body.Rows)
	}
	if body.Rows[0].OK != 0 || body.Rows[0].Error != "query is required" {
		t.Fatalf("error row %+v", body.Rows[0])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/tool-log/stats?tool=memory_search", nil)
	w = httptest.NewRecorder()
	s.handleToolLog(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("stats %d %s", w.Code, w.Body.String())
	}
	var st db.ToolLogStats
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.CallCount != 2 || st.ErrorCount != 1 || st.ConsecutiveSameArgs != 1 {
		t.Fatalf("stats %+v", st)
	}
	if len(st.TopDocs) != 1 || st.TopDocs[0].Path != "knowledge/a.md" || st.TopDocs[0].Count != 1 {
		t.Fatalf("docs %+v", st.TopDocs)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/tool-log", nil)
	w = httptest.NewRecorder()
	s.handleToolLog(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post %d", w.Code)
	}
}
