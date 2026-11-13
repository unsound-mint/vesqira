package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// The write of `multiuser import` (ADR 0050 section 9, slice 4). One function, importInto, does the whole job inside one transaction
// of the group server's catalog, and both the dry run and the write call it: the dry run rolls the transaction back, so what it lists
// is what the write does, down to the constraints a row would hit. Rows are copied table by table with their own columns; only the
// ids, the workspace, the author, the paths and the host change, and the places where a row's id is written inside text or JSON
// (figure scenes, report bodies, job requests, mask rules) are rewritten through the same id maps.

// importedHost is the host_id of a row whose file lived on a host of the exporting computer. Hosts are not imported (a host is an
// ssh alias of that computer), so the row must not read as a path on this server (0) or as a host of this server (a positive id).
const importedHost int64 = -1

// ownerAuthor is what the local catalog calls its one person; on the server that person is the importing account.
const ownerAuthor = "local-owner"

type importSpec struct {
	src     *sql.DB
	srcDir  string // the restored archive: figure previews are under figures/
	dstDir  string // the server's data directory
	owner   string // account id
	name    string
	maps    []pathMap
	archive string
}

type countLine struct {
	what string
	n    int
}

type importReport struct {
	workspace  int64
	datasets   []string
	counts     []countLine
	skipped    []string
	unresolved []string
	previews   []previewFile
	projects   []string
	noAttach   int // report images whose file the archive does not hold
	notImport  []countLine
}

// previewFile is one content-addressed file of the archive that a row needs: a figure preview (figures/<sha>.png) or a report
// attachment (attachments/<sha>).
type previewFile struct {
	dir     string // "figures" or "attachments"
	name    string // sha256 hex, plus ".png" for a preview
	from    string
	present bool // already in the server's folder (content-addressed: the same bytes)
	missing bool // not in the archive
}

func (p previewFile) key() string { return p.dir + "/" + p.name }

func (p previewFile) sha() string { return strings.TrimSuffix(p.name, ".png") }

type unresolvedError struct{ lines []string }

func (e *unresolvedError) Error() string {
	n := len(e.lines)
	show := e.lines
	if n > 20 {
		show = show[:20]
	}
	s := fmt.Sprintf("the export cannot be imported as it is (%d problems; nothing was written):\n  %s", n, strings.Join(show, "\n  "))
	if n > 20 {
		s += fmt.Sprintf("\n  ... and %d more", n-20)
	}
	return s
}

// ---- rows ----

type row struct {
	cols []string
	vals []any
}

func (r *row) idx(c string) int {
	for i, x := range r.cols {
		if x == c {
			return i
		}
	}
	return -1
}
func (r *row) get(c string) any {
	if i := r.idx(c); i >= 0 {
		return r.vals[i]
	}
	return nil
}
func (r *row) set(c string, v any) {
	if i := r.idx(c); i >= 0 {
		r.vals[i] = v
	}
}
func (r *row) str(c string) string {
	switch v := r.get(c).(type) {
	case string:
		return v
	case []byte:
		return string(v)
	}
	return ""
}
func (r *row) num(c string) int64 {
	switch v := r.get(c).(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

type querier interface {
	Query(q string, args ...any) (*sql.Rows, error)
}

func readAll(db querier, q string, args ...any) ([]row, error) {
	rs, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		return nil, err
	}
	var out []row
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		out = append(out, row{cols, vals})
	}
	return out, rs.Err()
}

// ---- id maps ----

type idMap struct {
	kind    string
	m       map[int64]int64
	lenient bool // an id that is not in the map is left as written (a saved region a scene drew and the owner later deleted)
}

func newMap(kind string) *idMap { return &idMap{kind: kind, m: map[int64]int64{}} }

type importer struct {
	sp        *importSpec
	tx        *sql.Tx
	ws        int64
	rep       *importReport
	hostNames map[int64]string

	ds, run, job, mask, fig, board, snap, report, region, sub, comment, corr, proj, step *idMap
}

func (im *importer) bad(format string, a ...any) {
	im.rep.unresolved = append(im.rep.unresolved, fmt.Sprintf(format, a...))
}

// to returns the new id for an old one; an id nobody can resolve is recorded and the import is refused at the end, because a
// reference left as written would point at some other row of the new workspace.
func (im *importer) to(m *idMap, old int64, where string) int64 {
	if n, ok := m.m[old]; ok {
		return n
	}
	if m.lenient {
		return old
	}
	im.bad("%s names %s %d, which is not in the export's catalog", where, m.kind, old)
	return 0
}

