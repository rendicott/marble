package agentproc

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	DefaultContextMaxChars = 16000
	CapCompact             = 6000
	CapFullRaw             = 12000
	CapFullOlder           = 6000
	CapMemory              = 4000
	CapReadPaths           = 1500
	MemoryTopK             = 5
)

// ContextSpec is a resolved source-list + cap (ADR-0031).
type ContextSpec struct {
	Sources  []string
	MaxChars int
	// Set is true when this layer explicitly provided a value (including empty = none).
	Set bool
}

// TurnText is one transcript line for assembly.
type TurnText struct {
	Role    string
	Content string
}

// ContextInputs is the materialized session snapshot Assemble consumes (pure).
type ContextInputs struct {
	SessionID  string
	Turns      []TurnText // chronological, oldest first
	MemoryHits []string   // already ranked, best first
	ReadPaths  []string
	Prompt     string // user task; used for auto heuristic only
}

// ContextBlock is the injected text plus a short marker for previews.
type ContextBlock struct {
	Text    string
	Marker  string
	Sources []string
}

// DefaultContextSources is Q12: full+memory unless opted out.
func DefaultContextSources() []string { return []string{"full", "memory"} }

// DefaultContextSpec is the global fallback.
func DefaultContextSpec() ContextSpec {
	return ContextSpec{Sources: DefaultContextSources(), MaxChars: DefaultContextMaxChars, Set: true}
}

