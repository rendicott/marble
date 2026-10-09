package db

import (
	"database/sql"
	"fmt"
	"time"
)

// SessionRow is a sessions table row.
type SessionRow struct {
	ID                string
	Title             string
	Status            string
	CreatedAt         string
	UpdatedAt         string
	ClosedAt          sql.NullString
	MessageCount      int
	Dirty             bool
	Workspace         string
	Model             string // last effective provider model string (ADR-0018 KD12)
	ModelID           string // catalog slug or "" (ADR-0018)
	ComputerID        string // bound peer computer slug (ADR-0020)
	AgentPresetID     string // subprocess session lock (ADR-0030); exclusive with ModelID
	SubprocessContext string // JSON {context,max_chars}; empty = inherit (ADR-0031)
	// AgentCWD is a project directory relative to the workspace. Empty means the workspace root.
	AgentCWD string
	MDPath   string
}

// migrateV12toV13 adds sessions.agent_cwd. Fresh databases reach v13 through
// upgradeSchema, so a second run (column already present) is a no-op.
func (d *DB) migrateV12toV13() error {
	if d.SQL == nil {
		return fmt.Errorf("no database")
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'agent_cwd'`).Scan(&n); err != nil {
		return fmt.Errorf("migrate v13: %w", err)
	}
	if n == 0 {
		if _, err := tx.Exec(`ALTER TABLE sessions ADD COLUMN agent_cwd TEXT NOT NULL DEFAULT ''`); err != nil && !sqliteDuplicateColumn(err) {
			return fmt.Errorf("migrate v13: %w", err)
		}
	}
	if _, err := tx.Exec(`UPDATE schema_meta SET schema_version = 13, updated_at = ? WHERE id = 1`, UTCNow()); err != nil {
		return err
	}
	return tx.Commit()
}

// UpsertSession inserts or updates a session index row.
func (d *DB) UpsertSession(s SessionRow) error {
	if !d.Writable() {
		return nil
	}
	if s.Status == "" {
		s.Status = "active"
	}
	dirty := 0
	if s.Dirty {
		dirty = 1
	}
	var closed interface{}
	if s.ClosedAt.Valid {
		closed = s.ClosedAt.String
	}
	_, err := d.SQL.Exec(`
		INSERT INTO sessions (id, title, status, created_at, updated_at, closed_at, message_count, dirty, workspace, model, model_id, md_path, computer_id, agent_preset_id, subprocess_context, agent_cwd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title=excluded.title,
			status=excluded.status,
			updated_at=excluded.updated_at,
			closed_at=excluded.closed_at,
			message_count=excluded.message_count,
			dirty=excluded.dirty,
			workspace=excluded.workspace,
			model=excluded.model,
			model_id=excluded.model_id,
			md_path=excluded.md_path,
			computer_id=excluded.computer_id,
			agent_preset_id=excluded.agent_preset_id,
			subprocess_context=excluded.subprocess_context,
			agent_cwd=excluded.agent_cwd
	`, s.ID, s.Title, s.Status, s.CreatedAt, s.UpdatedAt, closed, s.MessageCount, dirty, s.Workspace, s.Model, s.ModelID, s.MDPath, s.ComputerID, s.AgentPresetID, s.SubprocessContext, s.AgentCWD)
	return err
}

// ListSessions returns sessions ordered by updated_at desc.
// If includeClosed is false, only active rows.
func (d *DB) ListSessions(includeClosed bool) ([]SessionRow, error) {
	if !d.Writable() {
		return nil, nil
	}
	q := `SELECT id, title, status, created_at, updated_at, closed_at, message_count, dirty, workspace, model, model_id, md_path, COALESCE(computer_id,''), COALESCE(agent_preset_id,''), COALESCE(subprocess_context,''), COALESCE(agent_cwd,'')
		FROM sessions`
	if !includeClosed {
		q += ` WHERE status != 'closed'`
	}
	q += ` ORDER BY updated_at DESC`
	rows, err := d.SQL.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var s SessionRow
		var dirty int
		if err := rows.Scan(&s.ID, &s.Title, &s.Status, &s.CreatedAt, &s.UpdatedAt, &s.ClosedAt,
			&s.MessageCount, &dirty, &s.Workspace, &s.Model, &s.ModelID, &s.MDPath, &s.ComputerID, &s.AgentPresetID, &s.SubprocessContext, &s.AgentCWD); err != nil {
			return nil, err
		}
		s.Dirty = dirty != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// MarkClosed sets status closed.
func (d *DB) MarkClosed(id string, closedAt time.Time) error {
	if !d.Writable() {
		return nil
	}
	ts := closedAt.UTC().Format(time.RFC3339)
	_, err := d.SQL.Exec(`
		UPDATE sessions SET status='closed', closed_at=?, updated_at=?, dirty=0 WHERE id=?
	`, ts, ts, id)
	return err
}

// PruneClosed deletes closed sessions older than maxAgeDays and their events.
// Returns pruned session ids.
func (d *DB) PruneClosed(maxAgeDays int) (ids []string, err error) {
	if !d.Writable() || maxAgeDays < 0 {
		return nil, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -maxAgeDays).Format(time.RFC3339)
	rows, err := d.SQL.Query(`
		SELECT id FROM sessions
		WHERE status='closed' AND closed_at IS NOT NULL AND closed_at < ?
	`, cutoff)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if _, err := d.SQL.Exec(`DELETE FROM session_events WHERE session_id=?`, id); err != nil {
			return ids, err
		}
		if _, err := d.SQL.Exec(`DELETE FROM sessions WHERE id=?`, id); err != nil {
			return ids, err
		}
	}
	return ids, nil
}
