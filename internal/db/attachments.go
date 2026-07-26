package db

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Attachment limits (ADR-0019 Q2).
const (
	AttMaxFileBytes    = 8 << 20  // 8 MiB
	AttMaxMessageBytes = 20 << 20 // 20 MiB
	AttMaxPerMessage   = 10
	AttDocInjectMax    = 64 << 10 // 64 KiB model inject
	AttStagedTTL       = 24 * time.Hour
)

var attIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// AttachmentRow is a session_attachments row.
type AttachmentRow struct {
	ID        string
	SessionID string
	CreatedAt string
	Name      string
	MIME      string
	Kind      string // image | document
	ByteSize  int64
	SHA256    string
	Source    string // staged | user_upload | agent_attach
	Path      string // rel under $MEMORY
	MessageID string // empty if staged
	MetaJSON  string
}

func (d *DB) migrateV3toV4() error {
	if d.SQL == nil {
		return fmt.Errorf("no database")
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS session_attachments (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			name TEXT NOT NULL,
			mime TEXT NOT NULL,
			kind TEXT NOT NULL,
			byte_size INTEGER NOT NULL,
			sha256 TEXT,
			source TEXT NOT NULL,
			path TEXT NOT NULL,
			message_id TEXT,
			meta_json TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_att_session ON session_attachments(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_att_session_msg ON session_attachments(session_id, message_id)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("migrate v4: %w\nstmt: %s", err, s)
		}
	}
	now := UTCNow()
	if _, err := tx.Exec(`UPDATE schema_meta SET schema_version = 4, updated_at = ? WHERE id = 1`, now); err != nil {
		return err
	}
	return tx.Commit()
}

// NewAttachmentID returns a 32-char hex id.
func NewAttachmentID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ValidAttachmentID reports whether id is a valid attachment id.
func ValidAttachmentID(id string) bool {
	return attIDRe.MatchString(strings.TrimSpace(id))
}

// AttachmentAbsPath returns absolute path under $MEMORY for an attachment.
func (d *DB) AttachmentAbsPath(sessionID, attID string) (string, error) {
	if d == nil {
		return "", fmt.Errorf("no db")
	}
	sid := strings.TrimSpace(sessionID)
	aid := strings.TrimSpace(attID)
	if sid == "" || strings.ContainsAny(sid, `/\.`) || strings.Contains(sid, "..") {
		return "", fmt.Errorf("invalid session id")
	}
	if !ValidAttachmentID(aid) {
		return "", fmt.Errorf("invalid attachment id")
	}
	root := filepath.Clean(d.Root)
	abs := filepath.Clean(filepath.Join(root, "attachments", sid, aid))
	prefix := root + string(os.PathSeparator)
	if abs != root && !strings.HasPrefix(abs, prefix) {
		return "", fmt.Errorf("path escape")
	}
	return abs, nil
}

// AttachmentRelPath is relative path under $MEMORY.
func AttachmentRelPath(sessionID, attID string) string {
	return filepath.Join("attachments", sessionID, attID)
}

// SniffAttachment classifies bytes + filename into mime/kind or error.
func SniffAttachment(name string, data []byte) (mime, kind string, err error) {
	if len(data) == 0 {
		return "", "", fmt.Errorf("empty file")
	}
	if len(data) > AttMaxFileBytes {
		return "", "", fmt.Errorf("file too large (max %d bytes)", AttMaxFileBytes)
	}
	name = strings.TrimSpace(name)
	ext := strings.ToLower(filepath.Ext(name))
	sniff := http.DetectContentType(data[:min(512, len(data))])
	// strip charset
	if i := strings.Index(sniff, ";"); i >= 0 {
		sniff = strings.TrimSpace(sniff[:i])
	}

	switch {
	case sniff == "image/png" || ext == ".png":
		if sniff != "image/png" && sniff != "application/octet-stream" {
			// allow octet-stream with .png
			if !strings.HasPrefix(sniff, "image/") && sniff != "application/octet-stream" {
				return "", "", fmt.Errorf("file type not allowed")
			}
		}
		return "image/png", "image", nil
	case sniff == "image/jpeg" || ext == ".jpg" || ext == ".jpeg":
		return "image/jpeg", "image", nil
	case sniff == "image/webp" || ext == ".webp":
		return "image/webp", "image", nil
	case sniff == "image/gif" || ext == ".gif":
		return "image/gif", "image", nil
	case strings.HasPrefix(sniff, "image/svg") || ext == ".svg":
		return "", "", fmt.Errorf("file type not allowed")
	case strings.HasPrefix(sniff, "audio/") || strings.HasPrefix(sniff, "video/"):
		return "", "", fmt.Errorf("file type not allowed")
	case sniff == "application/pdf" || ext == ".pdf":
		return "", "", fmt.Errorf("file type not allowed")
	}

	// documents: need mostly text
	if !utf8.Valid(data) {
		return "", "", fmt.Errorf("file type not allowed")
	}
	// reject high control-char ratio
	ctrl := 0
	for _, r := range string(data[:min(len(data), 4096)]) {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			ctrl++
		}
	}
	if ctrl > 32 {
		return "", "", fmt.Errorf("file type not allowed")
	}

	switch ext {
	case ".md", ".markdown":
		return "text/markdown", "document", nil
	case ".csv":
		return "text/csv", "document", nil
	case ".json":
		return "application/json", "document", nil
	case ".html", ".htm":
		return "text/html", "document", nil
	case ".txt", ".log", "":
		if strings.HasPrefix(sniff, "text/") || sniff == "application/octet-stream" || sniff == "application/json" {
			return "text/plain", "document", nil
		}
	}
	if strings.HasPrefix(sniff, "text/") {
		return "text/plain", "document", nil
	}
	if sniff == "application/json" {
		return "application/json", "document", nil
	}
	return "", "", fmt.Errorf("file type not allowed")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// WriteAttachmentFile writes bytes to disk (immutable). Works in limp mode.
func (d *DB) WriteAttachmentFile(sessionID, attID string, data []byte) (rel string, sum string, err error) {
	abs, err := d.AttachmentAbsPath(sessionID, attID)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", "", err
	}
	if _, err := os.Stat(abs); err == nil {
		return "", "", fmt.Errorf("attachment already exists")
	}
	h := sha256.Sum256(data)
	sum = hex.EncodeToString(h[:])
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return "", "", err
	}
	return AttachmentRelPath(sessionID, attID), sum, nil
}

