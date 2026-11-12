package schema

import (
	"path/filepath"
	"testing"
)

// A catalog from before schema 30 gains the account tables, empty: a local catalog has no accounts, and upgrading creates none.
func TestAccountsMigrationAddsEmptyTables(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	for _, q := range []string{
		`CREATE TABLE audit(id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, action TEXT NOT NULL, kind TEXT NOT NULL, target INTEGER NOT NULL, detail TEXT NOT NULL, actor TEXT NOT NULL DEFAULT 'local-owner', on_behalf_of TEXT NOT NULL DEFAULT '', via TEXT NOT NULL DEFAULT 'local')`,
		`PRAGMA user_version = 29`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if version(t, db) != Version || Version < 30 {
		t.Fatalf("version %d", version(t, db))
	}
	for _, table := range []string{"accounts", "identities", "password_credentials", "totp_credentials", "recovery_codes", "sessions", "access_tokens", "setup_tokens"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d rows, %v", table, n, err)
		}
	}
}
