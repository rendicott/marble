package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/session"
	"github.com/rendicott/marble/internal/tools"
)

func (s *Server) handleSessionAttachments(w http.ResponseWriter, r *http.Request, sessionID string, rest []string) {
	if s.Registry == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	sess, err := s.Registry.EnsureLoaded(sessionID)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodPost:
			s.stageAttachment(w, r, sess)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	if len(rest) == 1 && rest[0] == "from_url" {
		s.handleAttachmentsFromURL(w, r, sess)
		return
	}
	attID := rest[0]
	if len(rest) == 1 {
		switch r.Method {
		case http.MethodGet:
			s.getAttachment(w, r, sess, attID)
		case http.MethodDelete:
			s.deleteAttachment(w, r, sess, attID)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleAttachmentsFromURL(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if sess.Status == "closed" {
		http.Error(w, "session closed", http.StatusBadRequest)
		return
	}
	if s.Tools == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	var in tools.AttachURLInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	results, err := s.Tools.AttachURLs(ctx, sess.ID, in, func(a tools.Attachment) {
		kind := "document"
		if strings.HasPrefix(a.Mime, "image/") {
			kind = "image"
		}
		sess.AddChatAttachment(session.UIAttachment{
			ID: a.Path, Name: a.Name, MIME: a.Mime, Kind: kind, Size: a.Size,
			SourceURL: a.SourceURL, Alt: a.Alt, Credit: a.Credit,
		})
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(results) == 1 {
		res := results[0]
		if !res.OK {
			writeJSON(w, attachURLHTTPStatus(res), res)
			return
		}
		writeJSON(w, http.StatusCreated, res)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"results": results})
}

func attachURLHTTPStatus(res tools.AttachURLResult) int {
	switch res.Error {
	case "too_large":
		return http.StatusRequestEntityTooLarge
	case "unsupported_type":
		return http.StatusUnsupportedMediaType
	case "http_status", "fetch_failed":
		return http.StatusBadGateway
	default:
		return http.StatusBadRequest
	}
}

func (s *Server) stageAttachment(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	if sess.Status == "closed" {
		http.Error(w, "session closed", http.StatusBadRequest)
		return
	}
	if sess.IsBusy() {
		http.Error(w, "session busy", http.StatusConflict)
		return
	}
	if err := r.ParseMultipartForm(db.AttMaxFileBytes + (1 << 20)); err != nil {
		http.Error(w, "bad multipart", http.StatusBadRequest)
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, db.AttMaxFileBytes+1))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	if len(data) > db.AttMaxFileBytes {
		http.Error(w, fmt.Sprintf("file too large (max %d bytes)", db.AttMaxFileBytes), http.StatusBadRequest)
		return
	}
	name := hdr.Filename
	if n := r.FormValue("name"); strings.TrimSpace(n) != "" {
		name = n
	}
	name = filepath.Base(name)
	row, err := s.Registry.Runner().StageAttachment(sess.ID, name, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":   row.ID,
		"name": row.Name,
		"mime": row.MIME,
		"kind": row.Kind,
		"size": row.ByteSize,
	})
}

func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request, sess *session.Session, attID string) {
	if s.Registry.DB() == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	d := s.Registry.DB()
	data, err := d.ReadAttachmentBytes(sess.ID, attID)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	mime := "application/octet-stream"
	kind := "document"
	name := attID
	if d.Writable() {
		if row, err := d.GetAttachment(attID); err == nil && row != nil && row.SessionID == sess.ID {
			mime = row.MIME
			kind = row.Kind
			name = row.Name
		}
	} else if m, k, err := db.SniffAttachment(name, data); err == nil {
		mime, kind = m, k
	}
	inline := r.URL.Query().Get("inline") == "1"
	// Safe GET policy (ADR-0019 + ADR-0027 Q6): images/audio may be inline; never serve docs as text/html
	safeAudio := kind == "audio" || (strings.HasPrefix(mime, "audio/") && isInlineAudioMIME(mime))
	if kind == "image" && inline && strings.HasPrefix(mime, "image/") && mime != "image/svg+xml" {
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", name))
	} else if safeAudio && isInlineAudioMIME(mime) {
		// Audio: default inline so ExoPlayer / <audio> can stream (ADR-0027); ?inline=0 forces download
		forceAttach := r.URL.Query().Get("inline") == "0"
		w.Header().Set("Content-Type", mime)
		if forceAttach {
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
		} else {
			w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", name))
		}
	} else {
		// documents always attachment + text/plain when text-ish.
		// Never serve image/svg+xml (XSS); treat like stored HTML.
		ct := "application/octet-stream"
		if strings.HasPrefix(mime, "text/") || mime == "application/json" || mime == "text/html" || mime == "text/markdown" || mime == "image/svg+xml" {
			ct = "text/plain; charset=utf-8"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func isInlineAudioMIME(mime string) bool {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "audio/mpeg", "audio/mp3", "audio/mp4", "audio/wav", "audio/ogg", "audio/webm":
		return true
	default:
		return false
	}
}

func (s *Server) deleteAttachment(w http.ResponseWriter, r *http.Request, sess *session.Session, attID string) {
	if sess.IsBusy() {
		http.Error(w, "session busy", http.StatusConflict)
		return
	}
	if s.Registry.DB() == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.Registry.DB().DeleteAttachment(sess.ID, attID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": attID})
}