// NormalizeSources expands none/auto, drops unknown tokens, and lets full win over compact.
func NormalizeSources(in []string, prompt string) []string {
	var raw []string
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		raw = append(raw, s)
	}
	if len(raw) == 1 && raw[0] == "none" {
		return nil
	}
	if len(raw) == 1 && raw[0] == "auto" {
		if autoWantsFull(prompt) {
			return []string{"full", "memory"}
		}
		return []string{"read_paths", "compact"}
	}
	seen := map[string]bool{}
	var out []string
	hasFull, hasCompact := false, false
	for _, s := range raw {
		switch s {
		case "compact":
			hasCompact = true
		case "full":
			hasFull = true
		case "memory", "read_paths":
		default:
			continue
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if hasFull && hasCompact {
		filtered := out[:0]
		for _, s := range out {
			if s != "compact" {
				filtered = append(filtered, s)
			}
		}
		out = filtered
	}
	return out
}

func autoWantsFull(prompt string) bool {
	p := strings.ToLower(prompt)
	needles := []string{
		"this session", "what we discussed", "the tracker", "earlier",
		"we discussed", "prior work", "last time", "as above", "the conversation",
	}
	for _, n := range needles {
		if strings.Contains(p, n) {
			return true
		}
	}
	return sessionIDRe.MatchString(p)
}

var sessionIDRe = regexp.MustCompile(`\b0[0-9a-z]{8,12}\b`)

// ResolveContextSpec: per-call → per-session → fallback (ADR-0031 Q10).
func ResolveContextSpec(call, session, fallback ContextSpec) ContextSpec {
	pick := fallback
	if session.Set {
		pick = session
	}
	if call.Set {
		pick = call
	}
	if pick.MaxChars <= 0 {
		if fallback.MaxChars > 0 {
			pick.MaxChars = fallback.MaxChars
		} else {
			pick.MaxChars = DefaultContextMaxChars
		}
	}
	pick.Sources = NormalizeSources(pick.Sources, "")
	pick.Set = true
	return pick
}

// ResolveContextSpecWithPrompt applies auto using the user prompt.
func ResolveContextSpecWithPrompt(call, session, fallback ContextSpec, prompt string) ContextSpec {
	spec := ResolveContextSpec(call, session, fallback)
	// Re-run auto if the chosen layer still has auto (Normalize in Resolve already dropped it
	// without prompt). Keep original sources from the winning layer.
	win := fallback
	if session.Set {
		win = session
	}
	if call.Set {
		win = call
	}
	if win.MaxChars > 0 {
		spec.MaxChars = win.MaxChars
	} else if spec.MaxChars <= 0 {
		spec.MaxChars = DefaultContextMaxChars
	}
	spec.Sources = NormalizeSources(win.Sources, prompt)
	spec.Set = true
	return spec
}

// Assemble builds the prepended context block. Pure given Inputs + Spec.
func Assemble(spec ContextSpec, in ContextInputs) ContextBlock {
	sources := NormalizeSources(spec.Sources, in.Prompt)
	max := spec.MaxChars
	if max <= 0 {
		max = DefaultContextMaxChars
	}
	if len(sources) == 0 {
		return ContextBlock{Marker: "none"}
	}

	var sections []ctxSection
	for _, src := range sources {
		body := materialize(src, in)
		body = redactSecrets(body)
		if strings.TrimSpace(body) == "" {
			continue
		}
		sections = append(sections, ctxSection{name: src, body: body})
	}

	dropOrder := []string{"read_paths", "memory", "full", "compact"}
	for {
		text := joinSections(in.SessionID, sections)
		if utf8.RuneCountInString(text) <= max || len(sections) == 0 {
			names := make([]string, 0, len(sections))
			for _, s := range sections {
				names = append(names, s.name)
			}
			if utf8.RuneCountInString(text) > max {
				text = truncateRunes(text, max)
			}
			return ContextBlock{Text: text, Marker: markerOf(names, text), Sources: names}
		}
		dropped := false
		for _, d := range dropOrder {
			for i, s := range sections {
				if s.name == d {
					sections = append(sections[:i], sections[i+1:]...)
					dropped = true
					break
				}
			}
			if dropped {
				break
			}
		}
		if !dropped {
			text := joinSections(in.SessionID, sections)
			text = truncateRunes(text, max)
			names := make([]string, 0, len(sections))
			for _, s := range sections {
				names = append(names, s.name)
			}
			return ContextBlock{Text: text, Marker: markerOf(names, text), Sources: names}
		}
	}
}

func materialize(src string, in ContextInputs) string {
	switch src {
	case "compact":
		return truncateRunes(extractiveDigest(in.Turns), CapCompact)
	case "full":
		return fullTranscript(in.Turns)
	case "memory":
		hits := in.MemoryHits
		if len(hits) > MemoryTopK {
			hits = hits[:MemoryTopK]
		}
		s := strings.Join(hits, "\n")
		return truncateRunes(s, CapMemory)
	case "read_paths":
		var b strings.Builder
		for _, p := range in.ReadPaths {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(p)
		}
		return truncateRunes(b.String(), CapReadPaths)
	}
	return ""
}

func fullTranscript(turns []TurnText) string {
	if len(turns) == 0 {
		return ""
	}
	// Newest first until CapFullRaw; remainder is older digest.
	var recent []TurnText
	raw := 0
	cut := len(turns)
	for i := len(turns) - 1; i >= 0; i-- {
		line := formatTurn(turns[i])
		n := utf8.RuneCountInString(line) + 1
		if raw+n > CapFullRaw && len(recent) > 0 {
			cut = i + 1
			break
		}
		recent = append(recent, turns[i])
		raw += n
		cut = i
	}
	var b strings.Builder
	for _, t := range recent {
		b.WriteString(formatTurn(t))
		b.WriteByte('\n')
	}
	if cut > 0 {
		older := extractiveDigest(turns[:cut])
		older = truncateRunes(older, CapFullOlder)
		if strings.TrimSpace(older) != "" {
			b.WriteString("\n— older (compact) —\n")
			b.WriteString(older)
		}
	}
	return strings.TrimSpace(b.String())
}

func extractiveDigest(turns []TurnText) string {
	if len(turns) == 0 {
		return ""
	}
	var b strings.Builder
	for _, t := range turns {
		b.WriteString(formatTurn(t))
		b.WriteByte('\n')
	}
	s := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(s) <= CapCompact {
		return s
	}
	// Keep the most recent tail of older history.
	return "…\n" + lastRunes(s, CapCompact-2)
}

func formatTurn(t TurnText) string {
	role := t.Role
	if role == "" {
		role = "unknown"
	}
	c := strings.TrimSpace(t.Content)
	c = strings.ReplaceAll(c, "\r\n", "\n")
	if utf8.RuneCountInString(c) > 800 && (role == "tool" || role == "attachment") {
		c = truncateRunes(c, 800)
	}
	return "[" + role + "] " + c
}

type ctxSection struct {
	name string
	body string
}

func joinSections(sessionID string, sections []ctxSection) string {
	if len(sections) == 0 {
		return ""
	}
	var b strings.Builder
	for i, s := range sections {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "[CONTEXT — Marble session %s · source: %s]\n%s", sessionID, s.name, strings.TrimSpace(s.body))
	}
	return b.String()
}

