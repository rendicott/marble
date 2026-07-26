package memory

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SessionMeta is list/index information from front matter.
type SessionMeta struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Kind         string     `json:"kind,omitempty"` // user | system
	ParentID     string     `json:"parent_session_id,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ClosedAt     *time.Time `json:"closed_at,omitempty"`
	Status       string     `json:"status"`
	MessageCount int        `json:"message_count"`
	Workspace    string     `json:"workspace,omitempty"`
	Model        string     `json:"model,omitempty"`
	ModelID      string     `json:"model_id,omitempty"` // catalog slug (ADR-0018)
}

// TranscriptMessage is one persisted UI-facing turn.
type TranscriptMessage struct {
	ID         string    `json:"id"`
	Role       string    `json:"role"`
	Content    string    `json:"content"`
	ToolName   string    `json:"tool_name,omitempty"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	// Actor identity (ADR-0017); UI/MD only — not model-facing.
	UserEmail string `json:"user_email,omitempty"`
	UserName  string `json:"user_name,omitempty"`
	UserSub   string `json:"user_sub,omitempty"`
}

// SessionDoc is the full on-disk session.
type SessionDoc struct {
	SessionMeta
	Messages []TranscriptMessage
}

// EncodeSession writes Markdown with YAML-like front matter.
func EncodeSession(doc *SessionDoc) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\n", doc.ID)
	fmt.Fprintf(&b, "title: %q\n", doc.Title)
	if doc.Kind != "" {
		fmt.Fprintf(&b, "kind: %s\n", doc.Kind)
	}
	if doc.ParentID != "" {
		fmt.Fprintf(&b, "parent_session_id: %s\n", doc.ParentID)
	}
	fmt.Fprintf(&b, "created_at: %s\n", doc.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "updated_at: %s\n", doc.UpdatedAt.Format(time.RFC3339))
	if doc.ClosedAt != nil {
		fmt.Fprintf(&b, "closed_at: %s\n", doc.ClosedAt.Format(time.RFC3339))
	} else {
		b.WriteString("closed_at: null\n")
	}
	status := doc.Status
	if status == "" {
		status = "active"
	}
	fmt.Fprintf(&b, "status: %s\n", status)
	fmt.Fprintf(&b, "message_count: %d\n", len(doc.Messages))
	fmt.Fprintf(&b, "workspace: %q\n", doc.Workspace)
	fmt.Fprintf(&b, "model: %q\n", doc.Model)
	if doc.ModelID != "" {
		fmt.Fprintf(&b, "model_id: %q\n", doc.ModelID)
	}
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# Session %s — %s\n\n", doc.ID, doc.Title)

	for _, m := range doc.Messages {
		ts := m.CreatedAt.Format(time.RFC3339)
		switch m.Role {
		case "tool":
			name := m.ToolName
			if name == "" {
				name = "tool"
			}
			fmt.Fprintf(&b, "## %s · tool · %s\n", ts, name)
			if m.ToolCallID != "" {
				fmt.Fprintf(&b, "<!-- tool_call_id: %s id: %s -->\n", m.ToolCallID, m.ID)
			} else if m.ID != "" {
				fmt.Fprintf(&b, "<!-- id: %s -->\n", m.ID)
			}
		default:
			fmt.Fprintf(&b, "## %s · %s\n", ts, m.Role)
			if m.ID != "" || m.UserEmail != "" {
				// HTML comment carries id + optional actor (ADR-0017)
				fmt.Fprintf(&b, "<!-- id: %s", m.ID)
				if m.UserEmail != "" {
					fmt.Fprintf(&b, " user_email: %s", m.UserEmail)
				}
				if m.UserName != "" {
					fmt.Fprintf(&b, " user_name: %q", m.UserName)
				}
				if m.UserSub != "" {
					fmt.Fprintf(&b, " user_sub: %s", m.UserSub)
				}
				b.WriteString(" -->\n")
			}
		}
		writeMessageContent(&b, m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

// writeMessageContent writes body text, neutralizing lines that would be parsed as
// message headings (timestamp · role) so tool/README markdown cannot split the session.
func writeMessageContent(b *strings.Builder, content string) {
	if content == "" {
		return
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if isMessageHeading(line) {
			// Zero-width space keeps visual content but breaks "## " delimiter match.
			b.WriteString("\u200b")
		}
		b.WriteString(line)
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	if !strings.HasSuffix(content, "\n") {
		b.WriteByte('\n')
	}
}

// DecodeSession parses a session markdown file.
func DecodeSession(raw string) (*SessionDoc, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	meta, body, err := splitFrontMatter(raw)
	if err != nil {
		return nil, err
	}
	doc := &SessionDoc{SessionMeta: meta}
	doc.Messages = parseMessages(body)
	if doc.MessageCount == 0 {
		doc.MessageCount = len(doc.Messages)
	}
	return doc, nil
}

func splitFrontMatter(raw string) (SessionMeta, string, error) {
	if !strings.HasPrefix(raw, "---\n") {
		return SessionMeta{}, "", fmt.Errorf("missing front matter")
	}
	rest := raw[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return SessionMeta{}, "", fmt.Errorf("unterminated front matter")
	}
	fm := rest[:end]
	body := rest[end+5:]
	meta, err := parseFrontMatter(fm)
	return meta, body, err
}

func parseFrontMatter(fm string) (SessionMeta, error) {
	var m SessionMeta
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, ":") {
			continue
		}
		key, val, _ := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = unquote(val)
		switch key {
		case "id":
			m.ID = val
		case "title":
			m.Title = val
		case "kind":
			m.Kind = val
		case "parent_session_id":
			m.ParentID = val
		case "created_at":
			m.CreatedAt = parseTime(val)
		case "updated_at":
			m.UpdatedAt = parseTime(val)
		case "closed_at":
			if val != "" && val != "null" {
				t := parseTime(val)
				m.ClosedAt = &t
			}
		case "status":
			m.Status = val
		case "message_count":
			m.MessageCount, _ = strconv.Atoi(val)
		case "workspace":
			m.Workspace = val
		case "model":
			m.Model = val
		case "model_id":
			m.ModelID = val
		}
	}
	if m.ID == "" {
		return m, fmt.Errorf("front matter missing id")
	}
	if m.Status == "" {
		m.Status = "active"
	}
	return m, nil
}

func parseMessages(body string) []TranscriptMessage {
	// Only split on true message headings: "## <RFC3339> · <role>[ · toolname]"
	// Ordinary markdown headings inside tool results (## Pre-reqs) must stay in content.
	lines := strings.Split(body, "\n")
	var out []TranscriptMessage
	var cur *TranscriptMessage
	var content []string

	flush := func() {
		if cur == nil {
			return
		}
		cur.Content = strings.TrimRight(strings.Join(content, "\n"), "\n")
		// Strip defensive ZWSP prefixes from encoded content lines.
		cur.Content = stripContentHeadingEscapes(cur.Content)
		out = append(out, *cur)
		cur = nil
		content = nil
	}

	for _, line := range lines {
		if isMessageHeading(line) {
			flush()
			cur = parseHeading(line[3:])
			continue
		}
		if cur == nil {
			continue
		}
		// HTML comment metadata
		if strings.HasPrefix(strings.TrimSpace(line), "<!--") {
			trim := strings.TrimSpace(line)
			trim = strings.TrimPrefix(trim, "<!--")
			trim = strings.TrimSuffix(trim, "-->")
			trim = strings.TrimSpace(trim)
			applyHTMLMeta(cur, trim)
			continue
		}
		content = append(content, line)
	}
	flush()
	return out
}

func applyHTMLMeta(cur *TranscriptMessage, trim string) {
	// Parse key: value pairs; values may be "quoted".
	for len(trim) > 0 {
		trim = strings.TrimSpace(trim)
		if trim == "" {
			break
		}
		colon := strings.IndexByte(trim, ':')
		if colon <= 0 {
			break
		}
		key := strings.TrimSpace(trim[:colon])
		rest := strings.TrimSpace(trim[colon+1:])
		var val string
		if strings.HasPrefix(rest, "\"") {
			// quoted
			rest = rest[1:]
			end := strings.IndexByte(rest, '"')
			if end < 0 {
				val = rest
				rest = ""
			} else {
				val = rest[:end]
				rest = strings.TrimSpace(rest[end+1:])
			}
			trim = rest
		} else {
			// unquoted: until next " key:" pattern or end
			// split on space before next key:
			sp := -1
			for i := 0; i < len(rest); i++ {
				if rest[i] == ' ' {
					// look ahead for word:
					j := i + 1
					for j < len(rest) && rest[j] == ' ' {
						j++
					}
					k := j
					for k < len(rest) && rest[k] != ':' && rest[k] != ' ' {
						k++
					}
					if k < len(rest) && rest[k] == ':' {
						sp = i
						break
					}
				}
			}
			if sp < 0 {
				val = rest
				trim = ""
			} else {
				val = strings.TrimSpace(rest[:sp])
				trim = strings.TrimSpace(rest[sp:])
			}
		}
		switch key {
		case "id":
			cur.ID = val
		case "tool_call_id":
			cur.ToolCallID = val
		case "user_email":
			cur.UserEmail = val
		case "user_name":
			cur.UserName = val
		case "user_sub":
			cur.UserSub = val
		}
	}
}

// isMessageHeading reports whether line is a Marble message delimiter, not body markdown.
// Format: "## <timestamp> · <role>" or "## <timestamp> · tool · <name>"
func isMessageHeading(line string) bool {
	if !strings.HasPrefix(line, "## ") {
		return false
	}
	h := line[3:]
	parts := strings.Split(h, " · ")
	if len(parts) < 2 {
		return false
	}
	if parseTime(strings.TrimSpace(parts[0])).IsZero() {
		return false
	}
	role := strings.TrimSpace(parts[1])
	switch role {
	case "user", "assistant", "tool", "system", "error", "harness", "attachment":
		return true
	default:
		return false
	}
}

func stripContentHeadingEscapes(s string) string {
	if !strings.Contains(s, "\u200b") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "\u200b## ") {
			lines[i] = line[len("\u200b"):]
		}
	}
	return strings.Join(lines, "\n")
}

