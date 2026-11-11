package schema

import (
	"path/filepath"
	"testing"
)

// A catalog from before interpretation tiers (schema 15) upgrades with every declaration kept and given the tier its author
// implies: the researcher's word (declared_by 'user') is `declared`; a rule's proposal is `inferred`, named by declared_by, with
// mapping version "1". Nothing existing becomes `verified`: no row was ever checked. The masks table is created empty.
func TestTierMigrationMapsDeclaredByFaithfully(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE datasets(id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL UNIQUE, name TEXT NOT NULL, size_bytes INTEGER NOT NULL,
			modified_at INTEGER NOT NULL, added_at INTEGER NOT NULL, header TEXT NOT NULL, host_id INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header) VALUES ('/a','a',1,1,1,'{}'),('/b','b',1,1,1,'{}'),('/c','c',1,1,1,'{}'),('/d','d',1,1,1,'{}')`,
		`CREATE TABLE interpretations(dataset_id INTEGER PRIMARY KEY, kind TEXT NOT NULL, unit TEXT NOT NULL, reliability_id INTEGER, declared_at INTEGER NOT NULL,
			size_bytes INTEGER NOT NULL DEFAULT 0, modified_ns INTEGER NOT NULL DEFAULT 0, declared_by TEXT NOT NULL DEFAULT 'user', frame TEXT, quantity TEXT)`,
		`INSERT INTO interpretations(dataset_id,kind,unit,declared_at,declared_by) VALUES (1,'axial_orientation','deg',1,'user'),(2,'axial_orientation','rad',1,'vgt-run-adapter'),(3,'axial_orientation','deg',1,'vesqira-analysis')`,
		`CREATE TABLE dataset_units(dataset_id INTEGER PRIMARY KEY, unit TEXT NOT NULL, declared_by TEXT NOT NULL DEFAULT 'user', declared_at INTEGER NOT NULL,
			size_bytes INTEGER NOT NULL DEFAULT 0, modified_ns INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO dataset_units(dataset_id,unit,declared_by,declared_at) VALUES (1,'K','user',1),(4,'K km/s','rohsa-run-adapter',1)`,
		`CREATE TABLE catalog_declarations(dataset_id INTEGER PRIMARY KEY, declaration TEXT NOT NULL, declared_by TEXT NOT NULL DEFAULT 'user', declared_at INTEGER NOT NULL,
			size_bytes INTEGER NOT NULL DEFAULT 0, modified_ns INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO catalog_declarations(dataset_id,declaration,declared_at) VALUES (2,'{}',1)`,
		`PRAGMA user_version = 15`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 18 {
		t.Fatalf("version %d", version(t, db))
	}
	type row struct{ tier, mapping string }
	check := func(table string, want map[int64]row) {
		t.Helper()
		rows, err := db.Query(`SELECT dataset_id,tier,mapping_version FROM ` + table + ` ORDER BY dataset_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[int64]row{}
		for rows.Next() {
			var id int64
			var r row
			if err := rows.Scan(&id, &r.tier, &r.mapping); err != nil {
				t.Fatal(err)
			}
			got[id] = r
		}
		if len(got) != len(want) {
			t.Fatalf("%s: %v, want %v", table, got, want)
		}
		for id, w := range want {
			if got[id] != w {
				t.Errorf("%s dataset %d: %+v, want %+v", table, id, got[id], w)
			}
		}
	}
	check("interpretations", map[int64]row{1: {"declared", ""}, 2: {"inferred", "1"}, 3: {"inferred", "1"}})
	check("dataset_units", map[int64]row{1: {"declared", ""}, 4: {"inferred", "1"}})
	check("catalog_declarations", map[int64]row{2: {"declared", ""}})
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM masks`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("masks table: %d %v", n, err)
	}
	// the rule that proposed a row stays readable next to its tier
	var by string
	if err := db.QueryRow(`SELECT declared_by FROM interpretations WHERE dataset_id=2`).Scan(&by); err != nil || by != "vgt-run-adapter" {
		t.Fatalf("declared_by %q %v", by, err)
	}
}
