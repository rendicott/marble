package session

import (
	"encoding/json"
	"log"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ProtocolVersion is the presentation protocol defined by ADR-0025.
const ProtocolVersion = 1

// Size caps (ADR-0025 Q6 locked).
const (
	MaxPresentationPhases   = 32
	MaxSpeechTextBytes      = 2 * 1024
	MaxVisualPayloadBytes   = 256 * 1024
	MaxPresentationJSONSize = 1 * 1024 * 1024
)

// ClientAdvertise is the optional body field on createSession / postMessage (ADR-0025).
// Unknown fields are ignored by encoding/json.
type ClientAdvertise struct {
	Name     string `json:"name"`
	Protocol int    `json:"protocol"`
}

// Presentation is the optional structured immersion block on assistant messages.
type Presentation struct {
	ProtocolVersion int     `json:"protocol_version"`
	Phases          []Phase `json:"phases"`
}

// Phase is one immersion beat (scrubber key = id).
type Phase struct {
	ID             string     `json:"id"`
	SpeechText     string     `json:"speech_text"`
	ProseMarkdown  string     `json:"prose_markdown,omitempty"`
	Visual         *Visual    `json:"visual,omitempty"`
	Audio          *PhaseAudio `json:"audio,omitempty"` // ADR-0027 additive; no protocol bump
}

// PhaseAudio is optional neural narration on a phase (ADR-0027).
type PhaseAudio struct {
	AttachmentID string `json:"attachment_id"`
	MIME         string `json:"mime,omitempty"`
	DurationMS   int    `json:"duration_ms,omitempty"`
	Voice        string `json:"voice,omitempty"`
	Provider     string `json:"provider,omitempty"`
}

// Visual is a first-class phase visual (exactly one payload matching kind).
type Visual struct {
	Kind              string     `json:"kind"` // svg | html | image_attachment_id | table
	SVG               string     `json:"svg,omitempty"`
	HTML              string     `json:"html,omitempty"`
	ImageAttachmentID string     `json:"image_attachment_id,omitempty"`
	Table             *TableData `json:"table,omitempty"`
}

// TableData is a structured table visual.
type TableData struct {
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
}

// WonderstandPromptPack is injected as an extra system message when the session
// advertises client.name=wonderstand and client.protocol>=1 (ADR-0025 M2).
const WonderstandPromptPack = `Wonderstand immersion client is connected (protocol 1).
Structure the final answer for immersion as well as the web UI:
- Keep normal markdown in the main answer (web and logs still need content).
- Prefer short TTS-friendly paragraphs (plain language; avoid raw markdown noise when spoken).
- Prefer lead-with-visual: concrete SVG fences, markdown tables, or image attachments when useful.
- Optionally emit a single fenced JSON block tagged presentation matching protocol 1:
  {"protocol_version":1,"phases":[{"id":"p1","speech_text":"…","visual":{"kind":"svg","svg":"<svg…/>"}}]}
  Phase ids must be stable and unique within the message. speech_text is plain text for TTS.
  visual.kind is one of: svg, html, image_attachment_id, table (with matching payload field).
  Omit visual on a phase to hold the previous visual on screen.
Do not invent attachment ids. Do not omit the normal markdown body.`

var presentationFenceRe = regexp.MustCompile("(?is)```presentation\\s*\\n(.*?)```")

// ExtractPresentationFence finds an optional ```presentation fenced JSON block in content.
// Returns the parsed Presentation (unclamped) and whether a fence was found and decoded.
// On parse failure returns (nil, true) so callers can log without failing the turn.
func ExtractPresentationFence(content string) (p *Presentation, found bool) {
	m := presentationFenceRe.FindStringSubmatch(content)
	if m == nil {
		return nil, false
	}
	raw := strings.TrimSpace(m[1])
	if raw == "" {
		return nil, true
	}
	var out Presentation
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		log.Printf("presentation: fence JSON decode failed: %v", err)
		return nil, true
	}
	return &out, true
}

