package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// MaxModelCatalogEntries is the soft cap for catalog rows (ADR-0018 Q13).
const MaxModelCatalogEntries = 32

// Reserved catalog ids that must never be used for user rows.
var reservedModelIDs = map[string]bool{
	"process":     true,
	"__process__": true,
}

// Allow dots so common model slugs like grok-4.5 / gpt-4.1 work (ADR-0018).
var modelIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ModelCatalogRow is one operator-defined model (ADR-0018).
type ModelCatalogRow struct {
	ID              string
	DisplayName     string
	Model           string
	BaseURL         string
	APIKeyEnv       string
	CostInputPer1M  *float64
	CostOutputPer1M *float64
	CostNotes       string
	CapReasoning    bool
	CapImages       bool
	CapVoice        bool
	CapTools        bool
	ContextLimit    int
	MaxOutput       int
	ContextReserve  int // 0 = inherit process
	Enabled         bool
	SortOrder       int
	Notes           string
	CreatedAt       string
	UpdatedAt       string
}

func (d *DB) migrateV2toV3() error {
	if d.SQL == nil {
		return fmt.Errorf("no database")
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS model_catalog (
			id TEXT PRIMARY KEY,
			display_name TEXT NOT NULL,
			model TEXT NOT NULL,
			base_url TEXT NOT NULL DEFAULT '',
			api_key_env TEXT NOT NULL DEFAULT '',
			cost_input_per_1m REAL,
			cost_output_per_1m REAL,
			cost_notes TEXT NOT NULL DEFAULT '',
			cap_reasoning INTEGER NOT NULL DEFAULT 0,
			cap_images INTEGER NOT NULL DEFAULT 0,
			cap_voice INTEGER NOT NULL DEFAULT 0,
			cap_tools INTEGER NOT NULL DEFAULT 1,
			context_limit INTEGER NOT NULL,
			max_output INTEGER NOT NULL,
			context_reserve INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			sort_order INTEGER NOT NULL DEFAULT 0,
			notes TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_model_catalog_sort
			ON model_catalog(enabled, sort_order, display_name)`,
		`ALTER TABLE sessions ADD COLUMN model_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE cron_jobs ADD COLUMN model_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE cron_runs ADD COLUMN model_id TEXT NOT NULL DEFAULT ''`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("migrate v3: %w\nstmt: %s", err, s)
		}
	}
	now := UTCNow()
	if _, err := tx.Exec(
		`UPDATE schema_meta SET schema_version = 3, updated_at = ? WHERE id = 1`,
		now,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// ValidateModelCatalog validates a row for create/update (processReserve for budget check when reserve is 0).
func ValidateModelCatalog(r *ModelCatalogRow, processReserve int) error {
	if r == nil {
		return fmt.Errorf("nil row")
	}
	id := strings.TrimSpace(r.ID)
	if !modelIDRe.MatchString(id) {
		return fmt.Errorf("id must match [a-z0-9][a-z0-9._-]{0,63}")
	}
	if reservedModelIDs[id] {
		return fmt.Errorf("id %q is reserved", id)
	}
	r.ID = id
	r.DisplayName = strings.TrimSpace(r.DisplayName)
	if r.DisplayName == "" || len(r.DisplayName) > 80 {
		return fmt.Errorf("display_name required (max 80)")
	}
	r.Model = strings.TrimSpace(r.Model)
	if r.Model == "" || len(r.Model) > 256 {
		return fmt.Errorf("model required (max 256)")
	}
	r.BaseURL = strings.TrimSpace(r.BaseURL)
	if r.BaseURL != "" {
		u, err := url.Parse(r.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("base_url must be empty or absolute http(s) URL")
		}
	}
	r.APIKeyEnv = strings.TrimSpace(r.APIKeyEnv)
	if r.APIKeyEnv != "" && !strings.EqualFold(r.APIKeyEnv, "none") {
		for _, part := range strings.Split(r.APIKeyEnv, ",") {
			p := strings.TrimSpace(part)
			if p == "" || strings.Contains(p, "=") {
				return fmt.Errorf("api_key_env must be env name(s), not key material")
			}
		}
	}
	if r.ContextLimit <= 0 || r.MaxOutput <= 0 {
		return fmt.Errorf("context_limit and max_output must be > 0")
	}
	if r.ContextReserve < 0 {
		return fmt.Errorf("context_reserve must be >= 0 (0 = inherit process)")
	}
	reserve := r.ContextReserve
	if reserve == 0 {
		reserve = processReserve
		if reserve <= 0 {
			reserve = 8192
		}
	}
	budget := r.ContextLimit - r.MaxOutput - reserve
	if budget < 1024 {
		return fmt.Errorf("effective budget (limit - max_out - reserve) must be >= 1024")
	}
	if r.CostInputPer1M != nil && *r.CostInputPer1M < 0 {
		return fmt.Errorf("cost_input_per_1m must be >= 0")
	}
	if r.CostOutputPer1M != nil && *r.CostOutputPer1M < 0 {
		return fmt.Errorf("cost_output_per_1m must be >= 0")
	}
	r.CostNotes = strings.TrimSpace(r.CostNotes)
	r.Notes = strings.TrimSpace(r.Notes)
	return nil
}

// ListModelCatalog returns all catalog rows ordered for UI.
func (d *DB) ListModelCatalog() ([]ModelCatalogRow, error) {
	if d.SQL == nil {
		return nil, nil
	}
	rows, err := d.SQL.Query(`
SELECT id, display_name, model, base_url, api_key_env,
  cost_input_per_1m, cost_output_per_1m, cost_notes,
  cap_reasoning, cap_images, cap_voice, cap_tools,
  context_limit, max_output, context_reserve, enabled, sort_order, notes,
  created_at, updated_at
FROM model_catalog
ORDER BY sort_order ASC, display_name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelCatalogRow
	for rows.Next() {
		r, err := scanModelCatalog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GetModelCatalog loads one catalog row by id.
func (d *DB) GetModelCatalog(id string) (*ModelCatalogRow, error) {
	if d.SQL == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	row := d.SQL.QueryRow(`
SELECT id, display_name, model, base_url, api_key_env,
  cost_input_per_1m, cost_output_per_1m, cost_notes,
  cap_reasoning, cap_images, cap_voice, cap_tools,
  context_limit, max_output, context_reserve, enabled, sort_order, notes,
  created_at, updated_at
FROM model_catalog WHERE id = ?`, strings.TrimSpace(id))
	r, err := scanModelCatalog(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("model not found")
	}
	return r, err
}

// CountModelCatalog returns total catalog rows.
func (d *DB) CountModelCatalog() (total, enabled int, err error) {
	if d.SQL == nil {
		return 0, 0, nil
	}
	err = d.SQL.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN enabled=1 THEN 1 ELSE 0 END),0) FROM model_catalog`).Scan(&total, &enabled)
	return
}

// InsertModelCatalog creates a catalog row.
func (d *DB) InsertModelCatalog(r ModelCatalogRow) error {
	if !d.Writable() {
		return fmt.Errorf("database not writable")
	}
	n, _, err := d.CountModelCatalog()
	if err != nil {
		return err
	}
	if n >= MaxModelCatalogEntries {
		return fmt.Errorf("catalog full (max %d)", MaxModelCatalogEntries)
	}
	if r.CreatedAt == "" {
		r.CreatedAt = UTCNow()
	}
	if r.UpdatedAt == "" {
		r.UpdatedAt = r.CreatedAt
	}
	_, err = d.SQL.Exec(`
INSERT INTO model_catalog (
  id, display_name, model, base_url, api_key_env,
  cost_input_per_1m, cost_output_per_1m, cost_notes,
  cap_reasoning, cap_images, cap_voice, cap_tools,
  context_limit, max_output, context_reserve, enabled, sort_order, notes,
  created_at, updated_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.DisplayName, r.Model, r.BaseURL, r.APIKeyEnv,
		r.CostInputPer1M, r.CostOutputPer1M, r.CostNotes,
		boolInt(r.CapReasoning), boolInt(r.CapImages), boolInt(r.CapVoice), boolInt(r.CapTools),
		r.ContextLimit, r.MaxOutput, r.ContextReserve, boolInt(r.Enabled), r.SortOrder, r.Notes,
		r.CreatedAt, r.UpdatedAt,
	)
	return err
}

// UpdateModelCatalog replaces a catalog row (id immutable).
func (d *DB) UpdateModelCatalog(r ModelCatalogRow) error {
	if !d.Writable() {
		return fmt.Errorf("database not writable")
	}
	if r.UpdatedAt == "" {
		r.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	res, err := d.SQL.Exec(`
UPDATE model_catalog SET
  display_name=?, model=?, base_url=?, api_key_env=?,
  cost_input_per_1m=?, cost_output_per_1m=?, cost_notes=?,
  cap_reasoning=?, cap_images=?, cap_voice=?, cap_tools=?,
  context_limit=?, max_output=?, context_reserve=?, enabled=?, sort_order=?, notes=?,
  updated_at=?
WHERE id=?`,
		r.DisplayName, r.Model, r.BaseURL, r.APIKeyEnv,
		r.CostInputPer1M, r.CostOutputPer1M, r.CostNotes,
		boolInt(r.CapReasoning), boolInt(r.CapImages), boolInt(r.CapVoice), boolInt(r.CapTools),
		r.ContextLimit, r.MaxOutput, r.ContextReserve, boolInt(r.Enabled), r.SortOrder, r.Notes,
		r.UpdatedAt, r.ID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("model not found")
	}
	return nil
}

// DeleteModelCatalog hard-deletes a catalog row.
func (d *DB) DeleteModelCatalog(id string) error {
	if !d.Writable() {
		return fmt.Errorf("database not writable")
	}
	res, err := d.SQL.Exec(`DELETE FROM model_catalog WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("model not found")
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type scannableModel interface {
	Scan(dest ...interface{}) error
}

func scanModelCatalog(row scannableModel) (*ModelCatalogRow, error) {
	var r ModelCatalogRow
	var cin, cout sql.NullFloat64
	var capR, capI, capV, capT, en int
	err := row.Scan(
		&r.ID, &r.DisplayName, &r.Model, &r.BaseURL, &r.APIKeyEnv,
		&cin, &cout, &r.CostNotes,
		&capR, &capI, &capV, &capT,
		&r.ContextLimit, &r.MaxOutput, &r.ContextReserve, &en, &r.SortOrder, &r.Notes,
		&r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if cin.Valid {
		v := cin.Float64
		r.CostInputPer1M = &v
	}
	if cout.Valid {
		v := cout.Float64
		r.CostOutputPer1M = &v
	}
	r.CapReasoning = capR != 0
	r.CapImages = capI != 0
	r.CapVoice = capV != 0
	r.CapTools = capT != 0
	r.Enabled = en != 0
	return &r, nil
}
