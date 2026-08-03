package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ADR-0022 long-turn efficiency: anti-repeat, escalate lock, sleep block, eval demotion.

const (
	fingerprintRingMax = 32
	evalMutateWindow   = 20
	toolNameRingMax    = 32
)

// ThrashPolicy is turn-independent config (from CLI). Zero AntiRepeatN disables anti-repeat.
type ThrashPolicy struct {
	AntiRepeatN     int  // default 3; 0 = off
	StuckEscalateK  int  // default 3
	BlockSleepShell bool // default true
	EvalMutateMax   int  // default 5; 0 = warn only
}

// DefaultThrashPolicy returns ADR-0022 defaults.
// AntiRepeatN is 0 (off): fingerprint anti-repeat is too crude for legitimate
// polling (file size, curl, ping, agent task_id). Opt-in with --anti-repeat-n=3.
func DefaultThrashPolicy() ThrashPolicy {
	return ThrashPolicy{
		AntiRepeatN:     0,
		StuckEscalateK:  3,
		BlockSleepShell: true,
		EvalMutateMax:   5,
	}
}

// FingerprintEvent records one tool call for anti-repeat (successes count — KD14).
type FingerprintEvent struct {
	FP      string
	Name    string
	OK      bool
	Summary string
}

// ThrashState is turn-scoped (lives on TurnContext).
type ThrashState struct {
	Events           []FingerprintEvent
	ToolNames        []string // last tools for continue packet / computer-heavy compact
	ComputerFailStreak int
	EscalateLock     bool
	EvalMutateRecent []bool // true = mutate eval in last calls (capped)
	LastURL          string
	LastFailure      string
	BanList          []string
	ChecklistHint    bool // advisory already fired
	ScreenshotStreak int
	WaitStreak       int
}

func (tc *TurnContext) thrash() *ThrashState {
	if tc == nil {
		return nil
	}
	if tc.Thrash == nil {
		tc.Thrash = &ThrashState{}
	}
	return tc.Thrash
}

// pureSleepRE matches shell commands that are only sleep/timeout delay.
var pureSleepRE = regexp.MustCompile(`(?is)^\s*(?:command\s+)?(?:/bin/)?(?:sleep|timeout)\s+\d+(?:\.\d+)?\s*$`)

// IsPureSleepCommand reports sleep-only shell (ADR-0022 P1.1 / KD6).
func IsPureSleepCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	// Strip simple quotes wrapping the whole command
	if (strings.HasPrefix(cmd, "'") && strings.HasSuffix(cmd, "'")) ||
		(strings.HasPrefix(cmd, `"`) && strings.HasSuffix(cmd, `"`)) {
		cmd = strings.TrimSpace(cmd[1 : len(cmd)-1])
	}
	return pureSleepRE.MatchString(cmd)
}

// ToolFingerprint builds a stable key for anti-repeat (all tools — KD11/Q9).
func ToolFingerprint(name, argsJSON string) string {
	name = strings.TrimSpace(name)
	norm := normalizeArgsForFingerprint(name, argsJSON)
	raw := name + "\x00" + norm
	if len(raw) > 600 {
		sum := sha256.Sum256([]byte(raw))
		return name + "\x00#" + hex.EncodeToString(sum[:12])
	}
	return raw
}

