package lifecycle_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/lifecycle"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

func TestDeletingARunUnlinksItsJobsAndOutputsAndDropsItsReview(t *testing.T) {
	cat, err := catalog.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	rn, err := runs.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Open(cat.DB()); err != nil {
		t.Fatal(err)
	}
	db := cat.DB()
	if _, err := db.Exec(`INSERT INTO datasets(id,path,name,size_bytes,modified_at,added_at,header) VALUES(7,'/p/psi.fits','psi.fits',1,0,0,'{}')`); err != nil {
		t.Fatal(err)
	}
	run, err := rn.Save("/runs/r1", runs.Parsed{Run: runs.Run{Name: "r1", Kind: "vgt", Params: []runs.Param{}, Diagnostics: map[string]string{}}},
		[]runs.Output{{Role: "psi", DatasetID: 7, Name: "psi.fits"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := rn.SetReview(run.ID, runs.Review{QC: "passed", Interpretation: "supports", Note: "ok"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs(host_id,name,template,script,script_hash,state,detail,slurm_id,work_dir,output_dir,created_at,run_id) VALUES(1,'j','vgt','','','completed','','','','',0,?)`, run.ID); err != nil {
		t.Fatal(err)
	}

	svc := lifecycle.New(db)
	im, err := svc.Impact(lifecycle.KindRun, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	effects := map[string]string{}
	for _, g := range im.Groups {
		effects[g.Key] = g.Effect
	}
	if effects["outputs"] != "stays" || effects["jobs"] != "unlinked" || effects["review"] != "removed" || im.Blocked {
		t.Fatalf("impact of a run: %+v", effects)
	}
	if _, err := svc.Delete(lifecycle.KindRun, run.ID, "stale"); !errors.Is(err, lifecycle.ErrChanged) {
		t.Fatalf("stale digest: %v", err)
	}
	if _, err := svc.Delete(lifecycle.KindRun, run.ID, im.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := rn.Get(run.ID); !errors.Is(err, runs.ErrNotFound) {
		t.Errorf("run still there: %v", err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM datasets WHERE id=7`).Scan(&n)
	if n != 1 {
		t.Error("the run's output dataset must stay in the catalog")
	}
	var runID *int64
	db.QueryRow(`SELECT run_id FROM jobs`).Scan(&runID)
	if runID != nil {
		t.Errorf("the job should be unlinked, run_id=%d", *runID)
	}
	db.QueryRow(`SELECT COUNT(*) FROM run_review`).Scan(&n)
	if n != 0 {
		t.Error("the run's review should go with it")
	}
}

func TestArchivedRunStaysInGetAndFlagsItself(t *testing.T) {
	cat, err := catalog.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	rn, _ := runs.Open(cat.DB())
	run, _ := rn.Save("/runs/r1", runs.Parsed{Run: runs.Run{Name: "r1", Kind: "vgt", Params: []runs.Param{}, Diagnostics: map[string]string{}}}, nil)
	svc := lifecycle.New(cat.DB())
	if changed, err := svc.Archive(lifecycle.KindRun, run.ID); err != nil || !changed {
		t.Fatalf("archive: %v %v", changed, err)
	}
	got, err := rn.Get(run.ID)
	if err != nil || !got.Archived {
		t.Fatalf("an archived run resolves and says so: %+v %v", got, err)
	}
	if changed, _ := svc.Archive(lifecycle.KindRun, run.ID); changed {
		t.Error("archiving twice is not a change")
	}
	if _, err := svc.Restore(lifecycle.KindRun, run.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := rn.Get(run.ID); got.Archived {
		t.Error("restored run still archived")
	}
	if _, err := svc.Archive(lifecycle.KindRun, 999); !errors.Is(err, lifecycle.ErrNotFound) {
		t.Errorf("archive of nothing: %v", err)
	}
}
