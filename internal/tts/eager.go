package tts

import (
	"context"
	"log"
	"strings"
	"time"
)

// PhaseSpeech is the minimal phase input for eager fill (avoids importing session).
type PhaseSpeech struct {
	ID         string
	SpeechText string
}

// PhaseAudioOut is filled audio metadata for a phase.
type PhaseAudioOut struct {
	ID           string
	AttachmentID string
	MIME         string
	DurationMS   int
	Voice        string
	Provider     string
}

// EagerFill synthesizes speech for phases missing audio (ADR-0027 M4).
// Best-effort: logs and continues on per-phase errors; never fails the turn.
func (m *Manager) EagerFill(ctx context.Context, sessionID, messageID string, phases []PhaseSpeech) []PhaseAudioOut {
	if m == nil || !m.EagerEnabled() || len(phases) == 0 {
		return nil
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
	}
	var out []PhaseAudioOut
	for _, ph := range phases {
		text := strings.TrimSpace(ph.SpeechText)
		if text == "" {
			continue
		}
		res, err := m.SynthesizeForSession(ctx, Request{
			Text:      text,
			SessionID: sessionID,
			MessageID: messageID,
			PhaseID:   ph.ID,
		})
		if err != nil {
			log.Printf("tts eager: phase %s: %v", ph.ID, err)
			continue
		}
		out = append(out, PhaseAudioOut{
			ID:           ph.ID,
			AttachmentID: res.AttachmentID,
			MIME:         res.MIME,
			DurationMS:   res.DurationMS,
			Voice:        res.Voice,
			Provider:     res.Provider,
		})
	}
	return out
}
