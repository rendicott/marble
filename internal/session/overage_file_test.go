package session

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rendicott/marble/internal/memory"

	_ "modernc.org/sqlite"
)

func TestRescueFileDryRunWritesNothing(t *testing.T) {
	root := writeFixtureMemory(t, 0)
	path := filepath.Join(root, "session", "ovfix01.md")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := RescueFile(FileRescueRequest{
		MemoryDir:      root,
		ID:             "ovfix01",
		Explicit:       true,
		ContextLimit:   2_000_000,
		MaxOutput:      1024,
		ContextReserve: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.Archive != "" || res.ContextLimit != 2_000_000 {
		t.Fatalf("%+v", res)
	}
	if res.Action != "noop" && res.Action != "rescued" && res.Action != "unrecoverable" {
		t.Fatalf("action %q", res.Action)
	}
	if res.Margin != rescueMargin(2_000_000) || res.Scale != rescueDefaultScale {
		t.Fatalf("margin %d scale %v", res.Margin, res.Scale)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("dry run changed the session file")
	}
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info2.ModTime().Equal(info.ModTime()) {
		t.Fatal("dry run changed mtime")
	}
	if _, err := os.Stat(filepath.Join(root, "backups")); !os.IsNotExist(err) {
		t.Fatal("dry run created a backup")
	}
}

func TestRescueFileApplyArchivesThenOmits(t *testing.T) {
	doc := decodeFixture(t)
	limit := limitThatDropsOldRound(t, doc)
	root := writeFixtureMemory(t, limit)
	at := time.Date(2026, 10, 22, 15, 4, 5, 0, time.UTC)
	path := filepath.Join(root, "session", "ovfix01.md")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := RescueFile(FileRescueRequest{
		MemoryDir: root,
		ID:        "ovfix01",
		Apply:     true,
		At:        at,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.DryRun || res.Action != "rescued" || res.OmittedMessages < 1 {
		t.Fatalf("%+v", res)
	}
	rel := "backups/overage/ovfix01/20261022T150405Z.md"
	if res.Archive != rel {
		t.Fatalf("archive %q", res.Archive)
	}
	archived, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	if string(archived) != string(before) {
		t.Fatal("archive is not the pre-image")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "<!-- model_omit: overage-rescue -->") {
		t.Fatal("live file missing omit flag")
	}
	if !strings.Contains(string(got), "OLDROUND") || !strings.Contains(string(got), "NEWROUND") {
		t.Fatal("rescue dropped transcript text")
	}
	doc, err = memory.DecodeSession(string(got))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Messages[len(doc.Messages)-1].Role != "harness" {
		t.Fatal("missing harness marker")
	}
}

func TestRescueFileApplyRefusesLiveLock(t *testing.T) {
	root := writeFixtureMemory(t, 1_000_000)
	if err := os.WriteFile(filepath.Join(root, "marble.lock"), []byte("2147483646\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Dead pid: the lock file is stale, so apply is allowed.
	if _, err := RescueFile(FileRescueRequest{MemoryDir: root, ID: "ovfix01", Apply: true}); err != nil {
		t.Fatal(err)
	}

	root = writeFixtureMemory(t, 1_000_000)
	if err := os.WriteFile(filepath.Join(root, "marble.lock"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RescueFile(FileRescueRequest{MemoryDir: root, ID: "ovfix01", Apply: true}); err == nil || !strings.Contains(err.Error(), "marble.lock") {
		t.Fatalf("apply lock: %v", err)
	}
	before := mustRead(t, filepath.Join(root, "session", "ovfix01.md"))
	res, err := RescueFile(FileRescueRequest{MemoryDir: root, ID: "ovfix01"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun {
		t.Fatal("dry run refused while the harness lock is held")
	}
	if mustRead(t, filepath.Join(root, "session", "ovfix01.md")) != before {
		t.Fatal("dry run changed the session file")
	}
}

func TestRescueFileNeedsOneSessionAndAWindow(t *testing.T) {
	root := writeFixtureMemory(t, 0)
	if _, err := RescueFile(FileRescueRequest{MemoryDir: root}); err == nil || !strings.Contains(err.Error(), "session id required") {
		t.Fatalf("missing id: %v", err)
	}
	if _, err := RescueFile(FileRescueRequest{MemoryDir: root, ID: "ovfix01"}); err == nil || !strings.Contains(err.Error(), "context-limit") {
		t.Fatalf("missing window: %v", err)
	}
	doc, err := memory.DecodeSession(mustRead(t, filepath.Join(root, "session", "ovfix01.md")))
	if err != nil {
		t.Fatal(err)
	}
	doc.Status = "closed"
	if err := os.WriteFile(filepath.Join(root, "session", "ovfix01.md"), []byte(memory.EncodeSession(doc)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RescueFile(FileRescueRequest{
		MemoryDir: root, ID: "ovfix01", Explicit: true,
		ContextLimit: 2_000_000, MaxOutput: 1024, ContextReserve: 8192,
	}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("closed: %v", err)
	}
}

func TestRescueFileScaleFromAssistantBody(t *testing.T) {
	root := writeFixtureMemory(t, 0)
	doc, err := memory.DecodeSession(mustRead(t, filepath.Join(root, "session", "ovfix01.md")))
	if err != nil {
		t.Fatal(err)
	}
	doc.Messages = append(doc.Messages, memory.TranscriptMessage{
		ID:        "m-err",
		Role:      "assistant",
		CreatedAt: time.Date(2026, 10, 22, 12, 2, 0, 0, time.UTC),
		Content:   "maximum context length is 1048576 tokens. However, you requested 1048656 tokens (655440 in the messages, 393216 in the completion). (est 600000, window 1048576)",
	})
	if err := os.WriteFile(filepath.Join(root, "session", "ovfix01.md"), []byte(memory.EncodeSession(doc)), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := RescueFile(FileRescueRequest{
		MemoryDir: root, ID: "ovfix01", Explicit: true,
		ContextLimit: 2_000_000, MaxOutput: 32768, ContextReserve: 8192,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := 655440.0 / 600000.0
	if res.Scale < want-0.0001 || res.Scale > want+0.0001 {
		t.Fatalf("scale %v want %v", res.Scale, want)
	}
}

func TestRescueFilePrefersCatalogOverFlags(t *testing.T) {
	root := writeFixtureMemory(t, 1_000_000)
	res, err := RescueFile(FileRescueRequest{
		MemoryDir: root, ID: "ovfix01", Explicit: true,
		ContextLimit: 10, MaxOutput: 1, ContextReserve: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ContextLimit != 1_000_000 {
		t.Fatalf("catalog lost to flags: %+v", res)
	}
}

func decodeFixture(t *testing.T) *memory.SessionDoc {
	t.Helper()
	doc, err := memory.DecodeSession(mustRead(t, "testdata/overage_fixture.md"))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// limitThatDropsOldRound is a window whose scaled history fits the newest
// round and not both, with cap_tools off.
func limitThatDropsOldRound(t *testing.T, doc *memory.SessionDoc) int {
	t.Helper()
	scale := rescueDefaultScale
	one := projectionScaled(doc.Messages[1:], scale)
	two := projectionScaled(doc.Messages, scale)
	overhead := rescueOverheadParts(doc.Kind, "", false, nil, doc.Messages)
	reserve := rescueMarkerReserve()
	fit := one + (two-one)/2
	room := fit + reserve
	target := room + overhead
	limit := target + retryMinOut
	for i := 0; i < 6; i++ {
		limit = target + rescueMargin(limit) + retryMinOut
	}
	margin := rescueMargin(limit)
	plan, err := PlanRescue(doc.Messages, overhead, limit, margin, retryMinOut, scale)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "rescued" || plan.OmittedMessages < 1 {
		t.Fatalf("limit %d plan %+v", limit, plan)
	}
	return limit
}

func writeFixtureMemory(t *testing.T, catalogLimit int) string {
	t.Helper()
	root := t.TempDir()
	raw, err := os.ReadFile("testdata/overage_fixture.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "session"), 0o755); err != nil {
		t.Fatal(err)
	}
	if catalogLimit <= 0 {
		if err := os.WriteFile(filepath.Join(root, "session", "ovfix01.md"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}
	doc, err := memory.DecodeSession(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	doc.ModelID = "fixture"
	if err := os.WriteFile(filepath.Join(root, "session", "ovfix01.md"), []byte(memory.EncodeSession(doc)), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "marble.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE model_catalog (
		id TEXT PRIMARY KEY,
		context_limit INTEGER NOT NULL,
		cap_tools INTEGER NOT NULL,
		enabled INTEGER NOT NULL,
		kind TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO model_catalog (id, context_limit, cap_tools, enabled, kind) VALUES ('fixture', ?, 0, 1, 'chat')`, catalogLimit)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
