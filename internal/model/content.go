package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Content is OpenAI-compatible message content: a string or multimodal parts (ADR-0019).
// Marshal rule: len(Parts)>0 → JSON array; else Text as string (including "").
type Content struct {
	Text  string
	Parts []ContentPart
}

// ContentPart is one multimodal part.
type ContentPart struct {
	Type     string    `json:"type"` // text | image_url
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL holds image reference (data: or marble-att://).
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"` // auto
}

// ContentFromText builds text-only content.
func ContentFromText(s string) Content {
	return Content{Text: s}
}

// ContentFromParts builds parts content (Parts wins on marshal).
func ContentFromParts(parts []ContentPart) Content {
	return Content{Parts: parts}
}

// PlainText returns concatenated text parts or Text field.
func (c Content) PlainText() string {
	if len(c.Parts) > 0 {
		var b strings.Builder
		for _, p := range c.Parts {
			if p.Type == "text" && p.Text != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return c.Text
}

// IsEmpty reports no text and no parts.
func (c Content) IsEmpty() bool {
	if len(c.Parts) > 0 {
		for _, p := range c.Parts {
			if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
				return false
			}
			if p.Type == "image_url" && p.ImageURL != nil && p.ImageURL.URL != "" {
				return false
			}
		}
		return true
	}
	return strings.TrimSpace(c.Text) == ""
}

// HasImages reports image_url parts present.
func (c Content) HasImages() bool {
	for _, p := range c.Parts {
		if p.Type == "image_url" && p.ImageURL != nil && p.ImageURL.URL != "" {
			return true
		}
	}
	return false
}

// MarshalJSON implements OpenAI wire format.
func (c Content) MarshalJSON() ([]byte, error) {
	if len(c.Parts) > 0 {
		return json.Marshal(c.Parts)
	}
	return json.Marshal(c.Text)
}

// UnmarshalJSON accepts string or array of parts.
func (c *Content) UnmarshalJSON(data []byte) error {
	data = bytesTrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*c = Content{}
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*c = Content{Text: s}
		return nil
	}
	if data[0] == '[' {
		var parts []ContentPart
		if err := json.Unmarshal(data, &parts); err != nil {
			return err
		}
		*c = Content{Parts: parts}
		return nil
	}
	return fmt.Errorf("content: expected string or array")
}

func bytesTrimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\r' || b[j-1] == '\t') {
		j--
	}
	return b[i:j]
}

// CloneContent deep-copies content.
func CloneContent(c Content) Content {
	out := Content{Text: c.Text}
	if len(c.Parts) > 0 {
		out.Parts = make([]ContentPart, len(c.Parts))
		for i, p := range c.Parts {
			out.Parts[i] = p
			if p.ImageURL != nil {
				u := *p.ImageURL
				out.Parts[i].ImageURL = &u
			}
		}
	}
	return out
}
