package tools

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rendicott/marble/internal/peerhub"
)

// Computer helpers are wired from main (ADR-0020).
// PeerHub is set on Registry.

func (r *Registry) resolveComputerID(tc *TurnContext, explicit string) (string, error) {
	id := strings.TrimSpace(explicit)
	if id != "" {
		return id, nil
	}
	if r.GetSessionComputerID != nil && tc != nil && tc.SessionID != "" {
		if sid, err := r.GetSessionComputerID(tc.SessionID); err == nil && sid != "" {
			return sid, nil
		}
	}
	// single online peer auto
	if r.ListComputers != nil {
		list, err := r.ListComputers()
		if err == nil {
			var online []string
			for _, c := range list {
				if c["online"] == true {
					if cid, ok := c["id"].(string); ok {
						online = append(online, cid)
					}
				}
			}
			if len(online) == 1 {
				return online[0], nil
			}
			if len(online) == 0 {
				return "", fmt.Errorf("no online computers — pair marble-peer and ensure it is connected")
			}
			return "", fmt.Errorf("multiple online computers — bind one with computer_bind or pass computer_id")
		}
	}
	return "", fmt.Errorf("no computer bound")
}

func (r *Registry) computerList(argsJSON string) (string, error) {
	if r.ListComputers == nil {
		return "", fmt.Errorf("computers not configured")
	}
	list, err := r.ListComputers()
	if err != nil {
		return "", err
	}
	b, _ := json.MarshalIndent(map[string]interface{}{"computers": list}, "", "  ")
	return string(b), nil
}

