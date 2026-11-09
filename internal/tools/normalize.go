package tools

import (
	"fmt"
	"strings"
)

// ADR-0040 D2 — edit normalization, on miss only.
//
// A byte-exact hit stays byte-exact: nothing about the file is rewritten, and the
// match is found the way it always was. Only when the exact search fails do we
// look again through typography — curly quotes, dashes, nbsp, ellipsis — and then
// through trimmed lines. Every fallback reports the tier that fired, and an
// ambiguous match is an error, never a guess.

type matchTier string

const (
	tierExact   matchTier = "exact"
	tierGlyph   matchTier = "glyph"
	tierTrimmed matchTier = "trimmed_lines"
)

// glyphReplacer maps common typographic variants onto the ASCII the model types.
var glyphReplacer = strings.NewReplacer(
	"\u2018", "'", "\u2019", "'",
	"\u201c", "\"", "\u201d", "\"",
	"\u2013", "-", "\u2014", "-",
	"\u00a0", " ", "\u2009", " ", "\u202f", " ",
	"\u2026", "...",
)

func normalizeGlyphs(s string) string { return glyphReplacer.Replace(s) }

// normMap returns the glyph-normalized text plus an owner map: owner[i] is the
// byte offset in the original string of the rune that produced normalized byte i
// (with a final sentinel entry so spans can end at the last rune).
func normMap(s string) (string, []int) {
	var b strings.Builder
	b.Grow(len(s))
	owner := make([]int, 0, len(s)+1)
	for i, r := range s {
		rep := glyphReplacer.Replace(string(r))
		b.WriteString(rep)
		for j := 0; j < len(rep); j++ {
			owner = append(owner, i)
		}
	}
	owner = append(owner, len(s))
	return b.String(), owner
}

// mapSpan converts a normalized [sn,en) byte span back to original offsets.
func mapSpan(owner []int, sn, en int) (int, int) {
	start := owner[sn]
	// A span that ends inside a multi-byte replacement still ends at that rune.
	for en < len(owner)-1 && owner[en] == owner[en-1] {
		en++
	}
	return start, owner[en]
}

// applyStringEdit applies old→new to text, exact first, then glyph, then trimmed
// lines. It returns the new text, the replacement count, and the tier that hit.
func applyStringEdit(path, text, old, new string, replaceAll bool) (string, int, matchTier, error) {
	if count := strings.Count(text, old); count > 0 {
		if count > 1 && !replaceAll {
			return "", 0, tierExact, fmt.Errorf("old_string matches %d times; set replace_all=true or provide a unique string", count)
		}
		if replaceAll {
			return strings.ReplaceAll(text, old, new), count, tierExact, nil
		}
		return strings.Replace(text, old, new, 1), 1, tierExact, nil
	}

	// Tier 2: same text, different typography.
	normText, owner := normMap(text)
	normOld, _ := normMap(old)
	if spans := allSpans(normText, normOld); len(spans) > 0 {
		if len(spans) > 1 && !replaceAll {
			return "", 0, tierGlyph, ambiguousErr(path, text, spans, "glyph", owner)
		}
		if replaceAll {
			var b strings.Builder
			last := 0
			for _, sp := range spans {
				s, e := mapSpan(owner, sp[0], sp[1])
				b.WriteString(text[last:s])
				b.WriteString(new)
				last = e
			}
			b.WriteString(text[last:])
			return b.String(), len(spans), tierGlyph, nil
		}
		s, e := mapSpan(owner, spans[0][0], spans[0][1])
		return text[:s] + new + text[e:], 1, tierGlyph, nil
	}

	// Tier 3: line-for-line, ignoring leading/trailing whitespace per line.
	start, end, matches, err := trimmedLineSpans(text, old)
	if err != nil {
		return "", 0, tierTrimmed, err
	}
	if matches == 0 {
		return "", 0, tierTrimmed, fmt.Errorf("old_string not found in %s (exact, glyph, and trimmed-line matching all failed)", path)
	}
	if matches > 1 && !replaceAll {
		return "", 0, tierTrimmed, fmt.Errorf("old_string matches %d line ranges after trimmed-line normalization in %s; set replace_all=true or add context. First at line %d",
			matches, path, 1+strings.Count(text[:start], "\n"))
	}
	if matches > 1 {
		return "", 0, tierTrimmed, fmt.Errorf("replace_all with a trimmed-line match is ambiguous in %s (%d ranges); provide a byte-exact old_string", path, matches)
	}
	replacement := matchRegionEndings(new, text[start:end])
	return text[:start] + replacement + text[end:], 1, tierTrimmed, nil
}

