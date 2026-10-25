package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // own connection; do not use db.Open
)

var bundleDirs = []string{
	"session", "daily", "knowledge", "skills", "mpub", "attachments", "blobs", "backups",
}

var bundleRootFiles = []string{
	"soul.md", "mcp.json", "tts.json", "sinks.json", "agent_process.json",
}

// ExportRequest is a state export. An empty OutDir prints stats and writes nothing.
type ExportRequest struct {
	MemoryDir      string
	OutDir         string
	Stats          bool
	Secrets        string
	AgeRecipients  []string
	PublicURL      string
	Workspace      string
	Model          string
	BaseURL        string
	HarnessVersion string
}

// Stats is the file inventory shown by a dry run and by --stats.
type Stats struct {
	DBBytes   int64
	DBExists  bool
	SnapBytes int64
	Schema    int
	Files     int
	FileBytes int64
	Prefixes  []PrefixStat
}

// PrefixStat is one top-level path inside the bundle's files/ tree.
type PrefixStat struct {
	Name  string
	Files int
	Bytes int64
}

// ExportResult is a finished export. Manifest is nil on a dry run.
type ExportResult struct {
	DryRun      bool
	Manifest    *Manifest
	SecretNames []string
	Stats       Stats
	Lines       []string
}

type bundledFile struct {
	rel  string
	abs  string
	mode os.FileMode
	size int64
	sha  string
}

