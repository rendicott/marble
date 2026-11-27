package session

import (
	"strings"
	"testing"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/model"
	"github.com/rendicott/marble/internal/tools"
)

const (
	winUser = "11111111111111111111111111111111"
	winDrop = "22222222222222222222222222222222"
	winKeep = "33333333333333333333333333333333"
	winMiss = "44444444444444444444444444444444"
)

func toolShot(id, callID string) model.Message {
	return model.Message{
		Role:       "tool",
		Name:       "computer_screenshot",
		ToolCallID: callID,
		Content:    toolResultContent(`{"ok":true,"attachment_id":"` + id + `"}`),
	}
}

func assistantCall(callID string) model.Message {
	return model.Message{
		Role:    "assistant",
		Content: model.ContentFromText("shot"),
		ToolCalls: []model.ToolCall{{
			ID:   callID,
			Type: "function",
			Function: model.FunctionCall{
				Name:      "computer_screenshot",
				Arguments: "{}",
			},
		}},
	}
}

func TestScreenshotNoteOnlyWhenImageSurvivesCap(t *testing.T) {
	msgs := []model.Message{
		{Role: "user", Content: model.ContentFromParts([]model.ContentPart{
			{Type: "text", Text: "look"}, imgPart(winUser),
		})},
		toolShot(winDrop, "c-drop"),
		toolShot(winKeep, "c-keep"),
	}
	for _, m := range msgs {
		if strings.Contains(m.Content.PlainText(), "ARE the peer screenshot") {
			t.Fatal("toolResultContent stored the look instruction")
		}
	}
	capped, omitted := applyImageLimit(msgs, 2)
	if omitted != 1 {
		t.Fatalf("omitted=%d want 1", omitted)
	}
	sent := annotateSentImages(capped)
	if strings.Contains(capped[2].Content.PlainText(), "ARE the peer screenshot") {
		t.Fatal("annotate mutated the capped prompt")
	}

	userText := sent[0].Content.PlainText()
	if !sent[0].Content.HasImages() || strings.Contains(userText, "ARE the peer screenshot") {
		t.Fatalf("user image should survive without the peer-screenshot order: %+v", sent[0].Content)
	}
	dropText := sent[1].Content.PlainText()
	if sent[1].Content.HasImages() || strings.Contains(dropText, "ARE the peer screenshot") || !strings.Contains(dropText, "NOT sent") {
		t.Fatalf("dropped image still claimed present: %s", dropText)
	}
	keepText := sent[2].Content.PlainText()
	if !sent[2].Content.HasImages() || !strings.Contains(keepText, peerScreenshotNote) {
		t.Fatalf("kept image missing the look instruction: %+v", sent[2].Content)
	}
	again := annotateSentImages(sent)
	if strings.Count(again[2].Content.PlainText(), "ARE the peer screenshot") != 1 {
		t.Fatal("note duplicated")
	}
}

func TestOmittedImageTextSaysNotSent(t *testing.T) {
	got := omittedImageText(imgPart("abcd"), 2)
	if !strings.Contains(got, "NOT sent") || !strings.Contains(got, "abcd") || !strings.Contains(got, "fresh screenshot is required") {
		t.Fatalf("placeholder: %s", got)
	}
	low := strings.ToLower(got)
	if strings.Contains(low, "look at") || strings.Contains(got, "ARE the peer screenshot") {
		t.Fatalf("placeholder implies pixels are present: %s", got)
	}
	anon := omittedImageText(model.ContentPart{Type: "image_url", ImageURL: &model.ImageURL{URL: "data:image/png;base64,xx"}}, 2)
	if !strings.Contains(anon, "NOT sent") || strings.Contains(anon, "data:") || strings.Contains(strings.ToLower(anon), "look at") {
		t.Fatalf("anonymous placeholder: %s", anon)
	}
}

func TestReloadedToolResultDoesNotClaimImagePresent(t *testing.T) {
	m := Message{
		Role:    "tool",
		Content: `computer_screenshot → {"ok":true,"attachment_id":"` + winKeep + `"}`,
	}
	c := historyContentFromUIMessage(m)
	if !c.HasImages() {
		t.Fatal("reload dropped the image part")
	}
	if strings.Contains(c.PlainText(), "ARE the peer screenshot") {
		t.Fatal("reload baked in the look instruction")
	}
}

