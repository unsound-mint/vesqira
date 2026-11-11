// Package lifecycle archives and deletes catalog entries (datasets, runs, figures, boards) and says, before it happens,
// what depends on them (ADR 0041). It works on the shared catalog database with plain SQL because an entry's
// dependents live in the tables of many packages; it imports none of them.
//
// Deleting an entry only forgets it. No file on disk or on a host is ever touched here; the one case where Vesqira
// owns bytes (a managed copy, an analysis result) is decided by the caller after the entry is gone.
package lifecycle

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	KindDataset = "dataset"
	KindRun     = "run"
	KindFigure  = "figure"
	KindBoard   = "board"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrChanged: what depends on the entry is no longer what the person was shown.
	ErrChanged = errors.New("what depends on this changed since the impact preview")
	// ErrBlocked: something pins the entry (a published review, a board, a report); archive it instead.
	ErrBlocked = errors.New("blocked")
)

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db} }

type querier interface {
	Query(q string, args ...any) (*sql.Rows, error)
	QueryRow(q string, args ...any) *sql.Row
	Exec(q string, args ...any) (sql.Result, error)
}

// Item is one dependent, named for a person.
type Item struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
}

// Group is one kind of dependent. Effect says what deleting does to it: "stays" (kept, unchanged), "unlinked" (kept,
// but no longer connected to the entry), "removed" (goes with the entry) or "blocks" (deletion is refused until it is
// dealt with). Items lists at most maxItems; Count is the full number.
type Group struct {
	Key    string `json:"key"`
	Effect string `json:"effect"`
	Text   string `json:"text"`
	Count  int    `json:"count"`
	Items  []Item `json:"items"`
}

const maxItems = 12

// Impact is the preview. Digest identifies exactly this set of dependents: delete takes it back and is refused if the
// dependents differ by then.
type Impact struct {
	Kind     string   `json:"kind"`
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Archived bool     `json:"archived"`
	Groups   []Group  `json:"groups"`
	Blocked  bool     `json:"blocked"`
	Blockers []string `json:"blockers"`
	Digest   string   `json:"digest"`
}

func has(q querier, table string) bool {
	var n int
	return q.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n) == nil && n > 0
}

// items runs a query returning (id, label) rows; a missing table is no dependents.
func items(q querier, table, query string, args ...any) ([]Item, error) {
	if !has(q, table) {
		return nil, nil
	}
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.Label); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

type builder struct {
	im  *Impact
	ids map[string][]int64 // for the digest: every id of every group, not only the ones listed
}

// add appends a group when it has members; text receives the count and returns the plain-words sentence.
func (b *builder) add(key, effect string, its []Item, text func(n int) string) {
	if len(its) == 0 {
		return
	}
	sort.Slice(its, func(i, j int) bool { return its[i].ID < its[j].ID })
	g := Group{Key: key, Effect: effect, Text: text(len(its)), Count: len(its), Items: its}
	ids := make([]int64, len(its))
	for i, it := range its {
		ids[i] = it.ID
	}
	b.ids[key] = ids
	if len(g.Items) > maxItems {
		g.Items = g.Items[:maxItems]
	}
	b.im.Groups = append(b.im.Groups, g)
	if effect == "blocks" {
		b.im.Blocked = true
		b.im.Blockers = append(b.im.Blockers, g.Text)
	}
}

func (b *builder) finish() {
	type g struct {
		Key    string  `json:"k"`
		Effect string  `json:"e"`
		IDs    []int64 `json:"i"`
	}
	d := struct {
		Kind   string `json:"kind"`
		ID     int64  `json:"id"`
		Groups []g    `json:"groups"`
	}{Kind: b.im.Kind, ID: b.im.ID, Groups: []g{}}
	for _, gr := range b.im.Groups {
		d.Groups = append(d.Groups, g{gr.Key, gr.Effect, b.ids[gr.Key]})
	}
	raw, _ := json.Marshal(d)
	sum := sha256.Sum256(raw)
	b.im.Digest = hex.EncodeToString(sum[:16])
	if b.im.Groups == nil {
		b.im.Groups = []Group{}
	}
	if b.im.Blockers == nil {
		b.im.Blockers = []string{}
	}
}

