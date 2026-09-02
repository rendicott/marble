package tts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/db"
)

var (
	// ErrDisabled is returned when TTS is off or provider is none.
	ErrDisabled = errors.New("tts_disabled")
	// ErrNotConfigured is returned when the API key env is empty.
	ErrNotConfigured = errors.New("tts_not_configured")
	// ErrTextTooLong is returned when text exceeds max_chars_per_request.
	ErrTextTooLong = errors.New("text_too_long")
	// ErrEmptyText is returned for empty/whitespace-only text.
	ErrEmptyText = errors.New("empty_text")
)

// Request is one synthesize call.
type Request struct {
	Text      string
	Voice     string
	Model     string
	Format    string
	SessionID string
	PhaseID   string
	MessageID string
}

// Result is synth or cache output plus session attachment id.
type Result struct {
	AttachmentID string
	MIME         string
	Bytes        int
	DurationMS   int
	Provider     string
	Voice        string
	Model        string
	Cached       bool
	URL          string
}

// Provider synthesizes speech bytes.
type Provider interface {
	Name() string
	Synthesize(ctx context.Context, apiKey string, req Request) (mime string, audio []byte, err error)
}

// Status is the public probe (no secrets).
type Status struct {
	Enabled                bool   `json:"enabled"`
	Provider               string `json:"provider"`
	Configured             bool   `json:"configured"`
	DefaultVoice           string `json:"default_voice"`
	DefaultModel           string `json:"default_model"`
	Cache                  bool   `json:"cache"`
	Format                 string `json:"format"`
	MaxCharsPerRequest     int    `json:"max_chars_per_request"`
	MaxConcurrent          int    `json:"max_concurrent"`
	EagerOnWonderstandTurn bool   `json:"eager_on_wonderstand_turn"`
	APIKeyEnv              string `json:"api_key_env"`
	// Lightweight metrics (process lifetime).
	SynthCalls   int64  `json:"synth_calls"`
	CacheHits    int64  `json:"cache_hits"`
	SynthErrors  int64  `json:"synth_errors"`
	CharsSpoken  int64  `json:"chars_spoken"`
	ConfigPath   string `json:"config_path,omitempty"`
	// Ready is true when a synth call can succeed (key + provider + required fields).
	Ready bool `json:"ready"`
	// ReadyHint explains why Ready is false (safe; no secrets).
	ReadyHint string `json:"ready_hint,omitempty"`
	// LastError is the most recent synthesize failure (truncated; no secrets).
	LastError string `json:"last_error,omitempty"`
}

// Manager owns config, provider, cache, and attachment writes.
type Manager struct {
	cfg        Config
	db         *db.DB
	memoryRoot string
	configPath string
	provider   Provider
	sem        chan struct{}
	forceOff   bool

	mu          sync.Mutex
	synthCalls  int64
	cacheHits   int64
	synthErrors int64
	charsSpoken int64
	lastError   string
}

// NewManager builds a TTS manager. provider may be nil when disabled.
func NewManager(cfg Config, d *db.DB, memoryRoot string, forceOff bool) *Manager {
	cfg = applyDefaults(cfg)
	m := &Manager{
		cfg:        cfg,
		db:         d,
		memoryRoot: memoryRoot,
		configPath: ResolveConfigPath("", memoryRoot),
		forceOff:   forceOff,
	}
	n := cfg.MaxConcurrent
	if n <= 0 {
		n = 2
	}
	m.sem = make(chan struct{}, n)
	m.rebuildProvider()
	return m
}

func (m *Manager) rebuildProvider() {
	if m == nil {
		return
	}
	m.provider = nil
	if m.forceOff || !m.cfg.Enabled {
		return
	}
	switch m.cfg.Provider {
	case "elevenlabs":
		m.provider = NewElevenLabs(nil)
	case "openai":
		m.provider = NewOpenAI(nil)
	default:
		if m.cfg.Provider != "none" {
			log.Printf("tts: unknown provider %q", m.cfg.Provider)
		}
	}
}

// ConfigPath returns the resolved tts.json path.
func (m *Manager) ConfigPath() string {
	if m == nil {
		return ""
	}
	return m.configPath
}

// SetConfigPath records where config was loaded from (for Settings).
func (m *Manager) SetConfigPath(path string) {
	if m == nil {
		return
	}
	m.configPath = path
}