// copyTable copies the rows of table that q selects. tf may change the row (nil keeps it as it is) or return errSkip. ownID names
// the column whose old value is mapped to the new row id (a table whose key is not its own autoincrement column passes "").
func (im *importer) copyTable(table, q string, ownID string, ids *idMap, tf func(r *row) error) (int, error) {
	rows, err := readAll(im.sp.src, q)
	if err != nil {
		return 0, fmt.Errorf("the exported catalog cannot be read (%s): %w", table, err)
	}
	n := 0
	for i := range rows {
		r := &rows[i]
		old := r.num(ownID)
		if tf != nil {
			if err := tf(r); err != nil {
				if errors.Is(err, errSkip) {
					continue
				}
				return n, err
			}
		}
		var cols []string
		var vals []any
		for j, c := range r.cols {
			if c == ownID && ownID == "id" {
				continue // a new autoincrement id
			}
			cols = append(cols, c)
			vals = append(vals, r.vals[j])
		}
		res, err := im.tx.Exec(`INSERT INTO `+table+`(`+strings.Join(cols, ",")+`) VALUES(`+strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")+`)`, vals...)
		if err != nil {
			return n, fmt.Errorf("the server's catalog refused a row of %s: %w", table, err)
		}
		if ids != nil && ownID != "" {
			nid := old
			if ownID == "id" {
				nid, _ = res.LastInsertId()
			}
			ids.m[old] = nid
		}
		n++
	}
	return n, nil
}

var errSkip = errors.New("skip")

func (im *importer) stamp(r *row) {
	r.set("workspace_id", im.ws)
	r.set("created_by", im.sp.owner)
}

func (im *importer) author(r *row, col string) {
	if r.str(col) == ownerAuthor {
		r.set(col, im.sp.owner)
	}
}

// ---- paths ----

// inRoot reports whether an absolute path lies inside a data root of the new workspace.
func (im *importer) inRoot(p string) (string, bool) {
	p = filepath.Clean(p)
	for _, m := range im.sp.maps {
		if p == m.to || strings.HasPrefix(p, m.to+string(filepath.Separator)) {
			return m.to, true
		}
	}
	return "", false
}

// place decides the path a row of this server gets: the explicit map, or the path as recorded when no map covers it. A recorded
// path that happens to lie inside a data root is refused: the server would read whatever file is there, which is a guess.
func (im *importer) place(where string, host int64, p string) (path string, hostID int64, how string) {
	if host != 0 {
		// a dataset's path on a host is keyed h<id>:<path>; the old id means nothing here, the host's name stays as text
		return fmt.Sprintf("[host %s] %s", im.hostName(host), strings.TrimPrefix(p, fmt.Sprintf("h%d:", host))), importedHost, "host"
	}
	if to := mapPath(im.sp.maps, p); to != "" {
		if _, ok := im.inRoot(to); !ok {
			im.bad("%s: %s maps to %s, outside every data root", where, p, to)
		}
		return to, 0, "mapped"
	}
	if root, ok := im.inRoot(p); ok {
		im.bad("%s: %s is not covered by a --map but lies inside the data root %s; give a --map for it so it is not guessed", where, p, root)
	}
	return p, 0, "kept"
}

var remoteDir = regexp.MustCompile(`^h(\d+):(.*)$`)

// placeRunDir does the same for a run's directory, which a remote run keys as h<host id>:<path>. The old host's name stays as text.
func (im *importer) placeRunDir(where, dir string) string {
	if m := remoteDir.FindStringSubmatch(dir); m != nil {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		return fmt.Sprintf("[host %s] %s", im.hostName(id), m[2])
	}
	p, _, _ := im.place(where, 0, dir)
	return p
}

func (im *importer) hostName(id int64) string {
	if n, ok := im.hostNames[id]; ok {
		return n
	}
	return fmt.Sprintf("#%d (not in the export)", id)
}

// ---- ids written inside JSON and text ----

type idRules struct {
	scalar map[string]*idMap // key -> map: the number under the key is an id
	arrays map[string]*idMap // key -> map: an array of numbers
	items  map[string]*idMap // key -> map: an array of objects whose "id" is an id
}

func (im *importer) sceneRules() idRules {
	return idRules{
		scalar: map[string]*idMap{"datasetId": im.ds, "reliabilityId": im.ds, "figureId": im.fig, "subsetId": im.sub}, // subsetId: a sky view's footprint of a virtual subset
		arrays: map[string]*idMap{"datasets": im.ds, "overlays": im.ds},
		items:  map[string]*idMap{"runs": im.run, "masks": im.mask, "saved": im.region},
	}
}

// remapJSON rewrites the ids a JSON text names, by key, keeping everything else as it was. Reading by key (as internal/access does
// for labels) means a field of a later scene version is still found.
func (im *importer) remapJSON(text, where string, rules idRules) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		im.bad("%s is not readable JSON, so the ids in it cannot be moved: %v", where, err)
		return text
	}
	conv := func(m *idMap, v any) (any, bool) {
		n, ok := v.(json.Number)
		if !ok {
			return v, false
		}
		old, err := n.Int64()
		if err != nil || old <= 0 {
			return v, false
		}
		return json.Number(strconv.FormatInt(im.to(m, old, where), 10)), true
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if m := rules.scalar[k]; m != nil {
					if nv, ok := conv(m, val); ok {
						x[k] = nv
						continue
					}
				}
				if arr, ok := val.([]any); ok {
					if m := rules.arrays[k]; m != nil {
						for i, e := range arr {
							if nv, ok := conv(m, e); ok {
								arr[i] = nv
							}
						}
					}
					if m := rules.items[k]; m != nil {
						for _, e := range arr {
							if obj, ok := e.(map[string]any); ok {
								if nv, ok := conv(m, obj["id"]); ok {
									obj["id"] = nv
								}
							}
						}
					}
				}
				walk(val)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(doc)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		im.bad("%s cannot be written again: %v", where, err)
		return text
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

var runRef = regexp.MustCompile(`\[\[run:(\d+)\]\]`)

func (im *importer) remapRunRefs(text, where string) string {
	return runRef.ReplaceAllStringFunc(text, func(s string) string {
		old, _ := strconv.ParseInt(runRef.FindStringSubmatch(s)[1], 10, 64)
		return fmt.Sprintf("[[run:%d]]", im.to(im.run, old, where))
	})
}

// ---- the import ----

var importTables = []string{"workspaces", "memberships", "workspace_roots", "audit", "datasets", "dataset_locations", "interpretations", "dataset_nature",
	"dataset_units", "catalog_declarations", "dataset_corrections", "dataset_access", "derived", "saved_regions", "masks", "subsets", "subset_versions",
	"runs", "run_review", "run_review_history", "run_outputs", "rohsa_runs", "jobs", "figures", "boards", "board_items", "snapshots", "comments",
	"snapshot_outcomes", "reports", "report_history", "report_links", "archived_entries", "projects", "project_answers", "exploration_steps", "run_views", "attachments"}

func hasTable(db interface {
	QueryRow(string, ...any) *sql.Row
}, name string) bool {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return n == 1
}

const srcWS = 1 // the local catalog's one workspace

func importInto(tx *sql.Tx, sp *importSpec, now int64) (*importReport, error) {
	im := &importer{sp: sp, tx: tx, rep: &importReport{}, hostNames: map[int64]string{}}
	for _, t := range importTables {
		if !hasTable(tx, t) {
			return nil, fmt.Errorf("the server's catalog has no %s table: start the group server once on it so it creates its tables, then import", t)
		}
	}
	im.ds, im.run, im.job, im.mask = newMap("dataset"), newMap("run"), newMap("job"), newMap("mask")
	im.fig, im.board, im.snap, im.report = newMap("figure"), newMap("board"), newMap("snapshot"), newMap("report")
	im.region, im.sub, im.comment, im.corr = newMap("saved region"), newMap("subset"), newMap("comment"), newMap("correction")
	im.proj, im.step = newMap("project"), newMap("exploration step")
	im.region.lenient = true

	if hs, err := readAll(sp.src, `SELECT id, name FROM hosts`); err == nil {
		for i := range hs {
			im.hostNames[hs[i].num("id")] = hs[i].str("name")
		}
	}

	// the workspace, its Owner and its data roots; the insert is the transaction's first statement, so it takes the write lock
	// before the name is judged
	res, err := tx.Exec(`INSERT INTO workspaces(name, created_at, created_by) SELECT ?,?,? WHERE NOT EXISTS(SELECT 1 FROM workspaces WHERE lower(name)=lower(?))`, sp.name, now, sp.owner, sp.name)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("a workspace named %q exists already: an import always goes into a new workspace", sp.name)
	}
	im.ws, _ = res.LastInsertId()
	im.rep.workspace = im.ws
	if _, err := tx.Exec(`INSERT INTO memberships(workspace_id,account_id,role,created_at) VALUES(?,?,'owner',?)`, im.ws, sp.owner, now); err != nil {
		return nil, err
	}
	seenRoot := map[string]bool{}
	for _, m := range sp.maps {
		if seenRoot[m.to] {
			continue
		}
		seenRoot[m.to] = true
		if _, err := tx.Exec(`INSERT INTO workspace_roots(workspace_id,path,added_at,added_by) VALUES(?,?,?,?)`, im.ws, m.to, now, sp.owner); err != nil {
			return nil, err
		}
	}

	// datasets, and what hangs off them
	cp := func(table, q, own string, ids *idMap, tf func(*row) error) error {
		n, err := im.copyTable(table, q, own, ids, tf)
		if err != nil {
			return err
		}
		if table != "" {
			im.rep.noteTable(table, n)
		}
		return nil
	}
	dsScope := `dataset_id IN (SELECT id FROM datasets WHERE workspace_id=1)`
	var plan []string
	err = cp("datasets", `SELECT * FROM datasets WHERE workspace_id=1 ORDER BY id`, "id", im.ds, func(r *row) error {
		name, host, p := r.str("name"), r.num("host_id"), r.str("path")
		to, hid, how := im.place("dataset "+name, host, p)
		im.stamp(r)
		r.set("path", to)
		r.set("host_id", hid)
		switch how {
		case "host":
			plan = append(plan, fmt.Sprintf("  %s: on host %q; kept as a record of a file not on this server (hosts are not imported)", name, im.hostNames[host]))
		case "mapped":
			state := "present"
			if _, err := os.Stat(to); err != nil {
				state = "not found there: it will be shown as missing"
			}
			plan = append(plan, fmt.Sprintf("  %s: %s -> %s (%s)", name, p, to, state))
		default:
			plan = append(plan, fmt.Sprintf("  %s: %s; no --map covers it, so it is kept as a record of a file not on this server, never guessed", name, p))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	im.rep.datasets = plan

	// A location row keeps what was recorded about the file: size, time and both fingerprints as declared by whoever took them.
	// The device and inode are the old computer's and mean nothing here, so they are cleared (unrecorded).
	if err := cp("dataset_locations", `SELECT * FROM dataset_locations WHERE workspace_id=1 ORDER BY id`, "id", nil, func(r *row) error {
		ds, ok := im.ds.m[r.num("dataset_id")]
		if !ok {
			return errSkip
		}
		to, hid, _ := im.place(fmt.Sprintf("a location of dataset %d", r.num("dataset_id")), r.num("host_id"), r.str("path"))
		im.stamp(r)
		r.set("dataset_id", ds)
		r.set("path", to)
		r.set("host_id", hid)
		r.set("file_dev", int64(0))
		r.set("file_ino", int64(0))
		return nil
	}); err != nil {
		return nil, err
	}
	child := func(r *row) error {
		r.set("dataset_id", im.to(im.ds, r.num("dataset_id"), "a record of dataset "+strconv.FormatInt(r.num("dataset_id"), 10)))
		return nil
	}
	for _, t := range []string{"interpretations", "dataset_nature", "dataset_units", "catalog_declarations"} {
		tf := child
		if t == "interpretations" {
			tf = func(r *row) error {
				_ = child(r)
				if v := r.get("reliability_id"); v != nil {
					r.set("reliability_id", im.to(im.ds, r.num("reliability_id"), "the reliability map of dataset "+strconv.FormatInt(r.num("dataset_id"), 10)))
				}
				return nil
			}
		}
		if err := cp(t, `SELECT * FROM `+t+` WHERE `+dsScope+` ORDER BY dataset_id`, "dataset_id", nil, tf); err != nil {
			return nil, err
		}
	}
	if err := cp("dataset_corrections", `SELECT * FROM dataset_corrections WHERE `+dsScope+` ORDER BY id`, "id", im.corr, func(r *row) error {
		_ = child(r)
		if v := r.get("reverts_id"); v != nil {
			r.set("reverts_id", im.to(im.corr, r.num("reverts_id"), "a correction"))
		}
		im.author(r, "author")
		return nil
	}); err != nil {
		return nil, err
	}
	if err := cp("dataset_access", `SELECT * FROM dataset_access WHERE workspace_id=1 ORDER BY id`, "id", nil, func(r *row) error {
		ds, ok := im.ds.m[r.num("dataset_id")]
		if !ok {
			im.rep.skipped = append(im.rep.skipped, fmt.Sprintf("an access label record of %q, a dataset that is no longer in the catalog", r.str("dataset_name")))
			return errSkip
		}
		im.stamp(r)
		r.set("dataset_id", ds)
		im.author(r, "author")
		return nil
	}); err != nil {
		return nil, err
	}
	if err := cp("derived", `SELECT * FROM derived WHERE `+dsScope+` ORDER BY dataset_id`, "dataset_id", nil, func(r *row) error {
		_ = child(r)
		var srcIDs []int64
		if json.Unmarshal([]byte(r.str("sources")), &srcIDs) == nil {
			for i, s := range srcIDs {
				srcIDs[i] = im.to(im.ds, s, "the sources of a derived dataset")
			}
			b, _ := json.Marshal(srcIDs)
			r.set("sources", string(b))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := cp("saved_regions", `SELECT * FROM saved_regions WHERE workspace_id=1 ORDER BY id`, "id", im.region, func(r *row) error {
		im.stamp(r)
		_ = child(r)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := cp("masks", `SELECT * FROM masks WHERE workspace_id=1 ORDER BY id`, "id", im.mask, func(r *row) error {
		where := fmt.Sprintf("mask %q", r.str("name"))
		to, hid, _ := im.place(where, r.num("host_id"), r.str("path"))
		im.stamp(r)
		r.set("dataset_id", im.to(im.ds, r.num("dataset_id"), where))
		r.set("path", to)
		r.set("host_id", hid)
		r.set("rule", im.remapJSON(r.str("rule"), where+" (its rule)", idRules{scalar: map[string]*idMap{"sourceDatasetId": im.ds}}))
		im.author(r, "author")
		return nil
	}); err != nil {
		return nil, err
	}
	if err := cp("subsets", `SELECT * FROM subsets WHERE workspace_id=1 ORDER BY id`, "id", im.sub, func(r *row) error {
		im.stamp(r)
		r.set("source_id", im.to(im.ds, r.num("source_id"), "a subset"))
		return nil
	}); err != nil {
		return nil, err
	}
	if err := cp("subset_versions", `SELECT * FROM subset_versions WHERE subset_id IN (SELECT id FROM subsets WHERE workspace_id=1) ORDER BY subset_id, version`, "", nil, func(r *row) error {
		r.set("subset_id", im.to(im.sub, r.num("subset_id"), "a subset version"))
		im.author(r, "author")
		return nil
	}); err != nil {
		return nil, err
	}

	// runs and the job records
	runScope := `run_id IN (SELECT id FROM runs WHERE workspace_id=1)`
	if err := cp("runs", `SELECT * FROM runs WHERE workspace_id=1 ORDER BY id`, "id", im.run, func(r *row) error {
		im.stamp(r)
		r.set("dir", im.placeRunDir("run "+r.str("name"), r.str("dir")))
		return nil
	}); err != nil {
		return nil, err
	}
	runChild := func(r *row) error {
		r.set("run_id", im.to(im.run, r.num("run_id"), "a record of run "+strconv.FormatInt(r.num("run_id"), 10)))
		return nil
	}
	if err := cp("run_review", `SELECT * FROM run_review WHERE `+runScope+` ORDER BY run_id`, "run_id", nil, runChild); err != nil {
		return nil, err
	}
	if err := cp("run_review_history", `SELECT * FROM run_review_history WHERE `+runScope+` ORDER BY id`, "id", nil, runChild); err != nil {
		return nil, err
	}
	if err := cp("run_outputs", `SELECT * FROM run_outputs WHERE `+runScope+` ORDER BY run_id, product`, "", nil, func(r *row) error {
		_ = runChild(r)
		r.set("dataset_id", im.to(im.ds, r.num("dataset_id"), "an output of a run"))
		return nil
	}); err != nil {
		return nil, err
	}
	if err := cp("rohsa_runs", `SELECT * FROM rohsa_runs WHERE `+runScope+` ORDER BY run_id`, "", nil, func(r *row) error {
		_ = runChild(r)
		r.set("dat_path", im.placeRunDir("a ROHSA run", r.str("dat_path")))
		return nil
	}); err != nil {
		return nil, err
	}
	// Jobs come in as records. Their host is not imported, so its name goes in front of the note as text, the job is tied to no host
	// and nothing owed or automatic stays on, so no unattended work starts from them. State, script and paths are what the exporting
	// computer last recorded.
	if err := cp("jobs", `SELECT * FROM jobs WHERE workspace_id=1 ORDER BY id`, "id", im.job, func(r *row) error {
		im.stamp(r)
		host := im.hostName(r.num("host_id"))
		r.set("note", fmt.Sprintf("Imported record: this job ran on the host %q of the exporting computer, which is not part of this server. %s", host, r.str("note")))
		r.set("host_id", int64(0))
		r.set("record_owed", int64(0))
		r.set("auto_import", int64(0))
		r.set("auto_import_tried", int64(0))
		r.set("request_ref", "")
		if v := r.get("run_id"); v != nil {
			r.set("run_id", im.to(im.run, r.num("run_id"), "a job"))
		}
		if v := r.get("parent_run_id"); v != nil {
			r.set("parent_run_id", im.to(im.run, r.num("parent_run_id"), "a job"))
		}
		r.set("request", im.remapJSON(r.str("request"), "the request of job "+r.str("name"), idRules{scalar: map[string]*idMap{"sourceRunId": im.run}}))
		return nil
	}); err != nil {
		return nil, err
	}

	// figures, boards, review snapshots
	if err := cp("figures", `SELECT * FROM figures WHERE workspace_id=1 ORDER BY id`, "id", im.fig, func(r *row) error {
		where := fmt.Sprintf("figure %q", r.str("title"))
		im.stamp(r)
		if v := r.get("dataset_id"); v != nil {
			r.set("dataset_id", im.to(im.ds, r.num("dataset_id"), where))
		}
		rules := im.sceneRules()
		r.set("scene", im.remapJSON(r.str("scene"), where+" (its scene)", rules))
		r.set("identity", im.remapJSON(r.str("identity"), where+" (its identity record)", rules))
		sha := r.str("preview_sha")
		pf := previewFile{dir: "figures", name: sha + ".png", from: filepath.Join(sp.srcDir, "figures", sha+".png")}
		if _, err := os.Stat(pf.from); err != nil {
			pf.missing = true
		}
		if _, err := os.Stat(filepath.Join(sp.dstDir, "figures", pf.name)); err == nil {
			pf.present = true
		}
		im.rep.previews = append(im.rep.previews, pf)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := cp("boards", `SELECT * FROM boards WHERE workspace_id=1 ORDER BY id`, "id", im.board, func(r *row) error {
		im.stamp(r)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := cp("board_items", `SELECT * FROM board_items WHERE board_id IN (SELECT id FROM boards WHERE workspace_id=1) ORDER BY board_id, position`, "", nil, func(r *row) error {
		r.set("board_id", im.to(im.board, r.num("board_id"), "a board item"))
		if r.get("figure_id") != nil {
			r.set("figure_id", im.to(im.fig, r.num("figure_id"), "a board item"))
		}
		if r.get("job_id") != nil {
			r.set("job_id", im.to(im.job, r.num("job_id"), "a board item"))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	snapScope := `snapshot_id IN (SELECT id FROM snapshots WHERE workspace_id=1)`
	if err := cp("snapshots", `SELECT * FROM snapshots WHERE workspace_id=1 ORDER BY id`, "id", im.snap, func(r *row) error {
		im.stamp(r)
		r.set("board_id", im.to(im.board, r.num("board_id"), "a review snapshot"))
		return nil
	}); err != nil {
		return nil, err
	}
	// superseded_by points at a later snapshot, which the first pass had not inserted yet
	sup, err := readAll(sp.src, `SELECT id, superseded_by FROM snapshots WHERE workspace_id=1 AND superseded_by<>0`)
	if err != nil {
		return nil, err
	}
	for i := range sup {
		if _, err := tx.Exec(`UPDATE snapshots SET superseded_by=? WHERE id=? AND workspace_id=?`, im.to(im.snap, sup[i].num("superseded_by"), "a superseded review snapshot"), im.snap.m[sup[i].num("id")], im.ws); err != nil {
			return nil, err
		}
	}
	if err := cp("comments", `SELECT * FROM comments WHERE `+snapScope+` ORDER BY id`, "id", im.comment, func(r *row) error {
		r.set("snapshot_id", im.to(im.snap, r.num("snapshot_id"), "a reviewer's comment"))
		if p := r.num("parent_id"); p != 0 {
			r.set("parent_id", im.to(im.comment, p, "a reply"))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := cp("snapshot_outcomes", `SELECT * FROM snapshot_outcomes WHERE `+snapScope+` ORDER BY id`, "id", nil, func(r *row) error {
		r.set("snapshot_id", im.to(im.snap, r.num("snapshot_id"), "an outcome of a review snapshot"))
		return nil
	}); err != nil {
		return nil, err
	}

	// reports
	if err := cp("reports", `SELECT * FROM reports WHERE workspace_id=1 ORDER BY id`, "id", im.report, func(r *row) error {
		im.stamp(r)
		r.set("body", im.remapRunRefs(r.str("body"), fmt.Sprintf("report %q", r.str("title"))))
		return nil
	}); err != nil {
		return nil, err
	}
	pages, err := readAll(sp.src, `SELECT id, parent_id FROM reports WHERE workspace_id=1 AND parent_id<>0`)
	if err != nil {
		return nil, err
	}
	for i := range pages {
		if _, err := tx.Exec(`UPDATE reports SET parent_id=? WHERE id=? AND workspace_id=?`, im.to(im.report, pages[i].num("parent_id"), "a report page"), im.report.m[pages[i].num("id")], im.ws); err != nil {
			return nil, err
		}
	}
	repScope := `report_id IN (SELECT id FROM reports WHERE workspace_id=1)`
	repChild := func(r *row) error {
		r.set("report_id", im.to(im.report, r.num("report_id"), "a record of a report"))
		return nil
	}
	if err := cp("report_history", `SELECT * FROM report_history WHERE `+repScope+` ORDER BY report_id, revision`, "", nil, func(r *row) error {
		_ = repChild(r)
		im.author(r, "author")
		r.set("body", im.remapRunRefs(r.str("body"), "an earlier revision of a report"))
		return nil
	}); err != nil {
		return nil, err
	}
	if err := cp("report_links", `SELECT * FROM report_links WHERE `+repScope+` ORDER BY report_id, kind, target`, "", nil, func(r *row) error {
		_ = repChild(r)
		if r.str("kind") == "run" {
			old, _ := strconv.ParseInt(r.str("target"), 10, 64)
			r.set("target", strconv.FormatInt(im.to(im.run, old, "a link of a report"), 10))
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// report images: the row is imported only with its file; an export from before attachments were archived has the rows only
	if err := cp("attachments", `SELECT * FROM attachments WHERE workspace_id=1 ORDER BY sha`, "", nil, func(r *row) error {
		sha := r.str("sha")
		pf := previewFile{dir: "attachments", name: sha, from: filepath.Join(sp.srcDir, "attachments", sha)}
		if _, err := os.Stat(pf.from); err != nil {
			im.rep.noAttach++
			return errSkip
		}
		if _, err := os.Stat(filepath.Join(sp.dstDir, "attachments", sha)); err == nil {
			pf.present = true
		}
		im.rep.previews = append(im.rep.previews, pf)
		im.stamp(r)
		return nil
	}); err != nil {
		return nil, err
	}

	// the archive marks of entries that were archived instead of deleted
	if err := cp("archived_entries", `SELECT * FROM archived_entries ORDER BY kind, entity_id`, "", nil, func(r *row) error {
		var m *idMap
		switch r.str("kind") {
		case "dataset":
			m = im.ds
		case "run":
			m = im.run
		case "figure":
			m = im.fig
		case "board":
			m = im.board
		default:
			return errSkip
		}
		n, ok := m.m[r.num("entity_id")]
		if !ok {
			return errSkip
		}
		r.set("entity_id", n)
		return nil
	}); err != nil {
		return nil, err
	}

	// working state: opened projects with the researcher's answers, saved run views and the exploration trail
	if err := cp("projects", `SELECT * FROM projects WHERE workspace_id=1 ORDER BY id`, "id", im.proj, func(r *row) error {
		where := fmt.Sprintf("project %q", r.str("name"))
		oldHost := r.num("host_id")
		to, hid, how := im.place(where, oldHost, r.str("root"))
		im.stamp(r)
		r.set("root", to)
		r.set("host_id", hid)
		switch how {
		case "host":
			im.rep.projects = append(im.rep.projects, fmt.Sprintf("  %s: on host %q; its folder stays as text, not a folder of this server", r.str("name"), im.hostNames[oldHost]))
		case "mapped":
			im.rep.projects = append(im.rep.projects, fmt.Sprintf("  %s: -> %s", r.str("name"), to))
		default:
			im.rep.projects = append(im.rep.projects, fmt.Sprintf("  %s: %s; no --map covers it, so it is shown as unavailable", r.str("name"), to))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := cp("project_answers", `SELECT * FROM project_answers WHERE project_id IN (SELECT id FROM projects WHERE workspace_id=1) ORDER BY project_id, need`, "", nil, func(r *row) error {
		r.set("project_id", im.to(im.proj, r.num("project_id"), "an answer of a project"))
		return nil
	}); err != nil {
		return nil, err
	}
	if err := cp("run_views", `SELECT * FROM run_views WHERE workspace_id=1 ORDER BY id`, "id", nil, func(r *row) error {
		im.stamp(r)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := cp("exploration_steps", `SELECT * FROM exploration_steps WHERE workspace_id=1 ORDER BY id`, "id", im.step, func(r *row) error {
		ds, ok := im.ds.m[r.num("dataset_id")]
		if !ok { // the trail of a dataset that is no longer in the catalog
			im.rep.skipped = append(im.rep.skipped, fmt.Sprintf("an exploration step (%q) on a dataset that is no longer in the catalog", r.str("label")))
			return errSkip
		}
		where := fmt.Sprintf("exploration step %q", r.str("label"))
		im.stamp(r)
		r.set("dataset_id", ds)
		if p := r.num("project_id"); p != 0 {
			r.set("project_id", im.to(im.proj, p, where))
		}
		if r.get("parent_id") != nil {
			r.set("parent_id", im.to(im.step, r.num("parent_id"), where))
		}
		r.set("scene", im.remapJSON(r.str("scene"), where+" (its scene)", im.sceneRules()))
		return nil
	}); err != nil {
		return nil, err
	}

	if len(im.rep.unresolved) > 0 {
		return im.rep, &unresolvedError{im.rep.unresolved}
	}

	// what is not imported, counted from the export
	for _, t := range []struct{ table, what string }{
		{"hosts", "hosts (ssh aliases of the exporting computer, not something a group server holds)"},
		{"sweeps", "parameter sweeps (they submit jobs, which a group server does not)"}, {"transfers", "file transfers (rsync over the exporting computer's ssh)"},
		{"offline_entries", "offline copies (cached on the exporting computer)"},
		{"notifications", "notifications (job and run events for the person at that computer, not research state)"},
		{"report_publications", "report publications (they belong to a publish server with its own key, not yet per workspace)"},
	} {
		if n, ok := count(sp.src, t.table); ok && n > 0 {
			im.rep.notImport = append(im.rep.notImport, countLine{t.what, int(n)})
		}
	}

	detail := fmt.Sprintf("from the export %s by the command line: %s", sp.archive, im.rep.summary())
	if _, err := tx.Exec(`INSERT INTO audit(at,action,kind,target,detail,actor,on_behalf_of,via,workspace_id) VALUES(?,?,?,?,?,'system','','cli',?)`,
		now, "workspace imported from a local export", "workspace", im.ws, detail, im.ws); err != nil {
		return nil, err
	}
	return im.rep, nil
}

func (r *importReport) noteTable(table string, n int) {
	labels := map[string]string{
		"datasets": "datasets", "figures": "figures", "boards": "boards", "snapshots": "review snapshots (links to an old review address must be reissued)",
		"comments": "reviewers' comments", "reports": "reports", "runs": "runs (kept as read-only records naming their old host as text)", "masks": "masks",
		"saved_regions": "regions", "subsets": "subsets", "jobs": "jobs (read-only records)", "dataset_corrections": "corrections", "dataset_access": "access label records",
		"report_history": "report revisions", "projects": "projects (their folders go through --map)", "project_answers": "project answers", "run_views": "saved run views",
		"exploration_steps": "exploration steps", "attachments": "report images",
	}
	if l, ok := labels[table]; ok {
		r.counts = append(r.counts, countLine{l, n})
	}
}

func (r *importReport) summary() string {
	var parts []string
	for _, c := range r.counts {
		parts = append(parts, fmt.Sprintf("%d %s", c.n, strings.SplitN(c.what, " (", 2)[0]))
	}
	return strings.Join(parts, ", ")
}

// copyPreviews puts the archive's figure previews and report images into the server's folders before the catalog commits. They are
// content-addressed, so a file that is already there is the same bytes; the file's content must match its name. It returns what
// it created, so a failed import can take those files away again.
func copyPreviews(rep *importReport, dstDir string) (created []string, err error) {
	seen := map[string]bool{}
	for _, p := range rep.previews {
		if p.missing || p.present || seen[p.key()] {
			continue
		}
		seen[p.key()] = true
		folder := filepath.Join(dstDir, p.dir)
		if err := os.MkdirAll(folder, 0o700); err != nil {
			return created, err
		}
		b, err := os.ReadFile(p.from)
		if err != nil {
			return created, err
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != p.sha() {
			return created, fmt.Errorf("the file %s in the archive does not match its name", p.key())
		}
		dst := filepath.Join(folder, p.name)
		tmp, err := os.CreateTemp(folder, ".tmp-*")
		if err != nil {
			return created, err
		}
		_, werr := tmp.Write(b)
		if werr == nil {
			werr = tmp.Sync()
		}
		if cerr := tmp.Close(); werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = os.Rename(tmp.Name(), dst)
		}
		if werr != nil {
			_ = os.Remove(tmp.Name())
			return created, werr
		}
		created = append(created, dst)
	}
	return created, nil
}

func removeAll(paths []string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

// printPlan lists what the import does (or did): the same text for the dry run and the write.
func printPlan(w io.Writer, rep *importReport, maps []pathMap) {
	if len(maps) > 0 {
		fmt.Fprintln(w, "Data roots the workspace gets (the --map targets):")
		for _, x := range maps {
			fmt.Fprintf(w, "  %s  (from %s)\n", x.to, x.from)
		}
		fmt.Fprintln(w)
	}
	var mapped, kept, onHosts int
	for _, l := range rep.datasets {
		switch {
		case strings.Contains(l, " -> "):
			mapped++
		case strings.Contains(l, "; kept as a record of a file not on this server (hosts"):
			onHosts++
		default:
			kept++
		}
	}
	fmt.Fprintln(w, "Datasets:")
	for _, l := range rep.datasets {
		fmt.Fprintln(w, l)
	}
	fmt.Fprintf(w, "  %d rewritten through a map, %d not covered, %d on hosts.\n\n", mapped, kept, onHosts)
	fmt.Fprintln(w, "Rows:")
	for _, c := range rep.counts {
		fmt.Fprintf(w, "  %d %s\n", c.n, c.what)
	}
	if len(rep.projects) > 0 {
		fmt.Fprintln(w, "  project folders:")
		for _, l := range rep.projects {
			fmt.Fprintln(w, "  "+l)
		}
	}
	for _, d := range []struct{ dir, what string }{{"figures", "figure preview pictures"}, {"attachments", "report images"}} {
		var n, have, miss int
		seen := map[string]bool{}
		for _, p := range rep.previews {
			if p.dir != d.dir || seen[p.key()] {
				continue
			}
			seen[p.key()] = true
			n++
			switch {
			case p.missing:
				miss++
			case p.present:
				have++
			}
		}
		if n > 0 {
			fmt.Fprintf(w, "  %d %s (%d already on this server, %d missing from the archive)\n", n, d.what, have, miss)
		}
	}
	if rep.noAttach > 0 {
		fmt.Fprintf(w, "  %d report images are missing: their files are not in this archive (exports made before attachments were archived do not hold them), so those reports show broken images\n", rep.noAttach)
	}
	for _, s := range rep.skipped {
		fmt.Fprintf(w, "  not imported: %s\n", s)
	}
	fmt.Fprintln(w, "\nNever imported: hosts (their destinations are ssh aliases of the exporting computer), the publisher key (not in an export), tokens and keys.")
	if len(rep.notImport) > 0 {
		fmt.Fprintln(w, "Left out of this export's catalog:")
		for _, c := range rep.notImport {
			fmt.Fprintf(w, "  %d %s\n", c.n, c.what)
		}
	}
	fmt.Fprintln(w, "Managed copies and derived results in the archive are not copied: a dataset that lived there is imported through a --map, or shown as missing.")
}
