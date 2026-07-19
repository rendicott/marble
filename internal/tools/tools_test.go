package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileTools(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{Workspace: dir, MaxResultChars: 10000}

	out := r.Execute("file_write", `{"path":"hello.txt","content":"hi marble\nline2"}`, nil)
	if strings.HasPrefix(out, "error:") {
		t.Fatalf("write: %s", out)
	}

	tc := &TurnContext{ReadPaths: map[string]bool{}}
	out = r.Execute("file_read", `{"path":"hello.txt"}`, tc)
	if out != "hi marble\nline2" {
		t.Fatalf("read got %q", out)
	}
	if !tc.ReadPaths["hello.txt"] {
		t.Fatal("expected read path tracked")
	}

	out = r.Execute("list_files", `{"path":"."}`, nil)
	if !strings.Contains(out, "hello.txt") {
		t.Fatalf("list: %s", out)
	}

	// absolute path under workspace is allowed
	out = r.Execute("list_files", fmt.Sprintf(`{"path":%q}`, dir), nil)
	if strings.HasPrefix(out, "error:") || !strings.Contains(out, "hello.txt") {
		t.Fatalf("list abs: %s", out)
	}

	out = r.Execute("file_read", `{"path":"../secret"}`, nil)
	if !strings.HasPrefix(out, "error:") {
		t.Fatalf("expected escape error, got %s", out)
	}
	// absolute outside workspace rejected
	out = r.Execute("list_files", `{"path":"/etc"}`, nil)
	if !strings.HasPrefix(out, "error:") {
		t.Fatalf("expected escape for /etc, got %s", out)
	}

	out = r.Execute("file_write", `{"path":"sub/a.txt","content":"nested"}`, nil)
	if strings.HasPrefix(out, "error:") {
		t.Fatal(out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "sub", "a.txt"))
	if err != nil || string(b) != "nested" {
		t.Fatalf("nested file: %v %q", err, b)
	}
}

func TestEditFileRequiresRead(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{Workspace: dir, MaxResultChars: 10000}
	_ = r.Execute("file_write", `{"path":"a.txt","content":"hello world"}`, nil)

	out := r.Execute("edit_file", `{"path":"a.txt","old_string":"hello","new_string":"hi"}`, &TurnContext{ReadPaths: map[string]bool{}})
	if !strings.Contains(out, "file_read") {
		t.Fatalf("expected prior-read error: %s", out)
	}

	tc := &TurnContext{ReadPaths: map[string]bool{}}
	_ = r.Execute("file_read", `{"path":"a.txt"}`, tc)
	out = r.Execute("edit_file", `{"path":"a.txt","old_string":"hello","new_string":"hi"}`, tc)
	if strings.HasPrefix(out, "error:") {
		t.Fatal(out)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(b) != "hi world" {
		t.Fatalf("got %q", b)
	}
}

func TestApplyPatchRollback(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{Workspace: dir, MaxResultChars: 10000}
	_ = r.Execute("file_write", `{"path":"keep.txt","content":"orig"}`, nil)

	tc := &TurnContext{ReadPaths: map[string]bool{"keep.txt": true}}
	// second update fails (no prior read on missing) — first add should roll back
	out := r.Execute("apply_patch", `{
		"edits":[
			{"op":"add","path":"new.txt","content":"x"},
			{"op":"update","path":"nope.txt","content":"y"}
		]
	}`, tc)
	if !strings.HasPrefix(out, "error:") {
		t.Fatalf("expected error, got %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("new.txt should have been rolled back")
	}
}

func TestGrepGlob(t *testing.T) {
	dir := t.TempDir()
	r := &Registry{Workspace: dir, MaxResultChars: 10000}
	_ = r.Execute("file_write", `{"path":"pkg/foo.go","content":"package pkg\nfunc Hello() {}\n"}`, nil)
	out := r.Execute("grep", `{"pattern":"Hello","path":"."}`, nil)
	if !strings.Contains(out, "foo.go") {
		t.Fatalf("grep: %s", out)
	}
	out = r.Execute("glob", `{"pattern":"**/*.go"}`, nil)
	if !strings.Contains(out, "foo.go") {
		t.Fatalf("glob: %s", out)
	}
}
