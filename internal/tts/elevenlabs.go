package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const elevenLabsTTSURL = "https://api.elevenlabs.io/v1/text-to-speech/"

// ElevenLabs implements Provider for ElevenLabs REST TTS.
type ElevenLabs struct {
	HTTP    *http.Client
	BaseURL string // override for tests
}

// NewElevenLabs returns an ElevenLabs provider.
func NewElevenLabs(client *http.Client) *ElevenLabs {
	if client == nil {
		client = &http.Client{Timeout: SynthTimeout}
	}
	return &ElevenLabs{HTTP: client, BaseURL: elevenLabsTTSURL}
}

func (e *ElevenLabs) Name() string { return "elevenlabs" }

type elevenBody struct {
	Text          string                 `json:"text"`
	ModelID       string                 `json:"model_id,omitempty"`
	VoiceSettings map[string]interface{} `json:"voice_settings,omitempty"`
}

func (e *ElevenLabs) Synthesize(ctx context.Context, apiKey string, req Request) (mime string, audio []byte, err error) {
	if e == nil {
		return "", nil, fmt.Errorf("elevenlabs: nil provider")
	}
	voice := strings.TrimSpace(req.Voice)
	if voice == "" {
		return "", nil, fmt.Errorf("elevenlabs: voice required (set default_voice in tts.json)")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = DefaultElevenLabsModel
	}
	base := e.BaseURL
	if base == "" {
		base = elevenLabsTTSURL
	}
	url := strings.TrimRight(base, "/") + "/" + voice
	// Prefer mp3 output query when using official API host
	if strings.Contains(url, "api.elevenlabs.io") && !strings.Contains(url, "?") {
		url += "?output_format=mp3_44100_128"
	}
	body := elevenBody{Text: req.Text, ModelID: model}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "audio/mpeg")
	httpReq.Header.Set("xi-api-key", apiKey)

	client := e.HTTP
	if client == nil {
		client = &http.Client{Timeout: SynthTimeout}
	}
	start := time.Now()
	res, err := client.Do(httpReq)
	if err != nil {
		return "", nil, fmt.Errorf("elevenlabs: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, dbAttMax()+1))
	if err != nil {
		return "", nil, err
	}
	if res.StatusCode == http.StatusUnauthorized {
		return "", nil, fmt.Errorf("%w: elevenlabs unauthorized", ErrNotConfigured)
	}
	if res.StatusCode == http.StatusTooManyRequests {
		return "", nil, fmt.Errorf("elevenlabs: rate limited (%s)", res.Status)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return "", nil, fmt.Errorf("elevenlabs: HTTP %d: %s", res.StatusCode, msg)
	}
	if len(data) == 0 {
		return "", nil, fmt.Errorf("elevenlabs: empty audio body")
	}
	_ = start // latency available for future metrics
	ct := res.Header.Get("Content-Type")
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if ct == "" || !strings.HasPrefix(ct, "audio/") {
		ct = "audio/mpeg"
	}
	return ct, data, nil
}

// dbAttMax mirrors attachment max without importing a cycle concern.
func dbAttMax() int64 { return 8 << 20 }
