package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/notify"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/schema"
	"github.com/unsound-mint/vesqira/services/api/internal/sweeps"
)

// Tables are created idempotently by the package that owns them, so nothing else forces a column change to be
// accompanied by a migration. This snapshot does: the catalog a fresh install builds must match the file recorded for
// schema.Version. Changing a table makes it fail; the fix is to add a migration for existing catalogs, bump
// schema.Version, and record the new snapshot with UPDATE_SCHEMA_SNAPSHOT=1 go test -run TestCatalogSchemaSnapshot.
func TestCatalogSchemaSnapshot(t *testing.T) {
	dir := t.TempDir()
	st, err := catalog.Open(filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := st.DB()
	for name, open := range map[string]func() error{
		"boards":  func() error { _, e := boards.Open(db, dir); return e },
		"runs":    func() error { _, e := runs.Open(db); return e },
		"hosts":   func() error { _, e := hosts.Open(db); return e },
		"jobs":    func() error { _, e := jobs.Open(db); return e },
		"sweeps":  func() error { _, e := sweeps.Open(db); return e },
		"audit":   func() error { _, e := audit.Open(db); return e },
		"notify":  func() error { _, e := notify.Open(db); return e },
		"regions": func() error { _, e := regions.Open(db); return e },
		"derived": func() error { _, e := derived.Open(db); return e },
		"reports": func() error { _, e := reports.Open(db, dir); return e },
	} {
		if err := open(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	rows, err := db.Query(`SELECT type, name, sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var typ, name, sql string
		if err := rows.Scan(&typ, &name, &sql); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "-- %s %s\n%s;\n\n", typ, name, strings.Join(strings.Fields(sql), " "))
	}
	got := b.String()
	path := filepath.Join("testdata", fmt.Sprintf("schema_v%d.sql", schema.Version))
	if os.Getenv("UPDATE_SCHEMA_SNAPSHOT") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no snapshot for schema version %d (%v): record it with UPDATE_SCHEMA_SNAPSHOT=1 after adding the migration", schema.Version, err)
	}
	if got != string(want) {
		t.Errorf("the catalog schema changed without a new schema.Version.\nAdd a migration for existing catalogs, bump schema.Version in internal/schema, and re-record the snapshot (see the comment on this test).\n--- recorded\n%s\n--- now\n%s", want, got)
	}
}
