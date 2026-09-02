package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpsertDeleteManagedEnv(t *testing.T) {
	dir := t.TempDir()
	SetMemoryDirForEnv(dir)
	t.Cleanup(func() { SetMemoryDirForEnv(""); InvalidateEnvOverlay() })

	path, err := UpsertManagedEnv("GEMINI_API_KEY", "secret-one")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "env")
	if path != want {
		t.Fatalf("path %q want %q", path, want)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("expected 0600-ish, got %v", st.Mode())
	}

	InvalidateEnvOverlay()
	key, used, ok := ResolveAPIKeyEnv("GEMINI_API_KEY")
	if !ok || key != "secret-one" || used != "GEMINI_API_KEY" {
		t.Fatalf("resolve got %q %q %v", key, used, ok)
	}

	if _, err := UpsertManagedEnv("GEMINI_API_KEY", "secret-two"); err != nil {
		t.Fatal(err)
	}
	InvalidateEnvOverlay()
	key, _, ok = ResolveAPIKeyEnv("GEMINI_API_KEY")
	if !ok || key != "secret-two" {
		t.Fatalf("after update %q ok=%v", key, ok)
	}

	if _, err := UpsertManagedEnv("OTHER_KEY", "abc"); err != nil {
		t.Fatal(err)
	}
	_, _, entries, err := ListManagedEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("entries %+v", entries)
	}

	if _, err := DeleteManagedEnv("GEMINI_API_KEY"); err != nil {
		t.Fatal(err)
	}
	InvalidateEnvOverlay()
	m, err := ParseEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, still := m["GEMINI_API_KEY"]; still {
		t.Fatal("expected key removed from managed file")
	}
	if _, still := m["OTHER_KEY"]; !still {
		t.Fatal("OTHER_KEY should remain")
	}
}

func TestValidateEnvKey(t *testing.T) {
	if err := ValidateEnvKey("GOOD_KEY1"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEnvKey("1bad"); err == nil {
		t.Fatal("expected error")
	}
	if err := ValidateEnvKey("has space"); err == nil {
		t.Fatal("expected error")
	}
}
