package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/fullschema"
	"github.com/unsound-mint/vesqira/services/api/internal/schema"
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
	if err := fullschema.Open(db, dir); err != nil {
		t.Fatal(err)
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

// The snapshot is only as complete as the stores fullschema opens. This fails when main.go opens a store on the catalog
// file that fullschema does not, which is how projects, project_entries and project_answers once went missing.
func TestFullSchemaOpensEveryStoreMainOpens(t *testing.T) {
	mainSrc, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	fullSrc, err := os.ReadFile(filepath.Join("internal", "fullschema", "fullschema.go"))
	if err != nil {
		t.Fatal(err)
	}
	found := regexp.MustCompile(`(\w+)\.Open\(store\.DB\(\)`).FindAllStringSubmatch(string(mainSrc), -1)
	if len(found) < 10 {
		t.Fatalf("found only %d store opens in main.go: the pattern needs updating", len(found))
	}
	for _, m := range found {
		if !strings.Contains(string(fullSrc), m[1]+".Open(db") {
			t.Errorf("main.go opens %s on the catalog but internal/fullschema does not: the schema snapshot would miss its tables", m[1])
		}
	}
}
