package tools

import (
	"encoding/base64"
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
	if actionID != "" {
		return conn.CallWithID(actionID, kind, payload, deadline)
	}
	return conn.Call(kind, payload, deadline)
}

// desktopClickNeedsScreenshot is how fresh a computer_screenshot must be before
// desktop_click is allowed (coords must come from vision, not guessing).
const desktopClickNeedsScreenshot = 90 * time.Second

func (r *Registry) computerScreenshot(argsJSON string, tc *TurnContext) (string, error) {
	var args struct {
		ComputerID string `json:"computer_id"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &args)
	res, err := r.peerCall(tc, args.ComputerID, "screenshot", map[string]interface{}{}, 120*time.Second)
	if err != nil {
		return "", err
	}
	out := map[string]interface{}{
		"ok":   true,
		"meta": res.Meta,
		// Nudge models: after a shot, prefer desktop click coords or describe the UI.
		"hint": "Screenshot image is attached for vision. LOOK at the image. If it is a lock/login screen, say DESKTOP LOCKED and stop clicking. If CDP was failing, use coords from the image with computer_desktop_act or describe the UI. OTP/SMS cannot be automated.",
	}
	if res.Text != "" {
		out["lock_warning"] = res.Text
	}
	if res.Meta != nil {
		if locked, ok := res.Meta["locked"].(bool); ok && locked {
			out["desktop_locked"] = true
		}
	}
	if res.ScreenshotB64 != "" && r.StageChatAttachment != nil && tc != nil {
		raw, err := base64.StdEncoding.DecodeString(res.ScreenshotB64)
		if err == nil {
			id, mime, kind, err := r.StageChatAttachment(tc.SessionID, "screenshot.jpg", raw)
			if err == nil {
				out["attachment_id"] = id
				out["mime"] = mime
				out["kind"] = kind
				if tc.OnChatAttachment != nil {
					tc.OnChatAttachment(Attachment{
						Path:   id,
						Name:   "screenshot.jpg",
						Inline: true,
						Mime:   mime,
						Size:   int64(len(raw)),
					})
				}
				// don't dump full base64 into tool result
				out["note"] = "screenshot stored as chat attachment"
				tc.LastScreenshotAt = time.Now()
				b, _ := json.Marshal(out)
				return string(b), nil
			}
		}
	}
	// fallback include truncated note
	if res.ScreenshotB64 != "" {
		out["screenshot_b64_len"] = len(res.ScreenshotB64)
		out["note"] = "screenshot captured (base64 length only; staging unavailable)"
		if tc != nil {
			tc.LastScreenshotAt = time.Now()
		}
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
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	action := strings.ToLower(strings.TrimSpace(args.Action))
	var kind string
	var payload map[string]interface{}
	switch action {
	case "click":
		// Require a recent screenshot so x,y are chosen from vision, not blind guesses.
		if tc == nil || tc.LastScreenshotAt.IsZero() || time.Since(tc.LastScreenshotAt) > desktopClickNeedsScreenshot {
			return "", fmt.Errorf("desktop click requires a recent computer_screenshot (within %s). Call computer_screenshot, LOOK at the image, pick x,y, then computer_desktop_act action=click", desktopClickNeedsScreenshot)
		}
		// Default left button — empty string causes xdotool BadValue on XWayland.
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
		return "", fmt.Errorf("unknown action %q (click|type|key). For Gmail use computer_browser_act action=open_gmail instead", args.Action)
	}
	res, err := r.peerCall(tc, args.ComputerID, kind, payload, 120*time.Second)
	if err != nil {
		return "", err
	}
	out := map[string]interface{}{"ok": res.OK}
	if res.Text != "" {
		out["text"] = res.Text
	}
	if !res.OK && res.Error != "" {
		return "", fmt.Errorf("%s", res.Error)
	}
	// After click: auto-screenshot so the model sees the post-click UI without an extra round-trip.
	if action == "click" {
		shotArgs, _ := json.Marshal(map[string]string{"computer_id": args.ComputerID})
		if shot, shotErr := r.computerScreenshot(string(shotArgs), tc); shotErr == nil {
			out["post_click_screenshot"] = json.RawMessage(shot)
			out["hint"] = "post-click screenshot attached — look at the image before the next click"
		} else {
			out["post_click_screenshot_error"] = shotErr.Error()
		}
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
			strings.Contains(msg, "bot_wall") || strings.Contains(msg, "snapshot error") {
			msg += " — NEXT: computer_screenshot (do not keep retrying CDP); then computer_desktop_act with x,y from the image, or computer_confirm if a human/OTP step is needed"
			// One free screenshot so the model is not blind after CDP failure.
			if shot, shotErr := r.computerScreenshot(mustJSON(map[string]string{"computer_id": cid}), tc); shotErr == nil {
				msg += "\n// auto-screenshot after CDP fail:\n" + shot
			}
		}
		return "", fmt.Errorf("%s", msg)
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
	if r.PeerHub != nil {
		r.PeerHub.PutConfirm(peerhub.PendingConfirm{
			ID:         confirmID,
			SessionID:  sessionID,
			ComputerID: cid,
			Prompt:     args.Prompt,
			Risk:       args.Risk,
			CreatedAt:  time.Now(),
			ExpiresAt:  expires,
		})
		defer r.PeerHub.DeleteConfirm(confirmID)
	}
	// Notify Marble UI (SSE) so operator can Accept without peer access.
	if tc != nil && tc.OnPeerConfirm != nil {
		tc.OnPeerConfirm(map[string]interface{}{
			"id":          confirmID,
			"session_id":  sessionID,
			"computer_id": cid,
			"prompt":      args.Prompt,
			"risk":        args.Risk,
			"expires_at":  expires.UTC().Format(time.RFC3339),
			"source":      "computer_confirm",
		})
	}

	res, err := r.peerCallID(tc, cid, confirmID, "confirm", map[string]interface{}{
		"prompt": args.Prompt,
		"risk":   args.Risk,
	}, 125*time.Second)
	if err != nil {
		return "", err
	}
	accepted := res.OK
	out := map[string]interface{}{
		"accepted":    accepted,
		"ok":          accepted,
		"confirm_id":  confirmID,
		"computer_id": cid,
	}
	if res.Text != "" {
		out["detail"] = res.Text
	}
	if !accepted {
		out["hint"] = "Denied or timed out (120s default deny). Accept from Marble UI (confirm card), peer tray, peer mini-UI, or desktop notification."
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
