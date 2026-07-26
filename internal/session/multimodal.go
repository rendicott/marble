package session

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/model"
)

const marbleAttScheme = "marble-att://"

// UIAttachment is a durable chip on a UI message (ADR-0019).
type UIAttachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	MIME string `json:"mime"`
	Kind string `json:"kind"` // image | document
	Size int64  `json:"size,omitempty"`
}

// Message with attachments (extend existing Message in session.go)
// We'll add Attachments field via patching session.Message

// materializeImages deep-clones msgs and expands marble-att:// to data: URLs.
func (r *Runner) materializeImages(sessionID string, msgs []model.Message) ([]model.Message, error) {
	out := cloneMessages(msgs)
	for i := range out {
		if len(out[i].Content.Parts) == 0 {
			continue
		}
		parts := make([]model.ContentPart, 0, len(out[i].Content.Parts))
		for _, p := range out[i].Content.Parts {
			if p.Type != "image_url" || p.ImageURL == nil {
				parts = append(parts, p)
				continue
			}
			url := p.ImageURL.URL
			if strings.HasPrefix(url, "data:") {
				parts = append(parts, p)
				continue
			}
			if !strings.HasPrefix(url, marbleAttScheme) {
				return nil, fmt.Errorf("unsupported image url scheme")
			}
			attID := strings.TrimPrefix(url, marbleAttScheme)
			data, mime, err := r.readAttachment(sessionID, attID)
			if err != nil {
				// placeholder text instead of failing whole turn
				parts = append(parts, model.ContentPart{
					Type: "text",
					Text: fmt.Sprintf("[missing attachment %s]", attID),
				})
				continue
			}
			b64 := base64.StdEncoding.EncodeToString(data)
			detail := p.ImageURL.Detail
			if detail == "" {
				detail = "auto"
			}
			parts = append(parts, model.ContentPart{
				Type: "image_url",
				ImageURL: &model.ImageURL{
					URL:    fmt.Sprintf("data:%s;base64,%s", mime, b64),
					Detail: detail,
				},
			})
		}
		out[i].Content = model.ContentFromParts(parts)
	}
	return out, nil
}

func (r *Runner) readAttachment(sessionID, attID string) ([]byte, string, error) {
	if r.Reg == nil || r.Reg.sqldb == nil {
		return nil, "", fmt.Errorf("no store")
	}
	d := r.Reg.sqldb
	// Prefer SQL meta for MIME
	mime := "image/png"
	if d.Writable() {
		if row, err := d.GetAttachment(attID); err == nil && row != nil {
			if row.SessionID != sessionID {
				return nil, "", fmt.Errorf("attachment not found")
			}
			mime = row.MIME
		}
	}
	data, err := d.ReadAttachmentBytes(sessionID, attID)
	if err != nil {
		return nil, "", err
	}
	return data, mime, nil
}

// buildUserContent builds model Content from text + attachment ids (sentinel form).
func (r *Runner) buildUserContent(sessionID, text string, attIDs []string) (model.Content, []UIAttachment, error) {
	var parts []model.ContentPart
	var uis []UIAttachment
	text = strings.TrimSpace(text)
	if text != "" {
		parts = append(parts, model.ContentPart{Type: "text", Text: text})
	}
	var total int64
	if len(attIDs) > db.AttMaxPerMessage {
		return model.Content{}, nil, fmt.Errorf("too many attachments")
	}
	seen := map[string]bool{}
	for _, id := range attIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		row, data, err := r.loadStagedOrFile(sessionID, id)
		if err != nil {
			return model.Content{}, nil, err
		}
		total += row.ByteSize
		if total > db.AttMaxMessageBytes {
			return model.Content{}, nil, fmt.Errorf("message attachments exceed size limit")
		}
		uis = append(uis, UIAttachment{
			ID: row.ID, Name: row.Name, MIME: row.MIME, Kind: row.Kind, Size: row.ByteSize,
		})
		if row.Kind == "image" {
			parts = append(parts, model.ContentPart{
				Type: "image_url",
				ImageURL: &model.ImageURL{
					URL:    marbleAttScheme + row.ID,
					Detail: "auto",
				},
			})
		} else {
			// document as text inject (capped)
			inject := string(data)
			if len(inject) > db.AttDocInjectMax {
				inject = inject[:db.AttDocInjectMax] + "\n…[truncated for model inject]"
			}
			parts = append(parts, model.ContentPart{
				Type: "text",
				Text: fmt.Sprintf("[attachment %s (%s)]\n%s", row.Name, row.MIME, inject),
			})
		}
		_ = data
	}
	if len(parts) == 0 {
		return model.Content{}, nil, fmt.Errorf("content or attachment_ids required")
	}
	if len(parts) == 1 && parts[0].Type == "text" && len(uis) == 0 {
		return model.ContentFromText(parts[0].Text), uis, nil
	}
	return model.ContentFromParts(parts), uis, nil
}

