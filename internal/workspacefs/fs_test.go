package workspacefs

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestJailAndCRUD(t *testing.T) {
	root := t.TempDir()
	fs, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Resolve("../etc/passwd"); err == nil {
		t.Fatal("expected escape error")
	}
	if err := fs.WriteText("a/b.txt", "hello"); err != nil {
		t.Fatal(err)
	}
	c, err := fs.ReadText("a/b.txt")
	if err != nil || c != "hello" {
		t.Fatalf("read %q %v", c, err)
	}
	if err := fs.Mkdir("a/sub"); err != nil {
		t.Fatal(err)
	}
	ents, err := fs.List("a", true)
	if err != nil || len(ents) < 2 {
		t.Fatalf("list: %+v %v", ents, err)
	}
	if err := fs.Rename("a/b.txt", "a/c.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Delete("a/c.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestTarGz(t *testing.T) {
	root := t.TempDir()
	fs, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o644)
	var buf bytes.Buffer
	if err := fs.WriteTarGz(".", &buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() < 20 {
		t.Fatalf("archive too small %d", buf.Len())
	}
}