// allSpans returns every non-overlapping occurrence of needle in hay.
func allSpans(hay, needle string) [][2]int {
	if needle == "" {
		return nil
	}
	var out [][2]int
	for from := 0; from <= len(hay)-len(needle); {
		i := strings.Index(hay[from:], needle)
		if i < 0 {
			break
		}
		out = append(out, [2]int{from + i, from + i + len(needle)})
		from += i + len(needle)
	}
	return out
}

// ambiguousErr names every candidate by line and excerpt. Guessing here would
// edit the wrong place silently, so we refuse. Spans are in normalized
// coordinates, so they must be mapped back before touching the real text.
func ambiguousErr(path, text string, spans [][2]int, tier string, owner []int) error {
	var b strings.Builder
	fmt.Fprintf(&b, "old_string matched %d places after %s normalization in %s; refusing to guess. Set replace_all=true or provide more context. Candidates:", len(spans), tier, path)
	for _, sp := range spans {
		s, e := mapSpan(owner, sp[0], sp[1])
		line := 1 + strings.Count(text[:s], "\n")
		excerpt := text[s:e]
		if r := []rune(excerpt); len(r) > 40 {
			excerpt = string(r[:40]) + "…"
		}
		excerpt = strings.ReplaceAll(excerpt, "\n", "⏎")
		fmt.Fprintf(&b, "\n  line %d: %q", line, excerpt)
	}
	return fmt.Errorf("%s", b.String())
}

// trimmedLineSpans finds whole-line ranges whose trimmed, glyph-normalized text
// equals the same treatment of old. Returns the byte span of the first range.
func trimmedLineSpans(text, old string) (start, end, matches int, err error) {
	want := trimmedLines(old)
	if len(want) == 0 {
		return 0, 0, 0, nil
	}
	lines := splitLineSpans(text)
	if len(lines) < len(want) {
		return 0, 0, 0, nil
	}
	var hits [][2]int
	for i := 0; i+len(want) <= len(lines); i++ {
		ok := true
		for k := range want {
			if lines[i+k].text != want[k] {
				ok = false
				break
			}
		}
		if ok {
			hits = append(hits, [2]int{i, i + len(want) - 1})
		}
	}
	if len(hits) == 0 {
		return 0, 0, 0, nil
	}
	first, last := hits[0][0], hits[0][1]
	start = lines[first].start
	end = lines[last].end
	// If the model's old_string carried the newline, take the file's too.
	if strings.HasSuffix(old, "\n") && end < len(text) && text[end] == '\n' {
		end++
	}
	return start, end, len(hits), nil
}

type lineSpan struct {
	start, end int
	text       string // trimmed + glyph-normalized
}

func splitLineSpans(text string) []lineSpan {
	var out []lineSpan
	for i := 0; i < len(text); {
		nl := len(text)
		if j := strings.IndexByte(text[i:], '\n'); j >= 0 {
			nl = i + j
		}
		// Keep CRLF out of the span: a region ending mid-line-ending would leave
		// the trailing \n behind while its \r was replaced away.
		end := nl
		if end > i && text[end-1] == '\r' {
			end--
		}
		out = append(out, lineSpan{start: i, end: end, text: strings.TrimSpace(normalizeGlyphs(text[i:end]))})
		if nl >= len(text) {
			break
		}
		i = nl + 1
	}
	return out
}

func trimmedLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	parts := strings.Split(s, "\n")
	for i := range parts {
		parts[i] = strings.TrimSpace(normalizeGlyphs(parts[i]))
	}
	return parts
}

// matchRegionEndings keeps the line endings the target region already used: a
// CRLF file stays CRLF where the edit lands, a mixed file keeps its mix.
func matchRegionEndings(replacement, region string) string {
	if !strings.Contains(region, "\r\n") {
		return replacement
	}
	return strings.ReplaceAll(strings.ReplaceAll(replacement, "\r\n", "\n"), "\n", "\r\n")
}
