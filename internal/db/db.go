package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// CurrentSchemaVersion is the schema this binary writes and fully supports.
// v1: sessions, events, blobs, settings, daemon_state
// v2: cron_jobs, cron_runs (ADR-0015)
// v3: model_catalog + model_id columns (ADR-0018)
// v4: session_attachments (ADR-0019)
// v5: computers + pairings + sessions.computer_id (ADR-0020)
// v6: clerk_session_state (ADR-0023 Clerk dashboard)
// v7: clerk_session_state.snoozed_until (Clerk snooze)
const CurrentSchemaVersion = 7

// Mode is normal dual-write or limp (files-only).
type Mode string

const (
	ModeNormal Mode = "normal"
	ModeLimp   Mode = "limp"
)

// DB wraps the SQLite connection and memory-root paths.
type DB struct {
	SQL    *sql.DB
	Root   string // --memory leaf
	Path   string // marble.db path
	Mode   Mode
	Reason string // limp reason if any

	mu       sync.Mutex
	lockFile *os.File
}

// Open opens or creates $MEMORY/marble.db. On schema mismatch without migrations, returns limp (SQL may be nil).
func Open(memoryRoot string) (*DB, error) {
	abs, err := filepath.Abs(memoryRoot)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(abs, "blobs"), 0o755); err != nil {
		return nil, fmt.Errorf("blobs dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(abs, "attachments"), 0o755); err != nil {
		return nil, fmt.Errorf("attachments dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(abs, "session"), 0o755); err != nil {
		return nil, fmt.Errorf("session dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(abs, "daily"), 0o755); err != nil {
		return nil, fmt.Errorf("daily dir: %w", err)
	}

	d := &DB{
		Root: abs,
		Path: filepath.Join(abs, "marble.db"),
		Mode: ModeNormal,
	}

	if err := d.acquireLock(); err != nil {
		return nil, err
	}

	existed := fileExists(d.Path)
	// modernc.org/sqlite driver name is "sqlite"
	sqlDB, err := sql.Open("sqlite", d.Path)
	if err != nil {
		d.releaseLock()
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	sqlDB.SetMaxOpenConns(1) // serialize writers
	for _, pragma := range []string{
		`PRAGMA busy_timeout=5000`,
		`PRAGMA journal_mode=WAL`,
		`PRAGMA foreign_keys=ON`,
	} {
		if _, err := sqlDB.Exec(pragma); err != nil {
			_ = sqlDB.Close()
			d.releaseLock()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	d.SQL = sqlDB

	if !existed {
		if err := d.migrateV1(); err != nil {
			d.Close()
			return nil, fmt.Errorf("create schema: %w", err)
		}
	}

	ver, err := d.readSchemaVersion()
	if err != nil {
		// corrupt / unreadable → limp, close SQL
		_ = sqlDB.Close()
		d.SQL = nil
		d.Mode = ModeLimp
		d.Reason = "database unreadable or corrupt: " + err.Error()
		return d, nil
	}

	if ver > CurrentSchemaVersion {
		_ = sqlDB.Close()
		d.SQL = nil
		d.Mode = ModeLimp
		d.Reason = fmt.Sprintf("database schema v%d is newer than harness v%d — upgrade harness or use another --memory", ver, CurrentSchemaVersion)
		return d, nil
	}
	if ver < CurrentSchemaVersion {
		if err := d.upgradeSchema(ver); err != nil {
			d.Close()
			return nil, fmt.Errorf("migrate schema v%d→v%d: %w", ver, CurrentSchemaVersion, err)
		}
	}

	// Ensure default settings exist (idempotent)
	_ = d.seedSettings()
	return d, nil
}

// upgradeSchema applies stepwise migrations from fromVer to CurrentSchemaVersion.
func (d *DB) upgradeSchema(fromVer int) error {
	for v := fromVer; v < CurrentSchemaVersion; v++ {
		switch v {
		case 1:
			if err := d.migrateV1toV2(); err != nil {
				return err
			}
		case 2:
			if err := d.migrateV2toV3(); err != nil {
				return err
			}
		case 3:
			if err := d.migrateV3toV4(); err != nil {
				return err
			}
		case 4:
			if err := d.migrateV4toV5(); err != nil {
				return err
			}
		case 5:
			if err := d.migrateV5toV6(); err != nil {
				return err
			}
		case 6:
			if err := d.migrateV6toV7(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("no migrator for schema v%d → v%d", v, v+1)
		}
	}
	return nil
}

// Close releases the DB and lock file.
func (d *DB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var err error
	if d.SQL != nil {
		err = d.SQL.Close()
		d.SQL = nil
	}
	d.releaseLock()
	return err
}

// Writable reports whether dual-write is allowed.
func (d *DB) Writable() bool {
	return d != nil && d.Mode == ModeNormal && d.SQL != nil
}

// Health returns fields for /api/health.
func (d *DB) Health() map[string]interface{} {
	if d == nil {
		return map[string]interface{}{"mode": "none"}
	}
	out := map[string]interface{}{
		"mode":                  string(d.Mode),
		"schema_version_binary": CurrentSchemaVersion,
		"db_path":               d.Path,
		"memory_path":           d.Root,
		"limp_reason":           d.Reason,
	}
	if d.Writable() {
		ver, err := d.readSchemaVersion()
		if err == nil {
			out["schema_version_db"] = ver
		}
	} else if d.Mode == ModeLimp {
		out["schema_version_db"] = nil
	}
	return out
}

func (d *DB) readSchemaVersion() (int, error) {
	var ver int
	err := d.SQL.QueryRow(`SELECT schema_version FROM schema_meta WHERE id = 1`).Scan(&ver)
	if err != nil {
		return 0, err
	}
	return ver, nil
}

func (d *DB) migrateV1() error {
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		`CREATE TABLE schema_meta (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			schema_version INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			created_by_harness TEXT
		)`,
		`CREATE TABLE settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			closed_at TEXT,
			message_count INTEGER NOT NULL DEFAULT 0,
			dirty INTEGER NOT NULL DEFAULT 0,
			workspace TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			md_path TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE session_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			ts TEXT NOT NULL,
			kind TEXT NOT NULL,
			role TEXT,
			content TEXT,
			content_truncated INTEGER NOT NULL DEFAULT 0,
			blob_id TEXT,
			tool_name TEXT,
			tool_call_id TEXT,
			tool_args_json TEXT,
			model TEXT,
			tokens_in_reported INTEGER,
			tokens_out_reported INTEGER,
			tokens_in_est INTEGER,
			tokens_out_est INTEGER,
			latency_ms INTEGER,
			finish_reason TEXT,
			error TEXT,
			meta_json TEXT
		)`,
		`CREATE INDEX idx_events_session_seq ON session_events(session_id, seq)`,
		`CREATE INDEX idx_events_session_ts ON session_events(session_id, ts)`,
		`CREATE INDEX idx_events_kind ON session_events(kind)`,
		`CREATE INDEX idx_events_blob ON session_events(blob_id)`,
		`CREATE TABLE blobs (
			id TEXT PRIMARY KEY,
			path TEXT NOT NULL,
			byte_size INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			session_id TEXT,
			content_sha256 TEXT
		)`,
		`CREATE TABLE daemon_state (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			last_sweep_at TEXT,
			next_sweep_at TEXT,
			last_sweep_duration_ms INTEGER,
			sessions_seen INTEGER,
			sessions_dirty INTEGER,
			sessions_flushed INTEGER,
			sessions_failed INTEGER,
			blobs_purged INTEGER,
			sessions_pruned INTEGER,
			last_error TEXT,
			last_daily_compact_at TEXT,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("migrate: %w\nstmt: %s", err, s)
		}
	}

	now := UTCNow()
	// Fresh DBs start at v1; upgradeSchema brings them to CurrentSchemaVersion.
	if _, err := tx.Exec(
		`INSERT INTO schema_meta (id, schema_version, created_at, updated_at, created_by_harness) VALUES (1, ?, ?, ?, ?)`,
		1, now, now, "marble-harness",
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO daemon_state (id, updated_at) VALUES (1, ?)`, now,
	); err != nil {
		return err
	}
	if err := seedSettingsTx(tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) seedSettings() error {
	return seedSettingsTx(d.SQL, UTCNow())
}

type execer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

func seedSettingsTx(e execer, now string) error {
	defaults := map[string]string{
		"blob_max_age_days":           "4",
		"closed_session_max_age_days": "4",
		"db_inline_max_bytes":         "32768",
		// ADR-0005 shell policy
		"shell_enabled":              "true",
		"shell_mode":                 "deny_list",
		"shell_allow_sudo":           "false",
		"shell_default_timeout_sec":  "60",
		"shell_max_timeout_sec":      "300",
		"shell_max_output_bytes":     "524288",
		"shell_cwd_strict":           "true",
		"shell_block_memory_paths":   "true",
		"shell_allow_patterns":       "[]",
		// shell_deny_patterns seeded lazily via DefaultSettings when empty
		// tool loop soft defaults (informational / future UI)
		"tool_round_soft":            "65",
		"tool_round_hard":            "80",
	}
	for k, v := range defaults {
		_, err := e.Exec(
			`INSERT OR IGNORE INTO settings (key, value, updated_at) VALUES (?, ?, ?)`,
			k, v, now,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// SettingString reads a string setting with default.
func (d *DB) SettingString(key, def string) string {
	if d == nil || !d.Writable() {
		return def
	}
	var v string
	err := d.SQL.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err != nil || v == "" {
		return def
	}
	return v
}

// SettingBool reads a bool setting (true/1/yes).
func (d *DB) SettingBool(key string, def bool) bool {
	s := strings.ToLower(strings.TrimSpace(d.SettingString(key, "")))
	if s == "" {
		return def
	}
	switch s {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

// SettingInt reads an int setting with default.
func (d *DB) SettingInt(key string, def int) int {
	if !d.Writable() {
		return def
	}
	var v string
	err := d.SQL.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err != nil {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}

// UTCNow returns RFC3339 UTC with Z.
func UTCNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// BlobsDir returns $MEMORY/blobs.
func (d *DB) BlobsDir() string {
	return filepath.Join(d.Root, "blobs")
}
