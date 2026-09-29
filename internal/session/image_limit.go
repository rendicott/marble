package session

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/model"
)

// Per-request image cap (session setting × model catalog max_images).
//
// Every image in model history (user attachments, peer screenshots) is re-sent
// on every model call. Some providers hard-reject requests over a count (vLLM
// --limit-mm-per-prompt → "At most 2 image(s) may be provided in one prompt"),
// and on the rest each stale screenshot is paid for again on every call. The
// cap trims the outbound copy only; stored history keeps every image.

// DefaultImageLimit applies when the session has no explicit setting.
const DefaultImageLimit = 2

// ImageLimitModel is the session setting meaning "the model's max_images"
// (unlimited when the model has none).
const ImageLimitModel = -1

// maxImageLimitSetting bounds operator input.
const maxImageLimitSetting = 100

// ImageLimitInfo is the public view of a session's image cap.
type ImageLimitInfo struct {
	Mode      string `json:"mode"`         // default | number | model
	Value     int    `json:"value"`        // configured cap for default/number modes
	By        string `json:"by,omitempty"` // "harness" when the harness set it
	ModelMax  int    `json:"model_max"`    // provider cap; 0 = unlimited/unknown
	Effective int    `json:"effective"`    // cap applied per request; 0 = unlimited
	Default   int    `json:"default"`
}

func imageLimitInfo(setting int, by string, modelMax int) ImageLimitInfo {
	info := ImageLimitInfo{By: by, ModelMax: modelMax, Default: DefaultImageLimit}
	switch {
	case setting == ImageLimitModel:
		info.Mode = "model"
		info.Effective = modelMax
		return info
	case setting > 0:
		info.Mode = "number"
		info.Value = setting
	default:
		info.Mode = "default"
		info.Value = DefaultImageLimit
	}
	info.Effective = info.Value
	if modelMax > 0 && modelMax < info.Effective {
		info.Effective = modelMax
	}
	return info
}

// ImageLimitFor returns the session's image cap resolved against em.
func (s *Session) ImageLimitFor(em EffectiveModel) ImageLimitInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return imageLimitInfo(s.ImageLimit, s.ImageLimitBy, em.MaxImages)
}

// encodeImageLimit / decodeImageLimit map the setting to session front matter.
func encodeImageLimit(v int) string {
	switch {
	case v == ImageLimitModel:
		return "model"
	case v > 0:
		return strconv.Itoa(v)
	}
	return ""
}

func decodeImageLimit(raw string) int {
	v, err := parseImageLimitSetting(raw)
	if err != nil {
		return 0
	}
	return v
}

// parseImageLimitSetting accepts "", "default", "model", or 1..maxImageLimitSetting.
func parseImageLimitSetting(raw string) (int, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "", "default":
		return 0, nil
	case "model", "max":
		return ImageLimitModel, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxImageLimitSetting {
		return 0, fmt.Errorf("image_limit must be \"model\", \"default\", or 1-%d", maxImageLimitSetting)
	}
	return n, nil
}

// countImageParts counts image_url parts across msgs.
func countImageParts(msgs []model.Message) int {
	n := 0
	for _, m := range msgs {
		for _, p := range m.Content.Parts {
			if p.Type == "image_url" {
				n++
			}
		}
	}
	return n
}