// Impact reports what depends on one entry.
func (s *Service) Impact(kind string, id int64) (Impact, error) { return impact(s.db, kind, id) }

func impact(q querier, kind string, id int64) (Impact, error) {
	im := Impact{Kind: kind, ID: id}
	b := &builder{im: &im, ids: map[string][]int64{}}
	table := map[string]string{KindDataset: "datasets", KindRun: "runs", KindFigure: "figures", KindBoard: "boards"}[kind]
	col := map[string]string{KindDataset: "name", KindRun: "name", KindFigure: "title", KindBoard: "title"}[kind]
	if table == "" || !has(q, table) || q.QueryRow(`SELECT `+col+` FROM `+table+` WHERE id=?`, id).Scan(&im.Name) != nil {
		return im, ErrNotFound
	}
	var n int
	_ = q.QueryRow(`SELECT COUNT(*) FROM archived_entries WHERE kind=? AND entity_id=?`, kind, id).Scan(&n)
	im.Archived = n > 0
	var err error
	switch kind {
	case KindDataset:
		err = datasetImpact(q, b, id)
	case KindRun:
		err = runImpact(q, b, id)
	case KindFigure:
		err = figureImpact(q, b, id)
	case KindBoard:
		err = boardImpact(q, b, id)
	}
	b.finish()
	return im, err
}

// inList is "?,?,?" for n placeholders with the ids as arguments.
func inList(vals []int64) (string, []any) {
	args := make([]any, len(vals))
	for i, v := range vals {
		args[i] = v
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(vals)), ","), args
}

// figuresOf lists the figures made from any of the datasets, with their revisions.
func figuresOf(q querier, datasetIDs ...int64) (figs []Item, revs []string, err error) {
	if !has(q, "figures") || len(datasetIDs) == 0 {
		return nil, nil, nil
	}
	in, args := inList(datasetIDs)
	rows, err := q.Query(`SELECT id,title,revision FROM figures WHERE dataset_id IN (`+in+`) ORDER BY id`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var it Item
		var rev string
		if err := rows.Scan(&it.ID, &it.Label, &rev); err != nil {
			return nil, nil, err
		}
		figs, revs = append(figs, it), append(revs, rev)
	}
	return figs, revs, rows.Err()
}

func boardsHolding(q querier, figureIDs []int64) ([]Item, error) {
	if len(figureIDs) == 0 {
		return nil, nil
	}
	in, args := inList(figureIDs)
	return items(q, "board_items", `SELECT DISTINCT b.id,b.title FROM boards b JOIN board_items i ON i.board_id=b.id WHERE i.figure_id IN (`+in+`)`, args...)
}

// snapshotsPinning lists the published reviews whose frozen copy holds one of the figure revisions. A snapshot holds its
// own copy of what it shows, so it keeps working without the figure.
func snapshotsPinning(q querier, revs []string) ([]Item, error) {
	if len(revs) == 0 {
		return nil, nil
	}
	cond := strings.TrimSuffix(strings.Repeat("instr(frozen, ?)>0 OR ", len(revs)), " OR ")
	args := make([]any, len(revs))
	for i, r := range revs {
		args[i] = `"revision":"` + r + `"`
	}
	return items(q, "snapshots", `SELECT id,label FROM snapshots WHERE `+cond, args...)
}

func idsOf(its []Item) []int64 {
	out := make([]int64, len(its))
	for i, it := range its {
		out[i] = it.ID
	}
	return out
}

