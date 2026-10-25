package state

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// rewriteSQL updates sessions.workspace and the prefix of sessions.md_path.
// The match is a path prefix: /home/a matches /home/a and /home/a/proj, and does
// not match /home/ab. Markdown bodies are not rewritten.
const rewriteSQL = `
UPDATE sessions SET
    workspace = ?1 || substr(workspace, length(?2) + 1),
    md_path = CASE
        WHEN md_path = ?2 OR substr(md_path, 1, length(?2) + 1) = (?2 || '/')
        THEN ?1 || substr(md_path, length(?2) + 1)
        ELSE md_path
    END
WHERE workspace = ?2 OR substr(workspace, 1, length(?2) + 1) = (?2 || '/')`

func normalizePrefix(p string) string {
	if p == "" || p == "/" {
		return p
	}
	return strings.TrimRight(p, "/")
}

func rewriteWorkspace(dbPath, from, to string) (int64, error) {
	sq, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return 0, err
	}
	defer sq.Close()
	sq.SetMaxOpenConns(1)
	if _, err := sq.Exec(`PRAGMA busy_timeout=15000`); err != nil {
		return 0, fmt.Errorf("busy_timeout: %w", err)
	}
	tx, err := sq.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(rewriteSQL, to, from)
	if err != nil {
		return 0, fmt.Errorf("rewrite workspace: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// countMarkdownCandidates counts markdown files under root whose bytes contain
// from. It reports a count only; v1 does not edit those files.
func countMarkdownCandidates(root, from string) (int, error) {
	if from == "" {
		return 0, nil
	}
	info, err := os.Stat(root)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, nil
	}
	n := 0
	needle := []byte(from)
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(b, needle) {
			n++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
