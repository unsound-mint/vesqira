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
	"github.com/unsound-mint/vesqira/services/api/internal/review"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

func TestAnalysisLocalAndOnAHostWithDerivedProvenance(t *testing.T) {
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy, reproject").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy and reproject is required")
		}
		t.Skip("python3 with astropy and reproject not available")
	}
	py, _ := exec.LookPath("python3")
	base := t.TempDir()
	localRoot, derivedDir := filepath.Join(base, "local"), filepath.Join(base, "derived")
	remoteHome, remoteRoot := filepath.Join(base, "remote-home"), filepath.Join(base, "remote_data")
	for _, d := range []string{localRoot, derivedDir, remoteHome, remoteRoot} {
		os.MkdirAll(d, 0o755)
	}
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), localRoot)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cube, _ := os.ReadFile(filepath.Join(localRoot, "hi_cube.fits"))
	os.WriteFile(filepath.Join(remoteRoot, "remote_cube.fits"), cube, 0o644)

	bin := filepath.Join(base, "slurm-bin")
	fakeSlurm(t, bin)
	sshScript := fmt.Sprintf("#!/bin/sh\nwhile [ $# -gt 0 ]; do case \"$1\" in -o) shift 2;; -T) shift;; *) break;; esac; done\nshift\ncmd=$(printf '%%s' \"$*\" | sed -e 's#\\$HOME#%s#g' -e 's#bash -lc#bash -c#')\nPATH=%s:$PATH exec sh -c \"$cmd\"\n", remoteHome, bin)
	sshBin := filepath.Join(base, "ssh")
	os.WriteFile(sshBin, []byte(sshScript), 0o755)
	files := map[string][]byte{}
	for _, f := range []string{"plane_worker.py", "fits_header.py", "analysis.py"} {
		b, _ := os.ReadFile(filepath.Join(repo, "services/scientific-worker", f))
		files[f] = b
	}
	mgr := hosts.NewManager(sshBin, files)
	mgr.ControlDir = t.TempDir()
	t.Cleanup(mgr.Close)

	cat, _ := catalog.Open("file:" + filepath.Join(base, "t.db"))
	defer cat.Close()
	hs, _ := hosts.Open(cat.DB())
	js, _ := jobs.Open(cat.DB())
	rn, _ := runs.Open(cat.DB())
	ds, _ := derived.Open(cat.DB())
	bs, _ := boards.Open(cat.DB(), base)
	rs, err := sourceroot.New([]string{localRoot, derivedDir})
	if err != nil {
		t.Fatal(err)
	}
	local := &worker.Persistent{Command: []string{py, "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(local.Close)
	hdr := worker.Runner{Command: []string{py, "-P", filepath.Join(repo, "services/scientific-worker/fits_header.py")}, Timeout: 20 * time.Second}
	ts := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: hdr, Planes: local, Cache: NewPlaneCache(8 << 20), Boards: bs, Runs: rn, HostDB: hs, Remote: mgr, Jobs: js, Derived: ds,
		Analysis: &AnalysisConfig{Python: py, Script: filepath.Join(repo, "services/scientific-worker/analysis.py"), DerivedDir: derivedDir, Timeout: 2 * time.Minute}}).Handler())
	defer ts.Close()
	rv := httptest.NewServer((&review.Handler{Store: bs}).Handler())
	defer rv.Close()
	api := func(p string) string { return ts.URL + p }
	reg := func(name string) catalog.Dataset {
		var d catalog.Dataset
		json.NewDecoder(post(t, ts, filepath.Join(localRoot, name), nil).Body).Decode(&d)
		return d
	}
	cubeDS, coarse := reg("hi_cube.fits"), reg("hi_cube_coarser.fits")

	// refusals first: options are numbers or known words, nothing else
	for name, in := range map[string]map[string]any{
		"unknown op":   {"op": "rm", "datasetId": cubeDS.ID},
		"option shell": {"op": "moment", "datasetId": cubeDS.ID, "params": map[string]string{"threshold": "1; rm -rf ~"}},
		"unknown opt":  {"op": "moment", "datasetId": cubeDS.ID, "params": map[string]string{"out": "/etc/passwd"}},
		"bad method":   {"op": "align", "datasetId": cubeDS.ID, "referenceId": coarse.ID, "params": map[string]string{"method": "../../x"}},
		"align no ref": {"op": "align", "datasetId": cubeDS.ID, "referenceId": 9999},
	} {
		if r, _ := postJSON(t, api("/api/analysis"), in); r.StatusCode < 400 {
			t.Errorf("%s accepted (%d)", name, r.StatusCode)
		}
	}

	// statistics: a summary, no new file
	r, sb := postJSON(t, api("/api/analysis"), map[string]any{"op": "stats", "datasetId": cubeDS.ID})
	if r.StatusCode != 200 || !strings.Contains(string(sb), `"no_value":576`) && !strings.Contains(string(sb), `"no_value": 576`) {
		t.Fatalf("stats: %d %s", r.StatusCode, sb)
	}

	// a moment map becomes a new dataset with its provenance; the input is untouched
	r, mb := postJSON(t, api("/api/analysis"), map[string]any{"op": "moment", "datasetId": cubeDS.ID, "params": map[string]string{"order": "0"}})
	if r.StatusCode != 201 {
		t.Fatalf("moment: %d %s", r.StatusCode, mb)
	}
	var m struct{ Dataset catalog.Dataset }
	json.Unmarshal(mb, &m)
	if !strings.HasPrefix(m.Dataset.Path, derivedDir) || m.Dataset.HostID != 0 {
		t.Errorf("a derived file lives in the derived folder: %s", m.Dataset.Path)
	}
	_, db := get(t, api("/api/datasets/"+itoa(int(m.Dataset.ID))+"/derived"))
	if !strings.Contains(string(db), `"op":"moment"`) || !strings.Contains(string(db), itoa(int(cubeDS.ID))) {
		t.Errorf("derived record: %s", db)
	}
	if after, _ := os.ReadFile(filepath.Join(localRoot, "hi_cube.fits")); string(after) != string(cube) {
		t.Error("the input file changed")
	}

	// alignment onto another grid records both sources
	r, ab := postJSON(t, api("/api/analysis"), map[string]any{"op": "align", "datasetId": cubeDS.ID, "referenceId": coarse.ID})
	if r.StatusCode != 201 || !strings.Contains(string(ab), "resolution") {
		t.Fatalf("align: %d %s", r.StatusCode, ab)
	}
	var al struct{ Dataset catalog.Dataset }
	json.Unmarshal(ab, &al)
	_, adb := get(t, api("/api/datasets/"+itoa(int(al.Dataset.ID))+"/derived"))
	var rec derived.Record
	json.Unmarshal(adb, &rec)
	if len(rec.Sources) != 2 || rec.Op != "align" {
		t.Errorf("align record: %s", adb)
	}

	// the reviewer learns how a figure's data was made, by file name only
	_, fb := postJSON(t, api("/api/figures"), map[string]any{"datasetId": m.Dataset.ID, "title": "Moment 0", "view": map[string]any{"kind": "plane", "channel": 0}, "previewPng": testPNG(t)})
	var fig boards.Figure
	json.Unmarshal(fb, &fig)
	_, bb := postJSON(t, api("/api/boards"), map[string]any{"title": "Derived"})
	var b boards.Board
	json.Unmarshal(bb, &b)
	postJSON(t, api("/api/boards/"+itoa(int(b.ID))+"/items"), map[string]any{"figureId": fig.ID})
	_, pub := postJSON(t, api("/api/boards/"+itoa(int(b.ID))+"/publish"), map[string]any{"hours": 1})
	var tok struct{ Token string }
	json.Unmarshal(pub, &tok)
	_, rbody := get(t, rv.URL+"/review-api/"+tok.Token)
	for _, want := range []string{`"derived"`, "hi_cube.fits", "never 0", "reproject_not_needed"} {
		if want == "reproject_not_needed" {
			continue
		}
		if !strings.Contains(string(rbody), want) {
			t.Errorf("the reviewer should see %q", want)
		}
	}
	for _, leak := range []string{base, derivedDir, localRoot} {
		if strings.Contains(string(rbody), leak) {
			t.Errorf("reviewer response leaks %q", leak)
		}
	}

	// a cube on a host is analysed there, as a job; local analysis refuses it
	_, hb := postJSON(t, api("/api/hosts"), map[string]any{"name": "Cluster", "dest": "login1", "python": py, "roots": []string{remoteRoot}})
	var h hosts.Host
	json.Unmarshal(hb, &h)
	_, rb := postJSON(t, api("/api/hosts/"+itoa(int(h.ID))+"/register"), map[string]any{"path": filepath.Join(remoteRoot, "remote_cube.fits")})
	var rem catalog.Dataset
	json.Unmarshal(rb, &rem)
	if r, _ := postJSON(t, api("/api/analysis"), map[string]any{"op": "moment", "datasetId": rem.ID}); r.StatusCode != 409 {
		t.Errorf("local analysis of a remote file: %d, want 409", r.StatusCode)
	}
	r, jb := postJSON(t, api("/api/analysis/job"), map[string]any{"op": "moment", "datasetId": rem.ID, "params": map[string]string{"order": "1", "threshold": "1.0"}})
	if r.StatusCode != 201 {
		t.Fatalf("analysis job: %d %s", r.StatusCode, jb)
	}
	var job jobs.Job
	json.Unmarshal(jb, &job)
	for _, want := range []string{"analysis.py", "moment", "--order 1", "--threshold 1.0", filepath.Join(remoteRoot, "remote_cube.fits"), "result.fits"} {
		if !strings.Contains(job.Script, want) {
			t.Errorf("script lacks %q:\n%s", want, job.Script)
		}
	}
	jp := func(p string) string { return api("/api/jobs/" + itoa(int(job.ID)) + p) }
	if r, _ := postJSON(t, jp("/submit"), map[string]any{"scriptHash": job.ScriptHash}); r.StatusCode != 200 {
		t.Fatalf("submit: %d", r.StatusCode)
	}
	// the "cluster" runs the script (the stand-in sbatch does not): same script, host's HOME, the job directory
	// ($HOME is the host's home in the script; here the host's home is a folder, and python keeps the real HOME's packages)
	jobDir := filepath.Join(remoteHome, "vesqira-jobs", job.WorkDir)
	sent, _ := os.ReadFile(filepath.Join(jobDir, "job.sh"))
	os.WriteFile(filepath.Join(jobDir, "run.sh"), []byte(strings.ReplaceAll(string(sent), "$HOME", remoteHome)), 0o755)
	run := exec.Command("bash", "run.sh")
	run.Dir = jobDir
	run.Env = append(os.Environ(), "SLURM_SUBMIT_DIR="+jobDir)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("running the job script: %v\n%s", err, out)
	}
	os.WriteFile(filepath.Join(bin, "state", "4242"), []byte("COMPLETED\n"), 0o644)
	postJSON(t, jp("/refresh"), map[string]any{})
	r, regb := postJSON(t, jp("/register"), map[string]any{})
	if r.StatusCode != 201 {
		t.Fatalf("register analysis result: %d %s", r.StatusCode, regb)
	}
	var out catalog.Dataset
	json.Unmarshal(regb, &out)
	if out.HostID != h.ID {
		t.Errorf("the result stays on the host: %+v", out)
	}
	_, odb := get(t, api("/api/datasets/"+itoa(int(out.ID))+"/derived"))
	if !strings.Contains(string(odb), `"op":"moment"`) || !strings.Contains(string(odb), itoa(int(rem.ID))) {
		t.Errorf("derived record on the host's result: %s", odb)
	}
}
