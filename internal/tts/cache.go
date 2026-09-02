package tts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// CacheKey builds a stable content-addressed key (ADR-0027 Q8).
func CacheKey(provider, model, voice, text string) string {
	n := NormalizeText(text)
	h := sha256.New()
	_, _ = h.Write([]byte(strings.TrimSpace(provider)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strings.TrimSpace(model)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strings.TrimSpace(voice)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(n))
	return hex.EncodeToString(h.Sum(nil))
}

// NormalizeText collapses Unicode whitespace runs (cache stability).
func NormalizeText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	prevSpace := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			if !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

type cacheMeta struct {
	MIME     string `json:"mime"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Voice    string `json:"voice"`
	Bytes    int    `json:"bytes"`
}

func (m *Manager) cacheDir() string {
	if m == nil || m.memoryRoot == "" {
		return ""
	}
	return filepath.Join(m.memoryRoot, "tts-cache")
}

func (m *Manager) cachePaths(key string) (bin, meta string) {
	dir := m.cacheDir()
	return filepath.Join(dir, key+".bin"), filepath.Join(dir, key+".json")
}

func (m *Manager) loadGlobalCache(key string) (data []byte, mime string, ok bool) {
	if m == nil || !m.cfg.Cache || key == "" {
		return nil, "", false
	}
	bin, metaPath := m.cachePaths(key)
	data, err := os.ReadFile(bin)
	if err != nil || len(data) == 0 {
		return nil, "", false
	}
	mime = "audio/mpeg"
	if b, err := os.ReadFile(metaPath); err == nil {
		var meta cacheMeta
		if json.Unmarshal(b, &meta) == nil && meta.MIME != "" {
			mime = meta.MIME
		}
	}
	return data, mime, true
}

func (m *Manager) storeGlobalCache(key, mime, provider, model, voice string, data []byte) {
	if m == nil || !m.cfg.Cache || key == "" || len(data) == 0 {
		return
	}
	dir := m.cacheDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	bin, metaPath := m.cachePaths(key)
	tmp := bin + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, bin); err != nil {
		_ = os.Remove(tmp)
		return
	}
	meta := cacheMeta{
		MIME:     mime,
		Provider: provider,
		Model:    model,
		Voice:    voice,
		Bytes:    len(data),
	}
	b, _ := json.Marshal(meta)
	_ = os.WriteFile(metaPath, b, 0o600)
}
