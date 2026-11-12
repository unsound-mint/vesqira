package schema

import (
	"path/filepath"
	"testing"
)

// A catalog from before schema 29 upgrades with every audit entry kept and attributed to the local owner, who made all of them.
func TestAuditActorMigrationAttributesExistingEntriesToTheLocalOwner(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE audit(id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, action TEXT NOT NULL, kind TEXT NOT NULL, target INTEGER NOT NULL, detail TEXT NOT NULL)`,
		`INSERT INTO audit(at,action,kind,target,detail) VALUES (1,'host added','host',1,'h')`,
		`PRAGMA user_version = 28`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 29 {
		t.Fatalf("version %d", version(t, db))
	}
	var actor, behalf, via string
	if err := db.QueryRow(`SELECT actor, on_behalf_of, via FROM audit WHERE id=1`).Scan(&actor, &behalf, &via); err != nil || actor != "local-owner" || behalf != "" || via != "local" {
		t.Fatalf("the existing entry: %q %q %q %v", actor, behalf, via, err)
	}
}
