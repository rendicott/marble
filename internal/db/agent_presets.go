package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// MaxAgentPresets is the catalog cap (ADR-0030).
const MaxAgentPresets = 32

var agentPresetIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// KnownAgentDrivers is the built-in driver set (ADR-0030 Q10).
var KnownAgentDrivers = []string{"grok", "claude", "opencode"}

// AgentPresetRow is one operator-defined subprocess preset (ADR-0030).
type AgentPresetRow struct {
	ID              string
	Driver          string
	DisplayName     string
	Command         string
	Model           string
	DefaultArgs     []string
	TimeoutSec      int
	Enabled         bool
	Detected        bool
	DetectedVersion string
	DetectedPath    string
	DetectedAt      string
	SortOrder       int
	Notes           string
	CreatedAt       string
	UpdatedAt       string
	// Context is JSON array of sources; empty string = inherit global default (ADR-0031).
	Context         string
	ContextMaxChars int
}

func (d *DB) migrateV8toV9() error {
	if d.SQL == nil {
		return fmt.Errorf("no database")
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`ALTER TABLE agent_presets ADD COLUMN context TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agent_presets ADD COLUMN context_max_chars INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sessions ADD COLUMN subprocess_context TEXT NOT NULL DEFAULT ''`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("migrate v9: %w\nstmt: %s", err, s)
		}
	}
	now := UTCNow()
	if _, err := tx.Exec(`UPDATE schema_meta SET schema_version = 9, updated_at = ? WHERE id = 1`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) migrateV7toV8() error {
	if d.SQL == nil {
		return fmt.Errorf("no database")
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS agent_presets (
			id TEXT PRIMARY KEY,
			driver TEXT NOT NULL,
			display_name TEXT NOT NULL,
			command TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			default_args TEXT NOT NULL DEFAULT '[]',
			timeout_sec INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			detected INTEGER NOT NULL DEFAULT 0,
			detected_version TEXT NOT NULL DEFAULT '',
			detected_path TEXT NOT NULL DEFAULT '',
			detected_at TEXT NOT NULL DEFAULT '',
			sort_order INTEGER NOT NULL DEFAULT 0,
			notes TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_agent_presets_sort
			ON agent_presets(enabled, sort_order, display_name)`,
		`ALTER TABLE sessions ADD COLUMN agent_preset_id TEXT NOT NULL DEFAULT ''`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("migrate v8: %w\nstmt: %s", err, s)
		}
	}
	now := UTCNow()
	if _, err := tx.Exec(`UPDATE schema_meta SET schema_version = 8, updated_at = ? WHERE id = 1`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func knownDriver(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, d := range KnownAgentDrivers {
		if d == n {
			return true
		}
	}
	return false
}

// ValidateAgentPreset checks a row for create/update.
func ValidateAgentPreset(r *AgentPresetRow) error {
	if r == nil {
		return fmt.Errorf("nil preset")
	}
	id := strings.TrimSpace(r.ID)
	if !agentPresetIDRe.MatchString(id) {
		return fmt.Errorf("id must match [a-z0-9][a-z0-9._-]{0,63}")
	}
	r.ID = id
	r.Driver = strings.ToLower(strings.TrimSpace(r.Driver))
	if !knownDriver(r.Driver) {
		return fmt.Errorf("driver must be one of %s", strings.Join(KnownAgentDrivers, ", "))
	}
	r.DisplayName = strings.TrimSpace(r.DisplayName)
	if r.DisplayName == "" {
		r.DisplayName = r.ID
	}
	if len(r.DisplayName) > 80 {
		return fmt.Errorf("display_name max 80")
	}
	r.Command = strings.TrimSpace(r.Command)
	if r.Command == "" {
		r.Command = r.Driver
	}
	r.Model = strings.TrimSpace(r.Model)
	r.Notes = strings.TrimSpace(r.Notes)
	if r.TimeoutSec < 0 {
		r.TimeoutSec = 0
	}
	if r.DefaultArgs == nil {
		r.DefaultArgs = []string{}
	}
	return nil
}

func (d *DB) CountAgentPresets() (int, error) {
	if !d.Writable() {
		return 0, nil
	}
	var n int
	err := d.SQL.QueryRow(`SELECT COUNT(*) FROM agent_presets`).Scan(&n)
	return n, err
}

func (d *DB) ListAgentPresets() ([]AgentPresetRow, error) {
	if !d.Writable() {
		return nil, nil
	}
	rows, err := d.SQL.Query(`
SELECT id, driver, display_name, command, model, default_args, timeout_sec, enabled,
       detected, detected_version, detected_path, detected_at, sort_order, notes, created_at, updated_at,
       COALESCE(context,''), COALESCE(context_max_chars,0)
FROM agent_presets ORDER BY sort_order, display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentPresetRow
	for rows.Next() {
		r, err := scanAgentPreset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) GetAgentPreset(id string) (*AgentPresetRow, error) {
	if !d.Writable() {
		return nil, fmt.Errorf("database not writable")
	}
	id = strings.TrimSpace(id)
	row := d.SQL.QueryRow(`
SELECT id, driver, display_name, command, model, default_args, timeout_sec, enabled,
       detected, detected_version, detected_path, detected_at, sort_order, notes, created_at, updated_at,
       COALESCE(context,''), COALESCE(context_max_chars,0)
FROM agent_presets WHERE id=?`, id)
	r, err := scanAgentPreset(row)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (d *DB) InsertAgentPreset(r AgentPresetRow) error {
	if !d.Writable() {
		return fmt.Errorf("database not writable")
	}
	if err := ValidateAgentPreset(&r); err != nil {
		return err
	}
	n, err := d.CountAgentPresets()
	if err != nil {
		return err
	}
	if n >= MaxAgentPresets {
		return fmt.Errorf("max %d presets", MaxAgentPresets)
	}
	args, _ := json.Marshal(r.DefaultArgs)
	en, det := 0, 0
	if r.Enabled {
		en = 1
	}
	if r.Detected {
		det = 1
	}
	now := UTCNow()
	if r.CreatedAt == "" {
		r.CreatedAt = now
	}
	if r.UpdatedAt == "" {
		r.UpdatedAt = now
	}
	_, err = d.SQL.Exec(`
INSERT INTO agent_presets (
  id, driver, display_name, command, model, default_args, timeout_sec, enabled,
  detected, detected_version, detected_path, detected_at, sort_order, notes, created_at, updated_at,
  context, context_max_chars
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Driver, r.DisplayName, r.Command, r.Model, string(args), r.TimeoutSec, en,
		det, r.DetectedVersion, r.DetectedPath, r.DetectedAt, r.SortOrder, r.Notes, r.CreatedAt, r.UpdatedAt,
		r.Context, r.ContextMaxChars)
	return err
}

func (d *DB) UpdateAgentPreset(r AgentPresetRow) error {
	if !d.Writable() {
		return fmt.Errorf("database not writable")
	}
	if err := ValidateAgentPreset(&r); err != nil {
		return err
	}
	args, _ := json.Marshal(r.DefaultArgs)
	en, det := 0, 0
	if r.Enabled {
		en = 1
	}
	if r.Detected {
		det = 1
	}
	r.UpdatedAt = UTCNow()
	res, err := d.SQL.Exec(`
UPDATE agent_presets SET
  driver=?, display_name=?, command=?, model=?, default_args=?, timeout_sec=?, enabled=?,
  detected=?, detected_version=?, detected_path=?, detected_at=?, sort_order=?, notes=?, updated_at=?,
  context=?, context_max_chars=?
WHERE id=?`,
		r.Driver, r.DisplayName, r.Command, r.Model, string(args), r.TimeoutSec, en,
		det, r.DetectedVersion, r.DetectedPath, r.DetectedAt, r.SortOrder, r.Notes, r.UpdatedAt,
		r.Context, r.ContextMaxChars, r.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("preset %q not found", r.ID)
	}
	return nil
}

func (d *DB) DeleteAgentPreset(id string) error {
	if !d.Writable() {
		return fmt.Errorf("database not writable")
	}
	res, err := d.SQL.Exec(`DELETE FROM agent_presets WHERE id=?`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("preset %q not found", id)
	}
	return nil
}

func (d *DB) UpdateAgentPresetDetection(id string, detected bool, path, version string) error {
	if !d.Writable() {
		return nil
	}
	det := 0
	if detected {
		det = 1
	}
	_, err := d.SQL.Exec(`
UPDATE agent_presets SET detected=?, detected_path=?, detected_version=?, detected_at=?, updated_at=?
WHERE id=?`,
		det, path, version, time.Now().UTC().Format(time.RFC3339), UTCNow(), strings.TrimSpace(id))
	return err
}

type presetScanner interface {
	Scan(dest ...any) error
}

func scanAgentPreset(row presetScanner) (AgentPresetRow, error) {
	var r AgentPresetRow
	var args string
	var en, det int
	err := row.Scan(&r.ID, &r.Driver, &r.DisplayName, &r.Command, &r.Model, &args, &r.TimeoutSec, &en,
		&det, &r.DetectedVersion, &r.DetectedPath, &r.DetectedAt, &r.SortOrder, &r.Notes, &r.CreatedAt, &r.UpdatedAt,
		&r.Context, &r.ContextMaxChars)
	if err != nil {
		if err == sql.ErrNoRows {
			return r, fmt.Errorf("preset not found")
		}
		return r, err
	}
	r.Enabled = en != 0
	r.Detected = det != 0
	if strings.TrimSpace(args) != "" {
		_ = json.Unmarshal([]byte(args), &r.DefaultArgs)
	}
	if r.DefaultArgs == nil {
		r.DefaultArgs = []string{}
	}
	return r, nil
}

// SeedAgentPresetsIfEmpty inserts rows when the table is empty (first migrate).
func (d *DB) SeedAgentPresetsIfEmpty(rows []AgentPresetRow) (int, error) {
	if d == nil || !d.Writable() {
		return 0, nil
	}
	n, err := d.CountAgentPresets()
	if err != nil || n > 0 {
		return 0, err
	}
	inserted := 0
	for _, row := range rows {
		if err := d.InsertAgentPreset(row); err != nil {
			continue
		}
		inserted++
	}
	return inserted, nil
}

// PublicAgentPreset is JSON for Settings / picker (no secrets).
func (r AgentPresetRow) Public() map[string]interface{} {
	return map[string]interface{}{
		"id":                r.ID,
		"driver":            r.Driver,
		"display_name":      r.DisplayName,
		"command":           r.Command,
		"model":             r.Model,
		"default_args":      r.DefaultArgs,
		"timeout_sec":       r.TimeoutSec,
		"enabled":           r.Enabled,
		"detected":          r.Detected,
		"detected_version":  r.DetectedVersion,
		"detected_path":     r.DetectedPath,
		"detected_at":       r.DetectedAt,
		"sort_order":        r.SortOrder,
		"notes":             r.Notes,
		"context":           parseContextJSON(r.Context),
		"context_max_chars": r.ContextMaxChars,
	}
}

func parseContextJSON(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}