// ClampPresentation enforces size caps (Q6): clamp + log, never error.
// Returns nil if the presentation is unusable after clamping (no phases).
func ClampPresentation(p *Presentation) *Presentation {
	if p == nil {
		return nil
	}
	if p.ProtocolVersion <= 0 {
		p.ProtocolVersion = ProtocolVersion
	}
	if len(p.Phases) > MaxPresentationPhases {
		log.Printf("presentation: clamping phases %d → %d", len(p.Phases), MaxPresentationPhases)
		p.Phases = p.Phases[:MaxPresentationPhases]
	}
	seen := make(map[string]int)
	out := make([]Phase, 0, len(p.Phases))
	for i, ph := range p.Phases {
		id := strings.TrimSpace(ph.ID)
		if id == "" {
			id = "p" + strconv.Itoa(i+1)
		}
		// Ensure uniqueness within message
		if n, ok := seen[id]; ok {
			seen[id] = n + 1
			id = id + "_" + strconv.Itoa(n+1)
		} else {
			seen[id] = 1
		}
		ph.ID = id
		ph.SpeechText = clampUTF8Bytes(ph.SpeechText, MaxSpeechTextBytes, "speech_text")
		ph.ProseMarkdown = clampUTF8Bytes(ph.ProseMarkdown, MaxSpeechTextBytes*4, "prose_markdown")
		if ph.Visual != nil {
			ph.Visual = clampVisual(ph.Visual)
		}
		out = append(out, ph)
	}
	p.Phases = out

	// Total JSON size cap
	b, err := json.Marshal(p)
	if err != nil {
		log.Printf("presentation: marshal for size check failed: %v", err)
		return p
	}
	if len(b) > MaxPresentationJSONSize {
		log.Printf("presentation: total JSON %d > %d; dropping presentation", len(b), MaxPresentationJSONSize)
		return nil
	}
	if len(p.Phases) == 0 {
		return nil
	}
	return p
}

func clampVisual(v *Visual) *Visual {
	if v == nil {
		return nil
	}
	kind := strings.ToLower(strings.TrimSpace(v.Kind))
	v.Kind = kind
	switch kind {
	case "svg":
		v.SVG = clampUTF8Bytes(v.SVG, MaxVisualPayloadBytes, "svg")
		v.HTML, v.ImageAttachmentID, v.Table = "", "", nil
	case "html":
		v.HTML = clampUTF8Bytes(v.HTML, MaxVisualPayloadBytes, "html")
		v.SVG, v.ImageAttachmentID, v.Table = "", "", nil
	case "image_attachment_id":
		v.ImageAttachmentID = strings.TrimSpace(v.ImageAttachmentID)
		v.SVG, v.HTML, v.Table = "", "", nil
		if v.ImageAttachmentID == "" {
			return nil
		}
	case "table":
		v.SVG, v.HTML, v.ImageAttachmentID = "", "", ""
		if v.Table == nil {
			return nil
		}
		// Soft clamp: drop excess rows rather than fail
		const maxRows = 200
		const maxCols = 32
		if len(v.Table.Headers) > maxCols {
			v.Table.Headers = v.Table.Headers[:maxCols]
		}
		if len(v.Table.Rows) > maxRows {
			log.Printf("presentation: clamping table rows %d → %d", len(v.Table.Rows), maxRows)
			v.Table.Rows = v.Table.Rows[:maxRows]
		}
		for i := range v.Table.Rows {
			if len(v.Table.Rows[i]) > maxCols {
				v.Table.Rows[i] = v.Table.Rows[i][:maxCols]
			}
		}
	default:
		// Unknown kind: keep speech path; drop visual (client would skip too)
		log.Printf("presentation: unknown visual.kind %q — dropping visual", kind)
		return nil
	}
	return v
}

func clampUTF8Bytes(s string, max int, label string) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	// Walk runes so we don't split mid-codepoint
	n := 0
	for i := range s {
		if i > max {
			log.Printf("presentation: clamping %s %d → %d bytes", label, len(s), n)
			return s[:n]
		}
		n = i
	}
	// whole string is within last rune boundary but len > max — trim
	if len(s) > max {
		// find last valid start <= max
		for max > 0 && !utf8.RuneStart(s[max]) {
			max--
		}
		log.Printf("presentation: clamping %s %d → %d bytes", label, len(s), max)
		return s[:max]
	}
	return s
}

// PresentationMetaJSON returns a small meta_json payload for session_events (not full body).
func PresentationMetaJSON(p *Presentation) string {
	if p == nil {
		return ""
	}
	b, err := json.Marshal(map[string]interface{}{
		"has_presentation":  true,
		"protocol_version":  p.ProtocolVersion,
		"phase_count":       len(p.Phases),
	})
	if err != nil {
		return ""
	}
	return string(b)
}

// MarshalPresentationCompact returns compact JSON for MD persistence, or empty.
func MarshalPresentationCompact(p *Presentation) string {
	if p == nil {
		return ""
	}
	b, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// UnmarshalPresentation parses presentation JSON (empty/invalid → nil).
func UnmarshalPresentation(raw string) *Presentation {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var p Presentation
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		log.Printf("presentation: unmarshal failed: %v", err)
		return nil
	}
	return ClampPresentation(&p)
}

// IsWonderstandProtocol reports whether sticky client should get prompt pack / enrichment.
func IsWonderstandProtocol(name string, protocol int) bool {
	return strings.EqualFold(strings.TrimSpace(name), "wonderstand") && protocol >= 1
}
