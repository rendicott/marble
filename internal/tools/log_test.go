package tools

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"testing"
)

// These pin the ADR-0040 instrument: the tier and spill rates are read by grepping
// the log, so a refactor that drops or multiplies a line must fail here. Each test
// swaps the global logger, which is safe only while this package has no
// t.Parallel() tests.

const tierLogMarker = "matched after"

func TestLogExactEditIsSilent(t *testing.T) {
	r, tc, _ := editFixture(t, "a.txt", "line1\nline2\n")
	old := log.Writer()
	defer log.SetOutput(old)
	var buf bytes.Buffer
	log.SetOutput(&buf)

	out := r.Execute("edit_file", editArgs("a.txt", "line2", "LINE2", false), tc)
	if !strings.Contains(out, "edited a.txt") {
		t.Fatalf("edit failed:\n%s", out)
	}
	if strings.Contains(buf.String(), tierLogMarker) {
		t.Fatalf("exact edit logged a normalization:\n%s", buf.String())
	}
}

func TestLogGlyphEditOneLine(t *testing.T) {
	r, tc, _ := editFixture(t, "b.txt", "const s = \u201chello\u201d\n")
	old := log.Writer()
	defer log.SetOutput(old)
	var buf bytes.Buffer
	log.SetOutput(&buf)

	out := r.Execute("edit_file", editArgs("b.txt", `const s = "hello"`, `const s = "bye"`, false), tc)
	if !strings.Contains(out, "edited b.txt") {
		t.Fatalf("edit failed:\n%s", out)
	}
	if n := strings.Count(buf.String(), "matched after glyph normalization"); n != 1 {
		t.Fatalf("want exactly 1 glyph line, got %d:\n%s", n, buf.String())
	}
}

// Dedupe regression: two glyph hunks on one file are one fact, not two.
func TestLogApplyPatchDedupesSameFile(t *testing.T) {
	r, tc, _ := editFixture(t, "p.txt", "\u201ca\u201d\n\u201cb\u201d\n")
	body := fmt.Sprintf(`{"edits":[{"op":"update","path":"p.txt","old_string":%q,"new_string":"x"},{"op":"update","path":"p.txt","old_string":%q,"new_string":"y"}]}`,
		`"a"`, `"b"`)
	old := log.Writer()
	defer log.SetOutput(old)
	var buf bytes.Buffer
	log.SetOutput(&buf)

	out := r.Execute("apply_patch", body, tc)
	if !strings.Contains(out, "apply_patch ok: 2 edit(s)") {
		t.Fatalf("patch failed:\n%s", out)
	}
	if n := strings.Count(out, "glyph normalization"); n != 2 {
		t.Fatalf("both hunks should have normalized, got %d notes:\n%s", n, out)
	}
	if n := strings.Count(buf.String(), tierLogMarker); n != 1 {
		t.Fatalf("want exactly 1 line for 2 hunks on one file, got %d:\n%s", n, buf.String())
	}
}

// The first hunk normalizes and writes, the second misses: nothing landed, so
// nothing may be counted.
func TestLogApplyPatchRollbackIsSilent(t *testing.T) {
	r, tc, abs := editFixture(t, "p.txt", "\u201cold\u201d\n")
	body := fmt.Sprintf(`{"edits":[{"op":"update","path":"p.txt","old_string":%q,"new_string":"zzz"},{"op":"update","path":"p.txt","old_string":"nope","new_string":"yyy"}]}`,
		`"old"`)
	old := log.Writer()
	defer log.SetOutput(old)
	var buf bytes.Buffer
	log.SetOutput(&buf)

	out := r.Execute("apply_patch", body, tc)
	if !strings.Contains(out, "error:") {
		t.Fatalf("expected failure:\n%s", out)
	}
	if got := mustRead(t, abs); got != "\u201cold\u201d\n" {
		t.Fatalf("rollback failed, file is %q", got)
	}
	if strings.Contains(buf.String(), tierLogMarker) {
		t.Fatalf("rolled-back patch logged a normalization:\n%s", buf.String())
	}
}

func TestLogSpillOneLine(t *testing.T) {
	r := spillTestRegistry(t, 100)
	old := log.Writer()
	defer log.SetOutput(old)
	var buf bytes.Buffer
	log.SetOutput(&buf)

	out := r.clampResult("shell_execute", strings.Repeat("x", 500))
	if !strings.Contains(out, "…[truncated; see spill]") {
		t.Fatalf("expected a spill:\n%s", out)
	}
	if n := strings.Count(buf.String(), "tools: spill"); n != 1 {
		t.Fatalf("want exactly 1 spill line, got %d:\n%s", n, buf.String())
	}
}
