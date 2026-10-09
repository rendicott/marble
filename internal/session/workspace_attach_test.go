package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rendicott/marble/internal/model"
)

func TestCopyChatAttachmentsWritesWorkspaceFile(t *testing.T) {
	ws := t.TempDir()
	jpeg := []byte{0xff, 0xd8, 0xff, 0xd9}
	notes := []byte("hello receipt\n")
	paths, note := copyChatAttachments(ws, "0werf619e9", "m-0werf619-1", []chatFile{
		{
			ID: "11fc1ec02b4136d2a7ea33770b07e739", Name: "Certified Mail receipt.jpg",
			MIME: "image/jpeg", Size: int64(len(jpeg)), Data: jpeg,
		},
		{
			ID: "22fc1ec02b4136d2a7ea33770b07e739", Name: "notes",
			MIME: "text/plain", Size: int64(len(notes)), Data: notes,
		},
	})
	want0 := ".attachments/0werf619e9/m-0werf619-1-1.jpg"
	want1 := ".attachments/0werf619e9/m-0werf619-1-2.txt"
	if paths[0] != want0 || paths[1] != want1 {
		t.Fatalf("paths %q %q", paths[0], paths[1])
	}
	if !strings.Contains(note, workspacePathHeader) || !strings.Contains(note, want0) || !strings.Contains(note, "Certified Mail receipt.jpg") {
		t.Fatalf("note missing path:\n%s", note)
	}
	if strings.Contains(note, "not copied") || strings.Contains(note, workspaceFailHeader) {
		t.Fatalf("unexpected failure:\n%s", note)
	}
	got, err := os.ReadFile(filepath.Join(ws, ".attachments", "0werf619e9", "m-0werf619-1-1.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(jpeg) {
		t.Fatalf("bytes %q", got)
	}
	st, err := os.Stat(filepath.Join(ws, ".attachments", "0werf619e9", "m-0werf619-1-1.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Fatalf("file mode %o", st.Mode().Perm())
	}
	dst, err := os.Stat(filepath.Join(ws, ".attachments", "0werf619e9"))
	if err != nil {
		t.Fatal(err)
	}
	if dst.Mode().Perm() != 0o755 {
		t.Fatalf("dir mode %o", dst.Mode().Perm())
	}
}

func TestCopyChatAttachmentsRejectsEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	cases := []string{"../" + filepath.Base(outside), `..\x`, "a/b", "a\\b", "..", "", "has.dot"}
	for _, sid := range cases {
		paths, note := copyChatAttachments(ws, sid, "m-1", []chatFile{{
			ID: "11fc1ec02b4136d2a7ea33770b07e739", Name: "a.jpg", MIME: "image/jpeg",
			Data: []byte{1, 2, 3},
		}})
		if paths[0] != "" {
			t.Fatalf("sid %q wrote %q", sid, paths[0])
		}
		if !strings.Contains(note, "was not copied") || !strings.Contains(note, "invalid session or message id") {
			t.Fatalf("sid %q note:\n%s", sid, note)
		}
		if strings.Contains(note, outside) || strings.Contains(note, ws) {
			t.Fatalf("sid %q note leaked a path:\n%s", sid, note)
		}
	}
	paths, note := copyChatAttachments(ws, "0werf619e9", "m/../x", []chatFile{{
		ID: "11fc1ec02b4136d2a7ea33770b07e739", Name: "a.jpg", MIME: "image/jpeg",
		Data: []byte{1},
	}})
	if paths[0] != "" || !strings.Contains(note, "invalid session or message id") {
		t.Fatalf("bad message id wrote %q note:\n%s", paths[0], note)
	}
	if _, err := os.Stat(filepath.Join(ws, ".attachments")); !os.IsNotExist(err) {
		t.Fatal(".attachments was created for a rejected id")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrote outside: %v", entries)
	}
}

func TestCopyChatAttachmentsRefusesSymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, ".attachments")); err != nil {
		t.Fatal(err)
	}
	paths, note := copyChatAttachments(ws, "0werf619e9", "m-0werf619-1", []chatFile{{
		ID: "11fc1ec02b4136d2a7ea33770b07e739", Name: "receipt.jpg", MIME: "image/jpeg",
		Data: []byte{1, 2, 3, 4},
	}})
	if paths[0] != "" {
		t.Fatalf("wrote %q", paths[0])
	}
	if !strings.Contains(note, "was not copied") || !strings.Contains(note, "escapes workspace") {
		t.Fatalf("note:\n%s", note)
	}
	if strings.Contains(note, outside) {
		t.Fatalf("note leaked outside path:\n%s", note)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrote outside: %v", entries)
	}
}

