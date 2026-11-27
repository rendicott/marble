package session

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/rendicott/marble/internal/agentproc"
	"github.com/rendicott/marble/internal/memory"
)

// soulContextHeading labels every-turn operator text inside a subprocess context block.
const soulContextHeading = "Every-turn context (soul) — from harness settings, applies to this turn only"

// soulOnDisk reads $MEMORY/soul.md. Missing file, read error, or a nil store yields "".
// Every call hits the filesystem; the text is never cached on the runner or the session.
func (r *Runner) soulOnDisk() string {
	if r == nil || r.Reg == nil {
		return ""
	}
	store := r.Reg.Store()
	if store == nil {
		return ""
	}
	text, err := store.ReadSoul()
	if err != nil {
		return ""
	}
	return text
}

// soulVersionSent is the soul.md bytes included for this turn.
// A blank file sends nothing. System sessions on the Marble loop send nothing
// (injectSoul's skip). The routed subprocess path has no such skip.
// The returned string is the raw file when it is applied, so its digest matches the file.
func (r *Runner) soulVersionSent(s *Session, source string) string {
	if source == "loop" && (s == nil || s.Kind == "system") {
		return ""
	}
	raw := r.soulOnDisk()
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	return raw
}

// notePromptSoul writes one prompt_soul event for the version this turn sends.
// The content is a digest and a character count. The soul text is not logged.
func (r *Runner) notePromptSoul(s *Session, source string) {
	if r == nil || r.Reg == nil || s == nil {
		return
	}
	sent := r.soulVersionSent(s, source)
	sha, chars := memory.SoulVersion(sent)
	line := fmt.Sprintf("soul_sha=%s chars=%d source=%s path=soul.md", sha, chars, source)
	r.Reg.logEvent(s, "prompt_soul", "system", line, "", "", "", nil, nil, nil, nil, nil, "", "")
}

// applySoulToBlock prepends the current soul.md to a subprocess context block.
// A blank soul or a nil store returns block unchanged.
func (r *Runner) applySoulToBlock(block agentproc.ContextBlock) agentproc.ContextBlock {
	raw := r.soulOnDisk()
	soul := strings.TrimSpace(raw)
	if soul == "" {
		return block
	}
	section := soulContextHeading + "\n" + soul
	if strings.TrimSpace(block.Text) == "" {
		block.Text = section
	} else {
		block.Text = section + "\n\n" + block.Text
	}
	block.Marker = withSoulMarker(block.Marker, block.Text)
	src := make([]string, 0, len(block.Sources)+1)
	src = append(src, "soul")
	src = append(src, block.Sources...)
	block.Sources = src
	return block
}

func withSoulMarker(prev, text string) string {
	label := "soul"
	if prev != "" && prev != "none" {
		base := prev
		if i := strings.Index(prev, " · "); i >= 0 {
			base = prev[:i]
		}
		if base != "" && base != "none" {
			label = "soul+" + base
		}
	}
	n := utf8.RuneCountInString(text)
	if n >= 1000 {
		return fmt.Sprintf("%s · %dk chars", label, (n+500)/1000)
	}
	return fmt.Sprintf("%s · %d chars", label, n)
}
