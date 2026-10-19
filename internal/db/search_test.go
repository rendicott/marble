package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/search"
)

func openSearchDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if got := schemaVersion(t, d); got != CurrentSchemaVersion {
		t.Fatalf("schema %d want %d", got, CurrentSchemaVersion)
	}
	return d
}

func TestMigrateV13toV14SearchTables(t *testing.T) {
	root := t.TempDir()
	d, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL.Exec(`DROP TABLE session_search_terms`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL.Exec(`DROP TABLE session_search`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL.Exec(`UPDATE schema_meta SET schema_version = 13 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if got := schemaVersion(t, d2); got != 14 {
		t.Fatalf("schema %d", got)
	}
	var n int
	if err := d2.SQL.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'session_search'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("session_search missing: %v n=%d", err, n)
	}
	if err := d2.SQL.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'idx_session_search_terms_term'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("term index missing: %v n=%d", err, n)
	}
}

// TestSearchAndroidAlias: the query "android" matches a session whose indexed
// text only contains "apk" and "gradle".
func TestSearchAndroidAlias(t *testing.T) {
	d := openSearchDB(t)
	terms := search.Terms([]search.MessageText{
		{Role: "user", Content: "the release build used apk and gradle today"},
	})
	if _, ok := terms["android"]; ok {
		t.Fatalf("index stored the query word: %v", terms)
	}
	if terms["apk"] == 0 || terms["gradle"] == 0 {
		t.Fatalf("indexed terms %v", terms)
	}
	if err := d.ReplaceSessionSearch(SessionSearchRow{
		SessionID:   "build1",
		Title:       "release notes",
		Description: "release notes",
		UpdatedAt:   "2026-10-18T12:00:00Z",
	}, terms, false); err != nil {
		t.Fatal(err)
	}
	// A session that only says "android" once, older, should rank below.
	onlyWord := search.Terms([]search.MessageText{{Role: "assistant", Content: "android"}})
	if err := d.ReplaceSessionSearch(SessionSearchRow{
		SessionID:   "saidit",
		Title:       "said android",
		Description: "said android",
		UpdatedAt:   "2026-10-01T12:00:00Z",
	}, onlyWord, false); err != nil {
		t.Fatal(err)
	}
	// ios words must not match android.
	ios := search.Terms([]search.MessageText{{Role: "user", Content: "xcode swift ipa"}})
	if err := d.ReplaceSessionSearch(SessionSearchRow{
		SessionID:   "phone",
		Title:       "ios",
		Description: "ios",
		UpdatedAt:   "2026-10-18T18:00:00Z",
	}, ios, false); err != nil {
		t.Fatal(err)
	}

	hits, err := d.SearchSessions(search.ExpandQuery("android"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits %+v", hits)
	}
	byID := map[string]SearchHit{}
	for _, h := range hits {
		byID[h.ID] = h
	}
	if _, ok := byID["build1"]; !ok {
		t.Fatalf("apk/gradle session missing: %+v", hits)
	}
	if _, ok := byID["phone"]; ok {
		t.Fatalf("ios session matched android: %+v", hits)
	}
	if byID["build1"].Score <= byID["saidit"].Score {
		t.Fatalf("match strength: build1=%d saidit=%d", byID["build1"].Score, byID["saidit"].Score)
	}

	// Bidirectional: "apk" also reaches a session that only says "android".
	back, err := d.SearchSessions(search.ExpandQuery("apk"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range back {
		if h.ID == "saidit" {
			found = true
		}
	}
	if !found {
		t.Fatalf("apk did not match android session: %+v", back)
	}
}

// TestSearchScrubSecret: a secret passed through ScrubSecrets does not appear
// in session_search_terms.
func TestSearchScrubSecret(t *testing.T) {
	const secret = "TOKENVALUE99"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte("TEST_API_KEY="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config.SetMemoryDirForEnv(dir)
	config.InvalidateEnvOverlay()
	t.Cleanup(func() {
		config.SetMemoryDirForEnv("")
		config.InvalidateEnvOverlay()
	})

	d := openSearchDB(t)
	terms := search.Terms([]search.MessageText{
		{Role: "tool", Content: "build apk gradle " + secret + " done"},
	})
	if err := d.ReplaceSessionSearch(SessionSearchRow{
		SessionID:   "tool1",
		Title:       "tool",
		Description: "tool",
		UpdatedAt:   "2026-10-18T12:00:00Z",
	}, terms, false); err != nil {
		t.Fatal(err)
	}
	got, err := d.SessionSearchTermSet("tool1")
	if err != nil {
		t.Fatal(err)
	}
	for term := range got {
		if strings.Contains(term, strings.ToLower(secret)) || strings.Contains(term, secret) {
			t.Fatalf("secret leaked into term %q (all %v)", term, got)
		}
	}
	if got["apk"] == 0 || got["gradle"] == 0 {
		t.Fatalf("tool terms dropped real words: %v", got)
	}
}

// TestSearchSkipsThinking: thinking content is not indexed.
func TestSearchSkipsThinking(t *testing.T) {
	d := openSearchDB(t)
	terms := search.Terms([]search.MessageText{
		{Role: "user", Content: "ship the apk"},
		{Role: "thinking", Content: "zephyrthought uniquechain"},
		{Role: "harness", Content: "harnessonlytoken stays out"},
		{Role: "system", Content: "systemonlytoken stays out"},
		{Role: "assistant", Content: "shipped"},
	})
	if err := d.ReplaceSessionSearch(SessionSearchRow{
		SessionID:   "think1",
		Title:       "t",
		Description: "t",
		UpdatedAt:   "2026-10-18T12:00:00Z",
	}, terms, false); err != nil {
		t.Fatal(err)
	}
	got, err := d.SessionSearchTermSet("think1")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"zephyrthought", "uniquechain", "harnessonlytoken", "systemonlytoken"} {
		if got[banned] != 0 {
			t.Fatalf("%s was indexed: %v", banned, got)
		}
	}
	if got["apk"] == 0 || got["shipped"] == 0 {
		t.Fatalf("kept text missing: %v", got)
	}
}