// pinnedDependents adds what hangs off figures made from a dataset or a run's outputs: boards and published reviews.
// They stay as they are.
func pinnedDependents(q querier, b *builder, figs []Item, revs []string, what string) error {
	b.add("figures", "stays", figs, func(n int) string {
		return plural(n, "figure was", "figures were") + " made from " + what + ". They stay: a figure is pinned evidence with its own picture and provenance."
	})
	bs, err := boardsHolding(q, idsOf(figs))
	if err != nil {
		return err
	}
	b.add("boards", "stays", bs, func(n int) string {
		return plural(n, "board holds", "boards hold") + " those figures. Nothing on them changes."
	})
	sn, err := snapshotsPinning(q, revs)
	if err != nil {
		return err
	}
	b.add("snapshots", "stays", sn, func(n int) string {
		return plural(n, "published review shows", "published reviews show") + " those figures. A review keeps its own frozen copy, so it keeps working for the reviewer."
	})
	return nil
}

func datasetImpact(q querier, b *builder, id int64) error {
	figs, revs, err := figuresOf(q, id)
	if err != nil {
		return err
	}
	if err := pinnedDependents(q, b, figs, revs, "this dataset"); err != nil {
		return err
	}
	rs, err := items(q, "run_outputs", `SELECT r.id,r.name FROM runs r JOIN run_outputs o ON o.run_id=r.id WHERE o.dataset_id=?`, id)
	if err != nil {
		return err
	}
	b.add("runs", "unlinked", rs, func(n int) string {
		return plural(n, "run lists", "runs list") + " this dataset as an output. The run stays; this link is removed."
	})
	ds, err := items(q, "derived", `SELECT d.dataset_id, COALESCE(ds.name, 'dataset '||d.dataset_id) FROM derived d, json_each(d.sources) j
		LEFT JOIN datasets ds ON ds.id=d.dataset_id WHERE j.value=? AND d.dataset_id<>?`, id, id)
	if err != nil {
		return err
	}
	b.add("derived", "stays", ds, func(n int) string {
		return plural(n, "dataset was", "datasets were") + " derived from this one. They stay; their provenance will name a dataset that is no longer in the catalog."
	})
	users, err := items(q, "interpretations", `SELECT i.dataset_id, ds.name FROM interpretations i JOIN datasets ds ON ds.id=i.dataset_id WHERE i.reliability_id=? AND i.dataset_id<>?`, id, id)
	if err != nil {
		return err
	}
	b.add("reliability", "blocks", users, func(n int) string {
		return plural(n, "dataset uses", "datasets use") + " this one as the reliability map of its orientation declaration. Clear that declaration first."
	})
	regs, err := items(q, "saved_regions", `SELECT id,name FROM saved_regions WHERE dataset_id=?`, id)
	if err != nil {
		return err
	}
	locs, err := items(q, "dataset_locations", `SELECT id, path FROM dataset_locations WHERE dataset_id=? AND is_primary=0`, id)
	if err != nil {
		return err
	}
	b.add("locations", "removed", locs, func(n int) string {
		return plural(n, "other location is", "other locations are") + " forgotten with it. The files themselves are never deleted."
	})
	steps, err := items(q, "exploration_steps", `SELECT id, label FROM exploration_steps WHERE dataset_id=?`, id)
	if err != nil {
		return err
	}
	b.add("history", "removed", steps, func(n int) string {
		return plural(n, "step of the exploration history goes", "steps of the exploration history go") + " with it (a step opens this dataset, so it could no longer be continued)."
	})
	b.add("regions", "removed", regs, func(n int) string {
		return plural(n, "saved region goes", "saved regions go") + " with it."
	})
	// A mask is a derived resource of its dataset: it goes with it from the catalog (its file on disk is never deleted). A result made
	// with it keeps its own record of the mask (type, rule, policy, fingerprint), so its provenance stays whole.
	mk, err := items(q, "masks", `SELECT id, type||' mask '||name FROM masks WHERE dataset_id=?`, id)
	if err != nil {
		return err
	}
	b.add("masks", "removed", mk, func(n int) string {
		return plural(n, "mask goes", "masks go") + " with it; the mask files stay on disk. Results made with them keep their own record of the masks."
	})
	var decl []Item
	for i, d := range []struct{ table, label string }{{"interpretations", "orientation declaration"}, {"dataset_nature", "declared nature"}, {"dataset_units", "declared unit"}, {"catalog_declarations", "declared catalog columns"}} {
		got, err := items(q, d.table, `SELECT dataset_id, '`+d.label+`' FROM `+d.table+` WHERE dataset_id=?`, id)
		if err != nil {
			return err
		}
		if len(got) > 0 {
			decl = append(decl, Item{ID: int64(i + 1), Label: d.label})
		}
	}
	b.add("declarations", "removed", decl, func(int) string {
		labels := make([]string, len(decl))
		for i, d := range decl {
			labels[i] = d.Label
		}
		return "What you declared about this dataset goes with it: " + strings.Join(labels, ", ") + "."
	})
	own, err := items(q, "derived", `SELECT dataset_id, op FROM derived WHERE dataset_id=?`, id)
	if err != nil {
		return err
	}
	b.add("record", "removed", own, func(int) string {
		return "Its own provenance record (" + own[0].Label + ") goes with it."
	})
	return nil
}

