package session

import (
	"testing"

	"github.com/rendicott/marble/internal/config"
	"github.com/rendicott/marble/internal/db"
)

func catalogRow(id, kind string, enabled bool) db.ModelCatalogRow {
	now := db.UTCNow()
	return db.ModelCatalogRow{
		ID: id, DisplayName: id, Model: id + "-model", Kind: kind,
		ContextLimit: 10000, MaxOutput: 1000, CapTools: kind != "image",
		Enabled: enabled, CreatedAt: now, UpdatedAt: now,
	}
}

func TestNewSessionUsesAvailableDefaultModel(t *testing.T) {
	root := t.TempDir()
	d, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	chat := catalogRow("grok", "chat", true)
	if err := d.InsertModelCatalog(chat); err != nil {
		t.Fatal(err)
	}
	if err := d.SetDefaultModelID("grok"); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(&Runner{Cfg: config.Config{Model: "proc"}}, nil, d, root, "proc")

	s := reg.Create("New session")
	if s.ModelID != "grok" {
		t.Fatalf("model_id %q want grok", s.ModelID)
	}
	sys := reg.CreateSystem("compact · parent", "parent")
	if sys.ModelID != "" {
		t.Fatalf("system session model_id %q want empty", sys.ModelID)
	}
}

func TestNewSessionFallsBackWhenDefaultUnavailable(t *testing.T) {
	root := t.TempDir()
	d, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	off := catalogRow("old", "chat", true)
	img := catalogRow("pic", "image", true)
	if err := d.InsertModelCatalog(off); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertModelCatalog(img); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(&Runner{Cfg: config.Config{Model: "proc"}}, nil, d, root, "proc")

	if err := d.SetDefaultModelID("missing"); err == nil {
		t.Fatal("missing model should be rejected")
	}
	if err := d.SetDefaultModelID("pic"); err == nil {
		t.Fatal("image model should be rejected")
	}
	if s := reg.Create("plain"); s.ModelID != "" {
		t.Fatalf("unset default pinned %q", s.ModelID)
	}

	if err := d.SetDefaultModelID("old"); err != nil {
		t.Fatal(err)
	}
	off.Enabled = false
	if err := d.UpdateModelCatalog(off); err != nil {
		t.Fatal(err)
	}
	saved, err := d.GetDefaultModelID()
	if err != nil {
		t.Fatal(err)
	}
	if saved != "" {
		t.Fatalf("disabling the default should clear it, still %q", saved)
	}
	if s := reg.Create("after disable"); s.ModelID != "" {
		t.Fatalf("disabled default pinned %q", s.ModelID)
	}

	// A saved id that no longer has a row still falls back.
	if err := d.SetDefaultModelID("old"); err == nil {
		t.Fatal("disabled model should not be saved as the default")
	}
	if err := d.InsertModelCatalog(catalogRow("temp", "chat", true)); err != nil {
		t.Fatal(err)
	}
	if err := d.SetDefaultModelID("temp"); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteModelCatalog("temp"); err != nil {
		t.Fatal(err)
	}
	if saved, _ := d.GetDefaultModelID(); saved != "" {
		t.Fatalf("delete should clear default, still %q", saved)
	}
	if s := reg.Create("after delete"); s.ModelID != "" {
		t.Fatalf("deleted default pinned %q", s.ModelID)
	}

	// A leftover id with no row is unavailable: new sessions use the launch model.
	if _, err := d.SQL.Exec(`
		INSERT INTO settings (key, value, updated_at) VALUES ('default_model_id', 'gone', ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		db.UTCNow(),
	); err != nil {
		t.Fatal(err)
	}
	got, err := d.AvailableDefaultModelID()
	if err != nil || got != "" {
		t.Fatalf("available %q err %v", got, err)
	}
	if s := reg.Create("dangling"); s.ModelID != "" {
		t.Fatalf("dangling default pinned %q", s.ModelID)
	}
}
