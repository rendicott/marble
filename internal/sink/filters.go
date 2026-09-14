package sink

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Filters are declarative per-sink match rules (ADR-0028 Q9/Q10).
type Filters struct {
	Kinds          []string `json:"kinds,omitempty"`
	SkipEmpty      *bool    `json:"skip_empty,omitempty"` // nil → true
	SkipCron       bool     `json:"skip_cron,omitempty"`
	OnlySessions   []string `json:"only_sessions,omitempty"`
	SkipSessions   []string `json:"skip_sessions,omitempty"`
	MinChars       int      `json:"min_chars,omitempty"`
	MinIntervalSec int      `json:"min_interval_sec,omitempty"`

	onlyRe []*regexp.Regexp `json:"-"`
	skipRe []*regexp.Regexp `json:"-"`
}

func (f *Filters) normalize() {
	if f == nil {
		return
	}
	kinds := make([]string, 0, len(f.Kinds))
	for _, k := range f.Kinds {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		kinds = append(kinds, k)
	}
	f.Kinds = kinds
	f.OnlySessions = trimList(f.OnlySessions)
	f.SkipSessions = trimList(f.SkipSessions)
	if f.MinChars < 0 {
		f.MinChars = 0
	}
	if f.MinIntervalSec < 0 {
		f.MinIntervalSec = 0
	}
	f.onlyRe = compileRegexes(f.OnlySessions)
	f.skipRe = compileRegexes(f.SkipSessions)
}

func (f Filters) validate() error {
	for _, k := range f.Kinds {
		if !knownKind(k) {
			return fmt.Errorf("unknown kind %q", k)
		}
	}
	for _, p := range f.OnlySessions {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("only_sessions %q: %w", p, err)
		}
	}
	for _, p := range f.SkipSessions {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("skip_sessions %q: %w", p, err)
		}
	}
	return nil
}

func knownKind(k string) bool {
	for _, x := range KnownKinds {
		if x == k {
			return true
		}
	}
	return false
}

func trimList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func compileRegexes(patterns []string) []*regexp.Regexp {
	if len(patterns) == 0 {
		return nil
	}
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		out = append(out, re)
	}
	return out
}

func (f Filters) skipEmpty() bool {
	if f.SkipEmpty == nil {
		return true
	}
	return *f.SkipEmpty
}

// Match reports whether ev should be delivered, and a skip reason if not.
func (f Filters) Match(ev TurnEvent) (ok bool, reason string) {
	if len(f.Kinds) > 0 {
		found := false
		for _, k := range f.Kinds {
			if k == ev.Kind {
				found = true
				break
			}
		}
		if !found {
			return false, "kind " + ev.Kind
		}
	}
	if f.skipEmpty() && strings.TrimSpace(ev.Message) == "" {
		return false, "empty"
	}
	if f.SkipCron && (ev.Kind == "cron" || ev.Kind == "continuation" || isCronTitle(ev.SessionTitle)) {
		return false, "cron"
	}
	if f.MinChars > 0 && utf8.RuneCountInString(ev.Message) < f.MinChars {
		return false, "min_chars"
	}
	id := ev.SessionID
	title := ev.SessionTitle
	if len(f.onlyRe) > 0 {
		hit := false
		for _, re := range f.onlyRe {
			if re.MatchString(id) || re.MatchString(title) {
				hit = true
				break
			}
		}
		if !hit {
			return false, "only_sessions"
		}
	}
	for _, re := range f.skipRe {
		if re.MatchString(id) || re.MatchString(title) {
			return false, "skip_sessions"
		}
	}
	return true, ""
}