func runImpact(q querier, b *builder, id int64) error {
	outs, err := items(q, "run_outputs", `SELECT d.id,d.name FROM run_outputs o JOIN datasets d ON d.id=o.dataset_id WHERE o.run_id=?`, id)
	if err != nil {
		return err
	}
	b.add("outputs", "stays", outs, func(n int) string {
		return plural(n, "output map is", "output maps are") + " registered as datasets. They stay in the catalog."
	})
	figs, revs, err := figuresOf(q, idsOf(outs)...)
	if err != nil {
		return err
	}
	if err := pinnedDependents(q, b, figs, revs, "this run's output maps"); err != nil {
		return err
	}
	jb, err := items(q, "jobs", `SELECT id,name FROM jobs WHERE run_id=? OR parent_run_id=?`, id, id)
	if err != nil {
		return err
	}
	b.add("jobs", "unlinked", jb, func(n int) string {
		return plural(n, "job is", "jobs are") + " connected to this run (its outputs, or drafted from it). The job stays; the link is removed."
	})
	rp, err := items(q, "report_links", `SELECT r.id,r.title FROM reports r JOIN report_links l ON l.report_id=r.id WHERE l.kind='run' AND l.target=?`, strconv.FormatInt(id, 10))
	if err != nil {
		return err
	}
	b.add("reports", "unlinked", rp, func(n int) string {
		return plural(n, "report mentions", "reports mention") + " this run. The text stays; the run block will say the run is no longer in the catalog."
	})
	rv, err := items(q, "run_review", `SELECT run_id, 'QC '||qc||', interpretation '||interpretation FROM run_review WHERE run_id=?`, id)
	if err != nil {
		return err
	}
	b.add("review", "removed", rv, func(int) string {
		var hist int
		if has(q, "run_review_history") {
			_ = q.QueryRow(`SELECT COUNT(*) FROM run_review_history WHERE run_id=?`, id).Scan(&hist)
		}
		return "Your assessment of this run (" + rv[0].Label + ") and its " + plural(hist, "earlier revision", "earlier revisions") + " go with it."
	})
	return nil
}

func figureImpact(q querier, b *builder, id int64) error {
	var rev string
	if err := q.QueryRow(`SELECT revision FROM figures WHERE id=?`, id).Scan(&rev); err != nil {
		return err
	}
	bs, err := boardsHolding(q, []int64{id})
	if err != nil {
		return err
	}
	b.add("boards", "blocks", bs, func(n int) string {
		return "It is on " + plural(n, "board", "boards") + ". Remove it from there first, or archive the figure instead."
	})
	rp, err := items(q, "report_links", `SELECT r.id,r.title FROM reports r JOIN report_links l ON l.report_id=r.id WHERE l.kind='figure' AND l.target=?`, rev)
	if err != nil {
		return err
	}
	b.add("reports", "blocks", rp, func(n int) string {
		return plural(n, "report embeds", "reports embed") + " this figure. Take it out of the text first, or archive the figure instead."
	})
	sn, err := snapshotsPinning(q, []string{rev})
	if err != nil {
		return err
	}
	b.add("snapshots", "stays", sn, func(n int) string {
		return plural(n, "published review shows", "published reviews show") + " this figure. A review keeps its own frozen copy and picture, so it keeps working."
	})
	return nil
}

