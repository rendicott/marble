package session

import "testing"

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