// Export writes a marble-state/v1 bundle, or a stats dry run when OutDir is empty.
// The SQLite snapshot is VACUUM INTO on a plain connection. The source database
// is not migrated and device_token_plain is cleared only on the snapshot.
func Export(req ExportRequest) (*ExportResult, error) {
	if strings.TrimSpace(req.MemoryDir) == "" {
		return nil, fmt.Errorf("memory dir required")
	}
	mem, err := filepath.Abs(req.MemoryDir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(mem)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("memory is not a directory: %s", mem)
	}
	sec, err := normalizeSecrets(req.Secrets)
	if err != nil {
		return nil, err
	}
	manifestSecrets := sec
	if sec == "omit" {
		manifestSecrets = "omitted"
	}
	if sec == "age" {
		if _, err := parseRecipients(req.AgeRecipients); err != nil {
			return nil, err
		}
		if _, err := readEnvFile(mem); err != nil {
			return nil, fmt.Errorf("secrets=age: %w", err)
		}
	}
	out := ""
	if strings.TrimSpace(req.OutDir) != "" {
		out, err = filepath.Abs(req.OutDir)
		if err != nil {
			return nil, err
		}
		if samePath(out, mem) || isUnder(out, mem) {
			return nil, fmt.Errorf("refusing to write the bundle over the memory directory")
		}
	}
	names, err := secretNames(mem)
	if err != nil {
		return nil, err
	}
	files, err := collectFiles(mem, out)
	if err != nil {
		return nil, err
	}
	stats := summarize(files)
	dbPath := filepath.Join(mem, "marble.db")
	dbFi, dbErr := os.Stat(dbPath)
	if dbErr != nil && !os.IsNotExist(dbErr) {
		return nil, dbErr
	}
	if dbErr == nil && !dbFi.Mode().IsRegular() {
		return nil, fmt.Errorf("marble.db is not a regular file")
	}
	if dbErr == nil {
		stats.DBExists = true
		stats.DBBytes = dbFi.Size()
	}
	if out == "" {
		return &ExportResult{
			DryRun:      true,
			SecretNames: names,
			Stats:       stats,
			Lines:       dryRunLines(mem, manifestSecrets, names, stats),
		}, nil
	}
	if !stats.DBExists {
		return nil, fmt.Errorf("marble.db: %w", os.ErrNotExist)
	}

	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	snapDir, err := os.MkdirTemp(out, ".marble-snap-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(snapDir)
	snapPath := filepath.Join(snapDir, "marble.db")
	schema, err := snapshotInto(dbPath, snapPath)
	if err != nil {
		return nil, err
	}
	finalDB := filepath.Join(out, "marble.db")
	if err := os.Remove(finalDB); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.Rename(snapPath, finalDB); err != nil {
		return nil, fmt.Errorf("place snapshot: %w", err)
	}
	dbSum, dbBytes, err := hashFile(finalDB)
	if err != nil {
		return nil, err
	}
	dbMode := os.FileMode(0o644)
	if fi, err := os.Stat(finalDB); err == nil {
		dbMode = fi.Mode()
	}
	_ = os.Chmod(finalDB, dbMode.Perm())
	stats.Schema = schema
	stats.SnapBytes = dbBytes

	entries := make([]FileEntry, 0, len(files))
	for _, f := range files {
		dst := filepath.Join(out, filepath.FromSlash(f.rel))
		sum, n, err := copyFileHash(f.abs, dst, f.mode)
		if err != nil {
			return nil, err
		}
		if n != f.size || !strings.EqualFold(sum, f.sha) {
			return nil, fmt.Errorf("source changed during export: %s", f.rel)
		}
		entries = append(entries, FileEntry{
			Path: f.rel, SHA256: sum, Bytes: n, Mode: formatMode(f.mode),
		})
	}

	if sec == "age" {
		plain, err := readEnvFile(mem)
		if err != nil {
			return nil, fmt.Errorf("secrets=age: %w", err)
		}
		parsed, err := parseRecipients(req.AgeRecipients)
		if err != nil {
			return nil, err
		}
		if err := encryptEnv(filepath.Join(out, "secrets.age"), plain, parsed); err != nil {
			return nil, err
		}
	} else if err := removeIfExists(filepath.Join(out, "secrets.age")); err != nil {
		return nil, err
	}

	host, _ := os.Hostname()
	m := &Manifest{
		Schema:    schemaMarbleStateV1,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Source: SourceInfo{
			Hostname:        host,
			PublicURL:       req.PublicURL,
			Workspace:       req.Workspace,
			Memory:          mem,
			HarnessVersion:  req.HarnessVersion,
			DBSchemaVersion: schema,
			Model:           req.Model,
			BaseURL:         req.BaseURL,
		},
		Files:    entries,
		DB:       DBEntry{Path: "marble.db", SHA256: dbSum, Bytes: dbBytes, SchemaVersion: schema},
		Secrets:  manifestSecrets,
		Excludes: defaultExcludes(),
	}
	if err := writeManifest(filepath.Join(out, "manifest.json"), m); err != nil {
		return nil, err
	}
	return &ExportResult{
		Manifest:    m,
		SecretNames: names,
		Stats:       stats,
		Lines:       exportLines(out, m, names, stats, req.Stats),
	}, nil
}

func dryRunLines(mem, sec string, names []string, stats Stats) []string {
	lines := []string{
		"dry run: pass -out to write a bundle",
		"memory: " + mem,
	}
	if stats.DBExists {
		lines = append(lines, fmt.Sprintf("db: marble.db %d bytes on disk (live file; -out writes a VACUUM INTO snapshot)", stats.DBBytes))
	} else {
		lines = append(lines, "db: marble.db missing")
	}
	lines = append(lines, sidecarLines(mem)...)
	lines = append(lines, fmt.Sprintf("files: %d (%d bytes)", stats.Files, stats.FileBytes))
	for _, p := range stats.Prefixes {
		lines = append(lines, fmt.Sprintf("  %s: %d files, %d bytes", p.Name, p.Files, p.Bytes))
	}
	secretLine := "secrets: " + sec
	if sec == "age" {
		secretLine = "secrets: age (would write secrets.age)"
	}
	lines = append(lines,
		secretLine,
		"secret names: "+formatNames(names),
		"excluded: "+strings.Join(defaultExcludes(), ", "),
	)
	return lines
}

func exportLines(out string, m *Manifest, names []string, stats Stats, showStats bool) []string {
	lines := []string{
		"exported " + out,
		"schema: " + m.Schema,
		"created_at: " + m.CreatedAt,
		fmt.Sprintf("source: hostname=%s public_url=%s workspace=%s memory=%s model=%s base_url=%s",
			m.Source.Hostname, m.Source.PublicURL, m.Source.Workspace, m.Source.Memory, m.Source.Model, m.Source.BaseURL),
		"harness: " + m.Source.HarnessVersion,
		fmt.Sprintf("db: %s %d bytes sha256=%s schema v%d", m.DB.Path, m.DB.Bytes, m.DB.SHA256, m.DB.SchemaVersion),
		fmt.Sprintf("files: %d", len(m.Files)),
		"secrets: " + m.Secrets,
		"secret names: " + formatNames(names),
		"excluded: " + strings.Join(m.Excludes, ", "),
	}
	if showStats {
		lines = append(lines, fmt.Sprintf("snapshot: %d bytes", stats.SnapBytes))
		for _, p := range stats.Prefixes {
			lines = append(lines, fmt.Sprintf("  %s: %d files, %d bytes", p.Name, p.Files, p.Bytes))
		}
	}
	return lines
}

func formatNames(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

func sidecarLines(memory string) []string {
	var lines []string
	for _, name := range []string{"marble.db-wal", "marble.db-shm"} {
		fi, err := os.Stat(filepath.Join(memory, name))
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		lines = append(lines, fmt.Sprintf("not bundled: %s %d bytes", name, fi.Size()))
	}
	return lines
}

func summarize(files []bundledFile) Stats {
	var st Stats
	st.Files = len(files)
	st.Prefixes = prefixStats(files)
	for _, f := range files {
		st.FileBytes += f.size
	}
	return st
}

func prefixStats(files []bundledFile) []PrefixStat {
	order := []string{}
	byName := map[string]*PrefixStat{}
	for _, f := range files {
		name := prefixName(f.rel)
		ps, ok := byName[name]
		if !ok {
			ps = &PrefixStat{Name: name}
			byName[name] = ps
			order = append(order, name)
		}
		ps.Files++
		ps.Bytes += f.size
	}
	out := make([]PrefixStat, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func prefixName(rel string) string {
	inner := strings.TrimPrefix(rel, "files/")
	if i := strings.IndexByte(inner, '/'); i >= 0 {
		return inner[:i]
	}
	return inner
}

func collectFiles(memory, outAbs string) ([]bundledFile, error) {
	var files []bundledFile
	for _, name := range bundleDirs {
		if err := walkIncludedDir(memory, name, outAbs, &files); err != nil {
			return nil, err
		}
	}
	for _, name := range bundleRootFiles {
		if err := addRootFile(memory, name, outAbs, &files); err != nil {
			return nil, err
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return files, nil
}

func walkIncludedDir(memory, name, outAbs string, files *[]bundledFile) error {
	root := filepath.Join(memory, name)
	fi, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if outAbs != "" && (samePath(path, outAbs) || isUnder(outAbs, path)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		return addRegular(memory, path, files)
	})
}

func addRootFile(memory, name, outAbs string, files *[]bundledFile) error {
	path := filepath.Join(memory, name)
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if outAbs != "" && (samePath(path, outAbs) || isUnder(outAbs, path)) {
		return nil
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	return addRegular(memory, path, files)
}

func addRegular(memory, path string, files *[]bundledFile) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to bundle symlink %s", path)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("refusing to bundle non-regular file %s", path)
	}
	rel, err := filepath.Rel(memory, path)
	if err != nil {
		return err
	}
	sum, n, err := hashFile(path)
	if err != nil {
		return err
	}
	*files = append(*files, bundledFile{
		rel:  "files/" + filepath.ToSlash(rel),
		abs:  path,
		mode: fi.Mode(),
		size: n,
		sha:  sum,
	})
	return nil
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func isUnder(dir, path string) bool {
	dir = filepath.Clean(dir)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func copyFileHash(src, dst string, mode os.FileMode) (string, int64, error) {
	if mode.Perm() == 0 {
		mode = 0o644
	}
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", 0, err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, copyErr := io.Copy(out, io.TeeReader(in, h))
	closeErr := out.Close()
	if copyErr != nil {
		return "", n, copyErr
	}
	if closeErr != nil {
		return "", n, closeErr
	}
	if err := os.Chmod(dst, mode.Perm()); err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// snapshotInto writes a consistent copy of srcPath to destPath.
// Connections are closed before return so the caller can hash the file.
// device_token_plain is cleared on the snapshot only (the column is NOT NULL,
// so the sealed value is an empty string).
func snapshotInto(srcPath, destPath string) (int, error) {
	src, err := sql.Open("sqlite", srcPath)
	if err != nil {
		return 0, err
	}
	src.SetMaxOpenConns(1)
	defer src.Close()

	ctx := context.Background()
	conn, err := src.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout=15000`); err != nil {
		return 0, fmt.Errorf("busy_timeout: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `VACUUM INTO `+quoteSQLString(destPath)); err != nil {
		return 0, fmt.Errorf("VACUUM INTO: %w", err)
	}
	_ = conn.Close()
	_ = src.Close()

	snap, err := sql.Open("sqlite", destPath)
	if err != nil {
		return 0, err
	}
	snap.SetMaxOpenConns(1)
	defer snap.Close()
	if err := integrityCheck(snap); err != nil {
		return 0, err
	}
	if err := clearDeviceTokenPlain(snap); err != nil {
		return 0, err
	}
	if err := integrityCheck(snap); err != nil {
		return 0, err
	}
	ver, err := readSchemaVersion(snap)
	if err != nil {
		return 0, err
	}
	if err := snap.Close(); err != nil {
		return 0, err
	}
	return ver, nil
}

func integrityCheck(sq *sql.DB) error {
	rows, err := sq.Query(`PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	defer rows.Close()
	var msgs []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		msgs = append(msgs, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(msgs) != 1 || msgs[0] != "ok" {
		if len(msgs) == 0 {
			return fmt.Errorf("integrity_check returned no rows")
		}
		return fmt.Errorf("integrity_check: %s", strings.Join(msgs, "; "))
	}
	return nil
}

func readSchemaVersion(sq *sql.DB) (int, error) {
	var ver int
	err := sq.QueryRow(`SELECT schema_version FROM schema_meta WHERE id = 1`).Scan(&ver)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return ver, nil
}

func clearDeviceTokenPlain(sq *sql.DB) error {
	_, err := sq.Exec(`UPDATE computer_pairings SET device_token_plain='' WHERE device_token_plain != ''`)
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "no such table") || strings.Contains(msg, "no such column") {
		return nil
	}
	return fmt.Errorf("clear device_token_plain: %w", err)
}