func boardImpact(q querier, b *builder, id int64) error {
	var figs, jobs int
	if has(q, "board_items") {
		_ = q.QueryRow(`SELECT COALESCE(SUM(figure_id IS NOT NULL),0), COALESCE(SUM(job_id IS NOT NULL),0) FROM board_items WHERE board_id=?`, id).Scan(&figs, &jobs)
	}
	var panels []Item
	for i := 0; i < figs+jobs; i++ {
		panels = append(panels, Item{ID: int64(i + 1), Label: "panel"})
	}
	b.add("panels", "removed", panels, func(int) string {
		return "The board's layout and notes go (" + plural(figs, "figure panel", "figure panels") + ", " + plural(jobs, "run panel", "run panels") + "). The figures themselves stay in the catalog."
	})
	sn, err := items(q, "snapshots", `SELECT id,label FROM snapshots WHERE board_id=?`, id)
	if err != nil {
		return err
	}
	b.add("snapshots", "blocks", sn, func(n int) string {
		return plural(n, "review was", "reviews were") + " published from this board. A published review is pinned evidence: archive the board instead."
	})
	return nil
}

// Archive hides an entry from the lists. It is still found by id and by every reference to it. It reports whether
// anything changed.
func (s *Service) Archive(kind string, id int64) (bool, error) {
	if _, err := impact(s.db, kind, id); err != nil {
		return false, err
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO archived_entries(kind,entity_id,archived_at) VALUES(?,?,?)`, kind, id, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Restore shows an archived entry in the lists again.
func (s *Service) Restore(kind string, id int64) (bool, error) {
	if _, err := impact(s.db, kind, id); err != nil {
		return false, err
	}
	res, err := s.db.Exec(`DELETE FROM archived_entries WHERE kind=? AND entity_id=?`, kind, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Deleted is what a delete reports back.
type Deleted struct {
	Impact Impact
	// OrphanPreview is the figure preview file nothing else uses any more; the caller may remove it.
	OrphanPreview string
}

// ChangedError carries the impact as it is now, for the person to look at again.
type ChangedError struct{ Impact Impact }

func (e *ChangedError) Error() string { return ErrChanged.Error() }
func (e *ChangedError) Unwrap() error { return ErrChanged }

// BlockedError carries the impact that blocks the delete.
type BlockedError struct{ Impact Impact }

func (e *BlockedError) Error() string {
	return "this cannot be deleted: " + strings.Join(e.Impact.Blockers, " ")
}
func (e *BlockedError) Unwrap() error { return ErrBlocked }

// Delete forgets an entry and what is keyed to it, in one transaction that first compares the impact with the digest
// the person saw. It never touches a file.
func (s *Service) Delete(kind string, id int64, digest string) (Deleted, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Deleted{}, err
	}
	defer tx.Rollback()
	im, err := impact(tx, kind, id)
	if err != nil {
		return Deleted{}, err
	}
	if digest == "" || digest != im.Digest {
		return Deleted{}, &ChangedError{im}
	}
	if im.Blocked {
		return Deleted{}, &BlockedError{im}
	}
	out := Deleted{Impact: im}
	if kind == KindFigure {
		if err := tx.QueryRow(`SELECT preview_sha FROM figures WHERE id=?`, id).Scan(&out.OrphanPreview); err != nil {
			return Deleted{}, err
		}
	}
	// kind is a bound parameter, never part of the SQL text; impact() above already refused any kind that is not one of
	// the constants.
	if _, err := tx.Exec(`DELETE FROM archived_entries WHERE kind=? AND entity_id=?`, kind, id); err != nil {
		return Deleted{}, fmt.Errorf("delete %s %d: %w", kind, id, err)
	}
	for _, st := range deleteSteps(kind) {
		if t := tableOf(st); t != "" && optionalTables[t] && !has(tx, t) {
			continue
		}
		if _, err := tx.Exec(st, id); err != nil {
			return Deleted{}, fmt.Errorf("delete %s %d: %w", kind, id, err)
		}
	}
	if out.OrphanPreview != "" { // still used by another figure or by a published review's frozen copy: keep the bytes
		var n int
		err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM figures WHERE preview_sha=?)+(SELECT COUNT(*) FROM snapshots WHERE instr(frozen, ?)>0)`, out.OrphanPreview, out.OrphanPreview).Scan(&n)
		if err != nil || n > 0 { // when in doubt the bytes stay
			out.OrphanPreview = ""
		}
	}
	if err := tx.Commit(); err != nil {
		return Deleted{}, err
	}
	return out, nil
}

