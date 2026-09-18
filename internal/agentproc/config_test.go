package agentproc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndSetContext(t *testing.T) {
	dir := t.TempDir()
	path := ConfigPath(dir)
	cfg := DefaultConfig()
	cfg.Context = ContextConfig{Default: []string{"none"}, MaxChars: 8000}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Context.Default) != 1 || got.Context.Default[0] != "none" || got.Context.MaxChars != 8000 {
		t.Fatalf("%+v", got.Context)
	}
	m, err := New(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetContext(ContextConfig{Default: []string{"full", "memory"}, MaxChars: 16000}); err != nil {
		t.Fatal(err)
	}
	live := m.Config().GlobalContextSpec()
	if live.Sources[0] != "full" || live.MaxChars != 16000 {
		t.Fatalf("%+v", live)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent_process.json")); err != nil {
		t.Fatal(err)
	}
}
