package schema

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// v30 builds a catalog exactly as schema 30 left it, from the recorded snapshot, with foreign keys on as the program opens it.
func v30(t *testing.T) (*sql.DB, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "schema_v30.sql"))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "c.db")
	db, err := sql.Open("sqlite", "file:"+p+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	// the snapshot joins each statement onto one line, so the one SQL comment in it would swallow the rest of its table
	text := strings.ReplaceAll(string(raw), "-- Unix nanoseconds ", "")
	var tables, rest []string // the snapshot lists indexes before tables
	for _, stmt := range strings.Split(text, ";\n\n") {
		var keep []string
		for _, line := range strings.Split(stmt, "\n") {
			if !strings.HasPrefix(line, "-- ") {
				keep = append(keep, line)
			}
		}
		if q := strings.TrimSpace(strings.Join(keep, "\n")); strings.HasPrefix(q, "CREATE TABLE") {
			tables = append(tables, q)
		} else if q != "" {
			rest = append(rest, q)
		}
	}
	for _, q := range append(tables, rest...) {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	if _, err := db.Exec(`PRAGMA user_version = 30`); err != nil {
		t.Fatal(err)
	}
	return db, p
}

func exec(t *testing.T, db *sql.DB, qs ...string) {
	t.Helper()
	for _, q := range qs {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	return n
}

// Schema 31 rebuilds datasets (and seven other tables) so a path is unique per workspace. Seven tables reference datasets with ON
// DELETE CASCADE: with foreign keys on, dropping the old table would delete every row that hangs off a dataset. The migration runs
// with them off, so every child row survives, the ids and the AUTOINCREMENT counter are kept, every existing row lands in
// workspace 1 written by local-owner, read state moves to per-account rows, and a slice 2 account keeps its access as an Owner.
func TestWorkspacesMigrationKeepsEveryRowAndEveryChildOfADataset(t *testing.T) {
	db, p := v30(t)
	exec(t, db,
		`INSERT INTO datasets(id,path,name,size_bytes,modified_at,added_at,header,host_id) VALUES(1,'/d/a.fits','a',1,1,1,'{}',0),(2,'/d/b.fits','b',1,1,1,'{}',0),(3,'/d/gone.fits','g',1,1,1,'{}',0)`,
		`DELETE FROM datasets WHERE id=3`, // the counter stays at 3
		`INSERT INTO interpretations(dataset_id,kind,unit,declared_at) VALUES(1,'axial','deg',1)`,
		`INSERT INTO dataset_units(dataset_id,unit,declared_at) VALUES(1,'K',1)`,
		`INSERT INTO catalog_declarations(dataset_id,declaration,declared_at) VALUES(2,'{}',1)`,
		`INSERT INTO dataset_locations(dataset_id,host_id,path,is_primary,size_bytes,modified_at,added_at) VALUES(1,0,'/d/a.fits',1,1,1,1),(2,0,'/d/b.fits',1,1,1,1)`,
		`INSERT INTO dataset_corrections(dataset_id,action,state,changes,reason,author,created_at,size_bytes,modified_ns) VALUES(1,'set','{}','{}','r','me',1,1,1)`,
		`INSERT INTO masks(dataset_id,type,name,path,width,height,channels,flagged,total,tier,method,rule,author,created_at,source_size,source_mtime,file_size,file_mtime,sha256) VALUES(1,'flag','m','/m/1.fits',1,1,1,0,1,'declared','x','r','me',1,1,1,1,1,'s')`,
		`INSERT INTO subsets(id,source_id,created_at) VALUES(1,2,1)`,
		`INSERT INTO subset_versions(subset_id,version,kind,name,reason,definition,source_size,source_mtime_ns,author,created_at) VALUES(1,1,'window','s','r','{}',1,1,'me',1)`,
		`INSERT INTO reports(id,key,title,body,revision,created_at,updated_at,seen_revision) VALUES(1,'k1','Weekly','b',3,1,1,2),(2,'k2','Other','b',1,1,1,0)`,
		`INSERT INTO notifications(at,kind,title,body,read,dedupe) VALUES(1,'job','t','b',1,'d1'),(2,'job','t','b',0,'d2')`,
		`INSERT INTO boards(title,created_at,updated_at) VALUES('B',1,1)`,
		`INSERT INTO accounts(id,name,admin,created_at) VALUES('acct-1','Ada',1,1)`,
		`INSERT INTO attachments(sha,mime,size,created_at) VALUES('abc','image/png',3,1)`,
	)
	if err := Guard(db, "file:"+p); err != nil {
		t.Fatal(err)
	}
	if v := version(t, db); v != Version || Version < 31 {
		t.Fatalf("version %d", v)
	}
	for table, want := range map[string]int{
		"datasets": 2, "interpretations": 1, "dataset_units": 1, "catalog_declarations": 1, "dataset_locations": 2,
		"dataset_corrections": 1, "masks": 1, "subsets": 1, "subset_versions": 1, "reports": 2, "attachments": 1,
	} {
		if n := count(t, db, `SELECT COUNT(*) FROM `+table); n != want {
			t.Errorf("%s: %d rows after the migration, want %d", table, n, want)
		}
	}
	var on int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&on); err != nil || on != 1 {
		t.Errorf("foreign keys are %d after the migration (%v), want them back on", on, err)
	}
	for _, table := range []string{"datasets", "dataset_locations", "figures", "reports", "runs", "run_views", "projects", "attachments", "boards", "masks", "subsets", "notifications"} {
		if n := count(t, db, `SELECT COUNT(*) FROM `+table+` WHERE workspace_id<>1`); n != 0 {
			t.Errorf("%s: %d rows outside workspace 1", table, n)
		}
	}
	if n := count(t, db, `SELECT COUNT(*) FROM boards WHERE created_by='local-owner'`); n != 1 {
		t.Errorf("board created_by not local-owner")
	}
	// the counter is carried over: id 3 was handed out once and is not handed out again
	res, err := db.Exec(`INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header) VALUES('/d/c.fits','c',1,1,1,'{}')`)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := res.LastInsertId(); id != 4 {
		t.Errorf("new dataset id %d, want 4", id)
	}
	// the same file in another workspace is another row; twice in one workspace is still refused
	exec(t, db, `INSERT INTO workspaces(id,name,created_at) VALUES(2,'Lab',1)`,
		`INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header,workspace_id) VALUES('/d/a.fits','a',1,1,1,'{}',2)`,
		`INSERT INTO reports(key,title,body,revision,created_at,updated_at,workspace_id) VALUES('k1','weekly','b',1,1,1,2)`,
		`INSERT INTO attachments(sha,mime,size,created_at,workspace_id) VALUES('abc','image/png',3,1,2)`)
	if _, err := db.Exec(`INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header) VALUES('/d/a.fits','a',1,1,1,'{}')`); err == nil {
		t.Error("a path registered twice in one workspace was accepted")
	}
	if _, err := db.Exec(`INSERT INTO reports(key,title,body,revision,created_at,updated_at) VALUES('k9','WEEKLY','b',1,1,1)`); err == nil {
		t.Error("a report title taken in the same workspace (any case) was accepted")
	}
	// the append-only trigger of a child table survived, and the cascade still works with the keys back on
	if _, err := db.Exec(`UPDATE dataset_corrections SET reason='x'`); err == nil {
		t.Error("dataset_corrections is no longer append-only")
	}
	exec(t, db, `DELETE FROM datasets WHERE id=2`)
	if n := count(t, db, `SELECT COUNT(*) FROM subsets`); n != 0 {
		t.Error("deleting a dataset no longer cascades to its subsets")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM report_seen WHERE account_id='local-owner' AND report_id=1 AND revision=2`); n != 1 {
		t.Error("the owner's seen revision was not carried into report_seen")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM notification_reads WHERE account_id='local-owner'`); n != 1 {
		t.Error("the owner's read notification was not carried into notification_reads")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM memberships WHERE account_id='acct-1' AND workspace_id=1 AND role='owner'`); n != 1 {
		t.Error("a slice 2 account lost its access to the workspace it used")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM workspaces WHERE id=1`); n != 1 {
		t.Error("workspace 1 missing")
	}
}

// A rebuild that would leave a reference dangling fails the step, and the catalog is left at the version it had.
func TestRebuildWithForeignKeysOffStillRefusesDanglingReferences(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	exec(t, db, `PRAGMA foreign_keys=ON`,
		`CREATE TABLE parent(id INTEGER PRIMARY KEY)`, `CREATE TABLE child(pid INTEGER REFERENCES parent(id) ON DELETE CASCADE)`,
		`INSERT INTO parent VALUES(1)`, `INSERT INTO child VALUES(1)`, `PRAGMA user_version = 1`)
	steps := []Migration{{To: 2, Name: "drop the parent", NoForeignKeys: true, Up: func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM parent`)
		return err
	}}}
	if err := guard(db, "file:"+p, 2, steps); err == nil {
		t.Fatal("a step that leaves a dangling reference was committed")
	}
	if v := version(t, db); v != 1 {
		t.Errorf("version %d after a failed step", v)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM child`); n != 1 {
		t.Error("the failed step's cascade was not rolled back")
	}
}

// A step run with foreign keys off leaves the references exactly as it found them: it may neither add a dangling row nor quietly
// remove one that was there (rows a catalog wrote with the keys off are the researcher's to decide about, not a migration's).
func TestRebuildWithForeignKeysOffLeavesExistingDanglingRowsAlone(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.db")
	db := open(t, p)
	exec(t, db, `CREATE TABLE parent(id INTEGER PRIMARY KEY)`, `CREATE TABLE child(pid INTEGER REFERENCES parent(id))`,
		`INSERT INTO child VALUES(7)`, `PRAGMA user_version = 1`) // dangling already: written with the keys off
	steps := []Migration{{To: 2, Name: "tidy", NoForeignKeys: true, Up: func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM child`)
		return err
	}}}
	if err := guard(db, "file:"+p, 2, steps); err == nil {
		t.Fatal("a step that removed an existing dangling row was committed")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM child`); n != 1 {
		t.Error("the dangling row is gone after a refused step")
	}
	var on int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&on); err != nil || on != 0 {
		t.Errorf("foreign keys are %d after the step on a connection that had them off", on)
	}
}
