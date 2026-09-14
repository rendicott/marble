package sink

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// TurnEvent is the normalized payload handed to every sink (ADR-0028).
type TurnEvent struct {
	SessionID      string    `json:"session_id"`
	SessionTitle   string    `json:"session_title"`
	Workspace      string    `json:"workspace"`
	ModelID        string    `json:"model_id"`
	TurnID         string    `json:"turn_id"`
	Kind           string    `json:"kind"` // complete | error | stop | cron | continuation
	Message        string    `json:"message"`
	Preview        string    `json:"preview"`
	DeepLink       string    `json:"deep_link"`
	Timestamp      time.Time `json:"timestamp"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
}

// KnownKinds is the allow-list used by filters and the Settings UI.
var KnownKinds = []string{"complete", "error", "stop", "cron", "continuation"}

const previewMaxRunes = 160

// PreviewOf returns the first line of s, collapsed, capped for banners.
func PreviewOf(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	return truncateRunes(s, previewMaxRunes)
}

func truncateRunes(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	if n == 1 {
		return string(r[:1])
	}
	return string(r[:n-1]) + "…"
}

// IdempotencyKey is the deterministic hash(sink, session, turn) (ADR-0028 Q11).
func IdempotencyKey(sinkID, sessionID, turnID string) string {
	h := sha256.New()
	h.Write([]byte(sinkID))
	h.Write([]byte{0})
	h.Write([]byte(sessionID))
	h.Write([]byte{0})
	h.Write([]byte(turnID))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16])
}

func isCronTitle(title string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(title)), "cron:")
}

func isContinuationText(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "[scheduled continuation]")
}

// ValidOrbReturnURL matches Orb NormalizeReturnURL: http or https, host
// required, no userinfo, ≤512 chars. Empty is "not set", not an error.
func ValidOrbReturnURL(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 512 {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return true
	default:
		return false
	}
}
