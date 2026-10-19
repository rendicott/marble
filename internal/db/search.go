package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// SessionSearchRow is the modal's per-session record plus the transcript hash
// a sweep uses to skip unchanged files (ADR-0037).
type SessionSearchRow struct {
	SessionID   string
	Title       string
	Description string
	UpdatedAt   string
	SourceHash  string
}

// SearchHit is one ranked session. Score is match strength (sum of term
// counts) and is not part of the HTTP response.
type SearchHit struct {
	ID          string
	Title       string
	Description string
	UpdatedAt   string
	Score       int
}

// migrateV13toV14 adds the session search term index.
func (d *DB) migrateV13toV14() error {
	if d.SQL == nil {
		return fmt.Errorf("no database")
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS session_search (
			session_id TEXT PRIMARY KEY,
			title TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT '',
			source_hash TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS session_search_terms (
			session_id TEXT NOT NULL,
			term TEXT NOT NULL,
			count INTEGER NOT NULL,
			PRIMARY KEY (session_id, term)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_session_search_terms_term ON session_search_terms(term)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("migrate v14: %w", err)
		}
	}
	if _, err := tx.Exec(`UPDATE schema_meta SET schema_version = 14, updated_at = ? WHERE id = 1`, UTCNow()); err != nil {
		return fmt.Errorf("migrate v14: %w", err)
	}
	return tx.Commit()
}

// ReplaceSessionSearch writes one session's display row and replaces its term
// counts. When keepHash is true, an existing source_hash is left alone so a
// live merge does not look like a fresh transcript to the sweep.
func (d *DB) ReplaceSessionSearch(row SessionSearchRow, terms map[string]int, keepHash bool) error {
	if !d.Writable() {
		return fmt.Errorf("database not writable")
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if keepHash {
		var existing string
		err := tx.QueryRow(`SELECT source_hash FROM session_search WHERE session_id = ?`, row.SessionID).Scan(&existing)
		if err == nil {
			row.SourceHash = existing
		} else if err != sql.ErrNoRows {
			return err
		}
	}
	if _, err := tx.Exec(`
		INSERT INTO session_search (session_id, title, description, updated_at, source_hash)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			title = excluded.title,
			description = excluded.description,
			updated_at = excluded.updated_at,
			source_hash = excluded.source_hash
	`, row.SessionID, row.Title, row.Description, row.UpdatedAt, row.SourceHash); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM session_search_terms WHERE session_id = ?`, row.SessionID); err != nil {
		return err
	}
	for term, n := range terms {
		if term == "" || n <= 0 {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO session_search_terms (session_id, term, count) VALUES (?, ?, ?)`,
			row.SessionID, term, n,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SearchSessions returns every session that contains at least one of terms.
// Ranking and the result cap happen after the live-memory union.
func (d *DB) SearchSessions(terms []string) ([]SearchHit, error) {
	if !d.Writable() {
		return nil, fmt.Errorf("database not writable")
	}
	terms = uniqueTerms(terms)
	if len(terms) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(terms))
	args := make([]interface{}, len(terms))
	for i, t := range terms {
		placeholders[i] = "?"
		args[i] = t
	}
	q := fmt.Sprintf(`
		SELECT s.session_id, s.title, s.description, s.updated_at, SUM(t.count)
		FROM session_search_terms t
		JOIN session_search s ON s.session_id = t.session_id
		WHERE t.term IN (%s)
		GROUP BY s.session_id
	`, strings.Join(placeholders, ","))
	rows, err := d.SQL.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.ID, &h.Title, &h.Description, &h.UpdatedAt, &h.Score); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SessionSearchHashes returns session_id → source_hash for the sweep.
func (d *DB) SessionSearchHashes() (map[string]string, error) {
	if !d.Writable() {
		return nil, fmt.Errorf("database not writable")
	}
	rows, err := d.SQL.Query(`SELECT session_id, source_hash FROM session_search`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return nil, err
		}
		out[id] = hash
	}
	return out, rows.Err()
}

// DeleteSessionSearch removes one session from both search tables.
func (d *DB) DeleteSessionSearch(id string) error {
	if !d.Writable() {
		return fmt.Errorf("database not writable")
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM session_search_terms WHERE session_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM session_search WHERE session_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SessionSearchTermSet returns the stored term → count map.
func (d *DB) SessionSearchTermSet(id string) (map[string]int, error) {
	if !d.Writable() {
		return nil, fmt.Errorf("database not writable")
	}
	rows, err := d.SQL.Query(`SELECT term, count FROM session_search_terms WHERE session_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var term string
		var n int
		if err := rows.Scan(&term, &n); err != nil {
			return nil, err
		}
		out[term] = n
	}
	return out, rows.Err()
}

func uniqueTerms(terms []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}
