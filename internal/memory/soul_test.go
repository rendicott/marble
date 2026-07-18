package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSoulReadWrite(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	text, err := s.ReadSoul()
	if err != nil || text != "" {
		t.Fatalf("empty: %q %v", text, err)
	}
	if err := s.WriteSoul("Be concise.\nPrefer tables."); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "soul.md")); err != nil {
		t.Fatal(err)
	}
	text, err = s.ReadSoul()
	if err != nil || text != "Be concise.\nPrefer tables." {
		t.Fatalf("got %q", text)
	}
	if err := s.WriteSoul("   \n  "); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "soul.md")); !os.IsNotExist(err) {
		t.Fatalf("expected remove empty soul, err=%v", err)
	}
}

func TestSoulMaxSize(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", SoulMaxBytes+1)
	if err := s.WriteSoul(big); err == nil {
		t.Fatal("expected size error")
	}
}
