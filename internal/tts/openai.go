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

const openAISpeechURL = "https://api.openai.com/v1/audio/speech"

// DefaultOpenAIModel is a low-latency OpenAI TTS model.
const DefaultOpenAIModel = "gpt-4o-mini-tts"

// OpenAI implements Provider for OpenAI audio/speech.
type OpenAI struct {
	HTTP    *http.Client
	BaseURL string // override for tests
}

// NewOpenAI returns an OpenAI speech provider.
func NewOpenAI(client *http.Client) *OpenAI {
	if client == nil {
		client = &http.Client{Timeout: SynthTimeout}
	}
	return &OpenAI{HTTP: client, BaseURL: openAISpeechURL}
}

func (o *OpenAI) Name() string { return "openai" }

type openAISpeechBody struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	Voice          string `json:"voice"`
	ResponseFormat string `json:"response_format,omitempty"`
}

func (o *OpenAI) Synthesize(ctx context.Context, apiKey string, req Request) (mime string, audio []byte, err error) {
	if o == nil {
		return "", nil, fmt.Errorf("openai: nil provider")
	}
	voice := strings.TrimSpace(req.Voice)
	if voice == "" {
		voice = "alloy"
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = DefaultOpenAIModel
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "mp3"
	}
	url := o.BaseURL
	if url == "" {
		url = openAISpeechURL
	}
	body := openAISpeechBody{
		Model:          model,
		Input:          req.Text,
		Voice:          voice,
		ResponseFormat: format,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	client := o.HTTP
	if client == nil {
		client = &http.Client{Timeout: SynthTimeout}
	}
	start := time.Now()
	res, err := client.Do(httpReq)
	if err != nil {
		return "", nil, fmt.Errorf("openai: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, dbAttMax()+1))
	if err != nil {
		return "", nil, err
	}
	if res.StatusCode == http.StatusUnauthorized {
		return "", nil, fmt.Errorf("%w: openai unauthorized", ErrNotConfigured)
	}
	if res.StatusCode == http.StatusTooManyRequests {
		return "", nil, fmt.Errorf("openai: rate limited (%s)", res.Status)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return "", nil, fmt.Errorf("openai: HTTP %d: %s", res.StatusCode, msg)
	}
	if len(data) == 0 {
		return "", nil, fmt.Errorf("openai: empty audio body")
	}
	_ = start
	return mimeForFormat(format), data, nil
}
