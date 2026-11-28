package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestFTS5Available fails loudly if the SQLite driver was built without FTS5,
// bm25(), snippet() or phrase MATCH. memory_search depends on all four.
func TestFTS5Available(t *testing.T) {
	s, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Exec(`CREATE VIRTUAL TABLE ft USING fts5(path UNINDEXED, body)`); err != nil {
		t.Fatalf("FTS5 unavailable in modernc.org/sqlite: %v", err)
	}
	for _, row := range [][2]string{
		{"a.md", "azure trusted signing for the windows build"},
		{"b.md", "windows windows windows update"},
	} {
		if _, err := s.Exec(`INSERT INTO ft(path, body) VALUES (?, ?)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	var path, snip string
	var score float64
	err = s.QueryRow(`SELECT path, bm25(ft, 0.0, 1.0) AS score, snippet(ft, 1, '[', ']', '…', 8)
		FROM ft WHERE ft MATCH ? ORDER BY score ASC LIMIT 1`, `"trusted signing"`).Scan(&path, &score, &snip)
	if err != nil {
		t.Fatalf("bm25/snippet/phrase MATCH: %v", err)
	}
	if path != "a.md" || score >= 0 || snip == "" {
		t.Fatalf("path=%s score=%v snippet=%q (bm25 must be negative)", path, score, snip)
	}
}

func TestOpenMemIndexCreatesSchema(t *testing.T) {
	root := t.TempDir()
	s, err := OpenMemIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	ver, err := memIndexVersion(s)
	if err != nil || ver != MemIndexSchemaVersion {
		t.Fatalf("version %d err %v", ver, err)
	}
	var mode string
	if err := s.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode %q %v", mode, err)
	}
	for _, name := range []string{"mem_doc", "mem_fts", "mem_doc_ai", "mem_doc_ad", "mem_doc_au", "mem_doc_kind"} {
		var n int
		if err := s.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s: %d %v", name, n, err)
		}
	}
	s.Close()
	if _, err := os.Stat(filepath.Join(root, MemIndexFile)); err != nil {
		t.Fatal(err)
	}
	// Reopen is idempotent.
	s2, err := OpenMemIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

func TestOpenMemIndexRefusesNewerSchema(t *testing.T) {
	root := t.TempDir()
	s, err := OpenMemIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(`UPDATE mem_index_meta SET schema_version = 99`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := OpenMemIndex(root); err == nil {
		t.Fatal("expected newer-schema error")
	}
}

func TestMemIndexTriggersKeepFTSInStep(t *testing.T) {
	s, err := OpenMemIndex(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	count := func(expr string) int {
		var n int
		if err := s.QueryRow(`SELECT COUNT(*) FROM mem_fts WHERE mem_fts MATCH ?`, expr).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if _, err := s.Exec(`INSERT INTO mem_doc(path, kind, body) VALUES ('knowledge/a.md', 'knowledge', 'alpha')`); err != nil {
		t.Fatal(err)
	}
	if count("alpha") != 1 {
		t.Fatal("insert not indexed")
	}
	if _, err := s.Exec(`UPDATE mem_doc SET body = 'beta' WHERE path = 'knowledge/a.md'`); err != nil {
		t.Fatal(err)
	}
	if count("alpha") != 0 || count("beta") != 1 {
		t.Fatal("update not re-indexed")
	}
	if _, err := s.Exec(`DELETE FROM mem_doc`); err != nil {
		t.Fatal(err)
	}
	if count("beta") != 0 {
		t.Fatal("delete not removed from index")
	}
	if _, err := s.Exec(`INSERT INTO mem_fts(mem_fts) VALUES('rebuild')`); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
}
