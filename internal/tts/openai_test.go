package tts

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAISynthesize(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"input":"hello"`) {
			t.Errorf("body %s", body)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("oai-mp3"))
	}))
	defer srv.Close()

	p := NewOpenAI(srv.Client())
	p.BaseURL = srv.URL
	mime, audio, err := p.Synthesize(context.Background(), "sk-test", Request{
		Text: "hello", Voice: "alloy", Model: DefaultOpenAIModel, Format: "mp3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if mime != "audio/mpeg" || string(audio) != "oai-mp3" {
		t.Fatalf("%s %q", mime, audio)
	}
	if sawAuth != "Bearer sk-test" {
		t.Fatalf("auth %q", sawAuth)
	}
}