func TestNoteRetainedImagesAndStillInWindow(t *testing.T) {
	unknown := &tools.TurnContext{
		RetainedImageIDs:    map[string]bool{winKeep: true},
		ImageLimitEffective: 2,
	}
	if unknown.ImageStillInWindow(winKeep) {
		t.Fatal("unknown window must not count as visible")
	}
	var nilTC *tools.TurnContext
	if nilTC.ImageStillInWindow(winKeep) {
		t.Fatal("nil context")
	}

	msgs := []model.Message{
		{Role: "user", Content: model.ContentFromParts([]model.ContentPart{imgPart(winUser)})},
		{Role: "tool", Content: model.ContentFromParts([]model.ContentPart{imgPart(winDrop)})},
		{Role: "tool", Content: model.ContentFromParts([]model.ContentPart{imgPart(winKeep)})},
	}
	before := msgs[1].Content.PlainText()
	capped, omitted := applyImageLimit(msgs, 2)
	if omitted != 1 {
		t.Fatalf("omitted=%d want 1", omitted)
	}
	tc := &tools.TurnContext{AttachedSinceRequest: []string{"stale-from-prior-request"}}
	noteRetainedImages(tc, capped, 2)
	if msgs[1].Content.PlainText() != before {
		t.Fatal("noteRetainedImages changed prompt content")
	}
	if !tc.RetainedWindowKnown || tc.ImageLimitEffective != 2 {
		t.Fatalf("window %+v", tc)
	}
	if !tc.RetainedImageIDs[winUser] || !tc.RetainedImageIDs[winKeep] || tc.RetainedImageIDs[winDrop] {
		t.Fatalf("ids %+v", tc.RetainedImageIDs)
	}
	if tc.RetainedUserImages != 1 {
		t.Fatalf("user images %d", tc.RetainedUserImages)
	}
	if len(tc.AttachedSinceRequest) != 0 {
		t.Fatalf("AttachedSinceRequest not reset: %v", tc.AttachedSinceRequest)
	}
	if !tc.ImageStillInWindow(winKeep) || !tc.ImageStillInWindow(winUser) {
		t.Fatal("kept id should still be in the window")
	}
	if tc.ImageStillInWindow(winDrop) {
		t.Fatal("dropped id should not be in the window")
	}

	// Shots staged after this request fill the slots left by the latest user message.
	const olderStaged = "55555555555555555555555555555555"
	const newerStaged = "66666666666666666666666666666666"
	tc.AttachedSinceRequest = []string{olderStaged, newerStaged}
	if tc.ImageStillInWindow(olderStaged) {
		t.Fatal("older staged shot does not fit the remaining slot")
	}
	if !tc.ImageStillInWindow(newerStaged) {
		t.Fatal("newest staged shot should take the remaining slot")
	}
	if tc.ImageStillInWindow(winDrop) {
		t.Fatal("dropped id became visible after later shots")
	}

	uncapped := &tools.TurnContext{AttachedSinceRequest: []string{"stale"}}
	noteRetainedImages(uncapped, msgs, 0)
	if !uncapped.RetainedWindowKnown || uncapped.ImageLimitEffective != 0 || len(uncapped.AttachedSinceRequest) != 0 {
		t.Fatalf("unlimited window %+v", uncapped)
	}
	if !uncapped.RetainedImageIDs[winDrop] || uncapped.RetainedUserImages != 1 {
		t.Fatalf("unlimited ids %+v user %d", uncapped.RetainedImageIDs, uncapped.RetainedUserImages)
	}
	if !uncapped.ImageStillInWindow("not-yet-attached") {
		t.Fatal("unlimited cap does not drop images")
	}
}

func TestOutboundNoteSkippedWhenBlobMissingOrCapped(t *testing.T) {
	root := t.TempDir()
	sqldb, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqldb.Close() })
	r := &Runner{}
	r.Reg = NewRegistry(r, nil, sqldb, root, "model")
	const sess = "sessimg01"
	for _, id := range []string{winUser, winKeep, winDrop} {
		if _, _, err := sqldb.WriteAttachmentFile(sess, id, []byte("png-bytes")); err != nil {
			t.Fatal(err)
		}
	}
	s := newSession(sess, "t")

	// Cap keeps the newest tool image (miss) and the user image. The on-disk
	// shot is dropped. miss has no blob, so nothing sent may say "look above".
	capped, omitted := applyImageLimit([]model.Message{
		{Role: "user", Content: model.ContentFromParts([]model.ContentPart{imgPart(winUser)})},
		assistantCall("c-drop"),
		toolShot(winDrop, "c-drop"),
		assistantCall("c-miss"),
		toolShot(winMiss, "c-miss"),
	}, 2)
	if omitted != 1 {
		t.Fatalf("omitted=%d want 1", omitted)
	}
	out := r.buildOutbound(s, capped)
	var sawMiss, sawDrop, sawUser bool
	for _, m := range out {
		text := m.Content.PlainText()
		switch {
		case strings.Contains(text, winMiss):
			sawMiss = true
			if m.Content.HasImages() || strings.Contains(text, "ARE the peer screenshot") || !strings.Contains(text, "[missing attachment "+winMiss+"]") {
				t.Fatalf("missing blob claimed present: %s", text)
			}
		case strings.Contains(text, winDrop):
			sawDrop = true
			if m.Content.HasImages() || strings.Contains(text, "ARE the peer screenshot") || !strings.Contains(text, "NOT sent") {
				t.Fatalf("capped image claimed present: %s", text)
			}
		case m.Role == "user" && m.Content.HasImages():
			sawUser = true
			if strings.Contains(text, "ARE the peer screenshot") {
				t.Fatal("user upload described as a peer screenshot")
			}
		}
	}
	if !sawMiss || !sawDrop || !sawUser {
		t.Fatalf("miss=%v drop=%v user=%v", sawMiss, sawDrop, sawUser)
	}

	// The image that survives the cap and loads is the one that may say look.
	kept, n := applyImageLimit([]model.Message{
		assistantCall("c-drop"),
		toolShot(winDrop, "c-drop"),
		assistantCall("c-keep"),
		toolShot(winKeep, "c-keep"),
	}, 1)
	if n != 1 {
		t.Fatalf("omitted=%d want 1", n)
	}
	out = r.buildOutbound(s, kept)
	var sawKeep bool
	for _, m := range out {
		text := m.Content.PlainText()
		if !strings.Contains(text, winKeep) {
			continue
		}
		sawKeep = true
		if !m.Content.HasImages() || !strings.Contains(text, peerScreenshotNote) {
			t.Fatalf("sent image missing the look instruction: %+v", m.Content)
		}
		if !strings.Contains(m.Content.Parts[len(m.Content.Parts)-1].Text, "ARE the peer screenshot") {
			t.Fatal("note is not after the image part")
		}
		url := ""
		for _, p := range m.Content.Parts {
			if p.Type == "image_url" && p.ImageURL != nil {
				url = p.ImageURL.URL
			}
		}
		if !strings.HasPrefix(url, "data:image/png;base64,") {
			t.Fatalf("image not materialized: %s", url)
		}
	}
	if !sawKeep {
		t.Fatal("kept image missing from outbound")
	}
}
