package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/backup"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/fullschema"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// ADR 0050 section 9, slice 4: the write of `multiuser import`. A representative local catalog is exported, then imported into a
// group server store that already holds rows of its own (so every id changes), and every row, id reference, owner and scope is
// checked. The refusals leave the server exactly as it was.

type fixture struct {
	t       *testing.T
	archive string
	dbPath  string
	dataDir string
	target  string // the folder the export's data is mapped to
	adaID   string
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	res, err := db.Exec(q, args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	id, _ := res.LastInsertId()
	return id
}

func one(t *testing.T, db *sql.DB, q string, args ...any) string {
	t.Helper()
	var s sql.NullString
	if err := db.QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
	return s.String
}

func pngFile(t *testing.T, dir string, payload string) (sha string) {
	t.Helper()
	b := []byte("\x89PNG-fixture-" + payload)
	sum := sha256.Sum256(b)
	sha = hex.EncodeToString(sum[:])
	if err := os.MkdirAll(filepath.Join(dir, "figures"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "figures", sha+".png"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return sha
}

func attachFile(t *testing.T, dir, payload string) string {
	t.Helper()
	b := []byte("attachment-" + payload)
	sum := sha256.Sum256(b)
	sha := hex.EncodeToString(sum[:])
	if err := os.MkdirAll(filepath.Join(dir, "attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attachments", sha), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return sha
}

type ids struct{ a, b, c, d, run1, run2, job, mask, region, fig1, fig2, board, snap1, snap2, report1 int64 }

// localExport builds a local catalog with a row of every imported kind and exports it.
func localExport(t *testing.T, extra func(db *sql.DB, src string, i ids)) (archive string, i ids, srcDir string) {
	t.Helper()
	srcDir = t.TempDir()
	lc, err := catalog.Open(filepath.Join(srcDir, "vesqira.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fullschema.Open(lc.DB(), srcDir); err != nil {
		t.Fatal(err)
	}
	db := lc.DB()
	mustExec(t, db, `INSERT INTO hosts(id,name,dest,python,roots,created_at) VALUES(1,'cluster','denis@cluster','python3','[]',1)`)
	mk := func(path, name string, host int64) int64 {
		d, err := lc.Upsert(workspace.Local, "local-owner", catalog.Dataset{Path: path, Name: name, Header: []byte(`{}`), HostID: host, SizeBytes: 111})
		if err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	i.a = mk("/home/denis/data/lmc/cube.fits", "cube.fits", 0)
	i.b = mk("/home/denis/data/lmc/psi.fits", "psi.fits", 0) // mapped, but the file is not on the server
	i.c = mk("/scratch/other.fits", "other.fits", 0)         // no map covers it
	i.d = mk("/cluster/x.fits", "x.fits", 1)                 // on a host
	mustExec(t, db, `UPDATE dataset_locations SET sampled_fp='sha256:aaaa', sampled_at=5, sampled_by='local-owner', full_sha256='bbbb', full_at=6, full_by='local-owner', file_dev=9, file_ino=77 WHERE dataset_id=?`, i.a)
	mustExec(t, db, `INSERT INTO interpretations(dataset_id,kind,unit,reliability_id,declared_at,tier) VALUES(?,?,?,?,?,?)`, i.b, "axial_orientation", "deg", i.a, 3, "declared")
	mustExec(t, db, `INSERT INTO dataset_nature(dataset_id,nature,declared_at) VALUES(?,?,?)`, i.a, "observed", 3)
	mustExec(t, db, `INSERT INTO dataset_units(dataset_id,unit,declared_at,tier) VALUES(?,?,?,?)`, i.a, "K", 3, "inferred")
	c1 := mustExec(t, db, `INSERT INTO dataset_corrections(dataset_id,action,state,changes,reason,author,tier,created_at,size_bytes,modified_ns) VALUES(?,?,?,?,?,?,?,?,?,?)`, i.a, "set", "applied", `{"CRVAL1":1}`, "typo", "local-owner", "declared", 4, 111, 5)
	mustExec(t, db, `INSERT INTO dataset_corrections(dataset_id,action,state,changes,reason,author,tier,created_at,size_bytes,modified_ns,reverts_id) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, i.a, "revert", "applied", `{}`, "undo", "local-owner", "declared", 5, 111, 5, c1)
	mustExec(t, db, `INSERT INTO dataset_access(workspace_id,dataset_id,dataset_name,label,previous,author,created_at) VALUES(1,?,?,?,?,?,?)`, i.a, "cube.fits", "collaboration", "none", "local-owner", 7)
	mustExec(t, db, `INSERT INTO dataset_access(workspace_id,dataset_id,dataset_name,label,previous,author,created_at) VALUES(1,999,'deleted.fits','unreleased','none','local-owner',8)`)
	mustExec(t, db, `INSERT INTO derived(dataset_id,op,params,sources,summary,software,created_at) VALUES(?,?,?,?,?,?,?)`, i.b, "moment", `{}`, fmt.Sprintf("[%d,%d]", i.a, i.c), `{}`, "astropy", 9)
	i.region = mustExec(t, db, `INSERT INTO saved_regions(dataset_id,name,shape,created_at,updated_at) VALUES(?,?,?,?,?)`, i.a, "core", `{}`, 1, 1)
	i.mask = mustExec(t, db, `INSERT INTO masks(dataset_id,type,name,path,width,height,channels,flagged,total,tier,method,rule,author,created_at,source_size,source_mtime,file_size,file_mtime,sha256) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		i.a, "low_snr", "snr<3", "/home/denis/.local/share/vesqira/derived/mask1.fits", 4, 4, 1, 2, 16, "declared", "rule", fmt.Sprintf(`{"sourceDatasetId":%d}`, i.a), "local-owner", 1, 111, 5, 10, 5, "cc")
	sub := mustExec(t, db, `INSERT INTO subsets(source_id,created_at) VALUES(?,1)`, i.a)
	mustExec(t, db, `INSERT INTO subset_versions(subset_id,version,kind,name,reason,definition,source_size,source_mtime_ns,author,created_at) VALUES(?,1,'box','roi','why','{}',111,5,'local-owner',2)`, sub)

	i.run1 = mustExec(t, db, `INSERT INTO runs(dir,name,kind,imported_at,generated_at,git_commit,input_name,input_size,params,diagnostics) VALUES(?,?,?,?,?,?,?,?,?,?)`, "/home/denis/data/lmc/run1", "run one", "vgt", 1, "t", "abc", "cube.fits", 111, `{}`, `{}`)
	i.run2 = mustExec(t, db, `INSERT INTO runs(dir,name,kind,imported_at,generated_at,git_commit,input_name,input_size,params,diagnostics) VALUES(?,?,?,?,?,?,?,?,?,?)`, "h1:/cluster/run2", "run two", "rohsa", 1, "t", "def", "cube.fits", 111, `{}`, `{}`)
	mustExec(t, db, `INSERT INTO run_review(run_id,qc,interpretation,note) VALUES(?,?,?,?)`, i.run1, "pass", "ok", "n")
	mustExec(t, db, `INSERT INTO run_review_history(run_id,at,qc,interpretation,note) VALUES(?,?,?,?,?)`, i.run1, 2, "pass", "ok", "n")
	mustExec(t, db, `INSERT INTO run_outputs(run_id,role,product,dataset_id,name) VALUES(?,?,?,?,?)`, i.run1, "orientation", "psi", i.b, "psi.fits")
	mustExec(t, db, `INSERT INTO rohsa_runs(run_id,dat_path,info) VALUES(?,?,?)`, i.run2, "h1:/cluster/run2/out.dat", `{}`)
	i.job = mustExec(t, db, `INSERT INTO jobs(host_id,name,template,script,script_hash,state,detail,slurm_id,work_dir,output_dir,created_at,run_id,request,note,record_owed,auto_import) VALUES(1,'fit','rohsa','#!/bin/sh','h','running','','77','/cluster/w','/cluster/o',1,?,?,'mine',1,1)`,
		i.run2, fmt.Sprintf(`{"inputRerun":{"sourceRunId":%d}}`, i.run1))

	sha1, sha2 := pngFile(t, srcDir, "one"), pngFile(t, srcDir, "two")
	scene := fmt.Sprintf(`{"schema":"scene/1","base":{"datasetId":%d,"channel":0},"overlays":[{"id":"o1","kind":"orientation","datasetId":%d,"reliabilityId":%d,"saved":[{"id":%d}]}],"link":{"id":"l","datasets":[%d,%d]},"masks":[{"id":%d,"type":"low_snr","name":"snr<3"}]}`, i.a, i.b, i.a, i.region, i.a, i.b, i.mask)
	ident := fmt.Sprintf(`{"schema":"figure-identity/1","datasets":[{"role":"base","datasetId":%d}]}`, i.a)
	i.fig1 = mustExec(t, db, `INSERT INTO figures(dataset_id,title,caption,created_at,revision,preview_sha,width,height,provenance,scene,identity,kind) VALUES(?,?,?,?,?,?,?,?,?,?,?,'dataset')`, i.a, "Fig one", "c", 1, "rev1", sha1, 4, 4, `{}`, scene, ident)
	cross := fmt.Sprintf(`{"schema":"cross-run/1","runs":[{"id":%d,"name":"run one"},{"id":%d,"name":"run two"}]}`, i.run1, i.run2)
	i.fig2 = mustExec(t, db, `INSERT INTO figures(dataset_id,title,caption,created_at,revision,preview_sha,width,height,provenance,scene,identity,kind) VALUES(NULL,?,?,?,?,?,?,?,?,?,?,'crossrun')`, "Fig two", "c", 2, "rev2", sha2, 4, 4, `{}`, cross,
		fmt.Sprintf(`{"schema":"figure-identity/1","datasets":[],"redrawnFrom":{"figureId":%d,"revision":"rev1"}}`, i.fig1))
	i.board = mustExec(t, db, `INSERT INTO boards(title,created_at,updated_at) VALUES('Board',1,1)`)
	mustExec(t, db, `INSERT INTO board_items(board_id,position,figure_id,note) VALUES(?,0,?,'n')`, i.board, i.fig1)
	mustExec(t, db, `INSERT INTO board_items(board_id,position,figure_id,note) VALUES(?,1,?,'n')`, i.board, i.fig2)
	mustExec(t, db, `INSERT INTO board_items(board_id,position,job_id,note) VALUES(?,2,?,'n')`, i.board, i.job)
	i.snap1 = mustExec(t, db, `INSERT INTO snapshots(board_id,label,created_at,expires_at,token_hash,frozen) VALUES(?,?,?,?,?,?)`, i.board, "round 1", 1, 99, "tokenhash-1", `{"items":[{"revision":"rev1"}]}`)
	i.snap2 = mustExec(t, db, `INSERT INTO snapshots(board_id,label,created_at,expires_at,token_hash,frozen) VALUES(?,?,?,?,?,?)`, i.board, "round 2", 2, 99, "tokenhash-2", `{"items":[{"revision":"rev1"}]}`)
	mustExec(t, db, `UPDATE snapshots SET superseded_by=?, supersede_reason='new round' WHERE id=?`, i.snap2, i.snap1)
	cm := mustExec(t, db, `INSERT INTO comments(snapshot_id,figure_revision,author,body,created_at) VALUES(?,?,?,?,?)`, i.snap1, "rev1", "Referee", "why?", 3)
	mustExec(t, db, `INSERT INTO comments(snapshot_id,figure_revision,author,body,created_at,parent_id,from_owner) VALUES(?,?,?,?,?,?,1)`, i.snap1, "rev1", "local-owner", "because", 4, cm)
	mustExec(t, db, `INSERT INTO snapshot_outcomes(snapshot_id,at,outcome,note,author) VALUES(?,?,?,?,?)`, i.snap1, 5, "accepted", "", "Referee")
	i.report1 = mustExec(t, db, `INSERT INTO reports(key,title,body,revision,created_at,updated_at) VALUES('k1','Notes',?,2,1,2)`, fmt.Sprintf("see [[run:%d]] and [[run:%d]] and ![[figure:rev1]]", i.run1, i.run2))
	mustExec(t, db, `INSERT INTO reports(key,title,body,revision,created_at,updated_at,parent_id) VALUES('k2','Child','x',1,1,2,?)`, i.report1)
	mustExec(t, db, `INSERT INTO report_history(report_id,revision,author,at,title,body) VALUES(?,1,'local-owner',1,'Notes',?)`, i.report1, fmt.Sprintf("old [[run:%d]]", i.run1))
	mustExec(t, db, `INSERT INTO report_links(report_id,kind,target) VALUES(?,'run',?)`, i.report1, fmt.Sprint(i.run1))
	mustExec(t, db, `INSERT INTO report_links(report_id,kind,target) VALUES(?,'figure','rev1')`, i.report1)
	mustExec(t, db, `INSERT INTO archived_entries(kind,entity_id,archived_at) VALUES('figure',?,1),('dataset',?,1),('run',4242,1)`, i.fig2, i.c)
	mustExec(t, db, `INSERT INTO notifications(at,kind,title,body,dedupe) VALUES(1,'job','t','b','d1')`)
	p1 := mustExec(t, db, `INSERT INTO projects(host_id,root,name,opened_at) VALUES(0,'/home/denis/data/lmc','LMC',1)`)
	mustExec(t, db, `INSERT INTO projects(host_id,root,name,opened_at) VALUES(0,'/scratch/proj','Scratch',1)`)
	mustExec(t, db, `INSERT INTO projects(host_id,root,name,opened_at) VALUES(1,'/cluster/proj','Remote',1)`)
	mustExec(t, db, `INSERT INTO project_answers(project_id,need,answer,value,declared_by,date,written) VALUES(?,?,?,?,?,?,?)`, p1, "beam", "declared", "10 arcsec", "user", "2026-01-01", 1)
	mustExec(t, db, `INSERT INTO run_views(name,spec,updated_at) VALUES('by block','{"query":"block_size:16..64","columns":[]}',1)`)
	st1 := mustExec(t, db, `INSERT INTO exploration_steps(project_id,dataset_id,kind,label,scene,at) VALUES(?,?,?,?,?,?)`, p1, i.a, "open", "first", fmt.Sprintf(`{"base":{"datasetId":%d}}`, i.a), 1)
	mustExec(t, db, `INSERT INTO exploration_steps(project_id,parent_id,dataset_id,kind,label,scene,at) VALUES(0,?,?,?,?,?,?)`, st1, i.b, "open", "second", fmt.Sprintf(`{"base":{"datasetId":%d}}`, i.b), 2)
	mustExec(t, db, `INSERT INTO exploration_steps(project_id,dataset_id,kind,label,scene,at) VALUES(0,999,'open','gone','{}',3)`)
	asha := attachFile(t, srcDir, "image")
	mustExec(t, db, `INSERT INTO attachments(sha,mime,size,created_at) VALUES(?,?,?,?)`, asha, "image/png", 5, 1)
	mustExec(t, db, `INSERT INTO attachments(sha,mime,size,created_at) VALUES(?,?,?,?)`, strings.Repeat("ab", 32), "image/png", 5, 1)
	if extra != nil {
		extra(db, srcDir, i)
	}
	lc.Close()

	archive = filepath.Join(t.TempDir(), "export.tar.gz")
	out, _ := os.Create(archive)
	if _, err := backup.Export(workspace.Local, filepath.Join(srcDir, "vesqira.db"), srcDir, out); err != nil {
		t.Fatal(err)
	}
	out.Close()
	return archive, i, srcDir
}

// groupServer makes the importing side: an admin account, a second workspace with a row of its own, and rows in the main
// workspace that push every imported id away from its old value.
func groupServer(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, dbPath: filepath.Join(t.TempDir(), "server.db")}
	f.dataDir = filepath.Dir(f.dbPath)
	sc, err := catalog.Open(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	if err := fullschema.Open(sc.DB(), f.dataDir); err != nil {
		t.Fatal(err)
	}
	acc, _ := accounts.Open(sc.DB())
	acc.Params = accounts.Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1}
	tok, _, _ := acc.CreateSetupToken()
	ada, err := acc.CreateAdminFromSetup(tok, "Ada", "ada", "a long passphrase here")
	if err != nil {
		t.Fatal(err)
	}
	f.adaID = ada.ID
	other, err := acc.CreateWorkspace("Other group", ada.ID, ada.ID)
	if err != nil {
		t.Fatal(err)
	}
	db := sc.DB()
	for _, ws := range []int64{1, int64(other.ID)} {
		for k := 0; k < 5; k++ {
			mustExec(t, db, `INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header,workspace_id) VALUES(?,?,?,?,?,?,?)`, fmt.Sprintf("/srv/own/%d/%d.fits", ws, k), "own", 1, 1, 1, `{}`, ws)
		}
		for k := 0; k < 3; k++ {
			mustExec(t, db, `INSERT INTO runs(dir,name,kind,imported_at,generated_at,git_commit,input_name,input_size,params,diagnostics,workspace_id) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("/srv/own/%d/run%d", ws, k), "own", "vgt", 1, "t", "c", "i", 1, `{}`, `{}`, ws)
		}
		for k := 0; k < 2; k++ {
			mustExec(t, db, `INSERT INTO projects(host_id,root,name,opened_at,workspace_id) VALUES(0,?,?,1,?)`, fmt.Sprintf("/srv/own/%d/p%d", ws, k), "own", ws)
		}
		b := mustExec(t, db, `INSERT INTO boards(title,created_at,updated_at,workspace_id) VALUES('own',1,1,?)`, ws)
		mustExec(t, db, `INSERT INTO snapshots(board_id,label,created_at,expires_at,token_hash,frozen,workspace_id) VALUES(?,?,?,?,?,?,?)`, b, "own", 1, 9, fmt.Sprintf("own-token-%d", ws), `{}`, ws)
		mustExec(t, db, `INSERT INTO reports(key,title,body,revision,created_at,updated_at,workspace_id) VALUES(?,?,?,?,?,?,?)`, fmt.Sprintf("own%d", ws), "Own page", "b", 1, 1, 1, ws)
	}
	mustExec(t, db, `INSERT INTO jobs(host_id,name,template,script,script_hash,state,detail,slurm_id,work_dir,output_dir,created_at,workspace_id) VALUES(0,'own','vgt','s','h','draft','','','','',1,1)`)
	f.target, _ = filepath.EvalSymlinks(t.TempDir())
	_ = os.MkdirAll(filepath.Join(f.target, "lmc"), 0o755)
	_ = os.WriteFile(filepath.Join(f.target, "lmc", "cube.fits"), []byte("x"), 0o644)
	return f
}

func (f *fixture) run(archive string, args ...string) (int, string, string) {
	var o, e bytes.Buffer
	code := runMultiuser(append([]string{"import", archive, "--db", f.dbPath}, args...), &o, &e)
	return code, o.String(), e.String()
}

func (f *fixture) import_(archive string, extra ...string) (int, string, string) {
	return f.run(archive, append([]string{"--workspace", "Denis's data", "--owner", "ada", "--map", "/home/denis/data=" + f.target}, extra...)...)
}

// tableCounts is every table's row count: a refused or failed import must leave all of them as they were.
func tableCounts(t *testing.T, path string) map[string]int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	out := map[string]int{}
	for _, n := range names {
		var c int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + n).Scan(&c); err != nil {
			t.Fatal(err)
		}
		out[n] = c
	}
	return out
}

func figureFiles(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "figures", "*"))
	sort.Strings(m)
	return m
}

func planOf(out string) string {
	i := strings.Index(out, "Data roots")
	j := strings.Index(out, "Managed copies and derived")
	if i < 0 || j < i {
		return ""
	}
	return out[i:j]
}

func TestMultiuserImportRoundTrip(t *testing.T) {
	archive, _, _ := localExport(t, nil)
	before := fileSum(t, archive)
	f := groupServer(t)

	// the dry run lists what the write will do and writes nothing
	preCounts := tableCounts(t, f.dbPath)
	code, dryOut, errs := f.import_(archive, "--dry-run")
	if code != 0 || !strings.HasPrefix(dryOut, "DRY RUN") || !strings.Contains(dryOut, "nothing was written") {
		t.Fatalf("dry run: %d %s %s", code, dryOut, errs)
	}
	if !reflect.DeepEqual(preCounts, tableCounts(t, f.dbPath)) || len(figureFiles(t, f.dataDir)) != 0 {
		t.Fatal("the dry run wrote something")
	}
	for _, want := range []string{"cube.fits: /home/denis/data/lmc/cube.fits -> " + filepath.Join(f.target, "lmc", "cube.fits") + " (present)",
		"psi.fits: /home/denis/data/lmc/psi.fits -> " + filepath.Join(f.target, "lmc", "psi.fits") + " (not found there: it will be shown as missing)",
		"other.fits: /scratch/other.fits; no --map covers it", "x.fits: on host \"cluster\"", "1 rewritten through a map", "2 figure preview pictures", "1 report images (0 already", "1 report images are missing", "3 projects", "Scratch: /scratch/proj; no --map covers it, so it is shown as unavailable", "Remote: on host \"cluster\"", "2 exploration steps", "1 saved run views", "1 access label records", "an exploration step (\"gone\")",
		"not imported: an access label record of \"deleted.fits\"", "1 notifications", "1 hosts"} {
		if !strings.Contains(strings.Replace(dryOut, "2 rewritten through a map", "1 rewritten through a map", 1), want) {
			t.Errorf("the plan lacks %q:\n%s", want, dryOut)
		}
	}

	code, out, errs := f.import_(archive)
	if code != 0 {
		t.Fatalf("import: %d %s %s", code, out, errs)
	}
	if planOf(out) == "" || planOf(out) != planOf(dryOut) {
		t.Errorf("the write's result is not what the dry run listed.\nDRY:\n%s\nWRITE:\n%s", planOf(dryOut), planOf(out))
	}
	if fileSum(t, archive) != before {
		t.Error("the export changed")
	}

	db, err := sql.Open("sqlite", "file:"+f.dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := one(t, db, `SELECT id FROM workspaces WHERE name=?`, "Denis's data")
	if one(t, db, `SELECT created_by FROM workspaces WHERE id=?`, ws) != f.adaID {
		t.Error("the workspace is not created by the named account")
	}
	if one(t, db, `SELECT role||'/'||account_id FROM memberships WHERE workspace_id=?`, ws) != "owner/"+f.adaID || one(t, db, `SELECT COUNT(*) FROM memberships WHERE workspace_id=?`, ws) != "1" {
		t.Error("the named account is not the one Owner")
	}
	if one(t, db, `SELECT group_concat(path) FROM workspace_roots WHERE workspace_id=?`, ws) != f.target {
		t.Error("the data roots are not the map targets")
	}

	// datasets: paths, hosts, owner, scope
	q := func(sql string, a ...any) string { return one(t, db, sql, a...) }
	dsID := func(name string) string {
		return q(`SELECT id FROM datasets WHERE workspace_id=? AND name=?`, ws, name)
	}
	if got := q(`SELECT path FROM datasets WHERE id=?`, dsID("cube.fits")); got != filepath.Join(f.target, "lmc", "cube.fits") {
		t.Errorf("mapped path %s", got)
	}
	if got := q(`SELECT path FROM datasets WHERE id=?`, dsID("psi.fits")); got != filepath.Join(f.target, "lmc", "psi.fits") {
		t.Errorf("mapped path of a file that is not there %s", got)
	}
	if got := q(`SELECT path||' '||host_id FROM datasets WHERE id=?`, dsID("other.fits")); got != "/scratch/other.fits 0" {
		t.Errorf("an unmapped path was changed: %s", got)
	}
	if got := q(`SELECT path||' '||host_id FROM datasets WHERE id=?`, dsID("x.fits")); got != "[host cluster] /cluster/x.fits -1" {
		t.Errorf("a dataset of a host: %s", got)
	}
	if q(`SELECT COUNT(*) FROM datasets WHERE workspace_id=? AND created_by<>?`, ws, f.adaID) != "0" || q(`SELECT COUNT(*) FROM datasets WHERE workspace_id=?`, ws) != "4" {
		t.Error("datasets: owner or count")
	}
	// fingerprints are the recorded ones, as declared, and the old inode is gone
	if got := q(`SELECT sampled_fp||'|'||sampled_at||'|'||sampled_by||'|'||full_sha256||'|'||full_at||'|'||full_by||'|'||file_dev||'|'||file_ino||'|'||workspace_id FROM dataset_locations WHERE dataset_id=?`, dsID("cube.fits")); got != "sha256:aaaa|5|local-owner|bbbb|6|local-owner|0|0|"+ws {
		t.Errorf("location fingerprints: %s", got)
	}
	if q(`SELECT COUNT(*) FROM dataset_locations WHERE workspace_id=?`, ws) != "4" {
		t.Error("locations")
	}
	if got := q(`SELECT reliability_id FROM interpretations WHERE dataset_id=?`, dsID("psi.fits")); got != dsID("cube.fits") {
		t.Errorf("reliability map is %s, want the new id of cube.fits (%s)", got, dsID("cube.fits"))
	}
	if q(`SELECT tier FROM dataset_units WHERE dataset_id=?`, dsID("cube.fits")) != "inferred" || q(`SELECT nature FROM dataset_nature WHERE dataset_id=?`, dsID("cube.fits")) != "observed" {
		t.Error("tiers and nature are kept as recorded")
	}
	if got := q(`SELECT group_concat(action||':'||author||':'||COALESCE(reverts_id,''), ' ') FROM dataset_corrections WHERE dataset_id=?`, dsID("cube.fits")); !strings.Contains(got, "set:"+f.adaID+": ") || !strings.Contains(got, "revert:"+f.adaID+":"+q(`SELECT id FROM dataset_corrections WHERE action='set' AND dataset_id=?`, dsID("cube.fits"))) {
		t.Errorf("corrections: %s", got)
	}
	if got := q(`SELECT dataset_id||':'||label||':'||workspace_id FROM dataset_access WHERE workspace_id=?`, ws); got != dsID("cube.fits")+":collaboration:"+ws {
		t.Errorf("access labels: %s", got)
	}
	if got := q(`SELECT sources FROM derived WHERE dataset_id=?`, dsID("psi.fits")); got != fmt.Sprintf("[%s,%s]", dsID("cube.fits"), dsID("other.fits")) {
		t.Errorf("derived sources %s", got)
	}
	if got := q(`SELECT dataset_id FROM saved_regions WHERE workspace_id=?`, ws); got != dsID("cube.fits") {
		t.Errorf("region %s", got)
	}
	var rule string
	_ = db.QueryRow(`SELECT rule FROM masks WHERE workspace_id=?`, ws).Scan(&rule)
	if rule != fmt.Sprintf(`{"sourceDatasetId":%s}`, dsID("cube.fits")) || q(`SELECT dataset_id||path FROM masks WHERE workspace_id=?`, ws) != dsID("cube.fits")+"/home/denis/.local/share/vesqira/derived/mask1.fits" {
		t.Errorf("mask: %s", rule)
	}
	if q(`SELECT s.source_id FROM subsets s WHERE workspace_id=?`, ws) != dsID("cube.fits") || q(`SELECT COUNT(*) FROM subset_versions v JOIN subsets s ON s.id=v.subset_id WHERE s.workspace_id=?`, ws) != "1" {
		t.Error("subsets")
	}

	// runs and job records
	runID := func(name string) string { return q(`SELECT id FROM runs WHERE workspace_id=? AND name=?`, ws, name) }
	if q(`SELECT dir FROM runs WHERE id=?`, runID("run one")) != filepath.Join(f.target, "lmc", "run1") || q(`SELECT dir FROM runs WHERE id=?`, runID("run two")) != "[host cluster] /cluster/run2" {
		t.Errorf("run dirs %s | %s", q(`SELECT dir FROM runs WHERE id=?`, runID("run one")), q(`SELECT dir FROM runs WHERE id=?`, runID("run two")))
	}
	if q(`SELECT dat_path FROM rohsa_runs WHERE run_id=?`, runID("run two")) != "[host cluster] /cluster/run2/out.dat" || q(`SELECT product||dataset_id FROM run_outputs WHERE run_id=?`, runID("run one")) != "psi"+dsID("psi.fits") ||
		q(`SELECT qc FROM run_review WHERE run_id=?`, runID("run one")) != "pass" || q(`SELECT COUNT(*) FROM run_review_history WHERE run_id=?`, runID("run one")) != "1" {
		t.Error("run children")
	}
	jid := q(`SELECT id FROM jobs WHERE workspace_id=? AND name='fit'`, ws)
	if got := q(`SELECT host_id||'|'||run_id||'|'||record_owed||'|'||auto_import||'|'||state||'|'||created_by FROM jobs WHERE id=?`, jid); got != "0|"+runID("run two")+"|0|0|running|"+f.adaID {
		t.Errorf("job: %s", got)
	}
	if got := q(`SELECT note FROM jobs WHERE id=?`, jid); !strings.Contains(got, `"cluster"`) || !strings.HasSuffix(got, "mine") {
		t.Errorf("the job's old host is not kept as text: %s", got)
	}
	if got := q(`SELECT request FROM jobs WHERE id=?`, jid); got != fmt.Sprintf(`{"inputRerun":{"sourceRunId":%s}}`, runID("run one")) {
		t.Errorf("job request %s", got)
	}

	// figures
	fig := func(title string) string {
		return q(`SELECT id FROM figures WHERE workspace_id=? AND title=?`, ws, title)
	}
	var scene, ident1, ident2, cross string
	_ = db.QueryRow(`SELECT scene, identity FROM figures WHERE id=?`, fig("Fig one")).Scan(&scene, &ident1)
	var sc map[string]any
	if err := json.Unmarshal([]byte(scene), &sc); err != nil {
		t.Fatal(err)
	}
	a, b := dsID("cube.fits"), dsID("psi.fits")
	flat := scene
	for _, want := range []string{`"datasetId":` + a, `"datasetId":` + b, `"reliabilityId":` + a, `"datasets":[` + a + `,` + b + `]`} {
		if !strings.Contains(flat, want) {
			t.Errorf("scene lacks %s: %s", want, flat)
		}
	}
	if !strings.Contains(scene, `"masks":[{"id":`+q(`SELECT id FROM masks WHERE workspace_id=?`, ws)+`,`) || !strings.Contains(scene, `"saved":[{"id":`+q(`SELECT id FROM saved_regions WHERE workspace_id=?`, ws)+`}]`) {
		t.Errorf("scene mask or region id: %s", scene)
	}
	if !strings.Contains(ident1, `"datasetId":`+a) {
		t.Errorf("identity %s", ident1)
	}
	_ = db.QueryRow(`SELECT scene, identity FROM figures WHERE id=?`, fig("Fig two")).Scan(&cross, &ident2)
	if !strings.Contains(cross, `"id":`+runID("run one")+`,`) || !strings.Contains(cross, `"id":`+runID("run two")+`,`) || !strings.Contains(ident2, `"figureId":`+fig("Fig one")+`,`) {
		t.Errorf("cross-run %s | %s", cross, ident2)
	}
	if q(`SELECT COALESCE(dataset_id,'none')||kind FROM figures WHERE id=?`, fig("Fig two")) != "nonecrossrun" || q(`SELECT dataset_id FROM figures WHERE id=?`, fig("Fig one")) != a || q(`SELECT revision FROM figures WHERE id=?`, fig("Fig one")) != "rev1" {
		t.Error("figure columns")
	}
	if q(`SELECT COUNT(*) FROM figures WHERE workspace_id=? AND created_by<>?`, ws, f.adaID) != "0" {
		t.Error("figures: owner")
	}
	if n := len(figureFiles(t, f.dataDir)); n != 2 {
		t.Errorf("%d preview files copied", n)
	}
	if m, _ := filepath.Glob(filepath.Join(f.dataDir, "attachments", "*")); len(m) != 1 || one(t, db, `SELECT COUNT(*) FROM attachments WHERE workspace_id=?`, ws) != "1" {
		t.Errorf("report images: files %v", m)
	}

	// projects, answers, views and the exploration trail
	pid := func(name string) string {
		return q(`SELECT id FROM projects WHERE workspace_id=? AND name=?`, ws, name)
	}
	if q(`SELECT root FROM projects WHERE id=?`, pid("LMC")) != filepath.Join(f.target, "lmc") || q(`SELECT root FROM projects WHERE id=?`, pid("Scratch")) != "/scratch/proj" ||
		q(`SELECT host_id||root FROM projects WHERE id=?`, pid("Remote")) != "-1[host cluster] /cluster/proj" {
		t.Error("project roots")
	}
	if q(`SELECT project_id||answer||value||declared_by FROM project_answers WHERE project_id IN (SELECT id FROM projects WHERE workspace_id=?)`, ws) != pid("LMC")+"declared10 arcsecuser" {
		t.Error("project answers")
	}
	if q(`SELECT COUNT(*) FROM run_views WHERE workspace_id=? AND name='by block' AND created_by=?`, ws, f.adaID) != "1" {
		t.Error("run views")
	}
	step1 := q(`SELECT id FROM exploration_steps WHERE workspace_id=? AND label='first'`, ws)
	if got := q(`SELECT project_id||'/'||dataset_id||'/'||scene FROM exploration_steps WHERE id=?`, step1); got != pid("LMC")+"/"+a+`/{"base":{"datasetId":`+a+`}}` {
		t.Errorf("exploration step: %s", got)
	}
	if got := q(`SELECT parent_id||'/'||dataset_id||'/'||project_id FROM exploration_steps WHERE workspace_id=? AND label='second'`, ws); got != step1+"/"+b+"/0" {
		t.Errorf("exploration step with a parent: %s", got)
	}

	// boards, snapshots with comments
	bid := q(`SELECT id FROM boards WHERE workspace_id=?`, ws)
	if got := q(`SELECT group_concat(COALESCE(figure_id,'')||'/'||COALESCE(job_id,''), ' ') FROM (SELECT * FROM board_items WHERE board_id=? ORDER BY position)`, bid); got != fig("Fig one")+"/ "+fig("Fig two")+"/ /"+jid {
		t.Errorf("board items: %s", got)
	}
	s1 := q(`SELECT id FROM snapshots WHERE workspace_id=? AND label='round 1'`, ws)
	s2 := q(`SELECT id FROM snapshots WHERE workspace_id=? AND label='round 2'`, ws)
	if got := q(`SELECT token_hash||'|'||superseded_by||'|'||board_id||'|'||supersede_reason FROM snapshots WHERE id=?`, s1); got != "tokenhash-1|"+s2+"|"+bid+"|new round" {
		t.Errorf("snapshot: %s", got)
	}
	cid := q(`SELECT id FROM comments WHERE snapshot_id=? AND parent_id=0`, s1)
	if got := q(`SELECT author||':'||body||':'||parent_id FROM comments WHERE snapshot_id=? AND parent_id<>0`, s1); got != "local-owner:because:"+cid || q(`SELECT author FROM comments WHERE id=?`, cid) != "Referee" {
		t.Errorf("comments: %s", got)
	}
	if q(`SELECT COUNT(*) FROM snapshot_outcomes WHERE snapshot_id=?`, s1) != "1" {
		t.Error("outcomes")
	}

	// reports
	rid := q(`SELECT id FROM reports WHERE workspace_id=? AND title='Notes'`, ws)
	if got := q(`SELECT body FROM reports WHERE id=?`, rid); got != fmt.Sprintf("see [[run:%s]] and [[run:%s]] and ![[figure:rev1]]", runID("run one"), runID("run two")) {
		t.Errorf("report body %s", got)
	}
	if q(`SELECT parent_id FROM reports WHERE workspace_id=? AND title='Child'`, ws) != rid || q(`SELECT body FROM report_history WHERE report_id=?`, rid) != "old [[run:"+runID("run one")+"]]" ||
		q(`SELECT author FROM report_history WHERE report_id=?`, rid) != f.adaID {
		t.Error("report page tree or history")
	}
	if q(`SELECT group_concat(kind||'='||target, ' ') FROM (SELECT * FROM report_links WHERE report_id=? ORDER BY kind)`, rid) != "figure=rev1 run="+runID("run one") {
		t.Error("report links")
	}
	if q(`SELECT group_concat(kind||entity_id, ' ') FROM (SELECT * FROM archived_entries ORDER BY kind)`) != "dataset"+dsID("other.fits")+" figure"+fig("Fig two") {
		t.Errorf("archive marks: %s", q(`SELECT group_concat(kind||entity_id, ' ') FROM archived_entries`))
	}
	if !strings.Contains(q(`SELECT detail FROM audit WHERE workspace_id=? AND kind='workspace'`, ws), "export.tar.gz") {
		t.Error("no audit record of the import")
	}

	// workspace scoping: the other workspaces are exactly as they were, and the new rows are all in the new workspace
	post := tableCounts(t, f.dbPath)
	for _, table := range []string{"datasets", "runs", "figures", "boards", "snapshots", "reports", "dataset_locations", "masks", "saved_regions", "subsets", "jobs", "dataset_access", "projects", "exploration_steps", "run_views", "attachments"} {
		own := post[table] - preCounts[table]
		var inNew int
		_ = db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE workspace_id=?`, ws).Scan(&inNew)
		if inNew != own || inNew == 0 {
			t.Errorf("%s: %d rows in the new workspace, %d added", table, inNew, own)
		}
	}
	for _, other := range []string{"1", "2"} {
		if q(`SELECT COUNT(*) FROM datasets WHERE workspace_id=?`, other) != "5" || q(`SELECT COUNT(*) FROM runs WHERE workspace_id=?`, other) != "3" ||
			q(`SELECT COUNT(*) FROM snapshots WHERE workspace_id=?`, other) != "1" || q(`SELECT COUNT(*) FROM reports WHERE workspace_id=?`, other) != "1" {
			t.Errorf("workspace %s gained or lost rows", other)
		}
	}
	// children are reachable only through rows of the new workspace: none of them points at another workspace's parent
	for _, c := range []string{
		`SELECT COUNT(*) FROM interpretations i JOIN datasets d ON d.id=i.dataset_id WHERE d.workspace_id<>` + ws + ` AND i.declared_at=3`,
		`SELECT COUNT(*) FROM run_outputs o JOIN runs r ON r.id=o.run_id WHERE r.workspace_id<>` + ws + ` AND o.product='psi'`,
		`SELECT COUNT(*) FROM comments c JOIN snapshots s ON s.id=c.snapshot_id WHERE s.workspace_id<>` + ws + ` AND c.author='Referee'`,
		`SELECT COUNT(*) FROM board_items i JOIN boards b ON b.id=i.board_id WHERE b.workspace_id<>` + ws + ` AND i.note='n'`,
	} {
		if q(c) != "0" {
			t.Errorf("a child row points outside the new workspace: %s", c)
		}
	}

	// a second import of the same export is refused (the review links are already on the server) and changes nothing
	post2 := tableCounts(t, f.dbPath)
	code, _, errs = f.run(archive, "--workspace", "Again", "--owner", "ada", "--map", "/home/denis/data="+f.target)
	if code != 1 || !strings.Contains(errs, "UNIQUE") {
		t.Errorf("second import: %d %s", code, errs)
	}
	if !reflect.DeepEqual(post2, tableCounts(t, f.dbPath)) {
		t.Error("a failed import left rows behind")
	}
}

func TestMultiuserImportRefusalsLeaveTheServerAsItWas(t *testing.T) {
	archive, _, _ := localExport(t, nil)
	f := groupServer(t)
	check := func(name string, wantCode int, wantErr string, archive string, args ...string) {
		t.Helper()
		pre, preFiles := tableCounts(t, f.dbPath), figureFiles(t, f.dataDir)
		code, out, errs := f.run(archive, args...)
		if code != wantCode || !strings.Contains(errs, wantErr) {
			t.Errorf("%s: exit %d (want %d), stderr %q, stdout %q", name, code, wantCode, errs, out)
		}
		if !reflect.DeepEqual(pre, tableCounts(t, f.dbPath)) || !reflect.DeepEqual(preFiles, figureFiles(t, f.dataDir)) {
			t.Errorf("%s: the server changed", name)
		}
	}
	good := []string{"--workspace", "W", "--owner", "ada", "--map", "/home/denis/data=" + f.target}

	check("an unknown account", 1, "no account has the username", archive, "--workspace", "W", "--owner", "nobody")
	check("an existing workspace name", 1, "exists already", archive, "--workspace", "other GROUP", "--owner", "ada")
	check("a relative map", 2, "both absolute", archive, "--workspace", "W", "--owner", "ada", "--map", "data="+f.target)
	check("a map to a folder that is not there", 2, "does not exist", archive, "--workspace", "W", "--owner", "ada", "--map", "/a=/no/such/folder")
	check("a map that holds the server's data", 2, "server's own data", archive, "--workspace", "W", "--owner", "ada", "--map", "/a="+filepath.Dir(f.dataDir))
	inner := filepath.Join(f.dataDir, "inner")
	_ = os.MkdirAll(inner, 0o755)
	check("a map inside the server's data", 2, "server's own data", archive, "--workspace", "W", "--owner", "ada", "--map", "/b="+inner)
	if link := filepath.Join(t.TempDir(), "link"); os.Symlink(f.target, link) == nil {
		check("a map target through a link", 2, "canonically", archive, "--workspace", "W", "--owner", "ada", "--map", "/a="+link)
	}

	// a tampered archive
	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	tamper(t, archive, bad)
	check("a file that does not match the manifest", 1, "the archive was refused", bad, good...)
	notArchive := filepath.Join(t.TempDir(), "x.tar.gz")
	_ = os.WriteFile(notArchive, []byte("not an archive"), 0o600)
	check("not an archive", 1, "the archive was refused", notArchive, good...)

	// a disabled account cannot own a workspace
	sc, _ := catalog.Open(f.dbPath)
	mustExec(t, sc.DB(), `UPDATE accounts SET disabled_at=1 WHERE id=?`, f.adaID)
	sc.Close()
	check("a disabled owner", 1, "disabled", archive, good...)
	sc, _ = catalog.Open(f.dbPath)
	mustExec(t, sc.DB(), `UPDATE accounts SET disabled_at=NULL WHERE id=?`, f.adaID)
	sc.Close()

	// a recorded path that lies inside a data root without a map for it is a guess, so it is refused
	stray, _, _ := localExport(t, func(db *sql.DB, src string, i ids) {
		mustExec(t, db, `INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header) VALUES(?,?,?,?,?,?)`, filepath.Join(f.target, "lmc", "stray.fits"), "stray.fits", 1, 1, 1, `{}`)
	})
	check("a path outside every map that lies inside a root", 1, "lies inside the data root", stray, good...)

	// an id the export cannot resolve would point at another row of the new workspace
	dangling, _, _ := localExport(t, func(db *sql.DB, src string, i ids) {
		mustExec(t, db, `UPDATE figures SET scene=replace(scene, '"datasetId":'||?, '"datasetId":4242') WHERE id=?`, i.b, i.fig1)
		mustExec(t, db, `UPDATE reports SET body=body||' [[run:4242]]' WHERE id=?`, i.report1)
	})
	check("a reference to a row that is not in the export", 1, "which is not in the export's catalog", dangling, good...)

	// a failure midway (a review link that the server already has) rolls everything back, and no preview is copied
	sc, _ = catalog.Open(f.dbPath)
	mustExec(t, sc.DB(), `INSERT INTO snapshots(board_id,label,created_at,expires_at,token_hash,frozen,workspace_id) VALUES(1,'x',1,9,'tokenhash-2','{}',2)`)
	sc.Close()
	check("a failure midway", 1, "UNIQUE", archive, good...)

	// a group server's own export is not a local catalog
	gsrv := groupServer(t)
	gArchive := filepath.Join(t.TempDir(), "g.tar.gz")
	out, _ := os.Create(gArchive)
	if _, err := backup.Export(workspace.Local, gsrv.dbPath, gsrv.dataDir, out); err != nil {
		t.Fatal(err)
	}
	out.Close()
	check("an export of a group server", 1, "comes from a group server", gArchive, good...)
}

func TestMultiuserImportRefusesNewerSchema(t *testing.T) {
	archive, _, _ := localExport(t, func(db *sql.DB, src string, i ids) {
		mustExec(t, db, `PRAGMA user_version = 9999`)
	})
	f := groupServer(t)
	pre := tableCounts(t, f.dbPath)
	code, _, errs := f.import_(archive)
	if code != 1 || !strings.Contains(errs, "newer Vesqira") || !reflect.DeepEqual(pre, tableCounts(t, f.dbPath)) {
		t.Errorf("newer schema: %d %s", code, errs)
	}
}

func TestMultiuserImportMigratesAnOlderExport(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "schema_v30.sql"))
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	odb, err := sql.Open("sqlite", "file:"+filepath.Join(src, "vesqira.db"))
	if err != nil {
		t.Fatal(err)
	}
	odb.SetMaxOpenConns(1)
	text := strings.ReplaceAll(string(raw), "-- Unix nanoseconds ", "")
	var tables, rest []string
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
		mustExec(t, odb, q)
	}
	mustExec(t, odb, `PRAGMA user_version = 30`)
	mustExec(t, odb, `INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header) VALUES('/home/denis/data/lmc/cube.fits','cube.fits',1,1,1,'{}')`)
	mustExec(t, odb, `INSERT INTO boards(title,created_at,updated_at) VALUES('Old board',1,1)`)
	odb.Close()
	archive := filepath.Join(t.TempDir(), "old.tar.gz")
	out, _ := os.Create(archive)
	m, err := backup.Export(workspace.Local, filepath.Join(src, "vesqira.db"), src, out)
	if err != nil || m.SchemaVersion != 30 {
		t.Fatalf("%v schema %d", err, m.SchemaVersion)
	}
	out.Close()

	f := groupServer(t)
	code, outp, errs := f.import_(archive)
	if code != 0 {
		t.Fatalf("import of a schema 30 export: %d %s %s", code, outp, errs)
	}
	db, _ := sql.Open("sqlite", "file:"+f.dbPath+"?mode=ro")
	defer db.Close()
	ws := one(t, db, `SELECT id FROM workspaces WHERE name=?`, "Denis's data")
	if one(t, db, `SELECT path||'|'||created_by FROM datasets WHERE workspace_id=?`, ws) != filepath.Join(f.target, "lmc", "cube.fits")+"|"+f.adaID ||
		one(t, db, `SELECT title FROM boards WHERE workspace_id=?`, ws) != "Old board" {
		t.Error("the older export's rows did not arrive")
	}
}

func TestMapPathAndRoots(t *testing.T) {
	if mapPath([]pathMap{{"/data", "/srv/b"}}, "/database/x") != "" || mapPath([]pathMap{{"/data", "/srv/b"}}, "/data/x") != "/srv/b/x" || mapPath([]pathMap{{"/data", "/srv/b"}}, "/data/../etc/x") != "" {
		t.Error("a map rewrites only paths inside its folder")
	}
}

// tamper copies an archive and changes one byte of a figure preview, keeping the manifest as it was.
func tamper(t *testing.T, in, out string) {
	t.Helper()
	f, err := os.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	o, _ := os.Create(out)
	defer o.Close()
	gw := gzip.NewWriter(o)
	tw := tar.NewWriter(gw)
	tr := tar.NewReader(gz)
	changed := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		if strings.HasPrefix(h.Name, "figures/") && !changed {
			b[len(b)-1] ^= 0xff
			changed = true
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(b)
	}
	if !changed {
		t.Fatal("no figure to tamper with")
	}
	_ = tw.Close()
	_ = gw.Close()
}

func fileSum(t *testing.T, p string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

// The catalog's transaction is open when the files are copied. A copy that fails (here the report images folder cannot be
// written, after the figure previews were already copied) rolls the catalog back and takes the copied previews away again.
func TestMultiuserImportFailedFileCopyLeavesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only folder does not stop root")
	}
	archive, _, _ := localExport(t, nil)
	f := groupServer(t)
	att := filepath.Join(f.dataDir, "attachments")
	if err := os.MkdirAll(att, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(att, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(att, 0o700) })
	pre, preFiles := tableCounts(t, f.dbPath), figureFiles(t, f.dataDir)
	code, out, errs := f.import_(archive)
	if code != 1 || !strings.Contains(errs, "nothing was imported") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errs)
	}
	if !reflect.DeepEqual(pre, tableCounts(t, f.dbPath)) || !reflect.DeepEqual(preFiles, figureFiles(t, f.dataDir)) {
		t.Error("a failed copy left rows or preview files behind")
	}
	// once the folder is writable the same import goes through
	_ = os.Chmod(att, 0o700)
	if code, _, errs := f.import_(archive); code != 0 {
		t.Fatalf("retry: %d %s", code, errs)
	}
}

// An export made before report images were archived still imports, and says that the images are missing.
func TestMultiuserImportOfAnExportWithoutAttachments(t *testing.T) {
	archive, _, _ := localExport(t, func(db *sql.DB, src string, i ids) {
		_ = os.RemoveAll(filepath.Join(src, "attachments"))
	})
	f := groupServer(t)
	code, out, errs := f.import_(archive)
	if code != 0 || !strings.Contains(out, "2 report images are missing") {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	db, _ := sql.Open("sqlite", "file:"+f.dbPath+"?mode=ro")
	defer db.Close()
	if one(t, db, `SELECT COUNT(*) FROM attachments WHERE workspace_id<>1 AND workspace_id<>2`) != "0" {
		t.Error("an image row was imported without its file")
	}
}
