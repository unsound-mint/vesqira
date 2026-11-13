package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/sweeps"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// The three leftovers of the family view (ADR 0070): a sweep's outputs opened as a family, with the jobs that cannot be compared listed and
// the reason; and the cleanup of the comparison maps a family run registered, which never touches a source and keeps what anything uses.
func TestASweepOpensAsAFamilyAndFamilyMapsCanBeCleanedUp(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	py, _ := exec.LookPath("python3")
	base := t.TempDir()
	localRoot, derivedDir := filepath.Join(base, "local"), filepath.Join(base, "derived")
	for _, d := range []string{localRoot, derivedDir} {
		os.MkdirAll(d, 0o755)
	}
	// maps on an 8 by 8 TAN grid: scalar ones in K (and one in Jy), one scalar on a 9 by 9 grid, and orientation maps in degrees
	mk := exec.Command(py, "-c", `import sys, numpy as np
from astropy.io import fits
d = sys.argv[1]
def w(name, data, unit=None):
    h = fits.Header()
    ny, nx = data.shape
    for i, (ct, cv, cd, n) in enumerate([("RA---TAN", 150.0, -0.01, nx), ("DEC--TAN", 2.0, 0.01, ny)], start=1):
        h["CTYPE%d" % i], h["CRVAL%d" % i], h["CDELT%d" % i], h["CRPIX%d" % i], h["CUNIT%d" % i] = ct, cv, cd, n / 2 + 0.5, "deg"
    h["RADESYS"] = "ICRS"
    if unit: h["BUNIT"] = unit
    fits.PrimaryHDU(data.astype("f4"), header=h).writeto(d + "/" + name + ".fits")
rng = np.random.default_rng(3)
base = rng.normal(5, 1, (8, 8))
w("s1", base, "K"); w("s2", base + 0.5, "K"); w("s3", rng.normal(5, 1, (9, 9)), "K"); w("s4", base, "Jy/beam"); w("s5", base * 2, "K")
ang = rng.uniform(0, 180, (8, 8))
w("o1", ang); w("o2", (ang + 5) % 180); w("o3", (ang + 9) % 180)`, localRoot)
	if out, err := mk.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cat, _ := catalog.Open("file:" + filepath.Join(base, "t.db"))
	defer cat.Close()
	ds, _ := derived.Open(cat.DB())
	js, _ := jobs.Open(cat.DB())
	rn, _ := runs.Open(cat.DB())
	hs, _ := hosts.Open(cat.DB())
	mgr := hosts.NewManager("/nonexistent-ssh", nil)
	t.Cleanup(mgr.Close)
	sw, err := sweeps.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := boards.Open(cat.DB(), base); err != nil { // creates the figures tables
		t.Fatal(err)
	}
	rs, err := sourceroot.New([]string{localRoot, derivedDir})
	if err != nil {
		t.Fatal(err)
	}
	local := &worker.Persistent{Command: []string{py, "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(local.Close)
	ts := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: headerWorker(t, py, repo), Planes: local, Cache: NewPlaneCache(8 << 20), Derived: ds, Jobs: js, Runs: rn, Sweeps: sw, HostDB: hs, Remote: mgr,
		Analysis: &AnalysisConfig{Python: py, Script: filepath.Join(repo, "services/scientific-worker/analysis.py"), DerivedDir: derivedDir, Timeout: 2 * time.Minute}}).Handler())
	defer ts.Close()
	reg := func(name string) catalog.Dataset {
		var d catalog.Dataset
		json.NewDecoder(post(t, ts, filepath.Join(localRoot, name+".fits"), nil).Body).Decode(&d)
		if d.ID == 0 {
			t.Fatalf("register %s", name)
		}
		return d
	}
	s1, s2, s3, s4, s5 := reg("s1"), reg("s2"), reg("s3"), reg("s4"), reg("s5")
	o1, o2, o3 := reg("o1"), reg("o2"), reg("o3")
	for _, o := range []catalog.Dataset{o1, o2} {
		if r, b := postJSON(t, fmt.Sprintf("%s/api/datasets/%d/interpretation", ts.URL, o.ID), map[string]any{"kind": "axial_orientation", "unit": "deg"}); r.StatusCode != 200 {
			t.Fatalf("declare: %d %s", r.StatusCode, b)
		}
	}
	get := func(p string) (int, string) {
		r, b := get(t, ts.URL+p)
		return r.StatusCode, string(b)
	}

	// a sweep of five jobs over ker; each finished job's run lists a reliability-like scalar output and an orientation output
	mkSweep := func(name string, scalar, orient []int64) sweeps.Sweep {
		var ids []int64
		var vals []map[string]string
		for i := range scalar {
			j, err := js.CreateDraft(jobs.Job{Name: fmt.Sprintf("%s-%d", name, i), Template: "vgt", Script: "#!/bin/sh\necho " + name + "\n", OutputDir: fmt.Sprintf("/x/%s/%d", name, i)})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, j.ID)
			vals = append(vals, map[string]string{"ker": fmt.Sprint(3 + 2*i), "dn": "16"})
			if scalar[i] == 0 && orient[i] == 0 { // no run yet
				continue
			}
			var outs []runs.Output
			if scalar[i] != 0 {
				outs = append(outs, runs.Output{Role: "support", Product: "map", DatasetID: scalar[i], Name: "map"})
			}
			if orient[i] != 0 {
				outs = append(outs, runs.Output{Role: runs.RoleOrientation, Product: "psi", DatasetID: orient[i], Name: "psi"})
			}
			run, err := rn.Save(workspace.Local, "t", fmt.Sprintf("/runs/%s/%d", name, i), runs.Parsed{Run: runs.Run{Name: fmt.Sprintf("%s run %d", name, i), Kind: "vgt"}}, outs)
			if err != nil {
				t.Fatal(err)
			}
			if err := js.LinkRun(j.ID, run.ID); err != nil {
				t.Fatal(err)
			}
		}
		s, err := sw.Create(sweeps.Sweep{Name: name, BaseJobID: ids[0], Params: []sweeps.Param{{Key: "ker", Label: "kernel", Values: []string{"3", "5", "7", "9", "11"}}, {Key: "dn", Values: []string{"16"}}}}, ids, vals)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	sc := mkSweep("scalars", []int64{s1.ID, s2.ID, s3.ID, s4.ID, 0}, []int64{o1.ID, o2.ID, o3.ID, 0, 0})
	type fam struct {
		Role  string `json:"role"`
		Roles []struct {
			Role string
			Jobs int
		}
		Members []sweepFamilyMember
		Left    []sweepFamilyLeft `json:"excluded"`
		Ref     int64             `json:"referenceId"`
		Note    string
	}
	read := func(id int64, q string) (int, fam) {
		code, b := get(fmt.Sprintf("/api/sweeps/%d/family%s", id, q))
		var f fam
		json.Unmarshal([]byte(b), &f)
		return code, f
	}
	// two kinds of output: nothing is chosen for the person, the roles are listed
	if code, f := read(sc.ID, ""); code != 200 || f.Role != "" || len(f.Roles) != 2 || len(f.Members) != 0 || !strings.Contains(f.Note, "choose") {
		t.Errorf("no role given: %d %+v", code, f)
	}
	code, f := read(sc.ID, "?role=support")
	if code != 200 || len(f.Members) != 2 || f.Members[0].DatasetID != s1.ID || f.Members[1].DatasetID != s2.ID || f.Ref != s1.ID {
		t.Fatalf("scalar members: %d %+v", code, f)
	}
	// the label is the parameter that differs across the sweep (dn takes one value, so it is not in it), in the grid's order
	if f.Members[0].Label != "kernel = 3" || f.Members[1].Label != "kernel = 5" {
		t.Errorf("labels %q %q", f.Members[0].Label, f.Members[1].Label)
	}
	why := map[int64]string{}
	for _, l := range f.Left {
		why[l.JobID] = l.Reason
	}
	if len(f.Left) != 3 || !strings.Contains(why[f.Left[0].JobID], "share a grid") || !strings.Contains(why[f.Left[1].JobID], "convert one first") || !strings.Contains(f.Left[2].Reason, "no result yet") {
		t.Errorf("left out: %+v", f.Left)
	}
	if code, _ := read(sc.ID, "?role=nonsense"); code != 422 {
		t.Errorf("an unknown role: %d", code)
	}
	// orientation outputs: the declared ones are compared; one nobody declared is left out, never taken as a scalar map
	if code, f := read(sc.ID, "?role=orientation"); code != 200 || len(f.Members) != 2 || len(f.Left) != 3 || !strings.Contains(f.Left[0].Reason, "has not been declared") {
		t.Errorf("orientation: %d %+v", code, f)
	}
	if code, _ := read(99999, ""); code != 404 {
		t.Errorf("an unknown sweep: %d", code)
	}

	// the members open in the family computation with the sweep's labels
	famReq := func() (int, struct {
		Maps []struct {
			Role    string
			Member  int
			Dataset catalog.Dataset
		}
		Summary struct{ Labels []string }
	}) {
		var out struct {
			Maps []struct {
				Role    string
				Member  int
				Dataset catalog.Dataset
			}
			Summary struct{ Labels []string }
		}
		r, b := postJSON(t, ts.URL+"/api/analysis/family", map[string]any{"datasetIds": []int64{s1.ID, s2.ID, s5.ID}, "referenceId": s1.ID, "labels": []string{"kernel = 3", "kernel = 5", "kernel = 7"}})
		json.Unmarshal(b, &out)
		return r.StatusCode, out
	}
	code, run1 := famReq()
	if code != 201 || len(run1.Maps) != 3 || run1.Summary.Labels[2] != "kernel = 7" {
		t.Fatalf("family run: %d %+v", code, run1)
	}
	_, run2 := famReq()
	if len(run2.Maps) != 3 {
		t.Fatalf("second run: %+v", run2)
	}

	// cleanup: six maps are listed, in two runs, none held
	type listed struct {
		Maps []familyMapView
	}
	list := func() listed {
		_, b := get("/api/analysis/family-maps")
		var l listed
		json.Unmarshal([]byte(b), &l)
		return l
	}
	l := list()
	runsSeen := map[string]int{}
	for _, m := range l.Maps {
		runsSeen[m.Run]++
		if len(m.Holds) != 0 || m.SizeBytes == 0 {
			t.Errorf("a fresh map: %+v", m)
		}
	}
	if len(l.Maps) != 6 || len(runsSeen) != 2 {
		t.Fatalf("listed %d maps in %d runs", len(l.Maps), len(runsSeen))
	}
	// something uses three of them: a figure with one as its base, a figure with another as a layer of its scene, and a dataset made from the third
	db := cat.DB()
	figure := func(base int64, scene string, rev string) {
		if _, err := db.Exec(`INSERT INTO figures(dataset_id,title,caption,created_at,revision,preview_sha,width,height,provenance,scene,workspace_id) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			base, "Figure "+rev, "", 1, rev, "sha"+rev, 8, 8, "{}", scene, 1); err != nil {
			t.Fatal(err)
		}
	}
	held := []int64{run1.Maps[0].Dataset.ID, run1.Maps[1].Dataset.ID, run1.Maps[2].Dataset.ID}
	figure(held[0], "", "r1")
	figure(s1.ID, fmt.Sprintf(`{"schema":"scene/1","base":{"datasetId":%d},"overlays":[{"id":"c","kind":"contours","datasetId":%d}]}`, s1.ID, held[1]), "r2")
	if err := ds.Save(derived.Record{DatasetID: s5.ID, Op: "convolve", Sources: []int64{held[2]}}); err != nil {
		t.Fatal(err)
	}
	l = list()
	nHeld := 0
	for _, m := range l.Maps {
		if len(m.Holds) > 0 {
			nHeld++
		}
	}
	if nHeld != 3 {
		t.Errorf("held maps: %d of %+v", nHeld, l.Maps)
	}
	// remove everything the second run made plus the held ones, and one that is not a family map at all
	var ask []int64
	for _, m := range run2.Maps {
		ask = append(ask, m.Dataset.ID)
	}
	ask = append(ask, held...)
	ask = append(ask, s1.ID)
	var files []string
	for _, id := range ask {
		if d, err := cat.Get(workspace.Local, id); err == nil {
			files = append(files, d.Path)
		}
	}
	r, b := postJSON(t, ts.URL+"/api/analysis/family-maps/remove", map[string]any{"datasetIds": ask})
	if r.StatusCode != 200 {
		t.Fatalf("remove: %d %s", r.StatusCode, b)
	}
	var rem struct{ Results []familyMapRemoval }
	json.Unmarshal(b, &rem)
	byID := map[int64]familyMapRemoval{}
	for _, x := range rem.Results {
		byID[x.DatasetID] = x
	}
	for _, m := range run2.Maps {
		if x := byID[m.Dataset.ID]; !x.Removed || x.Bytes != "removed" {
			t.Errorf("a free map was not removed: %+v", x)
		}
		if _, err := cat.Get(workspace.Local, m.Dataset.ID); err == nil {
			t.Errorf("map %d is still in the catalog", m.Dataset.ID)
		}
		if _, err := os.Stat(m.Dataset.Path); err == nil {
			t.Errorf("the file of map %d is still on disk", m.Dataset.ID)
		}
	}
	for i, id := range held {
		x := byID[id]
		if x.Removed || !strings.HasPrefix(x.Reason, "kept:") {
			t.Errorf("held map %d: %+v", i, x)
		}
		if _, err := cat.Get(workspace.Local, id); err != nil {
			t.Errorf("held map %d left the catalog", i)
		}
	}
	if x := byID[s1.ID]; x.Removed || !strings.Contains(x.Reason, "not a map made by a family comparison") {
		t.Errorf("a source: %+v", x)
	}
	for _, p := range []string{"s1", "s2", "s3", "s4", "s5", "o1", "o2", "o3"} {
		if _, err := os.Stat(filepath.Join(localRoot, p+".fits")); err != nil {
			t.Errorf("a source file was touched: %s", p)
		}
	}
	if _, err := cat.Get(workspace.Local, s1.ID); err != nil {
		t.Errorf("the source left the catalog")
	}
	// only held maps asked for: a refusal with the reason, nothing removed
	r, b = postJSON(t, ts.URL+"/api/analysis/family-maps/remove", map[string]any{"datasetIds": held[:1]})
	if r.StatusCode != 409 || !strings.Contains(string(b), "figure") {
		t.Errorf("a held map alone: %d %s", r.StatusCode, b)
	}
	if l := list(); len(l.Maps) != 3 {
		t.Errorf("maps left: %d", len(l.Maps))
	}
	if r, _ := postJSON(t, ts.URL+"/api/analysis/family-maps/remove", map[string]any{"datasetIds": []int64{}}); r.StatusCode != 422 {
		t.Errorf("an empty request: %d", r.StatusCode)
	}
	_ = files
	_ = o3
	_ = s3
	_ = s4
}
