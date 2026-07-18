package session

import (
	"github.com/rendicott/marble/internal/model"
	"github.com/rendicott/marble/internal/token"
)

// trimHistory returns a deep-enough copy of messages that fits within budget tokens.
// Always keeps the system message (if first) and prefers the newest messages.
func trimHistory(messages []model.Message, budget int, toolSchemaEstimate int) []model.Message {
	if budget < 512 {
		budget = 512
	}
	available := budget - toolSchemaEstimate
	if available < 256 {
		available = 256
	}

	// Always copy so we never mutate session history.
	msgs := cloneMessages(messages)

	if estimateAll(msgs) <= available {
		return msgs
	}

	var system []model.Message
	rest := msgs
	if len(msgs) > 0 && msgs[0].Role == "system" {
		system = msgs[:1]
		rest = msgs[1:]
	}

	sysCost := estimateAll(system)
	for len(rest) > 1 && sysCost+estimateAll(rest) > available {
		rest = rest[1:]
	}

	for sysCost+estimateAll(rest) > available && len(rest) > 0 {
		i := 0
		if len(rest[i].Content) > 200 {
			rest[i].Content = rest[i].Content[:200] + "\n…[trimmed for context budget]"
		} else if len(rest) > 1 {
			rest = rest[1:]
		} else {
			maxChars := available * 3
			if maxChars < 100 {
				maxChars = 100
			}
			if len(rest[0].Content) > maxChars {
				rest[0].Content = "…[trimmed]\n" + rest[0].Content[len(rest[0].Content)-maxChars:]
			}
			break
		}
	}

	out := make([]model.Message, 0, len(system)+len(rest))
	out = append(out, system...)
	out = append(out, rest...)
	return out
}

func cloneMessages(in []model.Message) []model.Message {
	out := make([]model.Message, len(in))
	for i, m := range in {
		out[i] = m
		if len(m.ToolCalls) > 0 {
			tc := make([]model.ToolCall, len(m.ToolCalls))
			copy(tc, m.ToolCalls)
			out[i].ToolCalls = tc
		}
	}
	return out
}

func estimateAll(msgs []model.Message) int {
	total := 0
	for _, m := range msgs {
		total += token.Estimate(m.Role) + token.Estimate(m.Content) + token.Estimate(m.Name) + token.Estimate(m.ToolCallID) + 8
		for _, tc := range m.ToolCalls {
			total += token.Estimate(tc.ID) + token.Estimate(tc.Function.Name) + token.Estimate(tc.Function.Arguments) + 8
		}
	}
	return total
}

func estimateTools(tools []model.ToolSpec) int {
	// Rough fixed cost for tool JSON schemas.
	n := 200
	for _, t := range tools {
		n += token.Estimate(t.Function.Name) + token.Estimate(t.Function.Description) + 80
	}
	return n
}