func normalizeArgsForFingerprint(name, argsJSON string) string {
	argsJSON = strings.TrimSpace(argsJSON)
	if argsJSON == "" {
		return "{}"
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		// fallback: collapse whitespace
		return collapseWS(argsJSON)
	}
	// Drop volatile / non-semantic keys
	delete(m, "computer_id")
	switch name {
	case "call_agent_process":
		// Poll mode is intentionally re-entrant (same task_id many times).
		// Start mode still fingerprints on prompt/format/workdir.
		if tid, ok := m["task_id"].(string); ok && strings.TrimSpace(tid) != "" {
			return "poll|" + strings.TrimSpace(tid)
		}
		// Normalize long prompts so tiny whitespace edits don't dodge thrash
		if p, ok := m["prompt"].(string); ok {
			m["prompt"] = thrashTrunc(collapseWS(p), 200)
		}
		delete(m, "timeout_sec")
	case "check_background_task":
		// Same: polling a BG task is expected to repeat
		if tid, ok := m["task_id"].(string); ok && strings.TrimSpace(tid) != "" {
			return "poll|" + strings.TrimSpace(tid)
		}
	case "shell_execute", "start_background_task":
		if cmd, ok := m["command"].(string); ok {
			m["command"] = collapseWS(cmd)
		}
		delete(m, "timeout_sec")
		delete(m, "cwd")
	case "computer_desktop_act":
		// click|x|y|button only
		act, _ := m["action"].(string)
		act = strings.ToLower(strings.TrimSpace(act))
		if act == "click" {
			return fmt.Sprintf("click|%v|%v|%v", m["x"], m["y"], m["button"])
		}
		if act == "key" {
			return fmt.Sprintf("key|%v", m["key"])
		}
		if act == "type" {
			t, _ := m["text"].(string)
			return "type|" + thrashTrunc(t, 80)
		}
	case "computer_browser_act":
		act, _ := m["action"].(string)
		act = strings.ToLower(strings.TrimSpace(act))
		target, _ := m["target"].(string)
		text, _ := m["text"].(string)
		return fmt.Sprintf("%s|%s|%s|%v|%v", act, thrashTrunc(target, 80), thrashTrunc(text, 80), m["x"], m["y"])
	case "computer_browser_open":
		u, _ := m["url"].(string)
		return "open|" + strings.TrimSpace(u)
	case "computer_screenshot", "computer_browser_snapshot", "computer_browser_tabs":
		return name
	}
	b, err := json.Marshal(m)
	if err != nil {
		return collapseWS(argsJSON)
	}
	s := string(b)
	if len(s) > 512 {
		sum := sha256.Sum256(b)
		return "#" + hex.EncodeToString(sum[:12])
	}
	return s
}