func markerOf(names []string, text string) string {
	if len(names) == 0 || strings.TrimSpace(text) == "" {
		return "none"
	}
	n := utf8.RuneCountInString(text)
	label := strings.Join(names, "+")
	if n >= 1000 {
		return fmt.Sprintf("%s · %dk chars", label, (n+500)/1000)
	}
	return fmt.Sprintf("%s · %d chars", label, n)
}

func truncateRunes(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	if n < 2 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

var secretLineRe = regexp.MustCompile(`(?i)(orb_ak_|orb_pk_|xoxb-|sk-[a-z0-9]|api[_-]?key\s*[:=])`)

func redactSecrets(s string) string {
	if s == "" || !secretLineRe.MatchString(s) {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if secretLineRe.MatchString(line) {
			lines[i] = "[redacted]"
		}
	}
	return strings.Join(lines, "\n")
}

// MemorySeedQuery concatenates the prompt and recent user turns (ADR-0031 Q6).
func MemorySeedQuery(prompt string, turns []TurnText) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(prompt))
	n := 0
	for i := len(turns) - 1; i >= 0 && n < 3; i-- {
		if turns[i].Role != "user" {
			continue
		}
		b.WriteByte(' ')
		b.WriteString(turns[i].Content)
		n++
	}
	s := strings.Join(strings.Fields(b.String()), " ")
	if utf8.RuneCountInString(s) > 400 {
		s = truncateRunes(s, 400)
	}
	return s
}

// ParseContextArg accepts a JSON string, list, or none/auto/clear.
func ParseContextArg(v interface{}) (ContextSpec, error) {
	spec := ContextSpec{Set: true, MaxChars: 0}
	switch t := v.(type) {
	case nil:
		spec.Set = false
		return spec, nil
	case string:
		s := strings.TrimSpace(t)
		if s == "" || strings.EqualFold(s, "clear") {
			spec.Sources = nil
			return spec, nil
		}
		if strings.Contains(s, ",") {
			spec.Sources = strings.Split(s, ",")
		} else {
			spec.Sources = []string{s}
		}
		return spec, nil
	case []interface{}:
		for _, x := range t {
			if s, ok := x.(string); ok {
				spec.Sources = append(spec.Sources, s)
			}
		}
		return spec, nil
	case []string:
		spec.Sources = append([]string{}, t...)
		return spec, nil
	default:
		return ContextSpec{}, fmt.Errorf("context must be a list or none/auto/clear")
	}
}

// InjectContext prepends a block to the user prompt.
func InjectContext(block ContextBlock, prompt string) string {
	if strings.TrimSpace(block.Text) == "" {
		return prompt
	}
	return strings.TrimRight(block.Text, "\n") + "\n\n" + prompt
}

func applyContextInject(req Request) Request {
	if strings.TrimSpace(req.ContextBlock) == "" {
		return req
	}
	req.Prompt = InjectContext(ContextBlock{Text: req.ContextBlock}, req.Prompt)
	return req
}
