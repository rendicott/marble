package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/rendicott/marble/internal/db"
)

// ImportRequest is a state import. Without Apply, the bundle is verified and a plan is printed.
type ImportRequest struct {
	MemoryDir       string
	InDir           string
	Apply           bool
	RewriteFrom     string
	RewriteTo       string
	AgeIdentityFile string
	ForceEnv        bool
	ForceDB         bool
}

// ImportResult is a verified or applied import.
type ImportResult struct {
	Manifest           *Manifest
	Applied            bool
	RowsRewritten      int64
	MarkdownCandidates int
	Lines              []string
	Warnings           []string
}

// Import verifies a bundle and, with Apply, copies it into the memory directory.
// A live marble.lock, a newer schema, or a checksum mismatch is a refusal and
// copies nothing.
func Import(req ImportRequest) (*ImportResult, error) {
	if strings.TrimSpace(req.MemoryDir) == "" {
		return nil, fmt.Errorf("memory dir required")
	}
	if strings.TrimSpace(req.InDir) == "" {
		return nil, fmt.Errorf("bundle dir required")
	}
	mem, err := filepath.Abs(req.MemoryDir)
	if err != nil {
		return nil, err
	}
	in, err := filepath.Abs(req.InDir)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(mem); err == nil && !fi.IsDir() {
		return nil, fmt.Errorf("memory is not a directory: %s", mem)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	from := normalizePrefix(req.RewriteFrom)
	to := normalizePrefix(req.RewriteTo)
	if (from == "") != (to == "") {
		return nil, fmt.Errorf("rewrite-workspace must be FROM=TO")
	}

	m, err := readManifest(in)
	if err != nil {
		return nil, err
	}
	if err := refuseNewerSchema(m); err != nil {
		return nil, err
	}
	if err := refuseLiveLock(mem); err != nil {
		return nil, err
	}
	if err := verifyBundle(in, m); err != nil {
		return nil, err
	}

	agePath := filepath.Join(in, "secrets.age")
	ageFi, ageErr := os.Stat(agePath)
	agePresent := ageErr == nil && ageFi.Mode().IsRegular()
	if ageErr != nil && !os.IsNotExist(ageErr) {
		return nil, ageErr
	}
	if m.Secrets == "age" && !agePresent {
		return nil, fmt.Errorf("manifest secrets is age but secrets.age is missing")
	}

	var warnings []string
	var plain []byte
	restoreEnv := false
	if agePresent && strings.TrimSpace(req.AgeIdentityFile) == "" {
		warnings = append(warnings, "secrets.age present but no --age-identity; env not restored")
	} else if agePresent {
		plain, err = decryptAge(agePath, req.AgeIdentityFile)
		if err != nil {
			return nil, err
		}
		restoreEnv = true
	} else if strings.TrimSpace(req.AgeIdentityFile) != "" {
		warnings = append(warnings, "no secrets.age in bundle; --age-identity ignored")
	}

	envPath := filepath.Join(mem, "env")
	dbPath := filepath.Join(mem, "marble.db")
	dbExists := false
	if fi, err := os.Stat(dbPath); err == nil && fi.Mode().IsRegular() {
		dbExists = true
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	envExists := false
	if _, err := os.Stat(envPath); err == nil {
		envExists = true
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if req.Apply && dbExists && m.DB.Path != "" && !req.ForceDB {
		return nil, fmt.Errorf("destination marble.db exists; refusing to overwrite (move it aside, or pass --force-db)")
	}
	if req.Apply && restoreEnv && envExists && !req.ForceEnv {
		return nil, fmt.Errorf("refusing to overwrite %s (pass --force-env)", envPath)
	}

	cands := 0
	if from != "" {
		cands, err = countMarkdownCandidates(filepath.Join(in, "files"), from)
		if err != nil {
			return nil, err
		}
	}

	lines := describeBundle(in, m)
	if !req.Apply {
		lines = append(lines, fmt.Sprintf("plan: copy marble.db and %d files into %s (pass -apply)", len(m.Files), mem))
		if dbExists && m.DB.Path != "" {
			lines = append(lines, "plan: destination marble.db exists; -apply will refuse without --force-db")
		}
		if restoreEnv {
			lines = append(lines, fmt.Sprintf("plan: would restore env (%d bytes, mode 0600)", len(plain)))
			if envExists && !req.ForceEnv {
				lines = append(lines, "plan: destination env exists; -apply will refuse without --force-env")
			}
		}
		if from != "" {
			lines = append(lines, fmt.Sprintf("rewrite: would update sessions.workspace and sessions.md_path prefix %s -> %s on -apply", from, to))
			lines = append(lines, fmt.Sprintf("markdown candidates: %d", cands))
		}
		return &ImportResult{
			Manifest:           m,
			MarkdownCandidates: cands,
			Lines:              lines,
			Warnings:           warnings,
		}, nil
	}

	if err := os.MkdirAll(mem, 0o755); err != nil {
		return nil, err
	}
	if m.DB.Path != "" {
		if err := placeDB(in, mem, m); err != nil {
			return nil, err
		}
	}
	for _, f := range m.Files {
		if err := placeFile(in, mem, f); err != nil {
			return nil, err
		}
	}
	lines = append(lines, "imported into "+mem)
	lines = append(lines, fmt.Sprintf("copied marble.db and %d files", len(m.Files)))

	if restoreEnv {
		if err := writeSecretEnv(envPath, plain); err != nil {
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("restored env (%d bytes, mode 0600)", len(plain)))
	}

	rows := int64(0)
	if from != "" {
		if m.DB.Path == "" {
			return nil, fmt.Errorf("no database to rewrite")
		}
		rows, err = rewriteWorkspace(dbPath, from, to)
		if err != nil {
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("rewrite: updated %s (%s -> %s)", rowsWord(rows), from, to))
		lines = append(lines, fmt.Sprintf("markdown candidates: %d", cands))
	}

	return &ImportResult{
		Manifest:           m,
		Applied:            true,
		RowsRewritten:      rows,
		MarkdownCandidates: cands,
		Lines:              lines,
		Warnings:           warnings,
	}, nil
}

func refuseNewerSchema(m *Manifest) error {
	v := m.DB.SchemaVersion
	if m.Source.DBSchemaVersion > v {
		v = m.Source.DBSchemaVersion
	}
	if v > db.CurrentSchemaVersion {
		return fmt.Errorf("database schema v%d is newer than this binary (v%d); upgrade the binary first", v, db.CurrentSchemaVersion)
	}
	return nil
}

func refuseLiveLock(memory string) error {
	alive, pid, path, err := liveLock(memory)
	if err != nil {
		return err
	}
	if alive {
		return fmt.Errorf("memory directory locked by live harness (pid %d): %s", pid, path)
	}
	return nil
}

// liveLock matches internal/db/lock.go: a lock is live only when its PID still exists.
func liveLock(memory string) (alive bool, pid int, path string, err error) {
	path = filepath.Join(memory, "marble.lock")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, 0, path, nil
	}
	if err != nil {
		return false, 0, path, err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return false, 0, path, nil
	}
	pid, err = strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return false, 0, path, nil
	}
	return processAlive(pid), pid, path, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks existence. Same rule as internal/db lock.go.
	return p.Signal(syscall.Signal(0)) == nil
}

func verifyBundle(in string, m *Manifest) error {
	seen := map[string]struct{}{}
	for _, f := range m.Files {
		clean, err := cleanFilesPath(f.Path)
		if err != nil {
			return err
		}
		if _, ok := seen[clean]; ok {
			return fmt.Errorf("duplicate manifest path %s", clean)
		}
		seen[clean] = struct{}{}
		if !validSHA(f.SHA256) {
			return fmt.Errorf("checksum: invalid sha256 for %s", clean)
		}
		abs := filepath.Join(in, filepath.FromSlash(clean))
		if err := verifyFile(abs, clean, f.SHA256, f.Bytes); err != nil {
			return err
		}
	}
	if m.DB.Path == "" {
		return nil
	}
	if filepath.Clean(m.DB.Path) != "marble.db" {
		return fmt.Errorf("db path must be marble.db, got %q", m.DB.Path)
	}
	if !validSHA(m.DB.SHA256) {
		return fmt.Errorf("checksum: invalid sha256 for marble.db")
	}
	return verifyFile(filepath.Join(in, "marble.db"), "marble.db", m.DB.SHA256, m.DB.Bytes)
}

func placeDB(in, mem string, m *Manifest) error {
	for _, name := range []string{"marble.db-wal", "marble.db-shm", "marble.db-journal"} {
		if err := removeIfExists(filepath.Join(mem, name)); err != nil {
			return err
		}
	}
	src := filepath.Join(in, "marble.db")
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	dst := filepath.Join(mem, "marble.db")
	sum, n, err := copyFileHash(src, dst, fi.Mode())
	if err != nil {
		return err
	}
	if n != m.DB.Bytes || !strings.EqualFold(sum, m.DB.SHA256) {
		_ = os.Remove(dst)
		return fmt.Errorf("checksum mismatch for marble.db")
	}
	return nil
}

func placeFile(in, mem string, f FileEntry) error {
	clean, err := cleanFilesPath(f.Path)
	if err != nil {
		return err
	}
	rel := strings.TrimPrefix(clean, "files/")
	dst := filepath.Join(mem, filepath.FromSlash(rel))
	relBack, err := filepath.Rel(mem, dst)
	if err != nil || relBack == ".." || strings.HasPrefix(relBack, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("unsafe destination for %s", f.Path)
	}
	mode, err := parseMode(f.Mode)
	if err != nil {
		return err
	}
	src := filepath.Join(in, filepath.FromSlash(clean))
	sum, n, err := copyFileHash(src, dst, mode)
	if err != nil {
		return err
	}
	if n != f.Bytes || !strings.EqualFold(sum, f.SHA256) {
		_ = os.Remove(dst)
		return fmt.Errorf("checksum mismatch for %s", clean)
	}
	return nil
}

func describeBundle(in string, m *Manifest) []string {
	return []string{
		"bundle: " + in,
		"schema: " + m.Schema,
		"created_at: " + m.CreatedAt,
		fmt.Sprintf("source: hostname=%s public_url=%s workspace=%s memory=%s model=%s base_url=%s",
			m.Source.Hostname, m.Source.PublicURL, m.Source.Workspace, m.Source.Memory, m.Source.Model, m.Source.BaseURL),
		"harness: " + m.Source.HarnessVersion,
		fmt.Sprintf("db: %s %d bytes sha256=%s schema v%d", m.DB.Path, m.DB.Bytes, m.DB.SHA256, m.DB.SchemaVersion),
		fmt.Sprintf("files: %d verified", len(m.Files)),
		"secrets: " + m.Secrets,
	}
}

func rowsWord(n int64) string {
	if n == 1 {
		return "1 session row"
	}
	return fmt.Sprintf("%d session rows", n)
}