// ConfigSnapshot returns a copy of the live config.
func (m *Manager) ConfigSnapshot() Config {
	if m == nil {
		return DefaultConfig()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// EagerEnabled reports whether eager_on_wonderstand_turn is on.
func (m *Manager) EagerEnabled() bool {
	if m == nil || !m.enabled() || m.provider == nil {
		return false
	}
	return m.cfg.EagerOnWonderstandTurn
}

// ApplyConfig updates live config (and optionally persists to disk).
func (m *Manager) ApplyConfig(cfg Config, persist bool) error {
	if m == nil {
		return fmt.Errorf("tts unavailable")
	}
	if m.forceOff {
		return fmt.Errorf("tts disabled via --tts-disable")
	}
	cfg = applyDefaults(cfg)
	if err := cfg.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg = cfg
	m.rebuildProvider()
	path := m.configPath
	m.mu.Unlock()
	if persist {
		if path == "" {
			return fmt.Errorf("no tts config path")
		}
		return Save(path, cfg)
	}
	return nil
}

// SetProvider replaces the provider (tests).
func (m *Manager) SetProvider(p Provider) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.provider = p
	m.mu.Unlock()
}

func (m *Manager) enabled() bool {
	if m == nil || m.forceOff {
		return false
	}
	return m.cfg.Enabled && m.cfg.Provider != "none"
}

// Status returns a safe operator probe.
func (m *Manager) Status() Status {
	if m == nil {
		d := DefaultConfig()
		return Status{
			Enabled: false, Provider: "none", Format: d.Format,
			MaxCharsPerRequest: d.MaxCharsPerRequest, Cache: d.Cache,
		}
	}
	m.mu.Lock()
	cfg := m.cfg
	prov := m.provider
	calls, hits, errs, chars := m.synthCalls, m.cacheHits, m.synthErrors, m.charsSpoken
	path := m.configPath
	lastErr := m.lastError
	m.mu.Unlock()
	_, _, keyOK := config.ResolveAPIKeyEnv(cfg.APIKeyEnv)
	en := !m.forceOff && cfg.Enabled && cfg.Provider != "none" && prov != nil
	configured := keyOK && en
	ready := configured
	hint := ""
	if m.forceOff {
		hint = "disabled via --tts-disable"
		ready = false
	} else if !cfg.Enabled || cfg.Provider == "none" || prov == nil {
		hint = "tts off or provider none"
		ready = false
	} else if !keyOK {
		hint = "api key env empty (" + cfg.APIKeyEnv + ")"
		ready = false
	} else if cfg.Provider == "elevenlabs" && strings.TrimSpace(cfg.DefaultVoice) == "" {
		// Key present → configured for Settings chips, but not ready to synth.
		ready = false
		configured = false
		hint = "elevenlabs needs default_voice (voice id) in Settings → TTS / tts.json"
	}
	st := Status{
		Enabled:                en,
		Provider:               cfg.Provider,
		Configured:             configured,
		Ready:                  ready,
		ReadyHint:              hint,
		DefaultVoice:           cfg.DefaultVoice,
		DefaultModel:           cfg.DefaultModel,
		Cache:                  cfg.Cache,
		Format:                 cfg.Format,
		MaxCharsPerRequest:     cfg.MaxCharsPerRequest,
		MaxConcurrent:          cfg.MaxConcurrent,
		EagerOnWonderstandTurn: cfg.EagerOnWonderstandTurn,
		APIKeyEnv:              cfg.APIKeyEnv,
		SynthCalls:             calls,
		CacheHits:              hits,
		SynthErrors:            errs,
		CharsSpoken:            chars,
		ConfigPath:             path,
		LastError:              lastErr,
	}
	return st
}

// SynthesizeForSession runs cache → provider → session attachment.
func (m *Manager) SynthesizeForSession(ctx context.Context, req Request) (Result, error) {
	if m == nil || !m.enabled() || m.provider == nil {
		return Result{}, ErrDisabled
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return Result{}, ErrEmptyText
	}
	if m.cfg.MaxCharsPerRequest > 0 && len([]rune(text)) > m.cfg.MaxCharsPerRequest {
		return Result{}, ErrTextTooLong
	}
	sid := strings.TrimSpace(req.SessionID)
	if sid == "" {
		return Result{}, fmt.Errorf("session_id required")
	}
	if m.db == nil {
		return Result{}, fmt.Errorf("database unavailable")
	}

	voice := strings.TrimSpace(req.Voice)
	if voice == "" {
		voice = strings.TrimSpace(m.cfg.DefaultVoice)
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(m.cfg.DefaultModel)
	}
	format := strings.TrimSpace(req.Format)
	if format == "" {
		format = m.cfg.Format
	}
	req.Text = text
	req.Voice = voice
	req.Model = model
	req.Format = format

	provName := m.provider.Name()
	key := CacheKey(provName, model, voice, text)

	apiKey, _, ok := config.ResolveAPIKeyEnv(m.cfg.APIKeyEnv)
	if !ok || strings.TrimSpace(apiKey) == "" {
		return Result{}, ErrNotConfigured
	}

	var (
		audio  []byte
		mime   string
		cached bool
	)
	if data, mimetype, hit := m.loadGlobalCache(key); hit {
		audio, mime, cached = data, mimetype, true
		m.mu.Lock()
		m.cacheHits++
		m.mu.Unlock()
	} else {
		select {
		case m.sem <- struct{}{}:
			defer func() { <-m.sem }()
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
		var err error
		mime, audio, err = m.provider.Synthesize(ctx, apiKey, req)
		if err != nil {
			m.mu.Lock()
			m.synthErrors++
			m.lastError = truncateTTSErr(err.Error(), 240)
			m.mu.Unlock()
			return Result{}, err
		}
		if mime == "" {
			mime = mimeForFormat(format)
		}
		m.storeGlobalCache(key, mime, provName, model, voice, audio)
		m.mu.Lock()
		m.synthCalls++
		m.charsSpoken += int64(len([]rune(text)))
		m.lastError = ""
		m.mu.Unlock()
	}

	attID, err := m.persistAudio(sid, mime, audio, key, provName, model, voice, req)
	if err != nil {
		return Result{}, err
	}
	return Result{
		AttachmentID: attID,
		MIME:         mime,
		Bytes:        len(audio),
		Provider:     provName,
		Voice:        voice,
		Model:        model,
		Cached:       cached,
		URL:          fmt.Sprintf("/api/sessions/%s/attachments/%s", sid, attID),
	}, nil
}

func mimeForFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "wav":
		return "audio/wav"
	case "ogg":
		return "audio/ogg"
	default:
		return "audio/mpeg"
	}
}

