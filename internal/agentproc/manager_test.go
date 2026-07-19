package agentproc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveCWDWorkdir(t *testing.T) {
	ws := t.TempDir()
	mem := t.TempDir()
	m, err := New(mem, ws)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.ResolveCWD("", "agent-runs/job1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

func TestResolveCWDEscape(t *testing.T) {
	ws := t.TempDir()
	mem := t.TempDir()
	m, err := New(mem, ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ResolveCWD("..", ""); err == nil {
		t.Fatal("expected escape error")
	}
}

func TestRunSyncFakeGrok(t *testing.T) {
	ws := t.TempDir()
	mem := t.TempDir()
	script := filepath.Join(t.TempDir(), "fake-grok")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho '{\"result\":\"hello from agent\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	aa := true
	cfg.Drivers["grok"] = DriverConfig{
		Enabled: true, Command: script, DefaultOutputFormat: "json", AutoApprove: &aa,
	}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(ConfigPath(mem), b, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := New(mem, ws)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := m.RunSync(ctx, "sess1", Request{Format: "grok", Prompt: "hi", CWD: ws})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("%+v", res)
	}
	if res.Summary != "hello from agent" {
		t.Fatalf("summary %q raw=%v", res.Summary, res.Raw)
	}
}

func TestMaxPerSession(t *testing.T) {
	ws := t.TempDir()
	mem := t.TempDir()
	m, err := New(mem, ws)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.cfg.MaxPerSession = 1
	m.mu.Unlock()
	if err := m.acquire("s"); err != nil {
		t.Fatal(err)
	}
	if err := m.acquire("s"); err == nil {
		t.Fatal("expected cap")
	}
	m.release("s")
	if err := m.acquire("s"); err != nil {
		t.Fatal(err)
	}
}