// applyImageLimit keeps at most limit image parts (limit ≤ 0 = unlimited).
// Images in the latest user message win first (the operator just attached
// them), then the most recent images elsewhere. Dropped images become a short
// text placeholder so the model knows something was there. msgs is not mutated.
func applyImageLimit(msgs []model.Message, limit int) ([]model.Message, int) {
	if limit <= 0 || countImageParts(msgs) <= limit {
		return msgs, 0
	}
	type pos struct{ mi, pi int }
	lastUser := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastUser = i
			break
		}
	}
	var priority []pos
	var rest []pos
	for mi := len(msgs) - 1; mi >= 0; mi-- {
		parts := msgs[mi].Content.Parts
		for pi := len(parts) - 1; pi >= 0; pi-- {
			if parts[pi].Type != "image_url" {
				continue
			}
			if mi == lastUser {
				priority = append(priority, pos{mi, pi})
			} else {
				rest = append(rest, pos{mi, pi})
			}
		}
	}
	keep := map[pos]bool{}
	for _, p := range append(priority, rest...) {
		if len(keep) >= limit {
			break
		}
		keep[p] = true
	}

	out := make([]model.Message, len(msgs))
	copy(out, msgs)
	omitted := 0
	for mi := range out {
		parts := out[mi].Content.Parts
		if len(parts) == 0 {
			continue
		}
		changed := false
		next := make([]model.ContentPart, 0, len(parts))
		for pi, p := range parts {
			if p.Type != "image_url" || keep[pos{mi, pi}] {
				next = append(next, p)
				continue
			}
			changed = true
			omitted++
			next = append(next, model.ContentPart{Type: "text", Text: omittedImageText(p, limit)})
		}
		if changed {
			out[mi].Content = collapseTextParts(next)
		}
	}
	return out, omitted
}

func omittedImageText(p model.ContentPart, limit int) string {
	id := ""
	if p.ImageURL != nil && strings.HasPrefix(p.ImageURL.URL, marbleAttScheme) {
		id = " " + strings.TrimPrefix(p.ImageURL.URL, marbleAttScheme)
	}
	return fmt.Sprintf("[harness] older image%s omitted (per-request image limit %d; only the most recent images are sent). Take a fresh screenshot if you need to see the current screen.", id, limit)
}

// collapseTextParts turns an all-text part list back into plain text content.
func collapseTextParts(parts []model.ContentPart) model.Content {
	var b strings.Builder
	for _, p := range parts {
		if p.Type != "text" {
			return model.ContentFromParts(parts)
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(p.Text)
	}
	return model.ContentFromText(b.String())
}

var (
	imageLimitErrRes = []*regexp.Regexp{
		// vLLM --limit-mm-per-prompt
		regexp.MustCompile(`at most (\d+) image`),
		regexp.MustCompile(`too many images[^0-9]*\d+\s*>\s*(\d+)`),
		regexp.MustCompile(`(?:maximum|max|up to|limit of|no more than)\s+(?:of\s+)?(\d+)\s+images?`),
		regexp.MustCompile(`images?[^.]{0,40}(?:limit|maximum|max)\s+(?:is\s+)?(\d+)`),
	}
	imageLimitErrHints = []string{"too many image", "image limit", "images exceed", "exceeds the maximum number of image"}
)

// parseImageLimitError extracts a provider's image cap from a request error.
// sent is how many images the rejected request carried; when the message
// names no number, the cap is assumed to be sent-1 (the retry trims further
// if still too high). ok=false when the error is not an image-count error.
func parseImageLimitError(errText string, sent int) (int, bool) {
	low := strings.ToLower(errText)
	if !strings.Contains(low, "image") || sent <= 0 {
		return 0, false
	}
	for _, re := range imageLimitErrRes {
		if m := re.FindStringSubmatch(low); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil || n < 1 || n >= sent {
				return 0, false
			}
			return n, true
		}
	}
	for _, h := range imageLimitErrHints {
		if strings.Contains(low, h) && sent > 1 {
			return sent - 1, true
		}
	}
	return 0, false
}

func learnedImageKey(em EffectiveModel) string {
	return strings.TrimRight(em.BaseURL, "/") + "|" + em.Model
}

// overlayLearnedImageLimit lowers em.MaxImages to a limit learned this process.
func (r *Runner) overlayLearnedImageLimit(em *EffectiveModel) {
	r.imageMaxMu.Lock()
	n := r.learnedImageMax[learnedImageKey(*em)]
	r.imageMaxMu.Unlock()
	if n > 0 && (em.MaxImages == 0 || n < em.MaxImages) {
		em.MaxImages = n
	}
}

