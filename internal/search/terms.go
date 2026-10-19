// Package search is the session-search tokenizer (ADR-0037).
// It stores no index. Callers keep term counts in memory and in SQLite.
package search

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/memory"
)

const (
	// UserTextCap is the maximum user or assistant text indexed from one message.
	UserTextCap = 8 * 1024
	// ToolTextCap is the maximum tool-result text indexed from one message.
	ToolTextCap = 2 * 1024
)

// aliasGroups are equivalence classes applied at query time only.
// A query token that is any member expands to the whole group.
// The table is hand-maintained; growing it is a code change.
var aliasGroups = [][]string{
	{"android", "apk", "gradle", "adb", "aapt"},
	{"ios", "xcode", "swift", "ipa"},
	{"dmg", "hdiutil", "notarize", "codesign"},
}

// aliasOf maps a stemmed token to its full group (including itself).
var aliasOf map[string][]string

func init() {
	aliasOf = make(map[string][]string, 16)
	for _, g := range aliasGroups {
		stemmed := make([]string, 0, len(g))
		seen := map[string]struct{}{}
		for _, w := range g {
			tok := stemToken(strings.ToLower(w))
			if tok == "" {
				continue
			}
			if _, ok := seen[tok]; ok {
				continue
			}
			seen[tok] = struct{}{}
			stemmed = append(stemmed, tok)
		}
		for _, tok := range stemmed {
			aliasOf[tok] = stemmed
		}
	}
}

// Hash is the hex sha256 of transcript bytes, stored as source_hash.
func Hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// MessageText is the only part of a transcript row the index reads.
type MessageText struct {
	Role    string
	Content string
}

// Terms counts indexable tokens across messages.
// Thinking, harness, system, attachment, and any other role contribute nothing.
// Tool text is scrubbed then clipped. User and assistant text are clipped only.
func Terms(msgs []MessageText) map[string]int {
	out := map[string]int{}
	for _, m := range msgs {
		text := IndexText(m.Role, m.Content)
		if text == "" {
			continue
		}
		for tok, n := range Tokens(text) {
			out[tok] += n
		}
	}
	return out
}

// TermsFromTranscript indexes a decoded session file.
func TermsFromTranscript(msgs []memory.TranscriptMessage) map[string]int {
	in := make([]MessageText, len(msgs))
	for i, m := range msgs {
		in[i] = MessageText{Role: m.Role, Content: m.Content}
	}
	return Terms(in)
}

// IndexText returns the text that may be tokenized for this role, or "" when
// the role is excluded. It never reads tool arguments, attachment bytes, or soul.
func IndexText(role, content string) string {
	switch role {
	case "user", "assistant":
		return clipBytes(content, UserTextCap)
	case "tool":
		return clipBytes(config.ScrubSecrets(content), ToolTextCap)
	default:
		// thinking, harness, system, attachment, and anything else.
		return ""
	}
}

// Tokens splits on non-alphanumeric boundaries, lowercases, drops tokens
// shorter than three characters, and strips one trailing "s".
func Tokens(s string) map[string]int {
	counts := map[string]int{}
	var buf []rune
	flush := func() {
		if len(buf) == 0 {
			return
		}
		tok := stemToken(strings.ToLower(string(buf)))
		buf = buf[:0]
		if tok == "" {
			return
		}
		counts[tok]++
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			buf = append(buf, r)
			continue
		}
		flush()
	}
	flush()
	return counts
}

// ExpandQuery tokenizes q and expands each token through the alias table.
// Expansion happens only here, never when indexing.
func ExpandQuery(q string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(tok string) {
		if tok == "" {
			return
		}
		if _, ok := seen[tok]; ok {
			return
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
	}
	var buf []rune
	flush := func() {
		if len(buf) == 0 {
			return
		}
		tok := stemToken(strings.ToLower(string(buf)))
		buf = buf[:0]
		if tok == "" {
			return
		}
		if group, ok := aliasOf[tok]; ok {
			for _, g := range group {
				add(g)
			}
			return
		}
		add(tok)
	}
	for _, r := range q {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			buf = append(buf, r)
			continue
		}
		flush()
	}
	flush()
	return out
}

func stemToken(tok string) string {
	if utf8.RuneCountInString(tok) < 3 {
		return ""
	}
	if strings.HasSuffix(tok, "s") {
		tok = strings.TrimSuffix(tok, "s")
	}
	if tok == "" {
		return ""
	}
	return tok
}

func clipBytes(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	i := n
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}
