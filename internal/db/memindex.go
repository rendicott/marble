package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// MemIndexFile is the memory search index, a sibling of marble.db in the
// memory root and outside the session/daily/knowledge markdown trees. It is a
// rebuildable cache of those trees: deleting it loses nothing, the next
// reindex recreates it. It is a separate file so a reindex transaction never
// holds marble.db's single writer connection while a turn is recording events.
const MemIndexFile = "memory-index.db"

// MemIndexSchemaVersion is the memory-index.db schema this binary writes.
// v1: mem_doc + mem_fts (FTS5, external content on mem_doc) + sync triggers
const MemIndexSchemaVersion = 1

// MemIndexPath returns $MEMORY/memory-index.db.
func MemIndexPath(memoryRoot string) string {
	return filepath.Join(memoryRoot, MemIndexFile)
}

// OpenMemIndex opens or creates the memory search index and migrates it to
// MemIndexSchemaVersion. A newer on-disk schema is an error; the caller falls
// back to the linear search and reports itself degraded.
func OpenMemIndex(memoryRoot string) (*sql.DB, error) {
	abs, err := filepath.Abs(memoryRoot)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open("sqlite", MemIndexPath(abs))
	if err != nil {
		return nil, fmt.Errorf("open memory index: %w", err)
	}
	// One connection: the pragmas below are per-connection, and the index is
	// written only by the reindexer, which holds its own mutex.
	sqlDB.SetMaxOpenConns(1)
	for _, pragma := range []string{
		`PRAGMA busy_timeout=5000`,
		`PRAGMA journal_mode=WAL`,
	} {
		if _, err := sqlDB.Exec(pragma); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("memory index %s: %w", pragma, err)
		}
	}
	ver, err := memIndexVersion(sqlDB)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("memory index version: %w", err)
	}
	if ver > MemIndexSchemaVersion {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("memory index schema v%d is newer than harness v%d", ver, MemIndexSchemaVersion)
	}
	for v := ver; v < MemIndexSchemaVersion; v++ {
		var merr error
		switch v {
		case 0:
			merr = migrateMemIndexV0toV1(sqlDB)
		default:
			merr = fmt.Errorf("no migrator for memory index v%d → v%d", v, v+1)
		}
		if merr != nil {
			_ = sqlDB.Close()
			return nil, merr
		}
	}
	return sqlDB, nil
}

func memIndexVersion(sqlDB *sql.DB) (int, error) {
	var n int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'mem_index_meta'`).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	var ver int
	if err := sqlDB.QueryRow(`SELECT schema_version FROM mem_index_meta WHERE id = 1`).Scan(&ver); err != nil {
		return 0, err
	}
	return ver, nil
}

// migrateMemIndexV0toV1 creates mem_doc, its FTS5 index, and the triggers that
// keep the two in step.
//
// mem_fts is external-content (content='mem_doc'), not contentless. The text
// is stored once either way, in mem_doc. External content additionally lets
// FTS5 read the column values back, which gives snippet() a match-centred
// excerpt, lets SELECT read mem_fts.path, and allows
// INSERT INTO mem_fts(mem_fts) VALUES('rebuild') if the two ever drift.
// A contentless table on this SQLite (3.41, before contentless_delete) can
// only delete a row by replaying its exact original values.
func migrateMemIndexV0toV1(sqlDB *sql.DB) error {
	tx, err := sqlDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS mem_index_meta (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			schema_version INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			last_reindexed_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS mem_doc (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			path         TEXT NOT NULL UNIQUE,
			kind         TEXT NOT NULL,
			title        TEXT NOT NULL DEFAULT '',
			headings     TEXT NOT NULL DEFAULT '',
			body         TEXT NOT NULL DEFAULT '',
			size_bytes   INTEGER NOT NULL DEFAULT 0,
			mtime_unix   INTEGER NOT NULL DEFAULT 0,
			content_hash TEXT NOT NULL DEFAULT '',
			indexed_at   TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS mem_doc_kind ON mem_doc(kind)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS mem_fts USING fts5(
			path UNINDEXED, title, headings, body,
			content='mem_doc', content_rowid='id',
			tokenize='unicode61 remove_diacritics 2'
		)`,
		`CREATE TRIGGER IF NOT EXISTS mem_doc_ai AFTER INSERT ON mem_doc BEGIN
			INSERT INTO mem_fts(rowid, path, title, headings, body)
			VALUES (new.id, new.path, new.title, new.headings, new.body);
		END`,
		`CREATE TRIGGER IF NOT EXISTS mem_doc_ad AFTER DELETE ON mem_doc BEGIN
			INSERT INTO mem_fts(mem_fts, rowid, path, title, headings, body)
			VALUES ('delete', old.id, old.path, old.title, old.headings, old.body);
		END`,
		// Only text columns re-index; a stat-only refresh (same hash, new
		// mtime) leaves the inverted index alone.
		`CREATE TRIGGER IF NOT EXISTS mem_doc_au AFTER UPDATE OF path, title, headings, body ON mem_doc BEGIN
			INSERT INTO mem_fts(mem_fts, rowid, path, title, headings, body)
			VALUES ('delete', old.id, old.path, old.title, old.headings, old.body);
			INSERT INTO mem_fts(rowid, path, title, headings, body)
			VALUES (new.id, new.path, new.title, new.headings, new.body);
		END`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("migrate memory index v1: %w", err)
		}
	}
	now := UTCNow()
	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO mem_index_meta (id, schema_version, created_at, updated_at) VALUES (1, 1, ?, ?)`,
		now, now,
	); err != nil {
		return fmt.Errorf("migrate memory index v1: %w", err)
	}
	return tx.Commit()
}
