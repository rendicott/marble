package session

import (
	"testing"
	"time"
)

func TestExtractAttachmentIDs(t *testing.T) {
	ids := extractAttachmentIDs(`{"ok":true,"attachment_id":"abc123","hint":"x"}`)
	if len(ids) != 1 || ids[0] != "abc123" {
		t.Fatalf("got %v", ids)
	}
	c := toolResultContent(`{"attachment_id":"abc123"}`)
	if !c.HasImages() {
		t.Fatal("expected image parts on tool result content")
	}
}

// Regression: session 0wdd4vkfme — computer_screenshot with CapImages=false used
// to call advisory() while s.mu was held; advisory → appendStep re-locks →
// deadlock, wedging GET /api/sessions and /api/health.
func TestRecordToolResultScreenshotWithoutCapImagesNoDeadlock(t *testing.T) {
	s := newSession("testid1234ab", "t")
	done := make(chan struct{})
	go func() {
		s.mu.Lock()
		_, note := recordToolResultLocked(s, "computer_screenshot", "call-1",
			`{"ok":true,"attachment_id":"e8e79790ae4fa9420ffe5c51310e439e"}`, false)
		s.mu.Unlock()
		if note == "" {
			t.Error("expected omit-images advisory note")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: recordToolResultLocked held s.mu and re-locked")
	}
	last := s.history[len(s.history)-1]
	if last.Content.HasImages() {
		t.Fatal("image parts should be stripped when CapImages=false")
	}
}