func (r *Runner) loadStagedOrFile(sessionID, attID string) (*db.AttachmentRow, []byte, error) {
	if r.Reg == nil || r.Reg.sqldb == nil {
		return nil, nil, fmt.Errorf("no store")
	}
	d := r.Reg.sqldb
	data, err := d.ReadAttachmentBytes(sessionID, attID)
	if err != nil {
		return nil, nil, fmt.Errorf("attachment not found: %s", attID)
	}
	row := &db.AttachmentRow{
		ID: attID, SessionID: sessionID, ByteSize: int64(len(data)),
		Name: attID, MIME: "application/octet-stream", Kind: "document", Source: "staged",
		Path: db.AttachmentRelPath(sessionID, attID),
	}
	if d.Writable() {
		if got, err := d.GetAttachment(attID); err == nil && got != nil {
			if got.SessionID != sessionID {
				return nil, nil, fmt.Errorf("attachment not found: %s", attID)
			}
			if got.MessageID != "" {
				return nil, nil, fmt.Errorf("attachment already committed: %s", attID)
			}
			row = got
		} else if err != nil {
			// limp-style: file exists but no row — sniff from bytes
			mime, kind, err2 := db.SniffAttachment(attID, data)
			if err2 != nil {
				return nil, nil, err2
			}
			row.MIME, row.Kind = mime, kind
		}
	} else {
		mime, kind, err2 := db.SniffAttachment(filepath.Base(attID), data)
		if err2 != nil {
			return nil, nil, err2
		}
		row.MIME, row.Kind = mime, kind
	}
	return row, data, nil
}

// StageAttachment writes bytes and optional SQL row.
func (r *Runner) StageAttachment(sessionID, name string, data []byte) (*db.AttachmentRow, error) {
	if r.Reg == nil || r.Reg.sqldb == nil {
		return nil, fmt.Errorf("no store")
	}
	d := r.Reg.sqldb
	mime, kind, err := db.SniffAttachment(name, data)
	if err != nil {
		return nil, err
	}
	if len(data) > db.AttMaxFileBytes {
		return nil, fmt.Errorf("file too large (max %d bytes)", db.AttMaxFileBytes)
	}
	id := db.NewAttachmentID()
	rel, sum, err := d.WriteAttachmentFile(sessionID, id, data)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = id
	}
	row := &db.AttachmentRow{
		ID: id, SessionID: sessionID, CreatedAt: db.UTCNow(),
		Name: name, MIME: mime, Kind: kind, ByteSize: int64(len(data)),
		SHA256: sum, Source: "staged", Path: rel,
	}
	if d.Writable() {
		if err := d.InsertAttachment(*row); err != nil {
			return nil, err
		}
	}
	return row, nil
}

// metaJSON helper for events
func attMetaJSON(ids []string) string {
	b, _ := json.Marshal(map[string]interface{}{"attachment_ids": ids})
	return string(b)
}
