package schema

import (
	"path/filepath"
	"testing"
)

// A catalog from before import rules (schema 17) upgrades with its projects kept and no rule or application record invented;
// the tables are usable and keyed by project and position (a rule list) and by project and path (an application record).
func TestImportRulesMigrationAddsEmptyTables(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE projects(id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO projects(id,name) VALUES (1, 'vgt')`,
		`PRAGMA user_version = 17`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	var projects, rules, applied int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM projects), (SELECT COUNT(*) FROM import_rules), (SELECT COUNT(*) FROM import_applied)`).Scan(&projects, &rules, &applied); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 18 || projects != 1 || rules != 0 || applied != 0 {
		t.Fatalf("version %d, %d projects, %d rules, %d applied", version(t, db), projects, rules, applied)
	}
	if _, err := db.Exec(`INSERT INTO import_rules(project_id,position,name,rule,saved_at) VALUES (1,0,'a','{}',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO import_rules(project_id,position,name,rule,saved_at) VALUES (1,0,'b','{}',1)`); err == nil {
		t.Fatal("two rules at one position were accepted")
	}
	if _, err := db.Exec(`INSERT INTO import_applied(project_id,path,rule,size_bytes,mtime_ns,outcome,applied_at) VALUES (1,'a.fits','a',1,2,'registered',3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO import_applied(project_id,path) VALUES (1,'b.fits')`); err == nil {
		t.Fatal("an application record without its outcome was accepted")
	}
}
