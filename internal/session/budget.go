package session

import (
	"strings"

	"github.com/rendicott/marble/internal/model"
	"github.com/rendicott/marble/internal/token"
)

// normalizeOutboundChatMessages fixes message order for strict OpenAI-compatible
// backends (vLLM etc.): "System message must be at the beginning."
// - Merges all leading consecutive system messages into one
// - Demotes any system that appears after user/assistant/tool to a user note
func normalizeOutboundChatMessages(msgs []model.Message) []model.Message {
	if len(msgs) == 0 {
		return msgs
	}
	var sysParts []string
	i := 0
	for i < len(msgs) && msgs[i].Role == "system" {
		t := strings.TrimSpace(msgs[i].Content.PlainText())
		if t != "" {
			sysParts = append(sysParts, t)
		}
		i++
	}
	rest := make([]model.Message, 0, len(msgs)-i)
	for ; i < len(msgs); i++ {
		m := msgs[i]
		if m.Role == "system" {
			// Orphan system mid-conversation — demote (never valid for strict APIs)
			t := strings.TrimSpace(m.Content.PlainText())
			if t == "" {
				continue
			}
			m.Role = "user"
			m.Content = model.ContentFromText("[system note]\n" + t)
			m.ToolCalls = nil
			m.ToolCallID = ""
			m.Name = ""
		}
		rest = append(rest, m)
	}
	out := make([]model.Message, 0, 1+len(rest))
	if len(sysParts) > 0 {
		out = append(out, model.Message{
			Role:    "system",
			Content: model.ContentFromText(strings.Join(sysParts, "\n\n")),
		})
	}
	out = append(out, rest...)
	return out
}

// trimHistory returns a deep-enough copy of messages that fits within budget tokens.
// Always keeps leading system message(s) and prefers the newest messages.
// Works on sentinel form (marble-att://), never base64 (ADR-0019).
func trimHistory(messages []model.Message, budget int, toolSchemaEstimate int) []model.Message {
	if budget < 512 {
		budget = 512
	}
	available := budget - toolSchemaEstimate
	if available < 256 {
		available = 256
	}

	msgs := cloneMessages(messages)

	if estimateAll(msgs) <= available {
		return msgs
	}

	// Keep all leading consecutive system messages (soul + compact + base).
	var system []model.Message
	rest := msgs
	i := 0
	for i < len(msgs) && msgs[i].Role == "system" {
		system = append(system, msgs[i])
		i++
	}
	rest = msgs[i:]

	sysCost := estimateAll(system)
	for len(rest) > 1 && sysCost+estimateAll(rest) > available {
		rest = rest[1:]
	}

	for sysCost+estimateAll(rest) > available && len(rest) > 0 {
		i := 0
		pt := rest[i].Content.PlainText()
		if len(pt) > 200 {
			rest[i].Content = model.ContentFromText(pt[:200] + "\n…[trimmed for context budget]")
			// drop image parts when trimming text of a multimodal message under pressure
			if rest[i].Content.HasImages() || len(rest[i].Content.Parts) > 0 {
				// keep text only after trim
			}
		} else if len(rest) > 1 {
			rest = rest[1:]
		} else {
			maxChars := available * 3
			if maxChars < 100 {
				maxChars = 100
			}
			pt = rest[0].Content.PlainText()
			if len(pt) > maxChars {
				rest[0].Content = model.ContentFromText("…[trimmed]\n" + pt[len(pt)-maxChars:])
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
		out[i].Content = model.CloneContent(m.Content)
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
		total += token.Estimate(m.Role) + token.Estimate(m.Name) + token.Estimate(m.ToolCallID) + 8
		total += estimateContent(m.Content)
		for _, tc := range m.ToolCalls {
			total += token.Estimate(tc.ID) + token.Estimate(tc.Function.Name) + token.Estimate(tc.Function.Arguments) + 8
		}
	}
	return total
}

func estimateContent(c model.Content) int {
	if len(c.Parts) == 0 {
		return token.Estimate(c.Text)
	}
	n := 0
	for _, p := range c.Parts {
		switch p.Type {
		case "text":
			n += token.Estimate(p.Text)
		case "image_url":
			// sentinel marble-att:// — no base64 length; use default image tokens
			n += token.DefaultImageTokens
		default:
			n += 8
		}
	}
	return n
}

func estimateTools(tools []model.ToolSpec) int {
	n := 200
	for _, t := range tools {
		n += token.Estimate(t.Function.Name) + token.Estimate(t.Function.Description) + 80
	}
	return n
}

// truncateContentText shortens plain text content.
func truncateContentText(c model.Content, max int) model.Content {
	pt := c.PlainText()
	if len(pt) <= max {
		return c
	}
	return model.ContentFromText(pt[:max] + "…")
}

// dropImageParts removes image_url parts; returns whether any were dropped.
func dropImageParts(c model.Content) (model.Content, bool) {
	if len(c.Parts) == 0 {
		return c, false
	}
	var kept []model.ContentPart
	dropped := false
	for _, p := range c.Parts {
		if p.Type == "image_url" {
			dropped = true
			continue
		}
		kept = append(kept, p)
	}
	if !dropped {
		return c, false
	}
	if len(kept) == 0 {
		return model.ContentFromText("[image attachment omitted: model has no image support]"), true
	}
	// if only text parts, may collapse to text
	allText := true
	var b strings.Builder
	for _, p := range kept {
		if p.Type != "text" {
			allText = false
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(p.Text)
	}
	if allText {
		return model.ContentFromText(b.String()), true
	}
	return model.ContentFromParts(kept), true
}
