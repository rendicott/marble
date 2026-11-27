package memory

import (
	"crypto/sha256"
	"encoding/hex"
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

func TestSoulVersion(t *testing.T) {
	sha, chars := SoulVersion("")
	if sha != "e3b0c44298fc" || chars != 0 {
		t.Fatalf("empty sha=%s chars=%d", sha, chars)
	}
	const text = "café"
	sha, chars = SoulVersion(text)
	if chars != 4 {
		t.Fatalf("chars %d", chars)
	}
	sum := sha256.Sum256([]byte(text))
	if sha != hex.EncodeToString(sum[:])[:12] {
		t.Fatalf("sha %s", sha)
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