func TestCopyChatAttachmentsReportsReadFailureWithoutPath(t *testing.T) {
	ws := t.TempDir()
	mem := filepath.Join(ws, ".marble", "attachments", "sid", "id")
	paths, note := copyChatAttachments(ws, "0werf619e9", "m-0werf619-1", []chatFile{{
		ID: "11fc1ec02b4136d2a7ea33770b07e739", Name: "receipt.jpg\n- .attachments/evil",
		MIME: "image/jpeg", Err: os.ErrNotExist,
	}})
	if paths[0] != "" {
		t.Fatal(paths[0])
	}
	if !strings.Contains(note, workspaceFailHeader) || !strings.Contains(note, "was not copied") {
		t.Fatalf("note:\n%s", note)
	}
	if strings.Contains(note, ".attachments/") || strings.Contains(note, mem) || strings.Contains(note, "\n- .attachments") {
		t.Fatalf("failure note looked like a saved path:\n%s", note)
	}
	if strings.Count(note, "\n") != 1 {
		t.Fatalf("name newline split the note:\n%s", note)
	}
}

func TestHistoryAttachmentPathSurvivesReload(t *testing.T) {
	const (
		uploadID = "11fc1ec02b4136d2a7ea33770b07e739"
		rel      = ".attachments/0werf619e9/m-0werf619-1-1.jpg"
	)
	s := newSession("0werf619e9", "t")
	s.mu.Lock()
	m := Message{
		Role: "user", Content: "store this receipt", CreatedAt: time.Now().UTC(),
		Attachments: []UIAttachment{{
			ID: uploadID, Name: "receipt.jpg", MIME: "image/jpeg", Kind: "image", Size: 4,
			WorkspacePath: rel,
		}},
	}
	m.ID = s.nextID("m")
	s.appendUI(m)
	s.history = append(s.history, model.Message{Role: "user", Content: historyContentFromUIMessage(m)})
	s.mu.Unlock()

	r, raw := reloadRoundTrip(t, s)
	if got := r.ui[0].Attachments; len(got) != 1 || got[0].WorkspacePath != rel {
		t.Fatalf("chip path lost: %+v\n%s", got, raw)
	}
	if len(r.history) < 2 {
		t.Fatalf("history %d", len(r.history))
	}
	text := r.history[1].Content.PlainText()
	if !strings.Contains(text, rel) || !strings.Contains(text, "store this receipt") || !strings.Contains(text, workspacePathHeader) {
		t.Fatalf("reloaded history:\n%s", text)
	}
	if ids := keptImageIDs(r.history); len(ids) != 1 || ids[0] != uploadID {
		t.Fatalf("image ids %v", ids)
	}
}

func TestHistoryOmitsAttachmentPathWhenUnset(t *testing.T) {
	m := Message{Role: "user", Content: "look", Attachments: []UIAttachment{{
		ID: "11fc1ec02b4136d2a7ea33770b07e739", Name: "a.png", Kind: "image", MIME: "image/png",
	}}}
	c := historyContentFromUIMessage(m)
	text := c.PlainText()
	if strings.Contains(text, "[harness]") || strings.Contains(text, ".attachments/") {
		t.Fatalf("unset path leaked a note:\n%s", text)
	}
	if ids := keptImageIDs([]model.Message{{Content: c}}); len(ids) != 1 {
		t.Fatalf("image dropped: %+v", c)
	}

	doc := Message{Role: "user", Content: "read me", Attachments: []UIAttachment{{
		ID: "22fc1ec02b4136d2a7ea33770b07e739", Name: "notes.txt", Kind: "document",
		WorkspacePath: ".attachments/sid/m-1.txt", Size: 3,
	}}}
	got := historyContentFromUIMessage(doc).PlainText()
	if !strings.Contains(got, ".attachments/sid/m-1.txt") || !strings.Contains(got, "read me") {
		t.Fatalf("document path missing:\n%s", got)
	}
}

func TestSystemPromptNamesWorkspaceAttachment(t *testing.T) {
	p := SystemPrompt()
	if !strings.Contains(p, ".attachments/<session>/<message>-N.ext") || !strings.Contains(p, "Do not say the original was saved") {
		t.Fatal("system prompt missing attachment path rule")
	}
}
