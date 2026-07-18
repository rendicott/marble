package token

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

// EstimateMessages sums estimates for a list of role/content pairs plus overhead.
func EstimateMessages(parts []string) int {
	total := 0
	for _, p := range parts {
		total += Estimate(p) + 4 // per-message framing overhead
	}
	return total
}
