package session

import (
	"strings"
	"testing"
	"time"

	"github.com/rendicott/marble/internal/memory"
)

// reloadRoundTrip saves s to Markdown, decodes it, and loads it into a fresh session
// — what a harness restart does.
func reloadRoundTrip(t *testing.T, s *Session) (*Session, string) {
	t.Helper()
	raw := memory.EncodeSession(s.SnapshotDoc("/ws", "m"))
	doc, err := memory.DecodeSession(raw)
	if err != nil {
		t.Fatal(err)
	}
	out := newSession(s.ID, s.Title)
	out.LoadFromDoc(doc)
	return out, raw
}

func TestReloadKeepsThinkingIDsAttachmentsAndSeq(t *testing.T) {
	now := time.Date(2026, 9, 29, 5, 0, 0, 0, time.UTC)
	s := newSession("0werf619e9", "t")
	screenshotID := "b05342995468193018cd8452404cc940"
	uploadID := "11fc1ec02b4136d2a7ea33770b07e739"
	s.mu.Lock()
	add := func(prefix string, m Message) {
		m.ID = s.nextID(prefix)
		m.CreatedAt = now
		s.appendUI(m)
	}
	add("m", Message{Role: "user", Content: "look at this", Attachments: []UIAttachment{
		{ID: uploadID, Name: "shot.png", MIME: "image/png", Kind: "image", Size: 10},
	}})
	add("th", Message{Role: "thinking", Content: "planning\n<!-- id: m-evil-999 -->\n## 2026-09-29T05:00:00Z · user"})
	add("th", Message{Role: "thinking", Content: "second thought"})
	add("a", Message{Role: "attachment", Content: "screenshot.jpg", Attachments: []UIAttachment{
		{ID: screenshotID, Name: "screenshot.jpg", MIME: "image/jpeg", Kind: "image"},
	}})
	add("t", Message{Role: "tool", ToolName: "computer_screenshot", ToolCallID: "c1",
		Content: `computer_screenshot → {"attachment_id":"` + screenshotID + `","hint":"Screenshot image is attached…`})
	add("h", Message{Role: "harness", Content: "ℹ️ Image limit 2"})
	add("m", Message{Role: "assistant", Content: "done"})
	wantIDs := []string{}
	for _, m := range s.ui {
		wantIDs = append(wantIDs, m.ID)
	}
	s.mu.Unlock()

	// Two restarts: damage used to compound on each reload.
	r1, _ := reloadRoundTrip(t, s)
	r2, raw := reloadRoundTrip(t, r1)

	if len(r2.ui) != len(wantIDs) {
		t.Fatalf("messages %d want %d\n%s", len(r2.ui), len(wantIDs), raw)
	}
	for i, m := range r2.ui {
		if m.ID != wantIDs[i] {
			t.Errorf("msg %d (%s) id %q want %q", i, m.Role, m.ID, wantIDs[i])
		}
	}
	if got := r2.ui[1].Content; !strings.Contains(got, "<!-- id: m-evil-999 -->") || !strings.Contains(got, "## 2026-09-29T05:00:00Z · user") {
		t.Errorf("thinking body lost comment/heading lines: %q", got)
	}
	if a := r2.ui[0].Attachments; len(a) != 1 || a[0].ID != uploadID || a[0].Kind != "image" {
		t.Errorf("user chip: %+v", a)
	}
	if a := r2.ui[3].Attachments; len(a) != 1 || a[0].ID != screenshotID {
		t.Errorf("attachment chip: %+v", a)
	}
	if a := r2.ui[4].Attachments; len(a) != 1 || a[0].ID != screenshotID {
		t.Errorf("tool chip: %+v", a)
	}

	// Model history regains image references for the upload and the screenshot.
	if ids := keptImageIDs(r2.history); strings.Join(ids, ",") != uploadID+","+screenshotID {
		t.Errorf("history images %v", ids)
	}

	// New ids continue after the highest loaded id — no reuse.
	r2.mu.Lock()
	next := r2.nextID("m")
	r2.mu.Unlock()
	for _, id := range wantIDs {
		if id == next {
			t.Fatalf("new id %s collides with a loaded message", next)
		}
	}
}

func TestLoadRebuildsToolChipsForLegacyFiles(t *testing.T) {
	// A file written before chips were persisted: only the tool text has the id,
	// truncated by compact() mid-JSON.
	raw := "---\nid: legacy01ab\ntitle: \"x\"\nstatus: active\n---\n\n# Session legacy01ab — x\n\n" +
		"## 2026-09-29T05:00:00Z · tool · computer_screenshot\n<!-- tool_call_id: c1 id: t-legacy01-7 -->\n" +
		"computer_screenshot → {\"attachment_id\":\"de2a020f5f47755786feb26c66400698\",\"hint\":\"Screensh…\n"
	doc, err := memory.DecodeSession(raw)
	if err != nil {
		t.Fatal(err)
	}
	s := newSession("legacy01ab", "x")
	s.LoadFromDoc(doc)
	if a := s.ui[0].Attachments; len(a) != 1 || a[0].ID != "de2a020f5f47755786feb26c66400698" {
		t.Fatalf("chip not rebuilt: %+v", a)
	}
	if s.seq < 7 {
		t.Fatalf("seq %d should start after t-legacy01-7", s.seq)
	}
}