func (m *Manager) persistAudio(sessionID, mime string, data []byte, cacheKey, provider, model, voice string, req Request) (string, error) {
	if !IsSafeAudioMIME(mime) {
		return "", fmt.Errorf("unsupported audio mime %q", mime)
	}
	attID := db.NewAttachmentID()
	rel, sum, err := m.db.WriteAttachmentFile(sessionID, attID, data)
	if err != nil {
		return "", err
	}
	ext := ".mp3"
	switch mime {
	case "audio/wav":
		ext = ".wav"
	case "audio/ogg":
		ext = ".ogg"
	case "audio/webm":
		ext = ".webm"
	case "audio/mp4":
		ext = ".m4a"
	}
	name := "tts-" + attID[:8] + ext
	meta, _ := json.Marshal(map[string]interface{}{
		"cache_key":  cacheKey,
		"provider":   provider,
		"model":      model,
		"voice":      voice,
		"phase_id":   req.PhaseID,
		"message_id": req.MessageID,
	})
	row := db.AttachmentRow{
		ID:        attID,
		SessionID: sessionID,
		CreatedAt: db.UTCNow(),
		Name:      name,
		MIME:      mime,
		Kind:      "audio",
		ByteSize:  int64(len(data)),
		SHA256:    sum,
		Source:    "tts",
		Path:      rel,
		MetaJSON:  string(meta),
	}
	if err := m.db.InsertAttachment(row); err != nil {
		return "", err
	}
	return attID, nil
}

// IsSafeAudioMIME reports whether mime may be served inline (ADR-0027 Q6).
func IsSafeAudioMIME(mime string) bool {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "audio/mpeg", "audio/mp3", "audio/mp4", "audio/wav", "audio/ogg", "audio/webm":
		return true
	default:
		return false
	}
}

// SynthTimeout is the default provider HTTP budget.
const SynthTimeout = 60 * time.Second

func truncateTTSErr(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
