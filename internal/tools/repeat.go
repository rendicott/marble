package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Repeat awareness (mpub report "marble-thrashing", 2026-10-06). The harness knows the
// turn's call history; the agent does not, so every identical result looked like the
// first. Advisory only — nothing here blocks a call (ADR-0022 anti-repeat still does,
// when enabled). A changed result is treated as progress (legitimate polling) and is
// never annotated.

const (
	repeatNoteAt       = 3    // identical call + identical result → note on this call
	repeatFailNoteAt   = 2    // identical call + identical error → note on this call
	repeatStubMinBytes = 1024 // smaller results are cheaper to resend than to stub
)

// readOnlyTools may have an unchanged repeat result replaced by a stub.
var readOnlyTools = map[string]bool{
	"file_read": true, "list_files": true, "codebase_summary": true, "grep": true, "glob": true,
	"memory_search": true, "memory_fetch": true, "skill_search": true, "skill_load": true,
	"cron_list": true, "cron_get": true, "model_list": true, "model_get": true,
	"agent_preset_list": true, "agent_preset_get": true, "mpub_list": true, "mpub_get": true,
	"web_fetch": true,
}

// repeatRec tracks one tool+args fingerprint for the turn.
type repeatRec struct {
	Label   string
	Calls   int    // identical tool+args calls this turn
	Same    int    // consecutive calls of this fingerprint returning the current result
	Hash    string // hash of the current result
	Stubbed int
}

// noteRepeat records the call and returns the result to hand the model: unchanged,
// with a repeat note appended, or (read-only, large, verifiably still in context) a stub.
func (r *Registry) noteRepeat(name, argsJSON, result string, tc *TurnContext) string {
	if tc == nil || isPollExempt(name, argsJSON) {
		return result
	}
	st := tc.thrash()
	if st.Repeats == nil {
		st.Repeats = map[string]*repeatRec{}
	}
	fp := ToolFingerprint(name, argsJSON)
	rec := st.Repeats[fp]
	if rec == nil {
		rec = &repeatRec{Label: repeatLabel(name, argsJSON)}
		st.Repeats[fp] = rec
	}
	rec.Calls++
	sum := sha256.Sum256([]byte(result))
	h := hex.EncodeToString(sum[:12])
	if rec.Hash != h {
		rec.Hash = h
		rec.Same = 1
		return result
	}
	rec.Same++

	if strings.HasPrefix(strings.TrimSpace(result), "error:") {
		if rec.Same >= repeatFailNoteAt {
			return result + fmt.Sprintf("\n\n[marble] identical failing call #%d this turn (%s): same error every time. "+
				"Retrying it unchanged will fail the same way. Change the approach, not the wording.", rec.Same, rec.Label)
		}
		return result
	}
	if readOnlyTools[name] && len(result) >= repeatStubMinBytes && tc.ResultVisible != nil && tc.ResultVisible(result) {
		rec.Stubbed++
		return fmt.Sprintf("[marble] unchanged: identical call #%d this turn (%s) returned exactly the same result as before, "+
			"which is still in your context above — %d bytes omitted. If you are looping, change the arguments or the tool.",
			rec.Same, rec.Label, len(result))
	}
	if rec.Same >= repeatNoteAt {
		return result + fmt.Sprintf("\n\n[marble] identical call #%d this turn (%s), result unchanged. "+
			"If you are looping, change the arguments or the tool.", rec.Same, rec.Label)
	}
	return result
}

// RepeatSummary lists fingerprints repeated with an unchanged result at least min times,
// most repeated first (at most max entries). Empty when nothing qualifies.
func (tc *TurnContext) RepeatSummary(min, max int) string {
	if tc == nil || tc.Thrash == nil || len(tc.Thrash.Repeats) == 0 {
		return ""
	}
	var recs []*repeatRec
	for _, rec := range tc.Thrash.Repeats {
		if rec.Same >= min {
			recs = append(recs, rec)
		}
	}
	if len(recs) == 0 {
		return ""
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].Same != recs[j].Same {
			return recs[i].Same > recs[j].Same
		}
		return recs[i].Label < recs[j].Label
	})
	if max > 0 && len(recs) > max {
		recs = recs[:max]
	}
	parts := make([]string, 0, len(recs))
	for _, rec := range recs {
		parts = append(parts, fmt.Sprintf("%s ×%d unchanged (%d calls)", rec.Label, rec.Same, rec.Calls))
	}
	return strings.Join(parts, "; ")
}

// repeatLabel is a short human form of tool+args for notes.
func repeatLabel(name, argsJSON string) string {
	a := collapseWS(argsJSON)
	if a == "" || a == "{}" {
		return name
	}
	return name + " " + thrashTrunc(a, 80)
}