func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func thrashTrunc(s string, n int) string {
	s = collapseWS(s)
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// isPollExempt: tools that must re-call the same args to wait on work (ADR-0022 exception).
func isPollExempt(name, argsJSON string) bool {
	switch name {
	case "call_agent_process", "check_background_task":
		var a struct {
			TaskID string `json:"task_id"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		return strings.TrimSpace(a.TaskID) != ""
	default:
		return false
	}
}

// isComputerTool reports computer_* family (escalate is computer-centric).
func isComputerTool(name string) bool {
	return strings.HasPrefix(name, "computer_")
}

// isComputerClickClass is hard-blocked while EscalateLock (KD5).
func isComputerClickClass(name, argsJSON string) bool {
	switch name {
	case "computer_desktop_act":
		var a struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		return strings.EqualFold(strings.TrimSpace(a.Action), "click")
	case "computer_browser_act":
		var a struct {
			Action string `json:"action"`
			Text   string `json:"text"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		act := strings.ToLower(strings.TrimSpace(a.Action))
		if act == "click" || act == "click_text" {
			return true
		}
		if act == "eval" || act == "evaluate" {
			return isEvalMutate(a.Text)
		}
	}
	return false
}

func isEvalMutate(expr string) bool {
	low := strings.ToLower(expr)
	return strings.Contains(low, ".click(") ||
		strings.Contains(low, "dispatchevent") ||
		strings.Contains(low, "dispatchmouseevent") ||
		strings.Contains(low, "inserttext") ||
		strings.Contains(low, ".value=") ||
		strings.Contains(low, "setfileinputfiles") ||
		(strings.Contains(low, "checked") && strings.Contains(low, "="))
}

// preflightThrash runs before tool execution. Returns error string if blocked (with "error: " prefix handled by caller).
func (r *Registry) preflightThrash(name, argsJSON string, tc *TurnContext) error {
	r.ensureThrashPolicy()
	pol := r.Thrash

	// Sleep-only shell (P1.1)
	if pol.BlockSleepShell && name == "shell_execute" {
		var a struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		if IsPureSleepCommand(a.Command) {
			return fmt.Errorf("sleep-only shell is blocked (ADR-0022). Use computer_browser_act action=wait (text=…, target=…, x=timeout_ms) or poll a real condition. Escape: --block-sleep-shell=false")
		}
	}

	st := tc.thrash()
	if st == nil {
		return nil
	}

	// Escalate lock: hard-block same-class computer clicks (KD5)
	if st.EscalateLock && isComputerClickClass(name, argsJSON) {
		return fmt.Errorf("escalate lock active (ADR-0022): desktop/browser click blocked after stuck computer use. NEXT: computer_confirm (one human step), computer_screenshot/snapshot to re-assess, shell/API path, or a different action class — not another identical click")
	}

	// Successful computer_confirm clears escalate lock
	// (handled in postflight)

	fp := ToolFingerprint(name, argsJSON)
	for _, ban := range st.BanList {
		if ban == fp {
			return fmt.Errorf("anti-repeat ban: this exact tool+args is forbidden for the rest of the turn. Change approach (different args, API/script, or computer_confirm)")
		}
	}

	// Anti-repeat N consecutive (KD1, KD2, KD14) — all tools.
	// n = prior consecutive same FP; this call would be n+1.
	// Exempt intentional poll loops (agent/BG status) — same task_id is not thrash.
	if pol.AntiRepeatN > 0 && !isPollExempt(name, argsJSON) {
		n := consecutiveFP(st.Events, fp)
		if n+1 >= pol.AntiRepeatN {
			// ban this fingerprint for the turn
			st.BanList = appendUnique(st.BanList, fp)
			if isComputerTool(name) {
				st.EscalateLock = true
				st.LastFailure = "anti-repeat on " + name
			}
			return fmt.Errorf("anti-repeat: %s with same args used %d times (success or fail counts). NEXT: (1) different action class (2) computer_confirm / ask user one step (3) API/script path (4) session_compact + re-plan. Forbidden: retry identical args", name, n+1)
		}
	}

	// Eval mutate hard limit (KD7)
	if pol.EvalMutateMax > 0 && name == "computer_browser_act" {
		var a struct {
			Action string `json:"action"`
			Text   string `json:"text"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		act := strings.ToLower(strings.TrimSpace(a.Action))
		if (act == "eval" || act == "evaluate") && isEvalMutate(a.Text) {
			count := countTrue(st.EvalMutateRecent)
			if count >= pol.EvalMutateMax {
				return fmt.Errorf("eval-mutate limit: %d mutate evals in recent tools (max %d). Use computer_browser_act action=click_text|type|set_input_files instead of Runtime.evaluate clicks", count, pol.EvalMutateMax)
			}
		}
	}

	// Screenshot / wait budgets (P2.3) — soft hard-block after 5 consecutive
	if name == "computer_screenshot" && st.ScreenshotStreak >= 5 {
		return fmt.Errorf("screenshot budget: %d consecutive screenshots. Act on the image (desktop click, browser act, or computer_confirm) or report status to the user", st.ScreenshotStreak)
	}
	if name == "computer_browser_act" {
		var a struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		if strings.EqualFold(a.Action, "wait") && st.WaitStreak >= 5 {
			return fmt.Errorf("wait budget: %d consecutive waits. Check state with snapshot or change approach", st.WaitStreak)
		}
	}

	return nil
}

func consecutiveFP(events []FingerprintEvent, fp string) int {
	n := 0
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].FP != fp {
			break
		}
		n++
	}
	return n
}

func countTrue(bs []bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// postflightThrash records the tool outcome and updates escalate / streaks.
func (r *Registry) postflightThrash(name, argsJSON, result string, tc *TurnContext) {
	if tc == nil {
		return
	}
	r.ensureThrashPolicy()
	st := tc.thrash()
	fp := ToolFingerprint(name, argsJSON)
	ok := !strings.HasPrefix(strings.TrimSpace(result), "error:")
	summary := thrashTrunc(collapseWS(result), 120)

	st.Events = append(st.Events, FingerprintEvent{FP: fp, Name: name, OK: ok, Summary: summary})
	if len(st.Events) > fingerprintRingMax {
		st.Events = st.Events[len(st.Events)-fingerprintRingMax:]
	}
	st.ToolNames = append(st.ToolNames, name)
	if len(st.ToolNames) > toolNameRingMax {
		st.ToolNames = st.ToolNames[len(st.ToolNames)-toolNameRingMax:]
	}

	// computer_confirm accepted clears escalate lock
	if name == "computer_confirm" && ok {
		if strings.Contains(result, `"accepted":true`) || strings.Contains(result, `"accepted": true`) {
			st.EscalateLock = false
			st.ComputerFailStreak = 0
		}
	}

	// Computer fail streak (KD4)
	if isComputerTool(name) {
		failish := !ok || isComputerFailResult(result)
		if failish {
			st.ComputerFailStreak++
			st.LastFailure = summary
			k := r.Thrash.StuckEscalateK
			if k <= 0 {
				k = 3
			}
			if st.ComputerFailStreak >= k {
				st.EscalateLock = true
			}
		} else if name != "computer_screenshot" && name != "computer_browser_snapshot" {
			// progress-ish success resets streak (not pure observation)
			if name == "computer_desktop_act" || name == "computer_browser_act" || name == "computer_browser_open" {
				st.ComputerFailStreak = 0
			}
		}
	}

	// Eval mutate window
	mutate := false
	if name == "computer_browser_act" {
		var a struct {
			Action string `json:"action"`
			Text   string `json:"text"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		act := strings.ToLower(strings.TrimSpace(a.Action))
		if (act == "eval" || act == "evaluate") && isEvalMutate(a.Text) {
			mutate = true
		}
	}
	st.EvalMutateRecent = append(st.EvalMutateRecent, mutate)
	if len(st.EvalMutateRecent) > evalMutateWindow {
		st.EvalMutateRecent = st.EvalMutateRecent[len(st.EvalMutateRecent)-evalMutateWindow:]
	}

	// Streaks for budgets
	if name == "computer_screenshot" {
		st.ScreenshotStreak++
	} else {
		st.ScreenshotStreak = 0
	}
	if name == "computer_browser_act" {
		var a struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		if strings.EqualFold(a.Action, "wait") {
			st.WaitStreak++
		} else {
			st.WaitStreak = 0
		}
	} else {
		st.WaitStreak = 0
	}

	// Pull URL hints from results
	if u := extractURLHint(result); u != "" {
		st.LastURL = u
	}
}

func isComputerFailResult(result string) bool {
	low := strings.ToLower(result)
	return strings.Contains(low, "cdp timeout") ||
		strings.Contains(low, "not_found") ||
		strings.Contains(low, "requires a recent") ||
		strings.Contains(low, "anti-repeat") ||
		strings.Contains(low, "snapshot error") ||
		strings.Contains(low, "bot_wall") ||
		strings.Contains(low, "escalate lock")
}

func extractURLHint(result string) string {
	// crude: look for "url": "https://...
	idx := strings.Index(result, `"url"`)
	if idx < 0 {
		idx = strings.Index(result, "url=")
		if idx >= 0 {
			rest := result[idx+4:]
			rest = strings.TrimSpace(rest)
			rest = strings.Trim(rest, `"'`)
			if i := strings.IndexAny(rest, " \n\r\t\"'"); i > 0 {
				rest = rest[:i]
			}
			if strings.HasPrefix(rest, "http") {
				return thrashTrunc(rest, 200)
			}
		}
		return ""
	}
	rest := result[idx:]
	// find http
	h := strings.Index(rest, "http")
	if h < 0 {
		return ""
	}
	rest = rest[h:]
	end := strings.IndexAny(rest, `"'\n\r `)
	if end > 0 {
		rest = rest[:end]
	}
	return thrashTrunc(rest, 200)
}

func (r *Registry) ensureThrashPolicy() {
	if r == nil {
		return
	}
	if !r.ThrashSet {
		r.Thrash = DefaultThrashPolicy()
		r.ThrashSet = true
	}
}

// ContinuePacket builds rich auto-continue state (P0.3 / P1.3).
func (tc *TurnContext) ContinuePacket(reason string) string {
	if tc == nil {
		return reason
	}
	st := tc.thrash()
	var b strings.Builder
	b.WriteString("Continue the unfinished work from the previous turn. ")
	b.WriteString("Do not restart completed steps. Pick up mid-task. ")
	b.WriteString("When fully done, reply to the user with a clear status summary.\n\n")
	b.WriteString("Why this continuation fired: ")
	b.WriteString(reason)
	b.WriteString("\n\n")
	if st != nil {
		if st.LastURL != "" {
			b.WriteString("Last URL: ")
			b.WriteString(st.LastURL)
			b.WriteString("\n")
		}
		if st.LastFailure != "" {
			b.WriteString("Last failure: ")
			b.WriteString(st.LastFailure)
			b.WriteString("\n")
		}
		if len(st.ToolNames) > 0 {
			n := st.ToolNames
			if len(n) > 12 {
				n = n[len(n)-12:]
			}
			b.WriteString("Last tools: ")
			b.WriteString(strings.Join(n, ", "))
			b.WriteString("\n")
		}
		if len(st.BanList) > 0 {
			b.WriteString("Ban (do not retry exact args): ")
			for i, ban := range st.BanList {
				if i > 0 {
					b.WriteString("; ")
				}
				// show tool name only for readability
				parts := strings.SplitN(ban, "\x00", 2)
				b.WriteString(parts[0])
			}
			b.WriteString("\n")
		}
		if st.EscalateLock {
			b.WriteString("Escalate lock was active — prefer computer_confirm or API over more blind clicks.\n")
		}
	}
	b.WriteString("\nRules: Do not retry ban list. Prefer API/script over GUI. ")
	b.WriteString("If UI stuck → computer_confirm one step. ")
	b.WriteString("Prefer verifying the user-stated success condition over provider-internal \"completed\". ")
	b.WriteString("If multi-step work remains, keep a short checklist in memory or a workspace file.\n")
	return b.String()
}

// ComputerHeavyShare returns fraction of last n tool names that are computer_*.
func (tc *TurnContext) ComputerHeavyShare(window int) float64 {
	if tc == nil || tc.Thrash == nil || window <= 0 {
		return 0
	}
	names := tc.Thrash.ToolNames
	if len(names) == 0 {
		return 0
	}
	if len(names) > window {
		names = names[len(names)-window:]
	}
	c := 0
	for _, n := range names {
		if isComputerTool(n) {
			c++
		}
	}
	return float64(c) / float64(len(names))
}

// EvalMutateWarning returns a non-empty suffix to append when eval mutate is used but under hard limit.
func EvalMutateWarning(argsJSON string, st *ThrashState, max int) string {
	var a struct {
		Action string `json:"action"`
		Text   string `json:"text"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	act := strings.ToLower(strings.TrimSpace(a.Action))
	if act != "eval" && act != "evaluate" {
		return ""
	}
	if !isEvalMutate(a.Text) {
		return ""
	}
	count := 0
	if st != nil {
		count = countTrue(st.EvalMutateRecent)
	}
	return fmt.Sprintf("\n// WARNING: eval used for mutation (%d recent; hard stop at %d). Prefer action=click_text|type|set_input_files.", count+1, max)
}
