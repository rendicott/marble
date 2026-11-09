package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// ADR-0040 D1 — resumable tool-result truncation.
//
// A result that overflows --max-tool-result is no longer amputated at a byte
// offset the model cannot see. The full body spills to a workspace file and the
// head is followed by a marker naming where the cut landed, so the model resumes
// with file_read instead of re-reading from line 1. Results that fit the cap are
// returned byte-for-byte unchanged: the prompt prefix, and therefore the
// provider's cache key, must not move.
const (
	// SpillDirName is the workspace-relative spill directory. Inside the jail, so
	// file_read can reach it with no new permission rule.
	SpillDirName = ".tool-results"

	spillMaxFiles = 50
	spillMaxBytes = 100 << 20
	spillMaxAge   = 24 * time.Hour
	// spillLineSlack is the most head budget we will surrender to land on a line
	// boundary. A single 10 MiB line must not shrink the head to nothing.
	spillLineSlack = 4096
)

var spillSeq atomic.Uint64

// clampResult applies the result cap. Sub-cap results pass through untouched.
func (r *Registry) clampResult(tool, s string) string {
	max := r.MaxResultChars
	if max <= 0 {
		max = 32000
	}
	if len(s) <= max {
		return s
	}
	cut, lineAligned := safeCut(s, max)
	rel, err := r.spillBody(tool, s)

	var b strings.Builder
	b.WriteString(s[:cut])
	b.WriteString("\n\n…[truncated; see spill]")
	if err != nil {
		// Spilling is best-effort: a read-only workspace must not swallow the head.
		b.WriteString("\nspilled_path: (unavailable)")
		b.WriteString("\nspill_error: " + err.Error())
	} else {
		b.WriteString("\nspilled_path: " + rel)
	}
	fmt.Fprintf(&b, "\nspilled_bytes: %d", len(s))
	fmt.Fprintf(&b, "\nnext_offset: %d", cut)
	fmt.Fprintf(&b, "\nnext_line: %d", 1+strings.Count(s[:cut], "\n"))
	fmt.Fprintf(&b, "\ntotal_bytes: %d", len(s))
	if !lineAligned {
		b.WriteString("\nresume_mid_line: true")
	}
	if err == nil {
		fmt.Fprintf(&b, "\nresume: file_read(path=%q, byte_offset=%d) continues from the cut", rel, cut)
	}
	return b.String()
}

// safeCut returns the byte offset to cut at: never inside a rune, and on a line
// boundary when one is within spillLineSlack bytes.
func safeCut(s string, max int) (int, bool) {
	if max >= len(s) {
		return len(s), true
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if i := strings.IndexByte(s[cut:], '\n'); i >= 0 && i <= spillLineSlack {
		return cut + i + 1, true
	}
	return cut, false // one enormous line: resume mid-line, but never mid-rune
}

func (r *Registry) spillBody(tool, body string) (string, error) {
	dir := filepath.Join(r.Workspace, SpillDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s-%04d.txt",
		time.Now().UTC().Format("20060102T150405.000000000"), spillSlug(tool), spillSeq.Add(1))
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		return "", err
	}
	sweepSpills(r.Workspace, name) // never evict the spill we just handed out
	return SpillDirName + "/" + name, nil
}

// spillSlug keeps a tool name safe for a filename.
func spillSlug(tool string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(tool) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 32 {
			break
		}
	}
	if b.Len() == 0 {
		return "tool"
	}
	return b.String()
}

// SweepSpills enforces the retention limits (24h, 50 files, 100 MiB).
func SweepSpills(workspace string) { sweepSpills(workspace, "") }

func sweepSpills(workspace, keep string) {
	dir := filepath.Join(workspace, SpillDirName)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type spill struct {
		name string
		size int64
		mod  time.Time
	}
	var files []spill
	var total int64
	now := time.Now()
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > spillMaxAge {
			_ = os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		files = append(files, spill{e.Name(), info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for len(files) > spillMaxFiles || total > spillMaxBytes {
		if len(files) == 0 {
			return
		}
		oldest := files[0]
		if oldest.name == keep {
			if len(files) == 1 {
				return
			}
			files = files[1:]
			continue
		}
		if err := os.Remove(filepath.Join(dir, oldest.name)); err == nil {
			total -= oldest.size
		}
		files = files[1:]
	}
}