func (r *Registry) computerBind(argsJSON string, tc *TurnContext) (string, error) {
	if tc == nil || tc.SessionID == "" {
		return "", fmt.Errorf("no session")
	}
	var args struct {
		ComputerID string `json:"computer_id"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &args)
	if r.SetSessionComputerID == nil {
		return "", fmt.Errorf("bind not configured")
	}
	if err := r.SetSessionComputerID(tc.SessionID, strings.TrimSpace(args.ComputerID)); err != nil {
		return "", err
	}
	out := map[string]interface{}{
		"computer_id": strings.TrimSpace(args.ComputerID),
		"session_id":  tc.SessionID,
	}
	// Surface os/caps so the agent doesn't have to discover them empirically
	// (field report peer-gui-loop-report, 2026-09-23, P2) — computer_list
	// already has them per-computer; bind just didn't echo them back.
	if id := strings.TrimSpace(args.ComputerID); id != "" && r.ListComputers != nil {
		if list, err := r.ListComputers(); err == nil {
			for _, c := range list {
				if cid, _ := c["id"].(string); cid == id {
					if os, ok := c["os"]; ok {
						out["os"] = os
					}
					if live, ok := c["live"].(map[string]interface{}); ok {
						if caps, ok := live["caps"]; ok {
							out["caps"] = caps
						}
					}
					break
				}
			}
		}
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// peerCaps returns the bound peer's advertised capabilities (zero value if offline).
func (r *Registry) peerCaps(tc *TurnContext, computerID string) (peerhub.Caps, bool) {
	if r.PeerHub == nil {
		return peerhub.Caps{}, false
	}
	cid, err := r.resolveComputerID(tc, computerID)
	if err != nil {
		return peerhub.Caps{}, false
	}
	conn := r.PeerHub.Get(cid)
	if conn == nil {
		return peerhub.Caps{}, false
	}
	return conn.Caps, true
}

// screenshotPrecision spells out the image↔display mapping so the agent can budget its
// pointing precision instead of deriving it from meta (field report
// marble-peer-region-capture: 1.5× downscale + a small mirror pane put tap targets at ~5 px).
func screenshotPrecision(meta map[string]interface{}, regionCap bool) string {
	if meta == nil {
		return ""
	}
	num := func(v interface{}) float64 {
		f, _ := v.(float64)
		return f
	}
	scale := num(meta["scale"])
	down, known := meta["downscaled"].(bool)
	if !known {
		down = scale > 1.01 // peers before caps.region
	}
	var parts []string
	if reg, ok := meta["region"].(map[string]interface{}); ok {
		full := num(reg["x"]) == 0 && num(reg["y"]) == 0 &&
			num(reg["w"]) == num(meta["screen_w"]) && num(reg["h"]) == num(meta["screen_h"])
		if !full {
			parts = append(parts, fmt.Sprintf("zoomed view: display region x=%.0f y=%.0f w=%.0f h=%.0f of %.0f×%.0f at %.2f image px per display px. Clicks use THIS image's pixels; post-click shots stay zoomed until a screenshot without region.",
				num(reg["x"]), num(reg["y"]), num(reg["w"]), num(reg["h"]), num(meta["screen_w"]), num(meta["screen_h"]), num(meta["zoom"])))
		}
	}
	if down && scale > 0 {
		p := fmt.Sprintf("image is %.2f× downscaled (1 image px = %.2f display px); targets under ~10 image px are unreliable.", scale, scale)
		if regionCap {
			p += " For small targets re-shoot with region={x,y,w,h} in this image's pixels for native detail."
		} else {
			p += " This peer predates region capture; upgrade marble-desktop-peer for native-detail crops."
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " ")
}

func (r *Registry) peerCall(tc *TurnContext, computerID, kind string, payload interface{}, deadline time.Duration) (peerhub.Envelope, error) {
	return r.peerCallID(tc, computerID, "", kind, payload, deadline)
}

func (r *Registry) peerCallID(tc *TurnContext, computerID, actionID, kind string, payload interface{}, deadline time.Duration) (peerhub.Envelope, error) {
	if r.PeerHub == nil {
		return peerhub.Envelope{}, fmt.Errorf("peer hub not configured")
	}
	cid, err := r.resolveComputerID(tc, computerID)
	if err != nil {
		return peerhub.Envelope{}, err
	}
	conn := r.PeerHub.Get(cid)
	if conn == nil {
		return peerhub.Envelope{}, fmt.Errorf("computer %q offline", cid)
	}
	// A peer paired with several harnesses only obeys the lock holder. Held
	// until this session's turn ends (session loop → PeerHub.ReleaseLocks).
	holder := ""
	if tc != nil {
		holder = tc.SessionID
	}
	if holder == "" {
		// No session turn to release at — hold the lock for this call only.
		holder = "call:" + uuid.NewString()
		defer r.PeerHub.ReleaseLocks(holder)
	}
	if err := r.PeerHub.AcquireLock(cid, holder); err != nil {
		return peerhub.Envelope{}, err
	}
	var lastErr error
	relocked := false
	for attempt := 0; attempt < 3; attempt++ {
		if tc != nil && tc.Ctx != nil {
			select {
			case <-tc.Ctx.Done():
				return peerhub.Envelope{}, tc.Ctx.Err()
			default:
			}
		}
		var res peerhub.Envelope
		var callErr error
		if actionID != "" {
			res, callErr = conn.CallWithID(actionID, kind, payload, deadline)
		} else {
			res, callErr = conn.Call(kind, payload, deadline)
		}
		if callErr == nil {
			return res, nil
		}
		lastErr = callErr
		if res.Meta["lock_required"] == true && !relocked {
			// Lock was cleared from the peer tray (or lost) — try once to retake it.
			relocked = true
			conn.MarkLockLost()
			if err := r.PeerHub.AcquireLock(cid, holder); err != nil {
				return peerhub.Envelope{}, err
			}
			continue
		}
		msg := callErr.Error()
		if !strings.Contains(msg, "peer busy") && !strings.Contains(msg, "action queue") {
			return peerhub.Envelope{}, callErr
		}
		time.Sleep(time.Duration(200+attempt*150) * time.Millisecond)
	}
	return peerhub.Envelope{}, lastErr
}

// desktopClickNeedsScreenshot is how fresh a computer_screenshot must be before
// desktop_click is allowed (coords must come from vision, not guessing).
const desktopClickNeedsScreenshot = 90 * time.Second

// postClickScreenshotGrace: reject redundant computer_screenshot right after a post-click shot.
const postClickScreenshotGrace = 8 * time.Second

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (r *Registry) notePeerAction(tc *TurnContext, summary string) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return
	}
	if len(summary) > 200 {
		summary = summary[:200] + "…"
	}
	if tc != nil && tc.OnPeerAction != nil {
		tc.OnPeerAction(summary)
	}
	if r.SetLastPeerAction != nil && tc != nil && tc.SessionID != "" {
		r.SetLastPeerAction(tc.SessionID, summary)
	}
}

// stagePeerScreenshot stores JPEG bytes as a chat attachment and updates TurnContext.
func (r *Registry) stagePeerScreenshot(tc *TurnContext, raw []byte, name string) (attachmentID, mime, kind, hash string, err error) {
	if r.StageChatAttachment == nil || tc == nil {
		return "", "", "", "", fmt.Errorf("screenshot staging unavailable")
	}
	if name == "" {
		name = "screenshot.jpg"
	}
	hash = sha256Hex(raw)
	id, mime, kind, err := r.StageChatAttachment(tc.SessionID, name, raw, "")
	if err != nil {
		return "", "", "", hash, err
	}
	if tc.OnChatAttachment != nil {
		tc.OnChatAttachment(Attachment{
			Path: id, Name: name, Inline: true, Mime: mime, Size: int64(len(raw)),
		})
	}
	tc.LastScreenshotAt = time.Now()
	tc.LastScreenshotHash = hash
	tc.LastScreenshotAttID = id
	tc.AttachedSinceRequest = append(tc.AttachedSinceRequest, id)
	return id, mime, kind, hash, nil
}

// focusedApp reads meta.focused_app. Empty when the peer did not report one.
func focusedApp(meta map[string]interface{}) string {
	if meta == nil {
		return ""
	}
	s, _ := meta["focused_app"].(string)
	return strings.TrimSpace(s)
}

// noteFocusedApp remembers the peer's focused app for the next desktop action.
func noteFocusedApp(tc *TurnContext, meta map[string]interface{}) {
	if tc == nil {
		return
	}
	if app := focusedApp(meta); app != "" {
		tc.LastFocusedApp = app
	}
}

// focusDrifted reports that type/key input was delivered to a different app
// than the one frontmost before the action. A click is allowed to move focus;
// that is how the next type learns its target. An empty baseline is not a
// change: there is no expected window to name, so expected_app would be a lie.
func focusDrifted(action, pre, post string) bool {
	if action != "type" && action != "key" {
		return false
	}
	pre, post = strings.TrimSpace(pre), strings.TrimSpace(post)
	return pre != "" && post != "" && pre != post
}

func focusChangedHint(post, pre string) string {
	return fmt.Sprintf("focus_changed: input was received by %q, not %q (the app that was frontmost before this action). The text did NOT land in the intended window. Click the intended field or pass expect_app so the peer activates it, then look at the post-action image before typing again.", post, pre)
}

func putExpectApp(payload map[string]interface{}, app string) {
	app = strings.TrimSpace(app)
	if app == "" || payload == nil {
		return
	}
	payload["expect_app"] = app
}

// ImageStillInWindow reports whether attID will actually be sent to the model.
// Unknown window (the loop has not recorded one) is not visible: skipping
// would point at an image we cannot prove the model can see.
func (tc *TurnContext) ImageStillInWindow(attID string) bool {
	if tc == nil || attID == "" || !tc.RetainedWindowKnown {
		return false
	}
	if tc.ImageLimitEffective <= 0 {
		return true // unlimited: the cap does not drop images
	}
	if tc.RetainedImageIDs[attID] {
		return true
	}
	slots := tc.ImageLimitEffective - tc.RetainedUserImages
	if slots <= 0 {
		return false
	}
	n := len(tc.AttachedSinceRequest)
	if n > slots {
		// Newest attachments take the slots left after the latest user message.
		for _, id := range tc.AttachedSinceRequest[n-slots:] {
			if id == attID {
				return true
			}
		}
		return false
	}
	for _, id := range tc.AttachedSinceRequest {
		if id == attID {
			return true
		}
	}
	return false
}

func (r *Registry) computerScreenshot(argsJSON string, tc *TurnContext) (string, error) {
	var args struct {
		ComputerID string                 `json:"computer_id"`
		Region     map[string]interface{} `json:"region"`
		Space      string                 `json:"space"`
		Scale      float64                `json:"scale"`
		MaxEdge    *int                   `json:"max_edge"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &args)
	payload := map[string]interface{}{}
	if len(args.Region) > 0 {
		payload["region"] = args.Region
		if sp := strings.TrimSpace(args.Space); sp != "" {
			payload["space"] = sp
		}
	}
	if args.Scale > 0 {
		payload["scale"] = args.Scale
	}
	if args.MaxEdge != nil {
		payload["max_edge"] = *args.MaxEdge
	}
	caps, _ := r.peerCaps(tc, args.ComputerID)
	if len(payload) > 0 && !caps.Region {
		return "", fmt.Errorf("this computer's marble-desktop-peer predates region capture (caps.region); upgrade the peer, or take a full screenshot without region/scale/max_edge")
	}

	// Suppress redundant shots right after an atomic post-click screenshot
	// (a deliberate region/scale request is never redundant). Only when that
	// attachment is still inside the retained image window — otherwise the
	// hint points at pixels the cap already dropped.
	if len(payload) == 0 && tc != nil && !tc.PostClickShotAt.IsZero() && time.Since(tc.PostClickShotAt) < postClickScreenshotGrace && tc.LastScreenshotAttID != "" && tc.ImageStillInWindow(tc.LastScreenshotAttID) {
		out := map[string]interface{}{
			"ok":            true,
			"skipped":       true,
			"attachment_id": tc.LastScreenshotAttID,
			"hint":          "Skipped redundant screenshot — a post-click image was just attached and is still in the images sent to you. LOOK at that attachment_id (do not re-screenshot). Next: click_button / different coords / computer_confirm.",
		}
		b, _ := json.Marshal(out)
		return string(b), nil
	}

	res, err := r.peerCall(tc, args.ComputerID, "screenshot", payload, 120*time.Second)
	if err != nil {
		return "", err
	}
	noteFocusedApp(tc, res.Meta)
	out := map[string]interface{}{
		"ok":   true,
		"meta": res.Meta,
		"hint": "Screenshot image is attached for vision. Coords are in IMAGE pixel space (meta.w×meta.h); meta.scale maps to screen. LOOK at the image. If lock/login screen: DESKTOP LOCKED — stop. Prefer click_button/click_text for labeled buttons; desktop click only from visible pixels. OTP/SMS → computer_confirm.",
	}
	if p := screenshotPrecision(res.Meta, caps.Region); p != "" {
		out["precision"] = p
	}
	if res.Text != "" {
		out["lock_warning"] = res.Text
	}
	if res.Meta != nil {
		if locked, ok := res.Meta["locked"].(bool); ok && locked {
			out["desktop_locked"] = true
		}
	}
	if res.ScreenshotB64 != "" {
		raw, decErr := base64.StdEncoding.DecodeString(res.ScreenshotB64)
		if decErr == nil && tc != nil {
			id, mime, kind, hash, stErr := r.stagePeerScreenshot(tc, raw, "screenshot.jpg")
			if stErr == nil {
				out["attachment_id"] = id
				out["mime"] = mime
				out["kind"] = kind
				out["sha256"] = hash
				out["note"] = "screenshot stored as chat attachment"
				r.notePeerAction(tc, "screenshot "+fmt.Sprintf("%vx%v", res.Meta["w"], res.Meta["h"]))
				b, _ := json.Marshal(out)
				return string(b), nil
			}
		}
		out["screenshot_b64_len"] = len(res.ScreenshotB64)
		out["note"] = "screenshot captured (base64 length only; staging unavailable)"
		if tc != nil {
			tc.LastScreenshotAt = time.Now()
		}
	}
	r.notePeerAction(tc, "screenshot")
	b, _ := json.Marshal(out)
	return string(b), nil
}

