package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The reviewer's suspected hang: if the file we must keep is also the oldest,
// the loop drops it from the candidate slice without deleting it and must still
// terminate, evicting the next-oldest instead.
func TestSweepSpillsTerminatesWhenKeepIsOldest(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, SpillDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// keep is the oldest, every other file is newer.
	keep := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(keep, []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(keep, old, old)
	for i := 0; i < spillMaxFiles+10; i++ {
		p := filepath.Join(dir, "f"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+string(rune('a'+i/26))+".txt")
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() { sweepSpills(ws, "keep.txt"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sweepSpills did not terminate with keep as the oldest file")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("keep file was evicted: %v", err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) > spillMaxFiles {
		t.Fatalf("cap not enforced: %d files", len(ents))
	}
}

// Insert-only hunk at the start of a file: the old side is empty, so its start
// must be oldBefore (0), not oldBefore+1.
func TestUnifiedDiffInsertOnlyHeader(t *testing.T) {
	d := unifiedDiff("x.txt", "", "a\nb\n")
	if !strings.Contains(d, "@@ -0,0 +1,2 @@") {
		t.Fatalf("insert-only header wrong:\n%s", d)
	}
}

// Delete-only hunk: the new side is empty, so the same rule applies to it.
func TestUnifiedDiffDeleteOnlyHeader(t *testing.T) {
	d := unifiedDiff("x.txt", "a\nb\n", "")
	if !strings.Contains(d, "@@ -1,2 +0,0 @@") {
		t.Fatalf("delete-only header wrong:\n%s", d)
	}
}

// A CRLF region edited with a single-line replacement (no newline supplied)
// must not be mangled.
func TestMatchRegionEndingsSingleLine(t *testing.T) {
	if got := matchRegionEndings("one line", "a\r\nb"); got != "one line" {
		t.Fatalf("single-line replacement altered: %q", got)
	}
	if got := matchRegionEndings("x\ny", "a\r\nb"); got != "x\r\ny" {
		t.Fatalf("multi-line replacement not CRLF-ified: %q", got)
	}
	if got := matchRegionEndings("x\ny", "a\nb"); got != "x\ny" {
		t.Fatalf("LF region altered: %q", got)
	}
}
