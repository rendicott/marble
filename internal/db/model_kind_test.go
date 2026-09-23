package db

import (
	"testing"
)

// v9ModelCatalogDDL is the model_catalog table shape before schema v10 added `kind`.
const v9ModelCatalogDDL = `CREATE TABLE model_catalog (
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
)`

func kindColumnExists(t *testing.T, d *DB) bool {
	t.Helper()
	rows, err := d.SQL.Query(`SELECT name FROM pragma_table_info('model_catalog') WHERE name = 'kind'`)
	if err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	defer rows.Close()
	return rows.Next()
}

func schemaVersion(t *testing.T, d *DB) int {
	t.Helper()
	ver, err := d.readSchemaVersion()
	if err != nil {
		t.Fatalf("readSchemaVersion: %v", err)
	}
	return ver
}

// TestMigrateV9toV10AddsKind simulates a v9 database (model_catalog without the
// kind column, rows already present) and asserts the v10 migration adds the
// column, defaults every existing row to chat, and bumps schema_version.
func TestMigrateV9toV10AddsKind(t *testing.T) {
	root := t.TempDir()

	d, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := UTCNow()
	if _, err := d.SQL.Exec(`INSERT INTO model_catalog (
		id, display_name, model, base_url, api_key_env,
		cost_notes, cap_reasoning, cap_images, cap_voice, cap_tools,
		context_limit, max_output, context_reserve, enabled, sort_order, notes,
		created_at, updated_at, kind
	) VALUES ('legacy-chat','Legacy chat','qwen3.6-35b','http://x/v1','none',
		'',1,1,0,1,262144,32768,8192,1,0,'',?,?, 'chat')`, now, now); err != nil {
		t.Fatal(err)
	}

	// Downgrade to the pre-v10 shape.
	if _, err := d.SQL.Exec(`DROP TABLE model_catalog`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL.Exec(v9ModelCatalogDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL.Exec(`INSERT INTO model_catalog (
		id, display_name, model, base_url, api_key_env,
		cost_notes, cap_reasoning, cap_images, cap_voice, cap_tools,
		context_limit, max_output, context_reserve, enabled, sort_order, notes,
		created_at, updated_at
	) VALUES ('legacy-chat','Legacy chat','qwen3.6-35b','http://x/v1','none',
		'',1,1,0,1,262144,32768,8192,1,0,'',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL.Exec(`UPDATE schema_meta SET schema_version = 9 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if kindColumnExists(t, d) {
		t.Fatal("precondition: kind column should be gone")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen: Open must walk v9 → v10.
	d2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if d2.Mode != ModeNormal {
		t.Fatalf("mode %s: %s", d2.Mode, d2.Reason)
	}
	if !kindColumnExists(t, d2) {
		t.Fatal("kind column missing after v10 migration")
	}
	if got := schemaVersion(t, d2); got != CurrentSchemaVersion {
		t.Fatalf("schema_version %d want %d", got, CurrentSchemaVersion)
	}
	row, err := d2.GetModelCatalog("legacy-chat")
	if err != nil {
		t.Fatal(err)
	}
	if row.Kind != "chat" {
		t.Fatalf("legacy row kind %q want chat", row.Kind)
	}
}

// TestMigrateV9toV10Idempotent covers the fresh-DB path, where migrateV2toV3
// already created the column and the v9→v10 step must not fail on the ALTER.
func TestMigrateV9toV10Idempotent(t *testing.T) {
	root := t.TempDir()
	d, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if !kindColumnExists(t, d) {
		t.Fatal("fresh DB missing kind column")
	}
	if got := schemaVersion(t, d); got != CurrentSchemaVersion {
		t.Fatalf("fresh schema_version %d want %d", got, CurrentSchemaVersion)
	}
	// Running the migrator again must be a no-op, not an error.
	if err := d.migrateV9toV10(); err != nil {
		t.Fatalf("re-running migrateV9toV10: %v", err)
	}
}

func TestNormalizeModelKind(t *testing.T) {
	cases := map[string]string{
		"":        "chat",
		"chat":    "chat",
		"CHAT":    "chat",
		" Chat ":  "chat",
		"image":   "image",
		"IMAGE":   "image",
		" Image ": "image",
		"bogus":   "bogus",
	}
	for in, want := range cases {
		if got := NormalizeModelKind(in); got != want {
			t.Fatalf("NormalizeModelKind(%q) = %q want %q", in, got, want)
		}
	}
}

func validKindRow(kind string) ModelCatalogRow {
	return ModelCatalogRow{
		ID:           "img-1",
		DisplayName:  "Image 1",
		Model:        "gpt-image-2.5-sunburst",
		Kind:         kind,
		BaseURL:      "https://api.openai.com/v1",
		APIKeyEnv:    "OPENAI_API_KEY",
		ContextLimit: 131072,
		MaxOutput:    8192,
		Enabled:      true,
		CreatedAt:    UTCNow(),
		UpdatedAt:    UTCNow(),
	}
}

func TestValidateModelCatalogKind(t *testing.T) {
	r := validKindRow("image")
	if err := ValidateModelCatalog(&r, 8192); err != nil {
		t.Fatalf("image kind rejected: %v", err)
	}
	// Empty normalises to chat.
	r2 := validKindRow("")
	if err := ValidateModelCatalog(&r2, 8192); err != nil {
		t.Fatal(err)
	}
	if r2.Kind != "chat" {
		t.Fatalf("empty kind normalised to %q want chat", r2.Kind)
	}
	// Anything else is rejected.
	r3 := validKindRow("video")
	err := ValidateModelCatalog(&r3, 8192)
	if err == nil || err.Error() != "kind must be chat or image" {
		t.Fatalf("bogus kind err %v", err)
	}
}

func TestModelCatalogKindRoundTrip(t *testing.T) {
	root := t.TempDir()
	d, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	img := validKindRow("image")
	img.CapTools = false
	if err := ValidateModelCatalog(&img, 8192); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertModelCatalog(img); err != nil {
		t.Fatal(err)
	}

	got, err := d.GetModelCatalog("img-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "image" {
		t.Fatalf("kind %q want image", got.Kind)
	}
	if got.CapTools {
		t.Fatal("cap_tools should stay false for the image row")
	}

	list, err := d.ListModelCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Kind != "image" {
		t.Fatalf("list kind %+v", list)
	}

	// Update to a chat row and confirm the column is written, not just defaulted.
	upd := img
	upd.Kind = "chat"
	upd.CapTools = true
	if err := d.UpdateModelCatalog(upd); err != nil {
		t.Fatal(err)
	}
	got2, err := d.GetModelCatalog("img-1")
	if err != nil {
		t.Fatal(err)
	}
	if got2.Kind != "chat" {
		t.Fatalf("after update kind %q want chat", got2.Kind)
	}
}
