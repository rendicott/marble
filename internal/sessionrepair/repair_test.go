package sessionrepair

import (
	"strings"
	"testing"
	"time"

	"github.com/rendicott/marble/internal/memory"
)

func ids(doc *memory.SessionDoc) string {
	var out []string
	for _, m := range doc.Messages {
		out = append(out, m.Role+":"+m.ID)
	}
	return strings.Join(out, " ")
}

func TestRepairDocRestoresStolenIDsAndChips(t *testing.T) {
	t0 := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	// Damage as the old loader left it: each id-less thinking run gave its last
	// id to the message before it; after the reload new messages reused low ids.
	doc := &memory.SessionDoc{SessionMeta: memory.SessionMeta{ID: "0werf619e9"}, Messages: []memory.TranscriptMessage{
		{Role: "user", ID: "th-0werf619-2", CreatedAt: t0},
		{Role: "thinking", CreatedAt: t0},
		{Role: "tool", ID: "t-0werf619-3", CreatedAt: t0},
		{Role: "tool", ID: "th-0werf619-6", CreatedAt: t0},
		{Role: "thinking", CreatedAt: t0},
		{Role: "thinking", CreatedAt: t0},
		{Role: "attachment", ID: "a-0werf619-7", Content: "screenshot.jpg", CreatedAt: t0.Add(time.Minute)},
		{Role: "tool", ID: "t-0werf619-8", Content: `computer_screenshot → {"attachment_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","hint":"…`, CreatedAt: t0.Add(time.Minute)},
		// post-reload messages: ids restarted low and collide
		{Role: "user", ID: "m-0werf619-4", CreatedAt: t0.Add(time.Hour)},
		{Role: "attachment", ID: "a-0werf619-7", Content: "screenshot.jpg", CreatedAt: t0.Add(time.Hour)},
	}}
	rows := []attRow{
		{id: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", name: "screenshot.jpg", mime: "image/jpeg", kind: "image", created: t0.Add(time.Minute)},
		{id: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", name: "screenshot.jpg", mime: "image/jpeg", kind: "image", created: t0.Add(time.Hour + time.Second)},
		{id: "cccccccccccccccccccccccccccccccc", name: "up.png", mime: "image/png", kind: "image", messageID: "m-0werf619-1", created: t0},
	}
	st := RepairDoc(doc, rows)
	got := ids(doc)
	for _, want := range []string{
		"user:m-0werf619-1 thinking:th-0werf619-2 tool:t-0werf619-3",
		"tool:t-0werf619-4 thinking:th-0werf619-5 thinking:th-0werf619-6 attachment:a-0werf619-7",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ids:\n%s\nmissing %q", got, want)
		}
	}
	seen := map[string]bool{}
	for _, m := range doc.Messages {
		if m.ID == "" || seen[m.ID] {
			t.Fatalf("empty or duplicate id after repair: %s", got)
		}
		seen[m.ID] = true
	}
	if st.StolenIDs != 2 {
		t.Errorf("stolen=%d want 2", st.StolenIDs)
	}
	chips := map[int]string{}
	for i, m := range doc.Messages {
		if len(m.Attachments) > 0 {
			chips[i] = m.Attachments[0].ID
		}
	}
	if chips[0] != "cccccccccccccccccccccccccccccccc" || chips[6] != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || chips[9] != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("chips %v", chips)
	}
	// Idempotent: a second pass finds nothing.
	if st2 := RepairDoc(doc, rows); st2.changed() {
		t.Fatalf("second pass changed: %+v", st2)
	}
}
