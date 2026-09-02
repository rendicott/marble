package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/session"
	"github.com/rendicott/marble/internal/tts"
)

// sessionTTSInfo builds the Session Info modal TTS block (process status + last audio).
func (s *Server) sessionTTSInfo(sessionID string) *session.InfoTTS {
	out := &session.InfoTTS{}
	if s.TTS != nil {
		st := s.TTS.Status()
		out.Enabled = st.Enabled
		out.Ready = st.Ready
		out.Configured = st.Configured
		out.Provider = st.Provider
		out.DefaultVoice = st.DefaultVoice
		out.DefaultModel = st.DefaultModel
		out.ReadyHint = st.ReadyHint
		out.LastError = st.LastError
		out.SynthCalls = st.SynthCalls
		out.SynthErrors = st.SynthErrors
	}
	if s.Registry == nil {
		return out
	}
	d := s.Registry.DB()
	if d == nil || !d.Writable() {
		return out
	}
	if n, err := d.CountTTSAttachments(sessionID); err == nil {
		out.ArtifactCount = n
	}
	rows, err := d.ListTTSAttachments(sessionID, 1)
	if err != nil || len(rows) == 0 {
		return out
	}
	r := rows[0]
	art := &session.InfoTTSArtifact{
		ID:        r.ID,
		Name:      r.Name,
		MIME:      r.MIME,
		Bytes:     r.ByteSize,
		MessageID: r.MessageID,
		CreatedAt: r.CreatedAt,
		URL:       fmt.Sprintf("/api/sessions/%s/attachments/%s", sessionID, r.ID),
	}
	if strings.TrimSpace(r.MetaJSON) != "" {
		var meta map[string]interface{}
		if json.Unmarshal([]byte(r.MetaJSON), &meta) == nil {
			if v, ok := meta["provider"].(string); ok {
				art.Provider = v
			}
			if v, ok := meta["model"].(string); ok {
				art.Model = v
			}
			if v, ok := meta["voice"].(string); ok {
				art.Voice = v
			}
			if v, ok := meta["phase_id"].(string); ok {
				art.PhaseID = v
			}
		}
	}
	out.LastArtifact = art
	return out
}

// handleTTSStatus serves GET /api/tts/status (ADR-0027).
func (s *Server) handleTTSStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.TTS == nil {
		writeJSON(w, http.StatusOK, (&tts.Manager{}).Status())
		return
	}
	writeJSON(w, http.StatusOK, s.TTS.Status())
}

// handleTTSSettings serves GET/PUT /api/settings/tts (config + status).
func (s *Server) handleTTSSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := tts.DefaultConfig()
		path := ""
		st := (&tts.Manager{}).Status()
		if s.TTS != nil {
			cfg = s.TTS.ConfigSnapshot()
			path = s.TTS.ConfigPath()
			st = s.TTS.Status()
		} else if s.Cfg.Memory != "" {
			path = tts.ResolveConfigPath(s.Cfg.TTSConfig, s.Cfg.Memory)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"config":      cfg,
			"status":      st,
			"config_path": path,
			"forced_off":  s.Cfg.TTSDisable,
		})
	case http.MethodPut:
		if s.TTS == nil {
			http.Error(w, "tts unavailable", http.StatusServiceUnavailable)
			return
		}
		if s.Cfg.TTSDisable {
			http.Error(w, "tts disabled via --tts-disable", http.StatusBadRequest)
			return
		}
		var body tts.Config
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if err := s.TTS.ApplyConfig(body, true); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":          true,
			"config":      s.TTS.ConfigSnapshot(),
			"status":      s.TTS.Status(),
			"config_path": s.TTS.ConfigPath(),
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSessionTTS serves POST /api/sessions/{id}/tts (ADR-0027).
// Allowed while the session is busy (Q5) — does not start an agent turn.
func (s *Server) handleSessionTTS(w http.ResponseWriter, r *http.Request, sessionID string, rest []string) {
	if len(rest) == 1 && rest[0] == "batch" {
		s.handleSessionTTSBatch(w, r, sessionID)
		return
	}
	if len(rest) > 0 {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.TTS == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "tts_disabled",
		})
		return
	}
	if s.Registry == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	sess, err := s.Registry.EnsureLoaded(sessionID)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if sess.Status == "closed" {
		http.Error(w, "session closed", http.StatusBadRequest)
		return
	}

	var body struct {
		Text      string `json:"text"`
		Voice     string `json:"voice"`
		Model     string `json:"model"`
		MessageID string `json:"message_id"`
		PhaseID   string `json:"phase_id"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), tts.SynthTimeout+5*time.Second)
	defer cancel()

	res, err := s.TTS.SynthesizeForSession(ctx, tts.Request{
		Text:      body.Text,
		Voice:     body.Voice,
		Model:     body.Model,
		SessionID: sessionID,
		MessageID: body.MessageID,
		PhaseID:   body.PhaseID,
	})
	if err != nil {
		writeTTSError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"attachment_id": res.AttachmentID,
		"mime":          res.MIME,
		"bytes":         res.Bytes,
		"duration_ms":   res.DurationMS,
		"voice":         res.Voice,
		"provider":      res.Provider,
		"model":         res.Model,
		"cached":        res.Cached,
		"url":           res.URL,
	})
}

func (s *Server) handleSessionTTSBatch(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.TTS == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "tts_disabled"})
		return
	}
	if s.Registry == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	sess, err := s.Registry.EnsureLoaded(sessionID)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if sess.Status == "closed" {
		http.Error(w, "session closed", http.StatusBadRequest)
		return
	}
	var body struct {
		Items []struct {
			Text      string `json:"text"`
			Voice     string `json:"voice"`
			Model     string `json:"model"`
			MessageID string `json:"message_id"`
			PhaseID   string `json:"phase_id"`
		} `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	const maxBatch = 32
	if len(body.Items) == 0 {
		http.Error(w, "items required", http.StatusBadRequest)
		return
	}
	if len(body.Items) > maxBatch {
		http.Error(w, "too many items (max 32)", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), tts.SynthTimeout*time.Duration(len(body.Items))+30*time.Second)
	defer cancel()
	results := make([]interface{}, 0, len(body.Items))
	for _, it := range body.Items {
		res, err := s.TTS.SynthesizeForSession(ctx, tts.Request{
			Text: it.Text, Voice: it.Voice, Model: it.Model,
			SessionID: sessionID, MessageID: it.MessageID, PhaseID: it.PhaseID,
		})
		if err != nil {
			results = append(results, map[string]string{
				"error":    err.Error(),
				"phase_id": it.PhaseID,
			})
			continue
		}
		results = append(results, map[string]interface{}{
			"attachment_id": res.AttachmentID,
			"mime":          res.MIME,
			"bytes":         res.Bytes,
			"duration_ms":   res.DurationMS,
			"voice":         res.Voice,
			"provider":      res.Provider,
			"model":         res.Model,
			"cached":        res.Cached,
			"url":           res.URL,
			"phase_id":      it.PhaseID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"results": results})
}

func writeTTSError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tts.ErrDisabled):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "tts_disabled"})
	case errors.Is(err, tts.ErrNotConfigured):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "tts_not_configured"})
	case errors.Is(err, tts.ErrEmptyText):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty_text"})
	case errors.Is(err, tts.ErrTextTooLong):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text_too_long"})
	default:
		msg := err.Error()
		if strings.Contains(strings.ToLower(msg), "unauthorized") || strings.Contains(msg, "tts_not_configured") {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "tts_not_configured", "detail": msg})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "provider_failed", "detail": msg})
	}
}
