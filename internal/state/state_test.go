package state_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/state"
	_ "modernc.org/sqlite"
)

func openMem(t *testing.T) (string, *db.DB) {
	t.Helper()
	mem := t.TempDir()
	d, err := db.Open(mem)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return mem, d
}

func writeFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func fileSum(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func mustExport(t *testing.T, req state.ExportRequest) *state.ExportResult {
	t.Helper()
	res, err := state.Export(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func openPlain(t *testing.T, path string) *sql.DB {
	t.Helper()
	sq, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sq.Close() })
	if err := sq.Ping(); err != nil {
		t.Fatal(err)
	}
	return sq
}

func TestRoundTrip(t *testing.T) {
	mem, d := openMem(t)
	now := db.UTCNow()
	if err := d.UpsertSession(db.SessionRow{
		ID: "sessport1", Title: "portability", Status: "active",
		CreatedAt: now, UpdatedAt: now,
		Workspace: "/tmp/ws-round", Model: "test-model",
		MDPath: "session/sessport1.md", MessageCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSettings(map[string]string{"blob_max_age_days": "9"}); err != nil {
		t.Fatal(err)
	}
	unix := time.Now().Unix()
	if _, err := d.SQL.Exec(`INSERT INTO computer_pairings
		(id, h_code, p_code, device_id, device_token_plain, os, caps_json, status, created_at, expires_at)
		VALUES ('pair1', 'H123', '', '', 'sekrit-token', '', '{}', 'joined', ?, ?)`, unix, unix+3600); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(mem, "session", "sessport1.md"), []byte("hello session\n"), 0o644)
	writeFile(t, filepath.Join(mem, "knowledge", "x.md"), []byte("knowledge body\n"), 0o640)
	writeFile(t, filepath.Join(mem, "mpub", "demo", "meta.json"), []byte(`{"slug":"demo"}`+"\n"), 0o644)
	writeFile(t, filepath.Join(mem, "mpub", "demo", "content.md"), []byte("# demo\n"), 0o644)
	writeFile(t, filepath.Join(mem, "mpub", "demo", "assets", "dot.bin"), []byte{0x00, 0x01, 0xff}, 0o644)
	writeFile(t, filepath.Join(mem, "attachments", "a1", "blob.bin"), []byte("attach-bytes"), 0o644)
	writeFile(t, filepath.Join(mem, "backups", "overage", "pre.md"), []byte("preimage\n"), 0o644)
	writeFile(t, filepath.Join(mem, "soul.md"), []byte("soul\n"), 0o644)
	writeFile(t, filepath.Join(mem, "tts-cache", "skip.bin"), []byte("rebuildable"), 0o644)
	writeFile(t, filepath.Join(mem, "notes-not-bundled.txt"), []byte("stray"), 0o644)

	// Export while this process still holds marble.lock, the way a live harness does.
	out := t.TempDir()
	res := mustExport(t, state.ExportRequest{
		MemoryDir:      mem,
		OutDir:         out,
		PublicURL:      "http://example.test",
		Workspace:      "/tmp/ws-round",
		Model:          "qwen-test",
		BaseURL:        "http://model.test/v1",
		HarnessVersion: "test-harness",
	})
	var plain string
	if err := d.SQL.QueryRow(`SELECT device_token_plain FROM computer_pairings WHERE id='pair1'`).Scan(&plain); err != nil {
		t.Fatal(err)
	}
	if plain != "sekrit-token" {
		t.Fatalf("source plaintext changed: %q", plain)
	}

	raw, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed state.Manifest
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Schema != "marble-state/v1" {
		t.Fatalf("schema %q", parsed.Schema)
	}
	if _, err := time.Parse(time.RFC3339, parsed.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(parsed.CreatedAt, "Z") {
		t.Fatalf("created_at %q", parsed.CreatedAt)
	}
	if parsed.Source.Hostname == "" || parsed.Source.Memory != mem {
		t.Fatalf("source %+v", parsed.Source)
	}
	if parsed.Source.PublicURL != "http://example.test" || parsed.Source.Workspace != "/tmp/ws-round" ||
		parsed.Source.Model != "qwen-test" || parsed.Source.BaseURL != "http://model.test/v1" ||
		parsed.Source.HarnessVersion != "test-harness" {
		t.Fatalf("source %+v", parsed.Source)
	}
	if parsed.Source.DBSchemaVersion != db.CurrentSchemaVersion || parsed.DB.SchemaVersion != db.CurrentSchemaVersion {
		t.Fatalf("schema source %d db %d", parsed.Source.DBSchemaVersion, parsed.DB.SchemaVersion)
	}
	if parsed.Secrets != "omitted" {
		t.Fatalf("secrets %q", parsed.Secrets)
	}
	wantEx := []string{"env", "tts-cache", "marble.lock", "marble.db-wal", "marble.db-shm"}
	if strings.Join(parsed.Excludes, ",") != strings.Join(wantEx, ",") {
		t.Fatalf("excludes %v", parsed.Excludes)
	}
	wantFiles := map[string]bool{
		"files/session/sessport1.md":     true,
		"files/knowledge/x.md":           true,
		"files/mpub/demo/meta.json":      true,
		"files/mpub/demo/content.md":     true,
		"files/mpub/demo/assets/dot.bin": true,
		"files/attachments/a1/blob.bin":  true,
		"files/backups/overage/pre.md":   true,
		"files/soul.md":                  true,
	}
	if len(parsed.Files) != len(wantFiles) {
		var got []string
		for _, f := range parsed.Files {
			got = append(got, f.Path)
		}
		t.Fatalf("files %v", got)
	}
	for _, f := range parsed.Files {
		if !wantFiles[f.Path] {
			t.Fatalf("unexpected %s", f.Path)
		}
		if strings.Contains(f.Path, "tts-cache") || strings.Contains(f.Path, "marble.lock") || strings.Contains(f.Path, "marble.db") {
			t.Fatalf("bundled excluded path %s", f.Path)
		}
		if f.Path == "files/knowledge/x.md" && f.Mode != "0640" {
			t.Fatalf("mode %s", f.Mode)
		}
		sum := fileSum(t, filepath.Join(out, filepath.FromSlash(f.Path)))
		if !strings.EqualFold(sum, f.SHA256) {
			t.Fatalf("bundle hash %s", f.Path)
		}
	}
	if fileSum(t, filepath.Join(out, "marble.db")) != parsed.DB.SHA256 {
		t.Fatal("bundle db sha mismatch")
	}
	if res.Manifest == nil || res.Manifest.DB.SHA256 != parsed.DB.SHA256 {
		t.Fatal("result manifest sha")
	}
	ents, _ := os.ReadDir(out)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".marble-snap-") {
			t.Fatalf("left snapshot dir %s", e.Name())
		}
	}

	dest := t.TempDir()
	plan, err := state.Import(state.ImportRequest{MemoryDir: dest, InDir: out})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applied {
		t.Fatal("dry run applied")
	}
	if ents, _ := os.ReadDir(dest); len(ents) != 0 {
		t.Fatalf("dry run wrote %v", ents)
	}
	if _, err := state.Import(state.ImportRequest{MemoryDir: dest, InDir: out, Apply: true}); err != nil {
		t.Fatal(err)
	}
	if fileSum(t, filepath.Join(dest, "marble.db")) != parsed.DB.SHA256 {
		t.Fatal("imported db sha mismatch")
	}
	for _, f := range parsed.Files {
		rel := strings.TrimPrefix(f.Path, "files/")
		p := filepath.Join(dest, filepath.FromSlash(rel))
		if fileSum(t, p) != f.SHA256 {
			t.Fatalf("imported hash %s", rel)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if f.Path == "files/knowledge/x.md" && fi.Mode().Perm() != 0o640 {
			t.Fatalf("imported mode %o", fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "marble.lock")); !os.IsNotExist(err) {
		t.Fatal("import created marble.lock")
	}
	if _, err := os.Stat(filepath.Join(dest, "tts-cache", "skip.bin")); !os.IsNotExist(err) {
		t.Fatal("tts-cache copied")
	}
	if _, err := os.Stat(filepath.Join(dest, "notes-not-bundled.txt")); !os.IsNotExist(err) {
		t.Fatal("stray file copied")
	}

	sq := openPlain(t, filepath.Join(dest, "marble.db"))
	var ws, md, val, token string
	if err := sq.QueryRow(`SELECT workspace, md_path FROM sessions WHERE id='sessport1'`).Scan(&ws, &md); err != nil {
		t.Fatal(err)
	}
	if ws != "/tmp/ws-round" || md != "session/sessport1.md" {
		t.Fatalf("session %s %s", ws, md)
	}
	if err := sq.QueryRow(`SELECT value FROM settings WHERE key='blob_max_age_days'`).Scan(&val); err != nil {
		t.Fatal(err)
	}
	if val != "9" {
		t.Fatalf("setting %q", val)
	}
	if err := sq.QueryRow(`SELECT device_token_plain FROM computer_pairings WHERE id='pair1'`).Scan(&token); err != nil {
		t.Fatal(err)
	}
	if token != "" {
		t.Fatalf("snapshot kept device_token_plain %q", token)
	}
}

func TestSecretsOmitAndAge(t *testing.T) {
	mem, d := openMem(t)
	env := []byte("TEST_STATE_KEY=fixture-not-a-real-secret\nOTHER=two\n# comment\n")
	writeFile(t, filepath.Join(mem, "env"), env, 0o600)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	outOmit := t.TempDir()
	res := mustExport(t, state.ExportRequest{MemoryDir: mem, OutDir: outOmit})
	if res.Manifest.Secrets != "omitted" {
		t.Fatalf("secrets %q", res.Manifest.Secrets)
	}
	if _, err := os.Stat(filepath.Join(outOmit, "secrets.age")); !os.IsNotExist(err) {
		t.Fatal("secrets.age written for omit")
	}
	if _, err := os.Stat(filepath.Join(outOmit, "files", "env")); !os.IsNotExist(err) {
		t.Fatal("env copied")
	}
	blob := strings.Join(res.Lines, "\n") + string(mustRead(t, filepath.Join(outOmit, "manifest.json")))
	if strings.Contains(blob, "fixture-not-a-real-secret") {
		t.Fatal("secret value leaked")
	}
	if !strings.Contains(blob, "OTHER") || !strings.Contains(blob, "TEST_STATE_KEY") {
		t.Fatalf("names missing: %s", blob)
	}
	if _, err := state.Export(state.ExportRequest{MemoryDir: mem, OutDir: t.TempDir(), Secrets: "include"}); err == nil {
		t.Fatal("include should fail")
	}
	if _, err := state.Export(state.ExportRequest{MemoryDir: mem, OutDir: t.TempDir(), Secrets: "age"}); err == nil {
		t.Fatal("age without recipient should fail")
	}

	ident, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	identPath := filepath.Join(t.TempDir(), "identity.txt")
	writeFile(t, identPath, []byte(ident.String()+"\n"), 0o600)
	outAge := t.TempDir()
	aged := mustExport(t, state.ExportRequest{
		MemoryDir:     mem,
		OutDir:        outAge,
		Secrets:       "age",
		AgeRecipients: []string{ident.Recipient().String()},
	})
	if aged.Manifest.Secrets != "age" {
		t.Fatalf("secrets %q", aged.Manifest.Secrets)
	}
	fi, err := os.Stat(filepath.Join(outAge, "secrets.age"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("secrets.age mode %o", fi.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(outAge, "files", "env")); !os.IsNotExist(err) {
		t.Fatal("env copied alongside age")
	}

	dest := t.TempDir()
	warnRes, err := state.Import(state.ImportRequest{MemoryDir: dest, InDir: outAge, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnRes.Warnings) == 0 {
		t.Fatal("expected warning without identity")
	}
	if _, err := os.Stat(filepath.Join(dest, "env")); !os.IsNotExist(err) {
		t.Fatal("env written without identity")
	}

	dest = t.TempDir()
	if _, err := state.Import(state.ImportRequest{
		MemoryDir: dest, InDir: outAge, Apply: true, AgeIdentityFile: identPath,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "env"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, env) {
		t.Fatal("env bytes differ")
	}
	fi, err = os.Stat(filepath.Join(dest, "env"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("env mode %o", fi.Mode().Perm())
	}

	if err := os.Remove(filepath.Join(dest, "marble.db")); err != nil {
		t.Fatal(err)
	}
	_, err = state.Import(state.ImportRequest{
		MemoryDir: dest, InDir: outAge, Apply: true, AgeIdentityFile: identPath,
	})
	if err == nil || !strings.Contains(err.Error(), "force-env") {
		t.Fatalf("got %v", err)
	}
	got, err = os.ReadFile(filepath.Join(dest, "env"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, env) {
		t.Fatal("env changed after refused overwrite")
	}
}

func TestRewriteWorkspace(t *testing.T) {
	mem, d := openMem(t)
	now := db.UTCNow()
	if err := d.UpsertSession(db.SessionRow{
		ID: "s1", Title: "a", Status: "active", CreatedAt: now, UpdatedAt: now,
		Workspace: "/home/a", MDPath: "/home/a/.marble/session/s1.md", AgentCWD: "proj",
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertSession(db.SessionRow{
		ID: "s2", Title: "ab", Status: "active", CreatedAt: now, UpdatedAt: now,
		Workspace: "/home/ab", MDPath: "/home/ab/session/s2.md", AgentCWD: "keep",
	}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(mem, "session", "s1.md"), []byte("notes /home/a/proj\n"), 0o644)
	writeFile(t, filepath.Join(mem, "session", "s2.md"), []byte("other home\n"), 0o644)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	mustExport(t, state.ExportRequest{MemoryDir: mem, OutDir: out})
	dest := t.TempDir()
	res, err := state.Import(state.ImportRequest{
		MemoryDir: dest, InDir: out, Apply: true,
		RewriteFrom: "/home/a", RewriteTo: "/home/b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RowsRewritten != 1 {
		t.Fatalf("rows %d", res.RowsRewritten)
	}
	if res.MarkdownCandidates != 1 {
		t.Fatalf("candidates %d", res.MarkdownCandidates)
	}
	body, err := os.ReadFile(filepath.Join(dest, "session", "s1.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("/home/a/proj")) {
		t.Fatalf("markdown rewritten: %s", body)
	}
	sq := openPlain(t, filepath.Join(dest, "marble.db"))
	var ws, md, cwd string
	if err := sq.QueryRow(`SELECT workspace, md_path, agent_cwd FROM sessions WHERE id='s1'`).Scan(&ws, &md, &cwd); err != nil {
		t.Fatal(err)
	}
	if ws != "/home/b" || md != "/home/b/.marble/session/s1.md" || cwd != "proj" {
		t.Fatalf("s1 %s %s %s", ws, md, cwd)
	}
	if err := sq.QueryRow(`SELECT workspace, md_path, agent_cwd FROM sessions WHERE id='s2'`).Scan(&ws, &md, &cwd); err != nil {
		t.Fatal(err)
	}
	if ws != "/home/ab" || md != "/home/ab/session/s2.md" || cwd != "keep" {
		t.Fatalf("s2 %s %s %s", ws, md, cwd)
	}
}

func TestSchemaSkewRefused(t *testing.T) {
	mem, d := openMem(t)
	writeFile(t, filepath.Join(mem, "knowledge", "x.md"), []byte("x\n"), 0o644)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	mustExport(t, state.ExportRequest{MemoryDir: mem, OutDir: out})
	path := filepath.Join(out, "manifest.json")
	var m state.Manifest
	if err := json.Unmarshal(mustRead(t, path), &m); err != nil {
		t.Fatal(err)
	}
	m.DB.SchemaVersion = 9999
	m.Source.DBSchemaVersion = 9999
	b, err := json.MarshalIndent(&m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	_, err = state.Import(state.ImportRequest{MemoryDir: dest, InDir: out, Apply: true})
	if err == nil || !strings.Contains(err.Error(), "upgrade the binary first") {
		t.Fatalf("got %v", err)
	}
	if ents, _ := os.ReadDir(dest); len(ents) != 0 {
		t.Fatalf("copied: %v", ents)
	}
}

func TestLiveLockRefused(t *testing.T) {
	mem, d := openMem(t)
	writeFile(t, filepath.Join(mem, "knowledge", "x.md"), []byte("x\n"), 0o644)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	mustExport(t, state.ExportRequest{MemoryDir: mem, OutDir: out})
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "marble.lock"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := state.Import(state.ImportRequest{MemoryDir: dest, InDir: out, Apply: true})
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "marble.db")); !os.IsNotExist(err) {
		t.Fatal("marble.db copied despite live lock")
	}
}

func TestTamperRefused(t *testing.T) {
	mem, d := openMem(t)
	writeFile(t, filepath.Join(mem, "knowledge", "x.md"), []byte("knowledge\n"), 0o644)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	mustExport(t, state.ExportRequest{MemoryDir: mem, OutDir: out})
	p := filepath.Join(out, "files", "knowledge", "x.md")
	b := mustRead(t, p)
	b[0] ^= 0xff
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	_, err := state.Import(state.ImportRequest{MemoryDir: dest, InDir: out, Apply: true})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "checksum") {
		t.Fatalf("got %v", err)
	}
	if ents, _ := os.ReadDir(dest); len(ents) != 0 {
		t.Fatalf("partial copy: %v", ents)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
