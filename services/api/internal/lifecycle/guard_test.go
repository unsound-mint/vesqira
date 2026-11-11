package lifecycle

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/fullschema"
)

// keyColumns are the columns that point at a deletable entry (dataset, run, figure, board) or carry a reference to one
// in a form SQL cannot follow. A table with one of them must be handled by deleteSteps or be listed in intentional.
var keyColumns = map[string]bool{
	"dataset_id": true, "run_id": true, "parent_run_id": true, "figure_id": true, "board_id": true, "reliability_id": true,
	"entity_id":  true, // archived_entries: polymorphic, cleared first in Delete
	"target":     true, // report_links: a run id or a figure revision, by kind
	"sources":    true, // derived: JSON list of dataset ids
	"frozen":     true, // snapshots: the published copy, which holds figure revisions and preview hashes
	"params":     true, // jobs/runs: JSON that may name datasets
	"provenance": true, // figures: JSON naming the source dataset
}

// intentional lists every "table.column" that deleting an entry deliberately leaves alone, with why. Adding a table here
// is a decision about what a delete means; say what keeps the data honest.
var intentional = map[string]string{
	"archived_entries.entity_id":     "cleared by the first statement of Delete, by (kind, entity_id), not by deleteSteps",
	"figures.dataset_id":             "a figure is pinned evidence with its own picture and provenance; it stays after its dataset is deleted (impact group figures: stays)",
	"figures.provenance":             "the figure's own frozen record of its source; truthful history, never rewritten",
	"board_items.figure_id":          "deleting a figure is blocked while a board holds it (impact group boards: blocks)",
	"report_links.target":            "reports keep their text: a run block says the run is no longer in the catalog (impact: unlinked); a figure that is embedded blocks the delete",
	"snapshots.board_id":             "deleting a board is blocked while a review was published from it (impact group snapshots: blocks)",
	"snapshots.frozen":               "a published review keeps its own frozen copy of figures and previews so the reviewer is unaffected (impact: stays)",
	"derived.sources":                "a derived dataset stays; its provenance names a source that is no longer in the catalog (impact group derived: stays)",
	"interpretations.reliability_id": "deleting a dataset used as a reliability map is blocked until that declaration is cleared (impact group reliability: blocks)",
	"jobs.params":                    "a job's JSON params record what was asked at the time; history is not rewritten",
	"sweeps.params":                  "the swept values as asked, a record of the grid; not a reference to a catalog entry",
	"runs.params":                    "the run's own recorded settings, not a reference to a catalog entry",
	"audit.target":                   "the audit log is append-only history; (kind, target) names an entry that may since have been deleted, and the delete itself is recorded there",
	"derived.params":                 "the operation's own recorded parameters (provenance), not a reference that must resolve",
	"notifications.run_id":           "a bounded event feed (notify.Keep) with its own text snapshot; the run link may point at a run deleted since (the UI link goes nowhere), which is history, not a dangling reference to repair",
}

var whereCol = regexp.MustCompile(`WHERE (\w+)=\?`)

func TestEveryKeyedTableIsHandled(t *testing.T) {
	cat, err := catalog.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	db, dir := cat.DB(), t.TempDir()
	if err := fullschema.Open(db, dir); err != nil { // the full schema: every store main.go opens on the catalog file
		t.Fatal(err)
	}

	handled := map[string]bool{} // "table.column" written by deleteSteps
	tables := map[string]bool{}
	for _, kind := range []string{KindDataset, KindRun, KindFigure, KindBoard} {
		for _, st := range deleteSteps(kind) {
			tables[tableOf(st)] = true
			if m := whereCol.FindStringSubmatch(st); m != nil {
				handled[tableOf(st)+"."+m[1]] = true
			}
		}
	}
	for tbl := range optionalTables { // a renamed table must not turn into a silent skip
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil || n == 0 {
			t.Errorf("lifecycle.optionalTables names %q, which the full schema does not have: rename it there too", tbl)
		}
	}
	for tbl := range tables {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil || n == 0 {
			t.Errorf("deleteSteps writes to %q, which the full schema does not have", tbl)
		}
	}

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
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
	seen := map[string]bool{}
	for _, tbl := range names {
		cols, err := db.Query(fmt.Sprintf(`SELECT name FROM pragma_table_info('%s')`, tbl))
		if err != nil {
			t.Fatal(err)
		}
		var cs []string
		for cols.Next() {
			var c string
			_ = cols.Scan(&c)
			cs = append(cs, c)
		}
		cols.Close()
		for _, c := range cs {
			key := tbl + "." + c
			if !keyColumns[c] {
				continue
			}
			seen[key] = true
			if handled[key] || intentional[key] != "" {
				continue
			}
			t.Errorf("table %s has column %s, which can point at a dataset, run, figure or board, but deleting an entry neither handles it nor allows it.\n"+
				"  Either add a statement for it to deleteSteps in internal/lifecycle/lifecycle.go (and describe it in the impact builders), or add %q with a reason to `intentional` in internal/lifecycle/guard_test.go.", tbl, c, key)
		}
	}
	var stale []string
	for key := range intentional {
		if !seen[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		t.Errorf("guard_test.go allows %s but no such table.column exists in the full schema: remove or rename the entry", key)
	}
	if t.Failed() {
		t.Logf("tables: %v", names)
	}
}
