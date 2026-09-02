package tts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/db"
)

type fakeProvider struct {
	name  string
	calls int
	mime  string
	audio []byte
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Synthesize(ctx context.Context, apiKey string, req Request) (string, []byte, error) {
	f.calls++
	mime := f.mime
	if mime == "" {
		mime = "audio/mpeg"
	}
	audio := f.audio
	if audio == nil {
		audio = []byte("fake-mp3-" + req.Text)
	}
	return mime, audio, nil
}

func TestNormalizeAndCacheKey(t *testing.T) {
	a := NormalizeText("  hello   world \n")
	b := NormalizeText("hello world")
	if a != b || a != "hello world" {
		t.Fatalf("%q vs %q", a, b)
	}
	k1 := CacheKey("elevenlabs", "m", "v", "hello   world")
	k2 := CacheKey("elevenlabs", "m", "v", "hello world")
	if k1 != k2 {
		t.Fatalf("keys differ")
	}
	k3 := CacheKey("elevenlabs", "m", "other", "hello world")
	if k1 == k3 {
		t.Fatal("voice should change key")
	}
}

func TestSynthesizeCacheHit(t *testing.T) {
	root := t.TempDir()
	d, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	t.Setenv("TEST_TTS_KEY", "secret-test-key")
	config.SetMemoryDirForEnv(root)
	t.Cleanup(func() { config.SetMemoryDirForEnv(""); config.InvalidateEnvOverlay() })

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Provider = "elevenlabs"
	cfg.APIKeyEnv = "TEST_TTS_KEY"
	cfg.DefaultVoice = "voiceA"
	cfg.DefaultModel = "flash-test"
	cfg.Cache = true

	m := NewManager(cfg, d, root, false)
	fp := &fakeProvider{name: "elevenlabs"}
	m.SetProvider(fp)

	req := Request{Text: "Package in transit", SessionID: "sess1", Voice: "voiceA", Model: "flash-test"}
	r1, err := m.SynthesizeForSession(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Cached || r1.AttachmentID == "" || fp.calls != 1 {
		t.Fatalf("first: %+v calls=%d", r1, fp.calls)
	}
	row, err := d.GetAttachment(r1.AttachmentID)
	if err != nil || row.Kind != "audio" || row.Source != "tts" {
		t.Fatalf("row %+v err %v", row, err)
	}

	r2, err := m.SynthesizeForSession(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Cached || fp.calls != 1 {
		t.Fatalf("expected cache hit: %+v calls=%d", r2, fp.calls)
	}
	if r2.AttachmentID == r1.AttachmentID {
		// new session attachment each time is OK (session-scoped URL); just ensure bytes path works
	}
	if _, err := os.Stat(filepath.Join(root, "tts-cache", CacheKey("elevenlabs", "flash-test", "voiceA", req.Text)+".bin")); err != nil {
		t.Fatalf("global cache missing: %v", err)
	}
}

func TestDisabledAndNotConfigured(t *testing.T) {
	root := t.TempDir()
	d, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	m := NewManager(DefaultConfig(), d, root, false)
	_, err = m.SynthesizeForSession(context.Background(), Request{Text: "hi", SessionID: "s"})
	if err != ErrDisabled {
		t.Fatalf("got %v", err)
	}

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Provider = "elevenlabs"
	cfg.APIKeyEnv = "MISSING_TTS_KEY_XYZ"
	cfg.DefaultVoice = "v"
	m2 := NewManager(cfg, d, root, false)
	m2.SetProvider(&fakeProvider{name: "elevenlabs"})
	_, err = m2.SynthesizeForSession(context.Background(), Request{Text: "hi", SessionID: "s"})
	if err != ErrNotConfigured {
		t.Fatalf("got %v", err)
	}
}

func TestSniffStillRejectsAudio(t *testing.T) {
	// User upload path must stay closed (ADR-0027: trusted TTS write only).
	_, _, err := db.SniffAttachment("x.mp3", []byte{0xFF, 0xFB, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected reject, got %v", err)
	}
}

func TestStatus(t *testing.T) {
	m := NewManager(DefaultConfig(), nil, "", false)
	st := m.Status()
	if st.Enabled || st.Configured {
		t.Fatalf("%+v", st)
	}
}
