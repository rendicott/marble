package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// byte_offset == len(text) is a legal "resume at end of file" request (it is what
// a spill marker's next_offset carries when the cut landed on the final byte).
// It must not panic on text[len(text)].
func TestFileReadByteOffsetAtEOF(t *testing.T) {
	ws := t.TempDir()
	body := "abc\ndef\n"
	if err := os.WriteFile(filepath.Join(ws, "e.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Registry{Workspace: ws, MaxResultChars: 32000}
	out, err := r.fileRead(`{"path":"e.txt","byte_offset":`+itoa(len(body))+`}`, nil)
	if err != nil {
		t.Fatalf("resume at EOF errored: %v", err)
	}
	if !strings.Contains(out, "byte_offset=") {
		t.Fatalf("unexpected result: %q", out)
	}
}

// A byte_offset read still satisfies the read-before-edit gate.
func TestFileReadByteOffsetMarksRead(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "g.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Registry{Workspace: ws, MaxResultChars: 32000}
	tc := &TurnContext{ReadPaths: map[string]bool{}}
	if _, err := r.fileRead(`{"path":"g.txt","byte_offset":6}`, tc); err != nil {
		t.Fatal(err)
	}
	if !wasRead(tc, "g.txt") {
		t.Fatal("byte_offset read did not mark the path as read")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
