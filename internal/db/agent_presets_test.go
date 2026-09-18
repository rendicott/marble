package db

import (
	"path/filepath"
	"testing"
)

func TestAgentPresetCRUD(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Mode != ModeNormal {
		t.Fatalf("mode %s %s", d.Mode, d.Reason)
	}
	row := AgentPresetRow{
		ID: "grok-fast", Driver: "grok", DisplayName: "Grok fast",
		Command: "grok", DefaultArgs: []string{"--no-plan"}, Enabled: true,
	}
	if err := d.InsertAgentPreset(row); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetAgentPreset("grok-fast")
	if err != nil || got.Driver != "grok" || !got.Enabled {
		t.Fatalf("%+v %v", got, err)
	}
	got.Command = "/bin/echo"
	if err := d.UpdateAgentPreset(*got); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateAgentPresetDetection("grok-fast", true, "/bin/echo", "echo 1"); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetAgentPreset("grok-fast")
	if !got.Detected || got.DetectedPath != "/bin/echo" {
		t.Fatalf("%+v", got)
	}
	n, err := d.SeedAgentPresetsIfEmpty([]AgentPresetRow{{ID: "x", Driver: "grok", Command: "grok", DisplayName: "x"}})
	if err != nil || n != 0 {
		t.Fatalf("seed nonempty n=%d err=%v", n, err)
	}
	if err := d.DeleteAgentPreset("grok-fast"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetAgentPreset("grok-fast"); err == nil {
		t.Fatal("expected missing")
	}
	_ = filepath.Join(dir, "marble.db")
}

func TestValidateAgentPreset(t *testing.T) {
	r := &AgentPresetRow{ID: "Bad", Driver: "grok"}
	if err := ValidateAgentPreset(r); err == nil {
		t.Fatal("expected bad id")
	}
	r = &AgentPresetRow{ID: "ok", Driver: "nope"}
	if err := ValidateAgentPreset(r); err == nil {
		t.Fatal("expected bad driver")
	}
}
