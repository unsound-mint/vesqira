package schema

import (
	"path/filepath"
	"testing"
)

// A schema 15 catalog gains the two VPN command columns; existing hosts keep every field and have no VPN command.
func TestHostVPNMigrationKeepsHostsAndAddsEmptyCommands(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE hosts(id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, dest TEXT NOT NULL, python TEXT NOT NULL, roots TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`INSERT INTO hosts(name,dest,python,roots,created_at) VALUES ('login1','login1','python3','["/d"]',1)`,
		`PRAGMA user_version = 15`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	var name, vc, vd string
	if err := db.QueryRow(`SELECT name, vpn_command, vpn_disconnect FROM hosts`).Scan(&name, &vc, &vd); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 17 || name != "login1" || vc != "" || vd != "" {
		t.Fatalf("version %d, %q %q %q", version(t, db), name, vc, vd)
	}
}
