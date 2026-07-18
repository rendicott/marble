package db

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SpillBlob writes data to $MEMORY/blobs/<id> and registers the blob row.
func (d *DB) SpillBlob(sessionID string, data []byte) (string, error) {
	if !d.Writable() {
		return "", fmt.Errorf("db not writable")
	}
	id := newBlobID()
	rel := filepath.Join("blobs", id)
	abs := filepath.Join(d.Root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return "", err
	}
	now := UTCNow()
	_, err := d.SQL.Exec(`
		INSERT INTO blobs (id, path, byte_size, created_at, session_id) VALUES (?, ?, ?, ?, ?)
	`, id, rel, len(data), now, nullStr(sessionID))
	if err != nil {
		_ = os.Remove(abs)
		return "", err
	}
	return id, nil
}

// DeleteSessionBlobs removes blob files and rows for a session.
func (d *DB) DeleteSessionBlobs(sessionID string) (int, error) {
	if !d.Writable() {
		return 0, nil
	}
	rows, err := d.SQL.Query(`SELECT id, path FROM blobs WHERE session_id=?`, sessionID)
	if err != nil {
		return 0, err
	}
	type pair struct{ id, path string }
	var list []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.path); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, p)
	}
	rows.Close()
	n := 0
	for _, p := range list {
		_ = os.Remove(filepath.Join(d.Root, p.path))
		if _, err := d.SQL.Exec(`DELETE FROM blobs WHERE id=?`, p.id); err != nil {
			return n, err
		}
		n++
	}
	// also blobs only referenced via events for this session
	rows2, err := d.SQL.Query(`
		SELECT DISTINCT e.blob_id FROM session_events e
		WHERE e.session_id=? AND e.blob_id IS NOT NULL
		  AND e.blob_id NOT IN (SELECT id FROM blobs WHERE session_id=?)
	`, sessionID, sessionID)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var bid string
			if rows2.Scan(&bid) == nil && bid != "" {
				var path string
				if d.SQL.QueryRow(`SELECT path FROM blobs WHERE id=?`, bid).Scan(&path) == nil {
					_ = os.Remove(filepath.Join(d.Root, path))
				}
				_, _ = d.SQL.Exec(`DELETE FROM blobs WHERE id=?`, bid)
				n++
			}
		}
	}
	return n, nil
}

// GCBlobs removes unreferenced blobs older than maxAgeDays.
func (d *DB) GCBlobs(maxAgeDays int) (int, error) {
	if !d.Writable() {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -maxAgeDays).Format(time.RFC3339)
	rows, err := d.SQL.Query(`
		SELECT b.id, b.path FROM blobs b
		WHERE b.created_at < ?
		  AND b.id NOT IN (SELECT blob_id FROM session_events WHERE blob_id IS NOT NULL)
	`, cutoff)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			return n, err
		}
		_ = os.Remove(filepath.Join(d.Root, path))
		if _, err := d.SQL.Exec(`DELETE FROM blobs WHERE id=?`, id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func newBlobID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
