package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

const rerunVGTConfig = "[input]\ninput_file = \"/data/old.fits\"\noutput_dir = \"\"\n\n[noise]\nnoise = 0.17\nnoise_mode = \"scalar\"\n\n[vgt]\ndn = 20\nker = 4\nmin_valid = 101\n"

const rerunROHSAConfig = "[input]\ninput = \"/data/old.fits\"\noutput_dir = \"\"\ncrop_size = 512\ncrop_x0 = 1\ncrop_y0 = 1\n\n[rohsa]\nn_gauss = [4]\nlambda_amp = 10.0\nlambda_mu = 10.0\nlambda_sig = 10.0\n"

// cubeHeader is a catalogued header of a three-axis cube: sky, sky, velocity.
func cubeHeader(nx, ny, nv int, pixDeg float64, bunit string) json.RawMessage {
	ax := func(i, n int, ctype, unit string, cdelt float64) map[string]any {
		return map[string]any{"axis": i, "length": n, "ctype": ctype, "cunit": unit, "crval": 0.0, "crpix": 1.0, "cdelt": cdelt}
	}
	h := map[string]any{"axes": []any{ax(1, nx, "RA---SIN", "deg", -pixDeg), ax(2, ny, "DEC--SIN", "deg", pixDeg), ax(3, nv, "VRAD", "m/s", 1000)}}
	if bunit != "" {
		h["bunit"], h["bunit_status"] = bunit, "declared"
	}
	b, _ := json.Marshal(h)
	return b
}

type rerunEnv struct {
	t    *testing.T
	api  func(string) string
	cat  *catalog.Store
	js   *jobs.Store
	rn   *runs.Store
	host hosts.Host
}

func newRerunEnv(t *testing.T) *rerunEnv {
	t.Helper()
	base := t.TempDir()
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	hs, _ := hosts.Open(cat.DB())
	js, _ := jobs.Open(cat.DB())
	rn, _ := runs.Open(cat.DB())
	rs, _ := sourceroot.New([]string{base})
	mgr := hosts.NewManager("/nonexistent/ssh", nil)
	t.Cleanup(mgr.Close)
	ts := httptest.NewServer((&Server{Roots: rs, Store: cat, Runs: rn, HostDB: hs, Remote: mgr, Jobs: js}).Handler())
	t.Cleanup(ts.Close)
	h, err := hs.Create(hosts.Host{Name: "Cluster", Dest: "login1", Roots: []string{"/data"}})
	if err != nil {
		t.Fatal(err)
	}
	return &rerunEnv{t: t, api: func(p string) string { return ts.URL + p }, cat: cat, js: js, rn: rn, host: h}
}

