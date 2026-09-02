package tts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigDisabled(t *testing.T) {
	c := DefaultConfig()
	if c.Enabled || c.Provider != "none" {
		t.Fatalf("default should be off: %+v", c)
	}
	if c.DefaultModel != DefaultElevenLabsModel {
		t.Fatalf("flash default %q", c.DefaultModel)
	}
}

func TestLoadMissingFile(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Enabled {
		t.Fatal("missing file should disable")
	}
}

func TestLoadAndValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tts.json")
	body := `{
  "enabled": true,
  "provider": "elevenlabs",
  "api_key_env": "ELEVENLABS_API_KEY",
  "default_voice": "voice1",
  "cache": true
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Enabled || c.Provider != "elevenlabs" || c.DefaultModel != DefaultElevenLabsModel {
		t.Fatalf("%+v", c)
	}
}

func TestValidateBadProvider(t *testing.T) {
	c := DefaultConfig()
	c.Provider = "nope"
	if err := c.Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveConfigPath(t *testing.T) {
	p := ResolveConfigPath("", "/mem")
	if p != "/mem/tts.json" {
		t.Fatalf("got %q", p)
	}
}
