package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/shellpolicy"
	"github.com/rendicott/marble/internal/tools"
)

func TestMemorySearchWritesToolCallLog(t *testing.T) {
	root := t.TempDir()
	mem := filepath.Join(root, "memory")
	if err := os.MkdirAll(filepath.Join(mem, "knowledge"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# Note\n\nThe zephyr beacon is the landmark.\n"
	if err := os.WriteFile(filepath.Join(mem, "knowledge", "note.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sqldb, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })

	toolsReg := &tools.Registry{Memory: mem, Workspace: t.TempDir()}
	reg := NewRegistry(nil, nil, sqldb, "", "")
	runner := &Runner{Tools: toolsReg, Reg: reg}
	s := &Session{ID: "sess-log"}

	args := `{"query":"zephyr beacon","max_results":5}`
	text := runner.observeTool(s, "memory_search", "call-1", args, nil)
	if !strings.Contains(text, "knowledge/note.md") {
		t.Fatalf("text %q", text)
	}

	bad := `{}`
	badText := runner.observeTool(s, "memory_search", "call-2", bad, nil)
	if !strings.HasPrefix(badText, "error:") || !strings.Contains(badText, "query is required") {
		t.Fatalf("error text %q", badText)
	}

	rows, err := sqldb.ListToolCallLogs(s.ID, "memory_search", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %d", len(rows))
	}
	byID := map[string]db.ToolCallLog{}
	for _, row := range rows {
		byID[row.ToolCallID] = row
	}
	hit := byID["call-1"]
	if hit.ArgsJSON != args {
		t.Fatalf("args %q", hit.ArgsJSON)
	}
	if hit.OK != 1 || hit.Error != "" {
		t.Fatalf("ok row %+v", hit)
	}
	if hit.HitCount == nil || *hit.HitCount != 1 {
		t.Fatalf("hit_count %+v", hit.HitCount)
	}
	if hit.LatencyMS < 0 {
		t.Fatalf("latency %d", hit.LatencyMS)
	}
	if hit.ResultChars != len(text) {
		t.Fatalf("chars %d want %d", hit.ResultChars, len(text))
	}
	var sum struct {
		Hits  int  `json:"hits"`
		Empty bool `json:"empty"`
		Data  struct {
			Docs []struct {
				Path          string   `json:"path"`
				MatchedPhrase bool     `json:"matched_phrase"`
				MatchedTokens []string `json:"matched_tokens"`
				DocBytes      int      `json:"doc_bytes"`
			} `json:"docs"`
			DocsScanned int `json:"docs_scanned"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(hit.ResultSummaryJSON), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Empty || sum.Hits != 1 || sum.Data.DocsScanned < 1 || len(sum.Data.Docs) != 1 {
		t.Fatalf("summary json %+v", sum)
	}
	doc := sum.Data.Docs[0]
	if doc.Path != "knowledge/note.md" || !doc.MatchedPhrase || doc.DocBytes != len(body) {
		t.Fatalf("doc %+v", doc)
	}
	if strings.Join(doc.MatchedTokens, ",") != "zephyr,beacon" {
		t.Fatalf("tokens %v", doc.MatchedTokens)
	}

	miss := byID["call-2"]
	if miss.OK != 0 || miss.Error == "" || miss.ArgsJSON != bad {
		t.Fatalf("error row %+v", miss)
	}
	if !strings.Contains(miss.Error, "query is required") {
		t.Fatalf("error text %q", miss.Error)
	}
	if miss.HitCount != nil || miss.ResultSummaryJSON != "" {
		t.Fatalf("error row should omit summary: %+v", miss)
	}
	if miss.LatencyMS < 0 {
		t.Fatalf("error latency %d", miss.LatencyMS)
	}
	if miss.Seq != 2 || hit.Seq != 1 {
		t.Fatalf("seq hit %d miss %d", hit.Seq, miss.Seq)
	}
}

// TestShellExecuteNonZeroExitStaysOK records a failing command as ok=1.
// ok is cleared only when the tool returns an error. The exit code lives in
// the summary, and the summary JSON is stored on the row.
func TestShellExecuteNonZeroExitStaysOK(t *testing.T) {
	root := t.TempDir()
	sqldb, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })

	ws := t.TempDir()
	pol := shellpolicy.New(ws, t.TempDir(), false, 5*time.Second, 10*time.Second)
	toolsReg := &tools.Registry{Workspace: ws, Policy: pol, MaxResultChars: 10000}
	reg := NewRegistry(nil, nil, sqldb, "", "")
	runner := &Runner{Tools: toolsReg, Reg: reg}
	s := &Session{ID: "sess-shell"}

	args := `{"command":"exit 7"}`
	text := runner.observeTool(s, "shell_execute", "call-exit", args, nil)
	if strings.HasPrefix(text, "error:") || !strings.Contains(text, "exit=7") {
		t.Fatalf("text %q", text)
	}

	rows, err := sqldb.ListToolCallLogs(s.ID, "shell_execute", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows %d", len(rows))
	}
	row := rows[0]
	if row.OK != 1 || row.Error != "" {
		t.Fatalf("ok row %+v", row)
	}
	if row.ArgsJSON != args {
		t.Fatalf("args %q", row.ArgsJSON)
	}
	if row.ResultSummaryJSON == "" {
		t.Fatal("empty result_summary_json")
	}
	if row.HitCount == nil || *row.HitCount != 0 {
		t.Fatalf("hit_count %+v", row.HitCount)
	}
	var sum struct {
		Hits  int  `json:"hits"`
		Empty bool `json:"empty"`
		Data  struct {
			ExitCode int  `json:"exit_code"`
			TimedOut bool `json:"timed_out"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(row.ResultSummaryJSON), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Hits != 0 || !sum.Empty || sum.Data.ExitCode != 7 || sum.Data.TimedOut {
		t.Fatalf("summary %+v json %s", sum, row.ResultSummaryJSON)
	}
}