// cube registers a dataset on the host.
func (e *rerunEnv) cube(path string, hostID int64, header json.RawMessage) catalog.Dataset {
	e.t.Helper()
	d, err := e.cat.Upsert(workspace.Local, "local-owner", catalog.Dataset{HostID: hostID, Path: path, Name: filepath.Base(path), SizeBytes: 4096, ModifiedAt: time.Unix(1700000000, 0), Header: header})
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

// recorded makes a job through the ordinary route and links a run to it, as an import does.
func (e *rerunEnv) recorded(template string, body map[string]any, name string) (runs.Run, jobs.Job) {
	e.t.Helper()
	body["hostId"], body["template"], body["name"] = e.host.ID, template, name
	r, b := postJSON(e.t, e.api("/api/jobs"), body)
	var j jobs.Job
	json.Unmarshal(b, &j)
	if r.StatusCode != 201 {
		e.t.Fatalf("draft: %d %s", r.StatusCode, b)
	}
	run, err := e.rn.Save(workspace.Local, "local-owner", "/data/vesqira_runs/"+name, runs.Parsed{Run: runs.Run{Name: name, Kind: template}}, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.js.LinkRun(j.ID, run.ID)
	return run, j
}

func vgtBody(cfg string, set map[string]string) map[string]any {
	return map[string]any{"vgt": map[string]any{"repoDir": "/shared/vgt", "configText": cfg, "set": set, "cpus": 8, "mem": "32G", "time": "04:00:00", "partition": "long"}}
}

func (e *rerunEnv) post(runID int64, body map[string]any) (int, string) {
	r, b := postJSON(e.t, e.api("/api/runs/"+itoa(int(runID))+"/rerun-on-input"), body)
	return r.StatusCode, string(b)
}

func (e *rerunEnv) jobCount() int {
	l, _ := e.js.List()
	return len(l)
}

// A draft made from a recorded run carries every setting but the input, says what it carried and what changed, records the full
// input identity and the link to the source run, and renders its script from the settings, not from the source script's text.
func TestConfigurationRunOnAnotherCube(t *testing.T) {
	t.Parallel()
	e := newRerunEnv(t)
	old := e.cube("/data/old.fits", e.host.ID, cubeHeader(256, 256, 40, 0.01, "K"))
	_ = old
	nw := e.cube("/data/new.fits", e.host.ID, cubeHeader(512, 400, 60, 0.005, "K"))
	locs, _ := e.cat.Locations(workspace.Local, nw.ID)
	sampled, full := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if err := e.cat.SetFingerprint(workspace.Local, locs[0].ID, "sampled", sampled, "api", nw.SizeBytes, nw.ModifiedAt.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := e.cat.SetFingerprint(workspace.Local, locs[0].ID, "full", full, "api", nw.SizeBytes, nw.ModifiedAt.UnixNano()); err != nil {
		t.Fatal(err)
	}
	src, srcJob := e.recorded("vgt", vgtBody(rerunVGTConfig, map[string]string{"ker": "6", "noise_mode": "channel"}), "src")
	// a hand edit to the source script is not carried over, and the summary says so
	if r, b := postJSON(t, e.api("/api/jobs/"+itoa(int(srcJob.ID))+"/script"), map[string]any{"script": srcJob.Script + "\necho edited by hand\n"}); r.StatusCode != 200 {
		t.Fatalf("edit: %d %s", r.StatusCode, b)
	}
	before := e.jobCount()

	// the preview says what would happen and stores nothing
	r, b := get(t, e.api("/api/runs/"+itoa(int(src.ID))+"/rerun-on-input?datasetId="+itoa(int(nw.ID))))
	if r.StatusCode != 200 || e.jobCount() != before {
		t.Fatalf("preview: %d %s (jobs %d -> %d)", r.StatusCode, b, before, e.jobCount())
	}
	conforms(t, responseSchemaAt(t, loadSpec(t), "/api/runs/{id}/rerun-on-input", "GET", 200, "application/json"), "rerun on input preview", b)

	code, out := e.post(src.ID, map[string]any{"datasetId": nw.ID})
	if code != 201 {
		t.Fatalf("draft: %d %s", code, out)
	}
	conforms(t, responseSchemaAt(t, loadSpec(t), "/api/runs/{id}/rerun-on-input", "POST", 201, "application/json"), "rerun on input", []byte(out))
	var res struct {
		Job     jobs.Job          `json:"job"`
		Summary inputRerunSummary `json:"summary"`
	}
	json.Unmarshal([]byte(out), &res)
	j, sum := res.Job, res.Summary
	if j.State != "draft" || j.ParentRunID == nil || *j.ParentRunID != src.ID || e.jobCount() != before+1 {
		t.Fatalf("job: %+v", j)
	}
	// every setting but the input: the overrides, the config's own values, the code folder and the resources
	for _, want := range []string{`input_file = "/data/new.fits"`, "ker = 6", `noise_mode = "channel"`, "dn = 20", "noise = 0.17", "min_valid = 101", "cd /shared/vgt",
		"#SBATCH --cpus-per-task=8", "#SBATCH --mem=32G", "#SBATCH --time=04:00:00", "#SBATCH --partition=long"} {
		if !strings.Contains(j.Script, want) {
			t.Errorf("the draft's script lacks %q:\n%s", want, j.Script)
		}
	}
	if strings.Contains(j.Script, "/data/old.fits") || strings.Contains(j.Script, "edited by hand") {
		t.Errorf("the old input or the source's hand edit was carried into the draft:\n%s", j.Script)
	}
	// the job hashes its input, so the run records the full SHA-256 as the job's own observation
	if !strings.Contains(j.Script, `"/data/new.fits" job full`) {
		t.Errorf("the draft does not ask the job to hash its input")
	}
	// what the summary says
	carried := map[string]string{}
	for _, c := range sum.Carried {
		carried[c.Key] = c.Value
	}
	if carried["vgt.ker"] != "6" || carried["vgt.dn"] != "20" || carried["noise.noise_mode"] != "channel" || carried["noise.noise"] != "0.17" || carried["code.repo_dir"] != "/shared/vgt" || carried["resources.cpus"] != "8" {
		t.Errorf("carried: %v", carried)
	}
	if _, ok := carried["input.input_file"]; ok {
		t.Errorf("the input is listed as carried: %v", carried)
	}
	if len(sum.Changed) != 1 || sum.Changed[0].Key != "input" || *sum.Changed[0].Parent != "/data/old.fits" || *sum.Changed[0].Now != "/data/new.fits" {
		t.Errorf("changed: %+v", sum.Changed)
	}
	if sum.SourceEditedLines == 0 || !strings.Contains(strings.Join(sum.Notes, " "), "edited by hand") {
		t.Errorf("the source's hand edit is not reported: %+v", sum)
	}
	// 0.01 deg and 0.005 deg per pixel: said in arcseconds, kept in pixels, nothing converted
	if !strings.Contains(strings.Join(sum.Notes, " "), "pixel scale differs") || !strings.Contains(strings.Join(sum.Notes, " "), "36 x 36 arcsec") || !strings.Contains(strings.Join(sum.Notes, " "), "18 x 18 arcsec") {
		t.Errorf("pixel scale note: %v", sum.Notes)
	}
	if sum.Mode != "best_effort" {
		t.Errorf("mode %q", sum.Mode)
	}

	// the input identity and the link are in the job's request
	rec := j.InputRerunOf()
	if rec == nil {
		// the job JSON carries no request; read it from the store
		got, _ := e.js.Get(j.ID)
		rec = got.InputRerunOf()
	}
	if rec == nil || rec.SourceRunID != src.ID || rec.SourceConfigHash == "" || rec.SourceConfigHash != rec.ConfigHash || rec.PreviousInput != "/data/old.fits" {
		t.Fatalf("record: %+v", rec)
	}
	in := rec.Input
	if in.DatasetID != nw.ID || in.Path != "/data/new.fits" || in.SizeBytes != 4096 || in.ModifiedAt != 1700000000 || in.SampledFP != sampled || in.FullSHA256 != full {
		t.Errorf("input identity: %+v", in)
	}

	// once the job's outputs are a run, that run is linked to its source and the two share a configuration hash
	run2, _ := e.rn.Save(workspace.Local, "local-owner", "/data/vesqira_runs/new", runs.Parsed{Run: runs.Run{Name: "new", Kind: "vgt"}}, nil)
	e.js.LinkRun(j.ID, run2.ID)
	var v2, v1 struct {
		ConfigHash *string         `json:"configHash"`
		InputRerun *inputRerunView `json:"inputRerun"`
	}
	_, b2 := get(t, e.api("/api/runs/"+itoa(int(run2.ID))))
	_, b1 := get(t, e.api("/api/runs/"+itoa(int(src.ID))))
	json.Unmarshal(b2, &v2)
	json.Unmarshal(b1, &v1)
	if v2.ConfigHash == nil || v1.ConfigHash == nil || *v2.ConfigHash != *v1.ConfigHash || *v2.ConfigHash != rec.ConfigHash {
		t.Errorf("config hashes: %s / %s", b1, b2)
	}
	if v2.InputRerun == nil || v2.InputRerun.SourceRunID != src.ID || !v2.InputRerun.SameConfiguration || v2.InputRerun.Input.FullSHA256 != full || v1.InputRerun != nil {
		t.Errorf("link: %s", b2)
	}
	// the runs list says the same, from one read
	_, lb := get(t, e.api("/api/runs"))
	if !strings.Contains(string(lb), `"sameConfiguration":true`) || !strings.Contains(string(lb), *v2.ConfigHash) {
		t.Errorf("the runs list does not carry the link: %s", lb)
	}
	// a later "run again" prefill of the new run carries the new input, not the old one
	_, pb := get(t, e.api("/api/runs/"+itoa(int(run2.ID))+"/rerun"))
	if !strings.Contains(string(pb), "/data/new.fits") || strings.Contains(string(pb), "/data/old.fits") {
		t.Errorf("run again of the new run does not start from the new input: %s", pb)
	}
}

// A run's recorded configuration is refused, with a sentence and nothing stored, whenever it cannot be repeated honestly.
func TestConfigurationRunOnAnotherCubeIsRefused(t *testing.T) {
	t.Parallel()
	e := newRerunEnv(t)
	e.cube("/data/old.fits", e.host.ID, cubeHeader(256, 256, 40, 0.01, "K"))
	okCube := e.cube("/data/ok.fits", e.host.ID, cubeHeader(300, 300, 40, 0.01, "K"))
	src, _ := e.recorded("vgt", vgtBody(rerunVGTConfig, nil), "src")

	imported, _ := e.rn.Save(workspace.Local, "local-owner", "/data/imp", runs.Parsed{Run: runs.Run{Name: "imp", Kind: "vgt"}}, nil)
	noSettings, _ := e.js.CreateDraft(jobs.Job{HostID: e.host.ID, Name: "old", Template: "vgt", Script: "#!/bin/bash\n", OutputDir: "/data/o"})
	oldRun, _ := e.rn.Save(workspace.Local, "local-owner", "/data/o", runs.Parsed{Run: runs.Run{Name: "o", Kind: "vgt"}}, nil)
	e.js.LinkRun(noSettings.ID, oldRun.ID)
	weighted := strings.Replace(rerunVGTConfig, "[noise]", "weight_file = \"/data/w.fits\"\n\n[noise]", 1)
	wRun, _ := e.recorded("vgt", vgtBody(weighted, nil), "weighted")
	bigDn, _ := e.recorded("vgt", vgtBody(rerunVGTConfig, map[string]string{"dn": "400"}), "bigdn")
	bigKer, _ := e.recorded("vgt", vgtBody(rerunVGTConfig, map[string]string{"ker": "350"}), "bigker")
	noRange := e.cube("/data/range.fits", e.host.ID, json.RawMessage(`{"axes":[{"axis":1,"length":300,"ctype":"RA---SIN"},{"axis":2,"length":300,"ctype":"DEC--SIN"}]}`))
	jy := e.cube("/data/jy.fits", e.host.ID, cubeHeader(300, 300, 40, 0.01, "Jy/beam"))
	local := e.cube("/local/x.fits", 0, cubeHeader(300, 300, 40, 0.01, "K"))
	otherHost, _ := hosts.Open(e.cat.DB())
	h2, _ := otherHost.Create(hosts.Host{Name: "Other", Dest: "login2", Roots: []string{"/data"}})
	elsewhere := e.cube("/data/else.fits", h2.ID, cubeHeader(300, 300, 40, 0.01, "K"))
	spaced := e.cube("/data/with space.fits", e.host.ID, cubeHeader(300, 300, 40, 0.01, "K"))
	reordered := e.cube("/data/zyx.fits", e.host.ID, json.RawMessage(`{"axes":[{"axis":1,"length":40,"ctype":"VRAD"},{"axis":2,"length":300,"ctype":"RA---SIN"},{"axis":3,"length":300,"ctype":"DEC--SIN"}]}`))

	cases := []struct {
		name  string
		run   int64
		body  map[string]any
		wants string
	}{
		{"imported run", imported.ID, map[string]any{"datasetId": okCube.ID}, "imported"},
		{"job without settings", oldRun.ID, map[string]any{"datasetId": okCube.ID}, "without its settings"},
		{"no dataset", src.ID, map[string]any{}, "choose the cube"},
		{"unknown dataset", src.ID, map[string]any{"datasetId": 9999}, "does not exist"},
		{"strict", src.ID, map[string]any{"datasetId": okCube.ID, "rerunMode": "strict"}, "strict"},
		{"bad mode", src.ID, map[string]any{"datasetId": okCube.ID, "rerunMode": "fast"}, "strict, best_effort or adapted"},
		{"weight file", wRun.ID, map[string]any{"datasetId": okCube.ID}, "weight file"},
		{"dn larger than the cube", bigDn.ID, map[string]any{"datasetId": okCube.ID}, "dn is 400 pixels"},
		{"ker larger than the cube", bigKer.ID, map[string]any{"datasetId": okCube.ID}, "ker is 350 pixels"},
		{"not a cube", src.ID, map[string]any{"datasetId": noRange.ID}, "2 axes"},
		{"axes in another order", src.ID, map[string]any{"datasetId": reordered.ID}, "not reordered"},
		{"noise in another unit", src.ID, map[string]any{"datasetId": jy.ID}, "noise = 0.17 is in the data's unit"},
		{"cube on this computer", src.ID, map[string]any{"datasetId": local.ID}, "on this computer"},
		{"cube on another host", src.ID, map[string]any{"datasetId": elsewhere.ID}, "another host"},
		{"unusable path", src.ID, map[string]any{"datasetId": spaced.ID}, "without spaces"},
		{"name too long", src.ID, map[string]any{"datasetId": e.cube("/data/old2.fits", e.host.ID, cubeHeader(256, 256, 40, 0.01, "K")).ID, "name": strings.Repeat("n", 41)}, "at most 40"},
	}
	before := e.jobCount()
	for _, c := range cases {
		code, out := e.post(c.run, c.body)
		if code != 422 || !strings.Contains(out, c.wants) {
			t.Errorf("%s: %d %s, want 422 mentioning %q", c.name, code, out, c.wants)
		}
	}
	// the file the run already read
	oldDS, _, _ := e.cat.LocationOwner(workspace.Local, e.host.ID, "/data/old.fits")
	if code, out := e.post(src.ID, map[string]any{"datasetId": oldDS}); code != 422 || !strings.Contains(out, "already used") {
		t.Errorf("same file: %d %s", code, out)
	}
	if code, _ := e.post(98765, map[string]any{"datasetId": okCube.ID}); code != 404 {
		t.Errorf("unknown run: %d", code)
	}
	if e.jobCount() != before {
		t.Errorf("a refused request stored a job: %d -> %d", before, e.jobCount())
	}
	// the same cube is accepted once nothing contradicts the settings
	if code, out := e.post(src.ID, map[string]any{"datasetId": okCube.ID}); code != 201 {
		t.Errorf("the compatible cube was refused: %d %s", code, out)
	}
}

// A unit that cannot be compared is said, not assumed to match: the previous input is not in the catalog, and neither side is
// declared in the file.
func TestConfigurationRunSaysWhatItCouldNotCompare(t *testing.T) {
	t.Parallel()
	e := newRerunEnv(t)
	nw := e.cube("/data/new.fits", e.host.ID, cubeHeader(300, 300, 40, 0.01, ""))
	src, _ := e.recorded("vgt", vgtBody(rerunVGTConfig, nil), "src")
	code, out := e.post(src.ID, map[string]any{"datasetId": nw.ID})
	if code != 201 {
		t.Fatalf("%d %s", code, out)
	}
	for _, want := range []string{"could not be compared", "previous input is not in the catalog", "no full SHA-256"} {
		if !strings.Contains(out, want) {
			t.Errorf("notes lack %q: %s", want, out)
		}
	}
}

// ROHSA: the input key is "input", the crop must fit the new cube (1-based, as the pipeline reads it), and the config hash
// is the same for the source and the draft.
func TestConfigurationRunOnAnotherCubeForROHSA(t *testing.T) {
	t.Parallel()
	e := newRerunEnv(t)
	e.cube("/data/old.fits", e.host.ID, cubeHeader(1024, 1024, 100, 0.01, "K"))
	big := e.cube("/data/big.fits", e.host.ID, cubeHeader(600, 600, 100, 0.01, "K"))
	small := e.cube("/data/small.fits", e.host.ID, cubeHeader(300, 700, 100, 0.01, "K"))
	src, _ := e.recorded("rohsa", map[string]any{"rohsa": map[string]any{"repoDir": "/shared/vgt", "configText": rerunROHSAConfig, "set": map[string]string{"n_gauss": "8"},
		"cpus": 16, "mem": "40G", "time": "12:00:00"}}, "fit")
	if code, out := e.post(src.ID, map[string]any{"datasetId": small.ID}); code != 422 || !strings.Contains(out, "crop_size is 512 pixels") {
		t.Errorf("crop larger than the cube: %d %s", code, out)
	}
	code, out := e.post(src.ID, map[string]any{"datasetId": big.ID})
	if code != 201 {
		t.Fatalf("%d %s", code, out)
	}
	var res struct {
		Job     jobs.Job          `json:"job"`
		Summary inputRerunSummary `json:"summary"`
	}
	json.Unmarshal([]byte(out), &res)
	for _, want := range []string{`input = "/data/big.fits"`, "n_gauss = [8]", "crop_size = 512", "crop_x0 = 1", "lambda_amp = 10.0"} {
		if !strings.Contains(res.Job.Script, want) {
			t.Errorf("script lacks %q:\n%s", want, res.Job.Script)
		}
	}
	if strings.Contains(res.Job.Script, "/data/old.fits") || res.Summary.SourceConfigHash == "" || res.Summary.SourceConfigHash != res.Summary.ConfigHash {
		t.Errorf("summary: %+v", res.Summary)
	}
	// a crop that starts too far over for the new cube is refused too
	off, _ := e.recorded("rohsa", map[string]any{"rohsa": map[string]any{"repoDir": "/shared/vgt", "configText": rerunROHSAConfig, "set": map[string]string{"crop_x0": "200"},
		"cpus": 16, "mem": "40G", "time": "12:00:00"}}, "off")
	if code, out := e.post(off.ID, map[string]any{"datasetId": big.ID}); code != 422 || !strings.Contains(out, "crop_x0 = 200") {
		t.Errorf("crop_x0: %d %s", code, out)
	}
}
