package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func spillTestRegistry(t *testing.T, maxChars int) *Registry {
	t.Helper()
	return &Registry{Workspace: t.TempDir(), MaxResultChars: maxChars}
}

// markerValue pulls one "key: value" line out of a spill marker.
func markerValue(t *testing.T, out, key string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, key+": ") {
			return strings.TrimPrefix(line, key+": ")
		}
	}
	t.Fatalf("marker key %q missing:\n%s", key, out)
	return ""
}

func markerHead(t *testing.T, out string) string {
	t.Helper()
	i := strings.Index(out, "\n\n…[truncated; see spill]")
	if i < 0 {
		t.Fatalf("no marker block:\n%s", out)
	}
	return out[:i]
}

// Sub-cap results must be byte-identical: any change moves the prompt prefix and
// breaks the provider cache (ADR-0040 D1, cache safety).
func TestClampResultSubCapUnchanged(t *testing.T) {
	r := spillTestRegistry(t, 100)
	in := "short result\nwith two lines"
	if got := r.clampResult("shell_execute", in); got != in {
		t.Fatalf("sub-cap result was rewritten:\n%q", got)
	}
	if _, err := os.Stat(filepath.Join(r.Workspace, SpillDirName)); !os.IsNotExist(err) {
		t.Fatal("no spill directory should be created when nothing spills")
	}
}

func TestClampResultSpillsWithResumeMarker(t *testing.T) {
	r := spillTestRegistry(t, 200)
	body := strings.Repeat("alpha bravo charlie delta\n", 200) // ~5.2 KB, many lines
	got := r.clampResult("shell_execute", body)

	if len(got) >= len(body) {
		t.Fatalf("clamped result not shorter than body: %d vs %d", len(got), len(body))
	}
	for _, want := range []string{
		"…[truncated; see spill]", "spilled_path: " + SpillDirName + "/",
		"spilled_bytes: ", "next_offset: ", "next_line: ", "total_bytes: ",
		"byte_offset=",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("marker missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "resume_mid_line") {
		t.Fatal("multi-line body should resume on a line boundary")
	}

	head := markerHead(t, got)
	if !utf8.ValidString(head) || !strings.HasSuffix(head, "\n") {
		t.Fatalf("head is not a clean line: %q", head[len(head)-40:])
	}
	if !strings.HasPrefix(body, head) {
		t.Fatal("head is not a prefix of the body")
	}

	// The spill holds the full body, so earlier context is re-readable too.
	rel := markerValue(t, got, "spilled_path")
	data, err := os.ReadFile(filepath.Join(r.Workspace, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("spill unreadable: %v", err)
	}
	if string(data) != body {
		t.Fatalf("spill body mismatch: %d vs %d bytes", len(data), len(body))
	}

	// Resume through file_read's byte_offset and stitch the halves back together.
	// Call the tool itself: through Execute the remainder would be clamped at this
	// test's tiny cap and spill again (covered by the chain check below).
	next := markerValue(t, got, "next_offset")
	res, err := r.fileRead(`{"path":"`+rel+`","byte_offset":`+next+`}`, nil)
	if err != nil {
		t.Fatalf("resume read failed: %v", err)
	}
	rest := strings.TrimPrefix(res, "path="+rel+" byte_offset="+next+"\n")
	if head+rest != body {
		t.Fatalf("resume did not reconstruct the body (%d + %d vs %d)", len(head), len(rest), len(body))
	}

	// Reading a spill that still overflows must chain, not loop: a fresh marker
	// points deeper into the same body, and never over the file being read.
	again := r.Execute("file_read", `{"path":"`+rel+`","byte_offset":`+next+`}`, nil)
	if !strings.Contains(again, "…[truncated; see spill]") {
		t.Fatalf("oversize spill read did not spill again:\n%s", again)
	}
	if deeper := markerValue(t, again, "spilled_path"); deeper == rel {
		t.Fatal("chain wrote over the spill it was reading")
	}
}

func TestClampResultNeverSplitsRune(t *testing.T) {
	r := spillTestRegistry(t, 101) // odd cap against 2-byte runes
	body := strings.Repeat("é", 400)
	got := r.clampResult("web_fetch", body)
	if head := markerHead(t, got); !utf8.ValidString(head) {
		t.Fatalf("head is not valid UTF-8: %q", head)
	}
	cut, _ := safeCut(body, 101)
	if cut%2 != 0 {
		t.Fatalf("cut %d lands mid-rune for 2-byte runes", cut)
	}
}

// One enormous line cannot be line-aligned; the marker must say so rather than lie.
func TestClampResultSingleLongLineIsMidLine(t *testing.T) {
	r := spillTestRegistry(t, 100)
	got := r.clampResult("shell_execute", strings.Repeat("x", 500))
	if !strings.Contains(got, "resume_mid_line: true") {
		t.Fatalf("expected resume_mid_line note:\n%s", got)
	}
	if markerValue(t, got, "next_offset") != "100" {
		t.Fatalf("expected cut at the cap:\n%s", got)
	}
}

func TestSafeCutRuneBoundary(t *testing.T) {
	if cut, _ := safeCut("aé", 2); cut != 1 {
		t.Fatalf("cut %d splits the rune", cut)
	}
	if cut, aligned := safeCut("abc\ndef\nghi", 4); cut != 8 || !aligned {
		t.Fatalf("cut %d aligned=%v, want 8/true", cut, aligned)
	}
	// No newline within reach: stay rune-safe and admit the cut is mid-line.
	if cut, aligned := safeCut("abc\ndef", 4); cut != 4 || aligned {
		t.Fatalf("cut %d aligned=%v, want 4/false", cut, aligned)
	}
}

func TestSweepSpillsAgeAndCount(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, SpillDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "stale.txt")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-(spillMaxAge + time.Hour))
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < spillMaxFiles+5; i++ {
		name := filepath.Join(dir, "fresh-"+time.Duration(i).String()+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	SweepSpills(ws)
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) > spillMaxFiles {
		t.Fatalf("count cap not enforced: %d files", len(ents))
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("expired spill survived the sweep")
	}
}

// The spill just handed to the model must survive its own sweep.
func TestSweepSpillsKeepsCurrent(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, SpillDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "current.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	sweepSpills(ws, "current.txt")
	if _, err := os.Stat(filepath.Join(dir, "current.txt")); err != nil {
		t.Fatalf("current spill was evicted: %v", err)
	}
}
