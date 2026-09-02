package session

import (
	"context"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/tts"
)

// BindEagerTTS wires ADR-0027 M4 eager phase.audio fill onto the runner.
func (r *Runner) BindEagerTTS(mgr *tts.Manager) {
	if r == nil || mgr == nil {
		return
	}
	r.FillPresentationAudio = func(sessionID, messageID string, pres *Presentation) {
		if pres == nil || !mgr.EagerEnabled() {
			return
		}
		phases := make([]tts.PhaseSpeech, 0, len(pres.Phases))
		for _, ph := range pres.Phases {
			if ph.Audio != nil && strings.TrimSpace(ph.Audio.AttachmentID) != "" {
				continue // already filled
			}
			text := strings.TrimSpace(ph.SpeechText)
			if text == "" {
				continue
			}
			phases = append(phases, tts.PhaseSpeech{ID: ph.ID, SpeechText: text})
		}
		if len(phases) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		filled := mgr.EagerFill(ctx, sessionID, messageID, phases)
		if len(filled) == 0 {
			return
		}
		byID := make(map[string]tts.PhaseAudioOut, len(filled))
		for _, f := range filled {
			byID[f.ID] = f
		}
		for i := range pres.Phases {
			f, ok := byID[pres.Phases[i].ID]
			if !ok {
				continue
			}
			pres.Phases[i].Audio = &PhaseAudio{
				AttachmentID: f.AttachmentID,
				MIME:         f.MIME,
				DurationMS:   f.DurationMS,
				Voice:        f.Voice,
				Provider:     f.Provider,
			}
		}
	}
}
