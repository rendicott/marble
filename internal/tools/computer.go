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
	b, _ := json.Marshal(map[string]interface{}{
		"computer_id": strings.TrimSpace(args.ComputerID),
		"session_id":  tc.SessionID,
	})
	return string(b), nil
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
	var lastErr error
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
	return id, mime, kind, hash, nil
}

func (r *Registry) computerScreenshot(argsJSON string, tc *TurnContext) (string, error) {
	var args struct {
		ComputerID string `json:"computer_id"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &args)

	// Suppress redundant shots right after an atomic post-click screenshot.
	if tc != nil && !tc.PostClickShotAt.IsZero() && time.Since(tc.PostClickShotAt) < postClickScreenshotGrace && tc.LastScreenshotAttID != "" {
		out := map[string]interface{}{
			"ok":            true,
			"skipped":       true,
			"attachment_id": tc.LastScreenshotAttID,
			"hint":          "Skipped redundant screenshot — a post-click image was just attached. LOOK at that attachment_id (do not re-screenshot). Next: click_button / different coords / computer_confirm.",
		}
		b, _ := json.Marshal(out)
		return string(b), nil
	}

	res, err := r.peerCall(tc, args.ComputerID, "screenshot", map[string]interface{}{}, 120*time.Second)
	if err != nil {
		return "", err
	}
	out := map[string]interface{}{
		"ok":   true,
		"meta": res.Meta,
		"hint": "Screenshot image is attached for vision. Coords are in IMAGE pixel space (meta.w×meta.h); meta.scale maps to screen. LOOK at the image. If lock/login screen: DESKTOP LOCKED — stop. Prefer click_button/click_text for labeled buttons; desktop click only from visible pixels. OTP/SMS → computer_confirm.",
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

func (r *Registry) computerDesktopAct(argsJSON string, tc *TurnContext) (string, error) {
	var args struct {
		ComputerID string `json:"computer_id"`
		Action     string `json:"action"`
		X          int    `json:"x"`
		Y          int    `json:"y"`
		Text       string `json:"text"`
		Key        string `json:"key"`
		Button     string `json:"button"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	action := strings.ToLower(strings.TrimSpace(args.Action))
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
	case "type":
		kind = "desktop_type"
		payload = map[string]interface{}{"text": args.Text}
	case "key":
		kind = "desktop_key"
		payload = map[string]interface{}{"key": args.Key}
	default:
		return "", fmt.Errorf("unknown action %q (click|type|key). For labeled UI use computer_browser_act action=click_button", args.Action)
	}
	preHash := ""
	if tc != nil {
		preHash = tc.LastScreenshotHash
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

	if action == "click" {
		if tc != nil {
			tc.LastClickX, tc.LastClickY, tc.LastClickSet = args.X, args.Y, true
		}
		r.notePeerAction(tc, fmt.Sprintf("desktop_click (%d,%d)", args.X, args.Y))

		var raw []byte
		if res.ScreenshotB64 != "" {
			raw, _ = base64.StdEncoding.DecodeString(res.ScreenshotB64)
		}
		// Fallback nested shot if peer did not return one (older peers).
		if len(raw) == 0 {
			shotArgs, _ := json.Marshal(map[string]string{"computer_id": args.ComputerID})
			// Temporarily clear PostClickShotAt so nested call is not skipped.
			if tc != nil {
				tc.PostClickShotAt = time.Time{}
			}
			if shot, shotErr := r.computerScreenshot(string(shotArgs), tc); shotErr == nil {
				out["post_click_screenshot"] = json.RawMessage(shot)
				out["hint"] = "post-click screenshot attached — look at the image before the next click (do not re-screenshot)"
				if tc != nil {
					tc.PostClickShotAt = time.Now()
				}
				if preHash != "" && tc != nil && tc.LastScreenshotHash == preHash {
					out["ui_unchanged"] = true
					out["ok"] = false
					out["hint"] = "ui_unchanged: pixels identical after click — STOP repeating these coords. NEXT: computer_browser_act action=click_button text=\"…\", open a direct URL, or computer_confirm for a human click."
				}
				b, _ := json.Marshal(out)
				return string(b), nil
			} else {
				out["post_click_screenshot_error"] = shotErr.Error()
			}
		} else if tc != nil {
			id, mime, kindName, hash, stErr := r.stagePeerScreenshot(tc, raw, "post-click.jpg")
			if stErr == nil {
				tc.PostClickShotAt = time.Now()
				out["attachment_id"] = id // top-level for multimodal extract
				out["mime"] = mime
				out["kind"] = kindName
				out["sha256"] = hash
				out["hint"] = "post-click screenshot attached — look at the image before the next click (do not call computer_screenshot again)"
				out["post_click_screenshot"] = map[string]interface{}{
					"attachment_id": id, "mime": mime, "kind": kindName, "meta": res.Meta,
				}
				if preHash != "" && hash == preHash {
					out["ui_unchanged"] = true
					out["ok"] = false
					out["hint"] = "ui_unchanged: pixels identical after click — STOP repeating these coords. NEXT: computer_browser_act action=click_button text=\"…\", open a direct URL, or computer_confirm for a human click."
				}
			} else {
				out["post_click_screenshot_error"] = stErr.Error()
			}
		}
	} else {
		r.notePeerAction(tc, "desktop_"+action)
	}
	b, _ := json.Marshal(out)
	return string(b), nil
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
