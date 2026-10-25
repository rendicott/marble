// Package state implements ADR-0039 Phase 1 harness export and import.
// Export snapshots SQLite with VACUUM INTO on its own connection. It does not
// call db.Open, so it does not take marble.lock and does not migrate the live
// database.
package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const schemaMarbleStateV1 = "marble-state/v1"

// Manifest is a marble-state/v1 bundle manifest.
type Manifest struct {
	Schema    string      `json:"schema"`
	CreatedAt string      `json:"created_at"`
	Source    SourceInfo  `json:"source"`
	Files     []FileEntry `json:"files"`
	DB        DBEntry     `json:"db"`
	Secrets   string      `json:"secrets"`
	Excludes  []string    `json:"excludes"`
}

// SourceInfo records where a bundle was taken from. Empty strings are left empty.
type SourceInfo struct {
	Hostname        string `json:"hostname"`
	PublicURL       string `json:"public_url"`
	Workspace       string `json:"workspace"`
	Memory          string `json:"memory"`
	HarnessVersion  string `json:"harness_version"`
	DBSchemaVersion int    `json:"db_schema_version"`
	Model           string `json:"model"`
	BaseURL         string `json:"base_url"`
}

// FileEntry is one regular file under files/.
type FileEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Mode   string `json:"mode"`
}

// DBEntry is the VACUUM INTO snapshot at the bundle root.
type DBEntry struct {
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	Bytes         int64  `json:"bytes"`
	SchemaVersion int    `json:"schema_version"`
}

func defaultExcludes() []string {
	return []string{"env", "tts-cache", "marble.lock", "marble.db-wal", "marble.db-shm"}
}

func readManifest(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	if m.Schema != schemaMarbleStateV1 {
		return nil, fmt.Errorf("unsupported bundle schema %q", m.Schema)
	}
	if m.Files == nil {
		m.Files = []FileEntry{}
	}
	return &m, nil
}

func writeManifest(path string, m *Manifest) error {
	if m.Files == nil {
		m.Files = []FileEntry{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func verifyFile(abs, label, wantSHA string, wantBytes int64) error {
	sum, n, err := hashFile(abs)
	if err != nil {
		return fmt.Errorf("checksum: %s: %w", label, err)
	}
	if n != wantBytes || !strings.EqualFold(sum, wantSHA) {
		return fmt.Errorf("checksum mismatch for %s", label)
	}
	return nil
}

func formatMode(m os.FileMode) string {
	return fmt.Sprintf("%04o", m.Perm())
}

func parseMode(s string) (os.FileMode, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0o644, nil
	}
	u, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("mode %q: %w", s, err)
	}
	return os.FileMode(u) & os.ModePerm, nil
}

// cleanFilesPath returns a slash-separated files/... path or an error.
func cleanFilesPath(p string) (string, error) {
	if p == "" || strings.Contains(p, "\x00") || filepath.IsAbs(p) {
		return "", fmt.Errorf("unsafe bundle path %q", p)
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	if !strings.HasPrefix(clean, "files/") {
		return "", fmt.Errorf("file path %q must start with files/", p)
	}
	rest := strings.TrimPrefix(clean, "files/")
	if rest == "" || rest == "." || rest == ".." || strings.HasPrefix(rest, "../") {
		return "", fmt.Errorf("unsafe bundle path %q", p)
	}
	return clean, nil
}

func quoteSQLString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