// tableOf names the table a statement of Delete writes to.
func tableOf(st string) string {
	f := strings.Fields(st)
	switch {
	case len(f) >= 3 && f[0] == "DELETE":
		return f[2]
	case len(f) >= 2 && f[0] == "UPDATE":
		return f[1]
	}
	return ""
}

// optionalTables are written by a store that a catalog may never have opened (ADR 0041). Every other table Delete
// writes to must exist: a missing or renamed one is an error, not a silent skip. TestEveryKeyedTableIsHandled runs
// against the full schema, so a rename fails there.
var optionalTables = map[string]bool{
	"exploration_steps": true,                                                                      // history store (ADR 0057)
	"saved_regions":     true,                                                                      // regions store
	"masks":             true,                                                                      // masks store (ADR 0055)
	"derived":           true,                                                                      // derived store
	"run_review":        true, "run_review_history": true, "run_outputs": true, "rohsa_runs": true, // runs store
	"jobs":        true, // jobs store
	"transfers":   true, // transfers store
	"board_items": true, // boards store
	"sweeps":      true, // sweeps store (ADR 0048)
}

// deleteSteps are the statements that forget what is keyed to an entry, each taking the entry id; the entry's own row
// goes last. A table keyed to an entry kind that is not named here (or in the allowlist of
// TestEveryKeyedTableIsHandled) would be left behind.
func deleteSteps(kind string) []string {
	switch kind {
	case KindDataset:
		return []string{
			`DELETE FROM run_outputs WHERE dataset_id=?`, `DELETE FROM saved_regions WHERE dataset_id=?`, `DELETE FROM exploration_steps WHERE dataset_id=?`, `DELETE FROM masks WHERE dataset_id=?`, `DELETE FROM derived WHERE dataset_id=?`,
			`DELETE FROM dataset_locations WHERE dataset_id=?`, `DELETE FROM interpretations WHERE dataset_id=?`, `DELETE FROM dataset_nature WHERE dataset_id=?`, `DELETE FROM dataset_units WHERE dataset_id=?`, `DELETE FROM catalog_declarations WHERE dataset_id=?`, `UPDATE transfers SET dataset_id=NULL WHERE dataset_id=?`,
			`DELETE FROM datasets WHERE id=?`}
	case KindRun:
		return []string{
			`UPDATE jobs SET run_id=NULL WHERE run_id=?`, `UPDATE jobs SET parent_run_id=NULL WHERE parent_run_id=?`, `UPDATE sweeps SET base_run_id=NULL WHERE base_run_id=?`,
			`DELETE FROM run_outputs WHERE run_id=?`, `DELETE FROM rohsa_runs WHERE run_id=?`, `DELETE FROM run_review WHERE run_id=?`, `DELETE FROM run_review_history WHERE run_id=?`,
			`DELETE FROM runs WHERE id=?`}
	case KindFigure:
		return []string{`DELETE FROM figures WHERE id=?`}
	case KindBoard:
		return []string{`DELETE FROM board_items WHERE board_id=?`, `UPDATE sweeps SET board_id=NULL WHERE board_id=?`, `DELETE FROM boards WHERE id=?`}
	}
	return nil
}