// heading: "2026-07-16T20:01:02-04:00 · user" or "… · tool · list_files"
// Caller must only pass lines that passed isMessageHeading.
func parseHeading(h string) *TranscriptMessage {
	m := &TranscriptMessage{CreatedAt: time.Now()}
	parts := strings.Split(h, " · ")
	if len(parts) >= 1 {
		if t := parseTime(strings.TrimSpace(parts[0])); !t.IsZero() {
			m.CreatedAt = t
		}
	}
	if len(parts) >= 2 {
		m.Role = strings.TrimSpace(parts[1])
	}
	if m.Role == "tool" && len(parts) >= 3 {
		m.ToolName = strings.TrimSpace(parts[2])
	}
	if m.Role == "" {
		// Should not happen for isMessageHeading-validated lines; keep a safe default.
		m.Role = "assistant"
	}
	return m
}

func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Time{}
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		u, err := strconv.Unquote(s)
		if err == nil {
			return u
		}
		return s[1 : len(s)-1]
	}
	return s
}

// EncodeDaily builds a deterministic daily digest.
func EncodeDaily(day time.Time, docs []*SessionDoc) string {
	day = day.In(time.Local)
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "date: %s\n", day.Format("2006-01-02"))
	fmt.Fprintf(&b, "generated_at: %s\n", time.Now().Format(time.RFC3339))
	b.WriteString("sessions:\n")
	for _, d := range docs {
		fmt.Fprintf(&b, "  - id: %s\n", d.ID)
		fmt.Fprintf(&b, "    title: %q\n", d.Title)
		fmt.Fprintf(&b, "    messages: %d\n", len(d.Messages))
	}
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# Daily log — %s\n\n", day.Format("2006-01-02"))

	for _, d := range docs {
		fmt.Fprintf(&b, "## %s — %s\n", d.ID, d.Title)
		firstUser, lastAsst, tools := extractSummary(d.Messages)
		if firstUser != "" {
			fmt.Fprintf(&b, "- Goal / opener: %s\n", oneLine(firstUser, 200))
		}
		if lastAsst != "" {
			fmt.Fprintf(&b, "- Outcome: %s\n", oneLine(lastAsst, 240))
		}
		if len(tools) > 0 {
			fmt.Fprintf(&b, "- Tools: %s\n", strings.Join(tools, ", "))
		}
		fmt.Fprintf(&b, "- Messages: %d · status: %s\n\n", len(d.Messages), d.Status)
	}
	return b.String()
}

func extractSummary(msgs []TranscriptMessage) (firstUser, lastAsst string, tools []string) {
	seen := map[string]bool{}
	for _, m := range msgs {
		switch m.Role {
		case "user":
			if firstUser == "" {
				firstUser = m.Content
			}
		case "assistant":
			lastAsst = m.Content
		case "tool":
			n := m.ToolName
			if n == "" {
				n = "tool"
			}
			if !seen[n] {
				seen[n] = true
				tools = append(tools, n)
			}
		}
	}
	return
}

func oneLine(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}
