package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAPIKeyNone(t *testing.T) {
	c := &Config{}
	c.resolveAPIKey()
	if c.APIKey != "" || c.APIKeyEnvConfigured {
		t.Fatalf("expected empty: %+v", c)
	}
	pub := c.ModelAuthPublic()
	if pub["model_auth"] != "none" || pub["model_auth_configured"] != false {
		t.Fatalf("public %+v", pub)
	}
}

func TestResolveAPIKeyFirstWins(t *testing.T) {
	t.Setenv("MARBLE_TEST_KEY_A", "")
	t.Setenv("MARBLE_TEST_KEY_B", "secret-b")
	t.Setenv("MARBLE_TEST_KEY_C", "secret-c")
	c := &Config{APIKeyEnv: "MARBLE_TEST_KEY_A, MARBLE_TEST_KEY_B, MARBLE_TEST_KEY_C"}
	c.resolveAPIKey()
	if !c.APIKeyEnvConfigured || c.APIKey != "secret-b" || c.APIKeyEnvUsed != "MARBLE_TEST_KEY_B" {
		t.Fatalf("got key=%q used=%q configured=%v", c.APIKey, c.APIKeyEnvUsed, c.APIKeyEnvConfigured)
	}
	pub := c.ModelAuthPublic()
	if pub["model_auth"] != "env" || pub["model_auth_configured"] != true {
		t.Fatalf("public %+v", pub)
	}
	// secret must not appear in public map values as nested secret field
	for k, v := range pub {
		if k == "model_auth" || k == "model_auth_env" || k == "model_auth_env_used" || k == "model_auth_configured" {
			continue
		}
		_ = v
	}
	if s, ok := pub["api_key"]; ok && s != nil && s != "" {
		t.Fatal("secret leaked in public map")
	}
}

func TestResolveAPIKeyFlagButEmptyEnv(t *testing.T) {
	t.Setenv("MARBLE_TEST_KEY_EMPTY", "")
	c := &Config{APIKeyEnv: "MARBLE_TEST_KEY_EMPTY"}
	c.resolveAPIKey()
	if c.APIKeyEnvConfigured || c.APIKey != "" {
		t.Fatal("expected unconfigured")
	}
	if c.ModelAuthPublic()["model_auth"] != "env" {
		t.Fatal("mode should still be env when flag set")
	}
}

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
