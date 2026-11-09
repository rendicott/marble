package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func editFixture(t *testing.T, name, body string) (*Registry, *TurnContext, string) {
	t.Helper()
	ws := t.TempDir()
	abs := filepath.Join(ws, name)
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Registry{Workspace: ws, MaxResultChars: 32000}
	// Stand in for a prior file_read on this path.
	tc := &TurnContext{ReadPaths: map[string]bool{name: true}}
	return r, tc, abs
}

func editArgs(path, old, new string, replaceAll bool) string {
	return fmt.Sprintf(`{"path":%q,"old_string":%q,"new_string":%q,"replace_all":%t}`,
		path, old, new, replaceAll)
}

func mustRead(t *testing.T, abs string) string {
	t.Helper()
	b, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A byte-exact hit stays exact, and now says what it did.
func TestEditExactReportsDiff(t *testing.T) {
	r, tc, abs := editFixture(t, "a.txt", "line1\nline2\nline3\n")
	out := r.Execute("edit_file", editArgs("a.txt", "line2", "LINE2", false), tc)
	if !strings.Contains(out, "edited a.txt (1 replacement(s))") {
		t.Fatalf("missing edit summary:\n%s", out)
	}
	if strings.Contains(out, "normalization") {
		t.Fatalf("exact hit should not claim normalization:\n%s", out)
	}
	for _, want := range []string{"--- a.txt", "+++ a.txt", "-line2", "+LINE2", "@@"} {
		if !strings.Contains(out, want) {
			t.Fatalf("diff missing %q:\n%s", want, out)
		}
	}
	if got := mustRead(t, abs); got != "line1\nLINE2\nline3\n" {
		t.Fatalf("file content wrong: %q", got)
	}
}

// The model types ASCII quotes; the file holds curly ones.
func TestEditGlyphNormalization(t *testing.T) {
	r, tc, abs := editFixture(t, "b.txt", "const s = \u201chello\u201d\n")
	out := r.Execute("edit_file", editArgs("b.txt", `const s = "hello"`, `const s = "bye"`, false), tc)
	if !strings.Contains(out, "matched after glyph normalization") {
		t.Fatalf("expected glyph-tier notice:\n%s", out)
	}
	if got := mustRead(t, abs); got != "const s = \"bye\"\n" {
		t.Fatalf("file content wrong: %q", got)
	}
}

// Glyph substitution changes byte lengths, so the span map must be right:
// the ellipsis case proves offsets are not naively reused.
func TestEditGlyphSpanMapping(t *testing.T) {
	r, tc, abs := editFixture(t, "c.txt", "head a\u2026b tail\nsecond a\u2026b tail\n")
	out := r.Execute("edit_file", editArgs("c.txt", "a...b", "aXXXb", true), tc)
	if !strings.Contains(out, "2 replacement(s)") {
		t.Fatalf("expected two replacements:\n%s", out)
	}
	if got := mustRead(t, abs); got != "head aXXXb tail\nsecond aXXXb tail\n" {
		t.Fatalf("span mapping corrupted the file: %q", got)
	}
}

// Two candidates after normalization is an error, naming both lines.
func TestEditAmbiguousAfterNormalization(t *testing.T) {
	r, tc, abs := editFixture(t, "d.txt", "\u201cx\u201d\n\u201cx\u201d\n")
	out := r.Execute("edit_file", editArgs("d.txt", `"x"`, `"y"`, false), tc)
	if !strings.Contains(out, "error:") || !strings.Contains(out, "refusing to guess") {
		t.Fatalf("expected refusal:\n%s", out)
	}
	for _, want := range []string{"line 1", "line 2", "replace_all=true"} {
		if !strings.Contains(out, want) {
			t.Fatalf("ambiguity report missing %q:\n%s", want, out)
		}
	}
	if got := mustRead(t, abs); got != "\u201cx\u201d\n\u201cx\u201d\n" {
		t.Fatalf("ambiguous edit must not touch the file: %q", got)
	}
}

// Indentation drift falls through to trimmed-line matching.
func TestEditTrimmedLineFallback(t *testing.T) {
	r, tc, abs := editFixture(t, "e.txt", "  foo bar\n  baz qux\n")
	out := r.Execute("edit_file", editArgs("e.txt", "foo bar\nbaz qux", "one\ntwo", false), tc)
	if !strings.Contains(out, "matched after trimmed_lines normalization") {
		t.Fatalf("expected trimmed-tier notice:\n%s", out)
	}
	if got := mustRead(t, abs); got != "one\ntwo\n" {
		t.Fatalf("file content wrong: %q", got)
	}
}

// A CRLF region stays CRLF even though the model typed LF.
func TestEditPreservesRegionLineEndings(t *testing.T) {
	r, tc, abs := editFixture(t, "f.txt", "  foo\r\n  bar\r\n")
	out := r.Execute("edit_file", editArgs("f.txt", "foo\nbar", "x\ny", false), tc)
	if !strings.Contains(out, "trimmed_lines") {
		t.Fatalf("expected trimmed tier:\n%s", out)
	}
	if got := mustRead(t, abs); got != "x\r\ny\r\n" {
		t.Fatalf("CRLF not preserved: %q", got)
	}
}

// Nothing found anywhere is a clear miss, naming every tier tried.
func TestEditMissNamesTiers(t *testing.T) {
	r, tc, abs := editFixture(t, "g.txt", "alpha\n")
	out := r.Execute("edit_file", editArgs("g.txt", "omega", "x", false), tc)
	if !strings.Contains(out, "exact, glyph, and trimmed-line matching all failed") {
		t.Fatalf("expected tier report:\n%s", out)
	}
	if got := mustRead(t, abs); got != "alpha\n" {
		t.Fatalf("failed edit touched the file: %q", got)
	}
}

// The read-before-edit gate still stands in front of normalization.
func TestEditReadGateBeforeNormalization(t *testing.T) {
	ws := t.TempDir()
	abs := filepath.Join(ws, "h.txt")
	if err := os.WriteFile(abs, []byte("\u201cx\u201d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Registry{Workspace: ws, MaxResultChars: 32000}
	tc := &TurnContext{ReadPaths: map[string]bool{}}
	out := r.Execute("edit_file", editArgs("h.txt", `"x"`, `"y"`, false), tc)
	if !strings.Contains(out, "requires a successful file_read") {
		t.Fatalf("gate did not fire first:\n%s", out)
	}
}

func TestUnifiedDiffContextAndHunk(t *testing.T) {
	before := "1\n2\n3\n4\n5\n6\n7\n8\n9\n"
	after := "1\n2\n3\n4\nFIVE\n6\n7\n8\n9\n"
	d := unifiedDiff("x.txt", before, after)
	if !strings.Contains(d, "@@ -2,7 +2,7 @@") {
		t.Fatalf("hunk header wrong:\n%s", d)
	}
	if strings.Contains(d, "-1\n") || strings.Contains(d, "-9\n") {
		t.Fatalf("context leaked beyond 3 lines:\n%s", d)
	}
	if !strings.Contains(d, "--- x.txt (18 bytes)") || !strings.Contains(d, "+++ x.txt (21 bytes)") {
		t.Fatalf("headers missing byte counts:\n%s", d)
	}
}

func TestUnifiedDiffCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 4000; i++ {
		b.WriteString(strings.Repeat("a", 40) + "\n")
	}
	before := b.String()
	after := strings.ReplaceAll(before, "a", "b")
	d := unifiedDiff("big.txt", before, after)
	if len(d) > diffMaxBytes+200 {
		t.Fatalf("diff not capped: %d bytes", len(d))
	}
	if !strings.Contains(d, "…[diff truncated at 4 KiB") {
		t.Fatalf("cap note missing:\n%s", d[maxInt(0, len(d)-120):])
	}
}

// apply_patch normalizes the same way and still rolls back completely.
func TestApplyPatchNormalizesAndRollsBack(t *testing.T) {
	r, tc, abs := editFixture(t, "p.txt", "\u201cold\u201d\n")
	body := fmt.Sprintf(`{"edits":[{"op":"update","path":"p.txt","old_string":%q,"new_string":%q}]}`,
		`"old"`, `"new"`)
	out := r.Execute("apply_patch", body, tc)
	if !strings.Contains(out, "apply_patch ok: 1 edit(s)") || !strings.Contains(out, "glyph normalization") {
		t.Fatalf("patch did not normalize:\n%s", out)
	}
	if got := mustRead(t, abs); got != "\"new\"\n" {
		t.Fatalf("patch content wrong: %q", got)
	}

	// A failing second edit must undo the first.
	body = fmt.Sprintf(`{"edits":[{"op":"update","path":"p.txt","old_string":%q,"new_string":"zzz"},{"op":"update","path":"p.txt","old_string":"nope","new_string":"yyy"}]}`,
		`"new"`)
	out = r.Execute("apply_patch", body, tc)
	if !strings.Contains(out, "error:") {
		t.Fatalf("expected failure:\n%s", out)
	}
	if got := mustRead(t, abs); got != "\"new\"\n" {
		t.Fatalf("rollback failed, file is %q", got)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
