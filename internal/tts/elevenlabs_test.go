package tts

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestElevenLabsSynthesize(t *testing.T) {
	var sawKey, sawVoice string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawKey = r.Header.Get("xi-api-key")
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) > 0 {
			sawVoice = parts[len(parts)-1]
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "hello") {
			t.Errorf("body %s", body)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("mp3data"))
	}))
	defer srv.Close()

	p := NewElevenLabs(srv.Client())
	p.BaseURL = srv.URL + "/"
	mime, audio, err := p.Synthesize(context.Background(), "k123", Request{
		Text: "hello", Voice: "voiceXYZ", Model: "eleven_flash_v2_5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if mime != "audio/mpeg" || string(audio) != "mp3data" {
		t.Fatalf("%s %q", mime, audio)
	}
	if sawKey != "k123" || sawVoice != "voiceXYZ" {
		t.Fatalf("key=%q voice=%q", sawKey, sawVoice)
	}
}

func TestElevenLabsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"invalid"}`))
	}))
	defer srv.Close()
	p := NewElevenLabs(srv.Client())
	p.BaseURL = srv.URL + "/"
	_, _, err := p.Synthesize(context.Background(), "bad", Request{Text: "x", Voice: "v"})
	if err == nil || !strings.Contains(err.Error(), "tts_not_configured") && err != ErrNotConfigured {
		// wrapped with %w
		if err == nil || !strings.Contains(err.Error(), "unauthorized") {
			t.Fatalf("got %v", err)
		}
	}
}
