package token

// DefaultImageTokens is used when dimensions are unknown (ADR-0019 KD8).
const DefaultImageTokens = 1500

// Estimate returns a rough token count for text.
// v1 uses chars/4 — good enough to keep the prompt under budget.
func Estimate(s string) int {
	if s == "" {
		return 0
	}
	n := (len(s) + 3) / 4
	if n < 1 {
		return 1
	}
	return n
}

// EstimateImage returns a rough vision token count from dimensions (ADR-0019).
func EstimateImage(w, h int) int {
	if w <= 0 || h <= 0 {
		return DefaultImageTokens
	}
	n := 85 + (w*h)/750
	if n < 85 {
		return 85
	}
	return n
}

// EstimateMessages sums estimates for a list of role/content pairs plus overhead.
func EstimateMessages(parts []string) int {
	total := 0
	for _, p := range parts {
		total += Estimate(p) + 4 // per-message framing overhead
	}
	return total
}