// InsertAttachment inserts a catalog row (normal mode only).
func (d *DB) InsertAttachment(r AttachmentRow) error {
	if !d.Writable() {
		return fmt.Errorf("database not writable")
	}
	_, err := d.SQL.Exec(`
INSERT INTO session_attachments (
  id, session_id, created_at, name, mime, kind, byte_size, sha256, source, path, message_id, meta_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.SessionID, r.CreatedAt, r.Name, r.MIME, r.Kind, r.ByteSize, nullStr(r.SHA256),
		r.Source, r.Path, nullStr(r.MessageID), nullStr(r.MetaJSON),
	)
	return err
}

// GetAttachment loads a row by id.
func (d *DB) GetAttachment(id string) (*AttachmentRow, error) {
	if d.SQL == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	row := d.SQL.QueryRow(`
SELECT id, session_id, created_at, name, mime, kind, byte_size, sha256, source, path, message_id, meta_json
FROM session_attachments WHERE id = ?`, strings.TrimSpace(id))
	return scanAttachment(row)
}

// CommitAttachments sets message_id and source for staged attachments.
func (d *DB) CommitAttachments(sessionID, messageID string, ids []string, source string) error {
	if !d.Writable() {
		return nil
	}
	if source == "" {
		source = "user_upload"
	}
	for _, id := range ids {
		res, err := d.SQL.Exec(`
UPDATE session_attachments SET message_id=?, source=?
WHERE id=? AND session_id=? AND message_id IS NULL`, messageID, source, id, sessionID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("attachment already committed: %s", id)
		}
	}
	return nil
}

// DeleteSessionAttachments removes rows and files for a session.
func (d *DB) DeleteSessionAttachments(sessionID string) (int, error) {
	n := 0
	// Always try FS dir
	dir := filepath.Join(d.Root, "attachments", sessionID)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			_ = os.Remove(filepath.Join(dir, e.Name()))
			n++
		}
		_ = os.Remove(dir)
	}
	if d.Writable() {
		_, err := d.SQL.Exec(`DELETE FROM session_attachments WHERE session_id=?`, sessionID)
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// GCStagedAttachments deletes staged attachments older than TTL.
func (d *DB) GCStagedAttachments(ttl time.Duration) (int, error) {
	if !d.Writable() {
		return 0, nil
	}
	if ttl <= 0 {
		ttl = AttStagedTTL
	}
	cutoff := time.Now().UTC().Add(-ttl).Format(time.RFC3339)
	rows, err := d.SQL.Query(`
SELECT id, session_id, path FROM session_attachments
WHERE message_id IS NULL AND created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	type rec struct{ id, sid, path string }
	var list []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.sid, &r.path); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, r)
	}
	rows.Close()
	n := 0
	for _, r := range list {
		_ = os.Remove(filepath.Join(d.Root, r.path))
		if _, err := d.SQL.Exec(`DELETE FROM session_attachments WHERE id=?`, r.id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// DeleteAttachment removes one staged attachment.
func (d *DB) DeleteAttachment(sessionID, attID string) error {
	abs, err := d.AttachmentAbsPath(sessionID, attID)
	if err != nil {
		return err
	}
	if d.Writable() {
		row, err := d.GetAttachment(attID)
		if err != nil {
			return err
		}
		if row.SessionID != sessionID {
			return fmt.Errorf("attachment not found")
		}
		if row.MessageID != "" {
			return fmt.Errorf("attachment already committed")
		}
		_, _ = d.SQL.Exec(`DELETE FROM session_attachments WHERE id=?`, attID)
	}
	_ = os.Remove(abs)
	return nil
}

// ReadAttachmentBytes reads file bytes after validation.
func (d *DB) ReadAttachmentBytes(sessionID, attID string) ([]byte, error) {
	abs, err := d.AttachmentAbsPath(sessionID, attID)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(abs)
}

func scanAttachment(row scannable) (*AttachmentRow, error) {
	var r AttachmentRow
	var sha, mid, meta sql.NullString
	err := row.Scan(
		&r.ID, &r.SessionID, &r.CreatedAt, &r.Name, &r.MIME, &r.Kind, &r.ByteSize,
		&sha, &r.Source, &r.Path, &mid, &meta,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("attachment not found")
		}
		return nil, err
	}
	r.SHA256 = sha.String
	r.MessageID = mid.String
	r.MetaJSON = meta.String
	return &r, nil
}