// computerExec runs a shell command on the bound peer and returns
// stdout/stderr/exit_code as text — the P0 fix for field report
// peer-gui-loop-report (2026-09-23): reading peer state no longer requires
// opening a terminal window and reading a screenshot of it.
func (r *Registry) computerExec(argsJSON string, tc *TurnContext) (string, error) {
	var args struct {
		ComputerID string `json:"computer_id"`
		Command    string `json:"command"`
		CWD        string `json:"cwd"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Command) == "" {
		return "", fmt.Errorf("command is required")
	}
	deadline := 90 * time.Second
	if args.TimeoutSec > 0 {
		deadline = time.Duration(args.TimeoutSec+15) * time.Second // give the peer a margin over its own timeout
	}
	if deadline > 5*time.Minute {
		deadline = 5 * time.Minute
	}
	res, err := r.peerCall(tc, args.ComputerID, "computer_exec", map[string]interface{}{
		"command": args.Command, "cwd": args.CWD, "timeout_sec": args.TimeoutSec,
	}, deadline)
	if err != nil {
		return "", err
	}
	out := map[string]interface{}{"ok": res.OK}
	if res.Meta != nil {
		for _, k := range []string{"stdout", "stderr", "exit_code", "shell", "truncated", "timed_out"} {
			if v, ok := res.Meta[k]; ok {
				out[k] = v
			}
		}
	}
	r.notePeerAction(tc, "computer_exec "+thrashTrunc(args.Command, 80))
	if !res.OK && res.Error != "" {
		b, _ := json.Marshal(out)
		return "", fmt.Errorf("%s\n%s", res.Error, string(b))
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (r *Registry) computerDesktopAct(argsJSON string, tc *TurnContext) (string, error) {
	var args struct {
		ComputerID string `json:"computer_id"`
		Action     string `json:"action"`
		X          int    `json:"x"`
		Y          int    `json:"y"`
		Text       string `json:"text"`
		Key        string `json:"key"`
		Button     string `json:"button"`
		Zoom       bool   `json:"zoom"`
		ExpectApp  string `json:"expect_app"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	action := strings.ToLower(strings.TrimSpace(args.Action))
	zoomNote := ""
	var kind string
	var payload map[string]interface{}
	switch action {
	case "click":
		if tc == nil || tc.LastScreenshotAt.IsZero() || time.Since(tc.LastScreenshotAt) > desktopClickNeedsScreenshot {
			return "", fmt.Errorf("desktop click requires a recent computer_screenshot (within %s). Call computer_screenshot, LOOK at the image (meta.w×meta.h image space), pick x,y, then computer_desktop_act action=click", desktopClickNeedsScreenshot)
		}
		btn := strings.TrimSpace(args.Button)
		if btn == "" || btn == "0" {
			btn = "1"
		}
		kind = "desktop_click"
		payload = map[string]interface{}{"x": args.X, "y": args.Y, "button": btn}
		if args.Zoom {
			if caps, _ := r.peerCaps(tc, args.ComputerID); caps.Region {
				payload["zoom"] = true
			} else {
				zoomNote = "zoom ignored: this marble-desktop-peer predates region capture (caps.region); upgrade the peer"
			}
		}
	case "type":
		kind = "desktop_type"
		payload = map[string]interface{}{"text": args.Text}
	case "key":
		kind = "desktop_key"
		payload = map[string]interface{}{"key": args.Key}
	default:
		return "", fmt.Errorf("unknown action %q (click|type|key). For labeled UI use computer_browser_act action=click_button", args.Action)
	}
	putExpectApp(payload, args.ExpectApp)
	preHash := ""
	preApp := ""
	if tc != nil {
		preHash = tc.LastScreenshotHash
		preApp = tc.LastFocusedApp
	}
	res, err := r.peerCall(tc, args.ComputerID, kind, payload, 120*time.Second)
	if err != nil {
		return "", err
	}
	out := map[string]interface{}{"ok": res.OK}
	if res.Text != "" {
		out["text"] = res.Text
	}
	if res.Meta != nil {
		out["meta"] = res.Meta
	}
	if !res.OK && res.Error != "" {
		return "", fmt.Errorf("%s", res.Error)
	}
	noteFocusedApp(tc, res.Meta)
	postApp := focusedApp(res.Meta)

	switch action {
	case "click":
		if tc != nil {
			tc.LastClickX, tc.LastClickY, tc.LastClickSet = args.X, args.Y, true
		}
		r.notePeerAction(tc, fmt.Sprintf("desktop_click (%d,%d)", args.X, args.Y))
		r.attachPostActionScreenshot(tc, args.ComputerID, res, preHash, out,
			"pixels identical after click — STOP repeating these coords. NEXT: computer_browser_act action=click_button text=\"…\", open a direct URL, or computer_confirm for a human click.")
		if zoomNote != "" {
			out["zoom_note"] = zoomNote
		}
		if caps, _ := r.peerCaps(tc, args.ComputerID); res.Meta != nil {
			if p := screenshotPrecision(res.Meta, caps.Region); p != "" {
				out["precision"] = p
			}
		}
	case "type", "key":
		r.notePeerAction(tc, "desktop_"+action)
		unchanged := "pixels identical after typing — the text likely did not land in a focused, editable field. STOP retyping the same text. NEXT: computer_screenshot to see where focus is and click the target field first, or computer_exec if this was meant to run a shell command (reads real stdout/stderr instead of a screenshot)."
		if action == "key" {
			unchanged = "pixels identical after key — the key likely did not land in the focused window. STOP repeating it. NEXT: computer_screenshot to see where focus is and click the target first."
		}
		r.attachPostActionScreenshot(tc, args.ComputerID, res, preHash, out, unchanged)
	}
	// Focus drift wins over the pixel-hash hint: a stolen focus repaints the
	// region, so ui_unchanged stays false and the keystrokes would otherwise
	// look successful.
	if focusDrifted(action, preApp, postApp) {
		out["ok"] = false
		out["focus_changed"] = true
		out["typed_into"] = postApp
		out["focused_app"] = postApp
		out["expected_app"] = preApp
		out["hint"] = focusChangedHint(postApp, preApp)
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// attachPostActionScreenshot stages the peer's atomic post-action screenshot
// (click, type, and key return one on current peers) and flags ui_unchanged
// when its hash matches the screenshot taken before the action. Originally
// click-only; generalized to type per field report peer-gui-loop-report
// (2026-09-23), which found no "did my keystrokes land?" signal for type at
// all — an agent retyped the same command into an unfocused window ~150 times
// with no visible failure. Key uses the same path. An older peer that returns
// no image falls through to a follow-up screenshot; if that also fails, the
// action result still succeeds and records post_click_screenshot_error.
func (r *Registry) attachPostActionScreenshot(tc *TurnContext, computerID string, res peerhub.Envelope, preHash string, out map[string]interface{}, unchangedHint string) {
	var raw []byte
	if res.ScreenshotB64 != "" {
		raw, _ = base64.StdEncoding.DecodeString(res.ScreenshotB64)
	}
	// Fallback nested shot if peer did not return one (older peers, or type
	// on a peer built before it started returning post-action screenshots).
	if len(raw) == 0 {
		shotArgs, _ := json.Marshal(map[string]string{"computer_id": computerID})
		if tc != nil {
			tc.PostClickShotAt = time.Time{} // don't let the nested call get skipped as "redundant"
		}
		shot, shotErr := r.computerScreenshot(string(shotArgs), tc)
		if shotErr != nil {
			out["post_click_screenshot_error"] = shotErr.Error()
			return
		}
		out["post_click_screenshot"] = json.RawMessage(shot)
		out["hint"] = "post-action screenshot attached — look at the image before repeating (do not re-screenshot)"
		if tc != nil {
			tc.PostClickShotAt = time.Now()
		}
		if preHash != "" && tc != nil && tc.LastScreenshotHash == preHash {
			out["ui_unchanged"] = true
			out["ok"] = false
			out["hint"] = "ui_unchanged: " + unchangedHint
		}
		return
	}
	if tc == nil {
		return
	}
	id, mime, kindName, hash, stErr := r.stagePeerScreenshot(tc, raw, "post-action.jpg")
	if stErr != nil {
		out["post_click_screenshot_error"] = stErr.Error()
		return
	}
	tc.PostClickShotAt = time.Now()
	out["attachment_id"] = id // top-level for multimodal extract
	out["mime"] = mime
	out["kind"] = kindName
	out["sha256"] = hash
	out["hint"] = "post-action screenshot attached — look at the image before repeating (do not call computer_screenshot again)"
	out["post_click_screenshot"] = map[string]interface{}{
		"attachment_id": id, "mime": mime, "kind": kindName, "meta": res.Meta,
	}
	if preHash != "" && hash == preHash {
		out["ui_unchanged"] = true
		out["ok"] = false
		out["hint"] = "ui_unchanged: " + unchangedHint
	}
}

func (r *Registry) computerBrowser(argsJSON string, tc *TurnContext, kind string) (string, error) {
	var args map[string]interface{}
	_ = json.Unmarshal([]byte(argsJSON), &args)
	if args == nil {
		args = map[string]interface{}{}
	}
	cid, _ := args["computer_id"].(string)
	delete(args, "computer_id")
	res, err := r.peerCall(tc, cid, kind, args, 120*time.Second)
	if err != nil {
		msg := err.Error()
		// Steer agent off CDP retry loops into visual desktop path.
		if strings.Contains(msg, "cdp timeout") || strings.Contains(msg, "not_found") ||
			strings.Contains(msg, "bot_wall") || strings.Contains(msg, "snapshot error") ||
			strings.Contains(msg, "ambiguous") {
			msg += " — NEXT: computer_screenshot (do not keep retrying CDP); then click_button / desktop coords from the image, or computer_confirm if a human/OTP step is needed"
			if shot, shotErr := r.computerScreenshot(mustJSON(map[string]string{"computer_id": cid}), tc); shotErr == nil {
				msg += "\n// auto-screenshot after CDP fail:\n" + shot
			}
		}
		r.notePeerAction(tc, kind+" err")
		return "", fmt.Errorf("%s", msg)
	}
	act, _ := args["action"].(string)
	if act != "" {
		r.notePeerAction(tc, "browser_"+strings.ToLower(act))
	} else {
		r.notePeerAction(tc, kind)
	}
	if res.Text != "" {
		text := res.Text
		if strings.Contains(text, "snapshot error") || strings.Contains(text, "cdp timeout") ||
			strings.Contains(text, "akam-sw") || strings.Contains(text, "bot_wall") ||
			strings.Contains(text, "bot/WAF") {
			text += "\n// NEXT: computer_screenshot then desktop click or report UI; stop CDP retries"
			if shot, shotErr := r.computerScreenshot(mustJSON(map[string]string{"computer_id": cid}), tc); shotErr == nil {
				text += "\n// auto-screenshot after CDP fail hint:\n" + shot
			}
		}
		return text, nil
	}
	b, _ := json.Marshal(map[string]interface{}{"ok": res.OK, "meta": res.Meta})
	return string(b), nil
}

// computerBrowserEnsure attaches to or launches the operator's Chrome with CDP on the peer.
// force=true quits a non-debug Chrome holding the profile and relaunches with remote debugging
// (cookies stay on disk). Prefer force=false first; use computer_confirm before force=true.
func (r *Registry) computerBrowserEnsure(argsJSON string, tc *TurnContext) (string, error) {
	var args struct {
		ComputerID string `json:"computer_id"`
		Force      bool   `json:"force"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &args)
	res, err := r.peerCall(tc, args.ComputerID, "browser_ensure", map[string]interface{}{
		"force": args.Force,
	}, 90*time.Second)
	if err != nil {
		// still return peer text if any (structured ensure error)
		if res.Text != "" {
			return "", fmt.Errorf("%v | %s", err, res.Text)
		}
		return "", err
	}
	if res.Text != "" {
		return res.Text, nil
	}
	b, _ := json.Marshal(map[string]interface{}{"ok": res.OK, "meta": res.Meta})
	return string(b), nil
}

func (r *Registry) computerConfirm(argsJSON string, tc *TurnContext) (string, error) {
	var args struct {
		ComputerID string `json:"computer_id"`
		Prompt     string `json:"prompt"`
		Risk       string `json:"risk"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &args)
	if strings.TrimSpace(args.Prompt) == "" {
		return "", fmt.Errorf("prompt required")
	}
	cid, err := r.resolveComputerID(tc, args.ComputerID)
	if err != nil {
		return "", err
	}
	// Shared id so harness UI Accept/Deny and peer mini-UI hit the same waitConfirm.
	confirmID := uuid.NewString()

	sessionID := ""
	if tc != nil {
		sessionID = tc.SessionID
	}
	expires := time.Now().Add(120 * time.Second)
	harnessURL := ""
	if r.PublicBaseURL != nil {
		if base := strings.TrimRight(strings.TrimSpace(r.PublicBaseURL()), "/"); base != "" {
			harnessURL = base + "/confirm/" + confirmID
		}
	}
	if r.PeerHub != nil {
		r.PeerHub.PutConfirm(peerhub.PendingConfirm{
			ID:         confirmID,
			SessionID:  sessionID,
			ComputerID: cid,
			Prompt:     args.Prompt,
			Risk:       args.Risk,
			URL:        harnessURL,
			CreatedAt:  time.Now(),
			ExpiresAt:  expires,
		})
		// Do NOT defer DeleteConfirm — keep until resolve/expiry so UI can still dismiss stale cards.
	}
	// Notify Marble UI (SSE) so operator can Accept without peer access.
	if tc != nil && tc.OnPeerConfirm != nil {
		tc.OnPeerConfirm(map[string]interface{}{
			"id":          confirmID,
			"session_id":  sessionID,
			"computer_id": cid,
			"prompt":      args.Prompt,
			"risk":        args.Risk,
			"url":         harnessURL,
			"expires_at":  expires.UTC().Format(time.RFC3339),
			"source":      "computer_confirm",
		})
	}

	payload := map[string]interface{}{
		"prompt": args.Prompt,
		"risk":   args.Risk,
	}
	if harnessURL != "" {
		// Peer notification should open the Tailscale-reachable harness page, not loopback mini-UI.
		payload["harness_url"] = harnessURL
	}
	res, err := r.peerCallID(tc, cid, confirmID, "confirm", payload, 125*time.Second)
	if err != nil {
		// Leave pending confirm for harness dismiss (stale/deny).
		return "", err
	}
	accepted := res.OK
	if r.PeerHub != nil && accepted {
		r.PeerHub.DeleteConfirm(confirmID)
	}
	out := map[string]interface{}{
		"accepted":    accepted,
		"ok":          accepted,
		"confirm_id":  confirmID,
		"computer_id": cid,
	}
	if harnessURL != "" {
		out["url"] = harnessURL
	}
	if res.Text != "" {
		out["detail"] = res.Text
	}
	if !accepted {
		out["hint"] = "Denied or timed out (120s default deny). Use the Marble confirm card or open the harness /confirm/{id} link (Tailscale-reachable). Deny/Dismiss clears stale cards."
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (r *Registry) computerStop(argsJSON string, tc *TurnContext) (string, error) {
	var args struct {
		ComputerID string `json:"computer_id"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &args)
	cid, err := r.resolveComputerID(tc, args.ComputerID)
	if err != nil {
		return "", err
	}
	if r.PeerHub == nil {
		return "", fmt.Errorf("no hub")
	}
	conn := r.PeerHub.Get(cid)
	if conn == nil {
		return "", fmt.Errorf("offline")
	}
	conn.Cancel()
	return `{"ok":true,"stopped":true}`, nil
}
