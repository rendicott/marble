package db

import (
	"testing"
)

func agentCWDColumn(t *testing.T, d *DB) bool {
	t.Helper()
	var n int
	if err := d.SQL.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'agent_cwd'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestMigrateV12toV13AgentCWD(t *testing.T) {
	root := t.TempDir()
	d, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := UTCNow()
	if err := d.UpsertSession(SessionRow{
		ID: "keep", Title: "t", Status: "active", CreatedAt: now, UpdatedAt: now, AgentCWD: "will-drop",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL.Exec(`ALTER TABLE sessions DROP COLUMN agent_cwd`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL.Exec(`UPDATE schema_meta SET schema_version = 12 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if agentCWDColumn(t, d) {
		t.Fatal("precondition: agent_cwd should be gone")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if d2.Mode != ModeNormal {
		t.Fatalf("mode %s: %s", d2.Mode, d2.Reason)
	}
	if !agentCWDColumn(t, d2) {
		t.Fatal("agent_cwd missing after v13 migration")
	}
	if got := schemaVersion(t, d2); got != CurrentSchemaVersion {
		t.Fatalf("schema_version %d want %d", got, CurrentSchemaVersion)
	}
	rows, err := d2.ListSessions(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "keep" || rows[0].AgentCWD != "" {
		t.Fatalf("migrated rows %+v", rows)
	}
	if err := d2.UpsertSession(SessionRow{
		ID: "keep", Title: "t", Status: "active", CreatedAt: now, UpdatedAt: now, AgentCWD: "projects/app",
	}); err != nil {
		t.Fatal(err)
	}
	rows, err = d2.ListSessions(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].AgentCWD != "projects/app" {
		t.Fatalf("upsert rows %+v", rows)
	}
	if err := d2.migrateV12toV13(); err != nil {
		t.Fatalf("re-running migrateV12toV13: %v", err)
	}
}
