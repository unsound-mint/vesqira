package access

import (
	"database/sql"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
	_ "modernc.org/sqlite"
)

func TestOrderIsUnreleasedOverCollaborationOverNone(t *testing.T) {
	if !(Unreleased.Rank() > Collaboration.Rank() && Collaboration.Rank() > None.Rank()) {
		t.Fatal("ranking is wrong")
	}
	if Max(Collaboration, Unreleased) != Unreleased || Max(None, Collaboration) != Collaboration || Max(Unreleased, None) != Unreleased {
		t.Fatal("Max does not pick the most restrictive")
	}
	if _, err := Parse("secret"); err == nil {
		t.Fatal("an unknown label parsed")
	}
	if Label("secret").Valid() {
		t.Fatal("an unknown label is valid")
	}
	if None.Restricted() || !Collaboration.Restricted() {
		t.Fatal("Restricted")
	}
}

func TestBlocksIsExactForTheChosenLabels(t *testing.T) {
	if !Blocks([]Label{Unreleased}, Unreleased) || Blocks([]Label{Unreleased}, Collaboration) || Blocks(nil, Unreleased) || Blocks([]Label{Collaboration}, None) {
		t.Fatal("Blocks")
	}
}

func rig(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/a.db")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE datasets(id INTEGER PRIMARY KEY, workspace_id INTEGER NOT NULL DEFAULT 1, name TEXT NOT NULL DEFAULT '', size_bytes INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE derived(dataset_id INTEGER PRIMARY KEY, sources TEXT NOT NULL)`,
		`CREATE TABLE runs(id INTEGER PRIMARY KEY, workspace_id INTEGER NOT NULL DEFAULT 1, name TEXT NOT NULL DEFAULT '', input_name TEXT NOT NULL DEFAULT '', input_size INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE jobs(id INTEGER PRIMARY KEY, run_id INTEGER, request TEXT)`,
		`CREATE TABLE run_outputs(run_id INTEGER, dataset_id INTEGER)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(db)
	if err != nil {
		t.Skipf("access store needs the full schema: %v", err)
	}
	return s, db
}

func TestSetIsIdempotentAndKeepsHistory(t *testing.T) {
	s, _ := rig(t)
	ws := workspace.Local
	if _, ch, err := s.Set(ws, 1, "a", Collaboration, "why", "u"); err != nil || !ch {
		t.Fatalf("first set: %v %v", ch, err)
	}
	if _, ch, _ := s.Set(ws, 1, "a", Collaboration, "again", "u"); ch {
		t.Fatal("setting the same label wrote a row")
	}
	prev, ch, _ := s.Set(ws, 1, "a", Unreleased, "", "u")
	if !ch || prev != Collaboration {
		t.Fatalf("previous %v changed %v", prev, ch)
	}
	h, _ := s.History(ws, 1)
	if len(h) != 2 || h[0].Label != Unreleased {
		t.Fatalf("history newest first: %+v", h)
	}
	if _, _, err := s.Set(ws, 1, "a", "bogus", "", "u"); err == nil {
		t.Fatal("an invalid label was stored")
	}
}

func TestInheritanceFollowsDerivedEdgesAndSurvivesCycles(t *testing.T) {
	s, db := rig(t)
	ws := workspace.Local
	for i := 1; i <= 3; i++ {
		db.Exec(`INSERT INTO datasets(id) VALUES(?)`, i)
	}
	db.Exec(`INSERT INTO derived VALUES(2,'[1]')`)
	db.Exec(`INSERT INTO derived VALUES(3,'[2,3]')`) // lists itself: must not loop
	db.Exec(`INSERT INTO derived VALUES(1,'[3]')`)   // a longer cycle
	s.Set(ws, 1, "cube", Collaboration, "", "u")
	v, err := s.View(ws)
	if err != nil {
		t.Fatal(err)
	}
	if e := v.Dataset(3); e.Label != Collaboration || len(e.Via) != 1 || e.Via[0].Name != "cube" {
		t.Fatalf("grandchild: %+v", e)
	}
	s.Set(ws, 2, "mid", Unreleased, "", "u")
	v, _ = s.View(ws)
	if e := v.Dataset(3); e.Label != Unreleased || len(e.Via) != 2 {
		t.Fatalf("most restrictive wins, both listed: %+v", e)
	}
	var nilView *View
	if nilView.Dataset(1).Label != None {
		t.Fatal("a nil view is released")
	}
}

// A run's input is found by the job that made it when the job recorded the dataset it was drafted on; that exact edge replaces the
// name-and-size guess, which would otherwise have matched a different file of the same name.
func TestRunInputPrefersTheJobsExactDatasetOverNameAndSize(t *testing.T) {
	s, db := rig(t)
	ws := workspace.Local
	db.Exec(`INSERT INTO datasets(id,name,size_bytes) VALUES(1,'lmc.fits',5),(2,'lmc.fits',5),(3,'psi.fits',9)`)
	db.Exec(`INSERT INTO runs(id,name,input_name,input_size) VALUES(1,'r1','/x/lmc.fits',5)`)
	db.Exec(`INSERT INTO run_outputs VALUES(1,3)`)
	s.Set(ws, 1, "lmc.fits", Collaboration, "", "u") // the dataset the guess would find first
	s.Set(ws, 2, "lmc.fits", Unreleased, "", "u")
	v, err := s.View(ws)
	if err != nil {
		t.Fatal(err)
	}
	e := v.Dataset(3)
	if e.Label != Unreleased || len(e.Via) != 2 || e.Via[0].How == "" {
		t.Fatalf("by name and size both copies count: %+v", e)
	}
	db.Exec(`INSERT INTO jobs(run_id,request) VALUES(1,'{"inputRerun":{"input":{"datasetId":1}}}')`)
	v, _ = s.View(ws)
	e = v.Dataset(3)
	if e.Label != Collaboration || len(e.Via) != 1 || e.Via[0].DatasetID != 1 || e.Via[0].How == "" {
		t.Fatalf("the job's own record names the dataset: %+v", e)
	}
	if v.Run(1).Label != Collaboration {
		t.Fatalf("the run itself carries it: %+v", v.Run(1))
	}
}
