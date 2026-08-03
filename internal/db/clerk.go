package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ClerkStateRow is durable Clerk dashboard state for one session (ADR-0023).
type ClerkStateRow struct {
	SessionID        string
	Summary          string
	NeedsUser        bool
	ActionItemsJSON  string
	LastUserSnippet  string
	IdleSince        sql.NullString // RFC3339
	SummaryUpdatedAt sql.NullString
	SummaryModel     string
	SummaryError     string
	SnoozedUntil     sql.NullString // RFC3339; active while > now
}

func (d *DB) migrateV5toV6() error {
	if !d.Writable() {
		return fmt.Errorf("db not writable")
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.Exec(`
		CREATE TABLE IF NOT EXISTS clerk_session_state (
			session_id TEXT PRIMARY KEY,
			summary TEXT NOT NULL DEFAULT '',
			needs_user INTEGER NOT NULL DEFAULT 0,
			action_items_json TEXT NOT NULL DEFAULT '[]',
			last_user_snippet TEXT NOT NULL DEFAULT '',
			idle_since TEXT,
			summary_updated_at TEXT,
			summary_model TEXT NOT NULL DEFAULT '',
			summary_error TEXT NOT NULL DEFAULT ''
		)
	`)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`UPDATE schema_meta SET schema_version = 6, updated_at = ? WHERE id = 1`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) migrateV6toV7() error {
	if !d.Writable() {
		return fmt.Errorf("db not writable")
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Idempotent: column may already exist if a partial migrate ran.
	_, err = tx.Exec(`ALTER TABLE clerk_session_state ADD COLUMN snoozed_until TEXT`)
	if err != nil && !isSQLiteDuplicateColumn(err) {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`UPDATE schema_meta SET schema_version = 7, updated_at = ? WHERE id = 1`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func isSQLiteDuplicateColumn(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "duplicate column") || strings.Contains(s, "already exists")
}

// UpsertClerkState inserts or updates summary fields without clearing snooze.
func (d *DB) UpsertClerkState(row ClerkStateRow) error {
	if !d.Writable() {
		return nil
	}
	needs := 0
	if row.NeedsUser {
		needs = 1
	}
	if row.ActionItemsJSON == "" {
		row.ActionItemsJSON = "[]"
	}
	var idle, sumAt interface{}
	if row.IdleSince.Valid {
		idle = row.IdleSince.String
	}
	if row.SummaryUpdatedAt.Valid {
		sumAt = row.SummaryUpdatedAt.String
	}
	_, err := d.SQL.Exec(`
		INSERT INTO clerk_session_state (
			session_id, summary, needs_user, action_items_json, last_user_snippet,
			idle_since, summary_updated_at, summary_model, summary_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			summary=excluded.summary,
			needs_user=excluded.needs_user,
			action_items_json=excluded.action_items_json,
			last_user_snippet=excluded.last_user_snippet,
			idle_since=excluded.idle_since,
			summary_updated_at=excluded.summary_updated_at,
			summary_model=excluded.summary_model,
			summary_error=excluded.summary_error
	`, row.SessionID, row.Summary, needs, row.ActionItemsJSON, row.LastUserSnippet,
		idle, sumAt, row.SummaryModel, row.SummaryError)
	return err
}

// GetClerkState returns clerk state or nil if missing.
func (d *DB) GetClerkState(sessionID string) (*ClerkStateRow, error) {
	if !d.Writable() {
		return nil, nil
	}
	var row ClerkStateRow
	var needs int
	err := d.SQL.QueryRow(`
		SELECT session_id, summary, needs_user, action_items_json, last_user_snippet,
			idle_since, summary_updated_at, summary_model, summary_error, snoozed_until
		FROM clerk_session_state WHERE session_id = ?
	`, sessionID).Scan(
		&row.SessionID, &row.Summary, &needs, &row.ActionItemsJSON, &row.LastUserSnippet,
		&row.IdleSince, &row.SummaryUpdatedAt, &row.SummaryModel, &row.SummaryError,
		&row.SnoozedUntil,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.NeedsUser = needs != 0
	return &row, nil
}

// ListClerkStates returns all clerk rows.
func (d *DB) ListClerkStates() ([]ClerkStateRow, error) {
	if !d.Writable() {
		return nil, nil
	}
	rows, err := d.SQL.Query(`
		SELECT session_id, summary, needs_user, action_items_json, last_user_snippet,
			idle_since, summary_updated_at, summary_model, summary_error, snoozed_until
		FROM clerk_session_state
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClerkStateRow
	for rows.Next() {
		var row ClerkStateRow
		var needs int
		if err := rows.Scan(
			&row.SessionID, &row.Summary, &needs, &row.ActionItemsJSON, &row.LastUserSnippet,
			&row.IdleSince, &row.SummaryUpdatedAt, &row.SummaryModel, &row.SummaryError,
			&row.SnoozedUntil,
		); err != nil {
			return nil, err
		}
		row.NeedsUser = needs != 0
		out = append(out, row)
	}
	return out, rows.Err()
}

// DeleteClerkState removes clerk state when a session is pruned.
func (d *DB) DeleteClerkState(sessionID string) error {
	if !d.Writable() {
		return nil
	}
	_, err := d.SQL.Exec(`DELETE FROM clerk_session_state WHERE session_id = ?`, sessionID)
	return err
}

// PatchClerkSnippetIdle updates snippet and/or idle_since without clearing summary or snooze.
func (d *DB) PatchClerkMeta(sessionID, lastUserSnippet string, idleSince *string, clearIdle bool) error {
	if !d.Writable() {
		return nil
	}
	// Ensure row exists
	_, _ = d.SQL.Exec(`
		INSERT INTO clerk_session_state (session_id) VALUES (?)
		ON CONFLICT(session_id) DO NOTHING
	`, sessionID)
	if clearIdle {
		_, err := d.SQL.Exec(`
			UPDATE clerk_session_state SET last_user_snippet = ?, idle_since = NULL WHERE session_id = ?
		`, lastUserSnippet, sessionID)
		return err
	}
	if idleSince != nil {
		_, err := d.SQL.Exec(`
			UPDATE clerk_session_state SET last_user_snippet = ?, idle_since = ? WHERE session_id = ?
		`, lastUserSnippet, *idleSince, sessionID)
		return err
	}
	_, err := d.SQL.Exec(`
		UPDATE clerk_session_state SET last_user_snippet = ? WHERE session_id = ?
	`, lastUserSnippet, sessionID)
	return err
}

// SetClerkSnooze sets or clears snoozed_until (RFC3339). Empty/clear → NULL.
func (d *DB) SetClerkSnooze(sessionID string, until *string) error {
	if !d.Writable() {
		return fmt.Errorf("db not writable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session_id required")
	}
	_, _ = d.SQL.Exec(`
		INSERT INTO clerk_session_state (session_id) VALUES (?)
		ON CONFLICT(session_id) DO NOTHING
	`, sessionID)
	if until == nil || *until == "" {
		_, err := d.SQL.Exec(`UPDATE clerk_session_state SET snoozed_until = NULL WHERE session_id = ?`, sessionID)
		return err
	}
	_, err := d.SQL.Exec(`UPDATE clerk_session_state SET snoozed_until = ? WHERE session_id = ?`, *until, sessionID)
	return err
}

// ClearClerkSnooze clears an active snooze for a session.
func (d *DB) ClearClerkSnooze(sessionID string) error {
	return d.SetClerkSnooze(sessionID, nil)
}
