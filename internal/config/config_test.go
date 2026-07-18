package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveMemoryDirCreatesMissing(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "marble-mem")
	abs, created, err := resolveMemoryDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected created=true")
	}
	if abs != path && abs != mustAbs(t, path) {
		// Abs may clean the path
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}

	// second call: exists
	abs2, created2, err := resolveMemoryDir(abs)
	if err != nil || created2 || abs2 != abs {
		t.Fatalf("second: abs=%s created=%v err=%v", abs2, created2, err)
	}
}

func TestResolveMemoryDirRejectsFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "notadir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := resolveMemoryDir(file)
	if err == nil {
		t.Fatal("expected error for file path")
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