// learnImageLimit records a provider image cap parsed from an error: in memory
// for this endpoint, on the catalog row (max_images), and on the session when
// its numeric setting is above the cap. The session change is published and
// noted in the transcript so the operator sees what the harness did.
func (r *Runner) learnImageLimit(s *Session, em *EffectiveModel, n, sent int, errText string) {
	r.imageMaxMu.Lock()
	if r.learnedImageMax == nil {
		r.learnedImageMax = map[string]int{}
	}
	key := learnedImageKey(*em)
	if cur := r.learnedImageMax[key]; cur == 0 || n < cur {
		r.learnedImageMax[key] = n
	}
	r.imageMaxMu.Unlock()
	em.MaxImages = n

	catalogNote := "remembered for this endpoint until the harness restarts"
	if em.CatalogID != "" && r.Reg != nil && r.Reg.sqldb != nil && r.Reg.sqldb.Writable() {
		if row, err := r.Reg.sqldb.GetModelCatalog(em.CatalogID); err == nil && row != nil {
			if row.MaxImages == 0 || row.MaxImages > n {
				if err := r.Reg.sqldb.SetModelMaxImages(em.CatalogID, n); err == nil {
					catalogNote = fmt.Sprintf("saved to model catalog %s (max_images=%d)", em.CatalogID, n)
				}
			} else {
				catalogNote = fmt.Sprintf("model catalog %s already has max_images=%d", em.CatalogID, row.MaxImages)
			}
		}
	}

	s.mu.Lock()
	sessionNote := ""
	configured := s.ImageLimit
	if configured == 0 {
		configured = DefaultImageLimit
	}
	if s.ImageLimit != ImageLimitModel && configured > n {
		s.ImageLimit = n
		s.ImageLimitBy = "harness"
		s.markDirty()
		sessionNote = fmt.Sprintf(" Session image limit changed %d → %d.", configured, n)
	} else if s.ImageLimit == ImageLimitModel {
		sessionNote = fmt.Sprintf(" Session uses the model max, now %d.", n)
	}
	info := imageLimitInfo(s.ImageLimit, s.ImageLimitBy, em.MaxImages)
	s.mu.Unlock()
	if r.Reg != nil {
		_ = r.Reg.PersistSession(s)
	}
	s.publish(Event{Type: "session_meta", ImageLimit: &info, ModelEff: em.Public(), At: time.Now()})

	r.harnessNote(s, fmt.Sprintf(
		"⚠️ Image limit learned: model %s rejected a request with %d images (provider allows %d per request). Limit %s.%s Retrying with older images omitted. Right-click / long-press 🔧 to review.\n\nProvider error: %s",
		em.Model, sent, n, catalogNote, sessionNote, truncateOneLine(errText, 240),
	))
}

// harnessNote appends a durable harness note to the transcript (UI only; the
// model never sees role=harness).
func (r *Runner) harnessNote(s *Session, note string) {
	s.mu.Lock()
	m := Message{ID: s.nextID("h"), Role: "harness", Content: note, CreatedAt: time.Now()}
	s.appendUI(m)
	s.mu.Unlock()
	if r.Reg != nil {
		r.Reg.logEvent(s, "harness_advisory", "system", note, "", "", "", nil, nil, nil, nil, nil, "", "")
	}
	s.publish(Event{Type: "message", Message: &m})
}

// SetSessionImageLimit sets the per-request image cap ("", "default", "model",
// or 1..100). Operator changes clear the harness attribution. Allowed while busy
// (applies from the next model call).
func (r *Registry) SetSessionImageLimit(id, raw string) (*Session, error) {
	v, err := parseImageLimitSetting(raw)
	if err != nil {
		return nil, err
	}
	s, err := r.EnsureLoaded(id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.ImageLimit = v
	s.ImageLimitBy = ""
	s.markDirty()
	s.mu.Unlock()
	_ = r.PersistSession(s)
	info := r.ImageLimitInfoFor(s)
	s.publish(Event{Type: "session_meta", ImageLimit: &info, At: time.Now()})
	return s, nil
}

// ImageLimitInfoFor resolves the session's cap against its current model.
func (r *Registry) ImageLimitInfoFor(s *Session) ImageLimitInfo {
	em := r.runner.resolveEffective(s, TurnOpts{})
	return s.ImageLimitFor(em)
}
