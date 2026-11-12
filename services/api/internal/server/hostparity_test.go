package server

import (
	"bytes"
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
	"github.com/unsound-mint/vesqira/services/api/internal/masks"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/review"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/subsets"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// hpEnv is one API with a local source root and a stand-in host (ADR 0064): the host is a folder reached through a fake ssh that runs the
// remote command here, with the real worker deployed into a scratch "remote home", and Slurm stand-ins whose jobs the test runs by hand.
// The same fixture files sit on both sides, so every operation on the host can be compared with the same operation on this computer.
type hpEnv struct {
	t                                                   *testing.T
	ts, rv                                              *httptest.Server
	base, localRoot, derivedDir, remoteRoot, remoteHome string
	bin                                                 string
	mgr                                                 *hosts.Manager
	h                                                   hosts.Host
	regions                                             *regions.Store
	boards                                              *boards.Store
}

func newHostParity(t *testing.T) *hpEnv {
	t.Helper()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	e := &hpEnv{t: t, base: t.TempDir()}
	e.localRoot, e.derivedDir = filepath.Join(e.base, "local"), filepath.Join(e.base, "derived")
	e.remoteHome, e.remoteRoot = filepath.Join(e.base, "remote-home"), filepath.Join(e.base, "remote_data")
	for _, d := range []string{e.localRoot, e.derivedDir, e.remoteHome, e.remoteRoot} {
		os.MkdirAll(d, 0o755)
	}
	gen := exec.Command(py, filepath.Join(repo, "fixtures/generate.py"), e.localRoot)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// a mask file a researcher made elsewhere: nonzero where flagged, on the cube's pixels (no WCS: matched by shape)
	mk := exec.Command(py, "-c", `import sys, numpy as np
from astropy.io import fits
a = np.zeros((64, 24, 32), dtype='u1'); a[:, 3:7, 4:12] = 1; a[10, 20, 30] = 5
fits.PrimaryHDU(a).writeto(sys.argv[1])`, filepath.Join(e.localRoot, "flags.fits"))
	if out, err := mk.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, n := range []string{"hi_cube.fits", "flags.fits"} {
		b, _ := os.ReadFile(filepath.Join(e.localRoot, n))
		os.WriteFile(filepath.Join(e.remoteRoot, n), b, 0o644)
	}
	e.bin = filepath.Join(e.base, "slurm-bin")
	fakeSlurm(t, e.bin)
	sshScript := fmt.Sprintf(`#!/bin/sh
[ -e %q ] && exit 255
while [ $# -gt 0 ]; do case "$1" in -o) shift 2;; -T) shift;; *) break;; esac; done
shift
cmd=$(printf '%%s' "$*" | sed -e 's#\$HOME#%s#g' -e 's#bash -lc#bash -c#')
PATH=%s:$PATH exec sh -c "$cmd"
`, filepath.Join(e.remoteHome, "DOWN"), e.remoteHome, e.bin)
	sshBin := filepath.Join(e.base, "ssh")
	os.WriteFile(sshBin, []byte(sshScript), 0o755)
	e.mgr = hosts.NewManager(sshBin, workerFiles(t, repo))
	e.mgr.ControlDir = t.TempDir()
	t.Cleanup(e.mgr.Close)

	cat, err := catalog.Open("file:" + filepath.Join(e.base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	hs, _ := hosts.Open(cat.DB())
	js, _ := jobs.Open(cat.DB())
	rn, _ := runs.Open(cat.DB())
	ds, _ := derived.Open(cat.DB())
	bs, _ := boards.Open(cat.DB(), e.base)
	ms, err := masks.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	rg, _ := regions.Open(cat.DB())
	sb, _ := subsets.Open(cat.DB())
	e.regions, e.boards = rg, bs
	rs, err := sourceroot.New([]string{e.localRoot, e.derivedDir})
	if err != nil {
		t.Fatal(err)
	}
	local := &worker.Persistent{Command: []string{py, "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(local.Close)
	e.ts = httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: headerWorker(t, py, repo), Planes: local, Cache: NewPlaneCache(8 << 20), Boards: bs, Runs: rn,
		HostDB: hs, Remote: e.mgr, Jobs: js, Derived: ds, Masks: ms, Regions: rg, Subsets: sb,
		Analysis: &AnalysisConfig{Python: py, Script: filepath.Join(repo, "services/scientific-worker/analysis.py"), DerivedDir: e.derivedDir, Timeout: 2 * time.Minute}}).Handler())
	t.Cleanup(e.ts.Close)
	e.rv = httptest.NewServer((&review.Handler{Store: bs}).Handler())
	t.Cleanup(e.rv.Close)
	code, hb := e.call("POST", "/api/hosts", map[string]any{"name": "Cluster", "dest": "login1", "python": py, "roots": []string{e.remoteRoot}})
	if code != 201 {
		t.Fatalf("host: %d %s", code, hb)
	}
	json.Unmarshal(hb, &e.h)
	return e
}

func (e *hpEnv) call(method, p string, body any) (int, []byte) {
	e.t.Helper()
	return callJSON(e.t, method, e.ts.URL+p, body)
}

func (e *hpEnv) get(p string) (int, []byte) {
	e.t.Helper()
	r, b := get(e.t, e.ts.URL+p)
	return r.StatusCode, b
}

// pair registers the same file on this computer and on the host.
func (e *hpEnv) pair(name string) (loc, rem catalog.Dataset) {
	e.t.Helper()
	json.NewDecoder(post(e.t, e.ts, filepath.Join(e.localRoot, name), nil).Body).Decode(&loc)
	code, b := e.call("POST", fmt.Sprintf("/api/hosts/%d/register", e.h.ID), map[string]any{"path": filepath.Join(e.remoteRoot, name)})
	if code != 201 || loc.ID == 0 {
		e.t.Fatalf("register %s: %d %s (local %d)", name, code, b, loc.ID)
	}
	json.Unmarshal(b, &rem)
	return loc, rem
}

type hpMask struct {
	masks.Mask
	Stale bool `json:"stale"`
}

func (e *hpEnv) mask(id int64, body map[string]any) (int, hpMask, string) {
	e.t.Helper()
	code, b := e.call("POST", fmt.Sprintf("/api/datasets/%d/masks", id), body)
	var m hpMask
	json.Unmarshal(b, &m)
	return code, m, string(b)
}

// runJob submits a drafted job, runs its script as the cluster would (the stand-in sbatch does not), marks it completed and refreshes it.
func (e *hpEnv) runJob(job jobs.Job, edit func(outDir string)) {
	e.t.Helper()
	jp := fmt.Sprintf("/api/jobs/%d", job.ID)
	if code, b := e.call("POST", jp+"/submit", map[string]any{"scriptHash": job.ScriptHash}); code != 200 {
		e.t.Fatalf("submit: %d %s", code, b)
	}
	_, jb := e.get(jp)
	var cur jobs.Job
	json.Unmarshal(jb, &cur)
	jobDir := filepath.Join(e.remoteHome, "vesqira-jobs", cur.WorkDir)
	sent, _ := os.ReadFile(filepath.Join(jobDir, "job.sh"))
	os.WriteFile(filepath.Join(jobDir, "run.sh"), []byte(strings.ReplaceAll(string(sent), "$HOME", e.remoteHome)), 0o755)
	run := exec.Command("bash", "run.sh")
	run.Dir = jobDir
	run.Env = append(os.Environ(), "SLURM_SUBMIT_DIR="+jobDir)
	if out, err := run.CombinedOutput(); err != nil {
		e.t.Fatalf("running the job script: %v\n%s", err, out)
	}
	if edit != nil {
		edit(cur.OutputDir)
	}
	os.WriteFile(filepath.Join(e.bin, "state", cur.SlurmID), []byte("COMPLETED\n"), 0o644)
	e.call("POST", jp+"/refresh", map[string]any{})
}

func derivedFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, x := range ents {
		out = append(out, x.Name())
	}
	return out
}

// hostMaskFile is the one file of a mask in the host's derived folder (the API never says a mask's path): mask-<dataset>-<type>-<hex>.fits.
func hostMaskFile(t *testing.T, dir string, m masks.Mask) string {
	t.Helper()
	got, _ := filepath.Glob(filepath.Join(dir, fmt.Sprintf("mask-%d-%s-*.fits", m.DatasetID, m.Type)))
	if len(got) != 1 {
		t.Fatalf("mask %d on the host: %v", m.ID, got)
	}
	return got[0]
}

// Masks of a file on a host are made and kept on the host, in its derived folder, by the host's copy of the program; the hatch plane, the
// region spectrum and histogram with masks, and a statistics job with masks give the numbers the same operations give on a local copy of
// the same file. Nothing of the mask crosses but its summary and one plane; the refusals that remain say what to do.
func TestMasksOnAHostMatchTheSameMasksHere(t *testing.T) {
	t.Parallel()
	e := newHostParity(t)
	loc, rem := e.pair("hi_cube.fits")
	locFlags, remFlags := e.pair("flags.fits")
	hostDerived := filepath.Join(e.remoteRoot, "vesqira_derived")
	before := derivedFiles(t, e.derivedDir)

	reg := func(d catalog.Dataset) int64 {
		g, err := e.regions.Create(d.ID, "Knot", regions.Shape{Shape: "rect", X0: 4, Y0: 3, X1: 11, Y1: 9})
		if err != nil {
			t.Fatal(err)
		}
		return g.ID
	}
	locReg, remReg := reg(loc), reg(rem)
	hostile := `Knot "east" ' $(touch ` + filepath.Join(e.base, "PWNED") + `) ; \ !`
	cases := []struct {
		name     string
		body     func(d catalog.Dataset, region, flags int64) map[string]any
		wantType string
	}{
		{"missing", func(catalog.Dataset, int64, int64) map[string]any { return map[string]any{"method": "missing"} }, masks.Missing},
		{"low_snr_mad", func(catalog.Dataset, int64, int64) map[string]any {
			return map[string]any{"method": "low_snr", "k": 3.0, "sigma": map[string]any{"kind": "mad"}}
		}, masks.LowSNR},
		{"saturated", func(catalog.Dataset, int64, int64) map[string]any {
			return map[string]any{"method": "saturated", "threshold": 0.5, "source": "the receiver's datasheet, 'linear' up to 0.5 K"}
		}, masks.Saturated},
		{"regions", func(_ catalog.Dataset, region, _ int64) map[string]any {
			return map[string]any{"method": "regions", "regionIds": []int64{region}, "name": hostile}
		}, masks.UserExcluded},
		{"import", func(_ catalog.Dataset, _, flags int64) map[string]any {
			return map[string]any{"method": "import", "type": "other", "importDatasetId": flags, "nonzero": "flagged", "meaning": "a 'cosmetic' defect list"}
		}, masks.Other},
	}
	made := map[string][2]hpMask{}
	for _, c := range cases {
		code, lm, lb := e.mask(loc.ID, c.body(loc, locReg, locFlags.ID))
		if code != 201 {
			t.Fatalf("%s here: %d %s", c.name, code, lb)
		}
		code, rm, rb := e.mask(rem.ID, c.body(rem, remReg, remFlags.ID))
		if code != 201 {
			t.Fatalf("%s on the host: %d %s", c.name, code, rb)
		}
		if lm.Type != c.wantType || rm.Type != c.wantType || rm.HostID != e.h.ID || lm.HostID != 0 {
			t.Fatalf("%s: types %s/%s, hosts %d/%d", c.name, lm.Type, rm.Type, lm.HostID, rm.HostID)
		}
		if lm.Flagged != rm.Flagged || lm.Total != rm.Total || lm.Width != rm.Width || lm.Height != rm.Height || lm.Channels != rm.Channels || lm.Tier != rm.Tier || lm.Method != rm.Method {
			t.Fatalf("%s: the host's mask differs from the local one:\n here %+v\n host %+v", c.name, lm, rm)
		}
		if lm.Flagged == 0 && c.name != "low_snr_mad" {
			t.Errorf("%s flags nothing: the comparison proves little", c.name)
		}
		var lr, rr map[string]any
		json.Unmarshal(lm.Rule, &lr)
		json.Unmarshal(rm.Rule, &rr)
		delete(lr, masks.SourceDatasetKey)
		delete(rr, masks.SourceDatasetKey)
		if fmt.Sprint(lr) != fmt.Sprint(rr) {
			t.Errorf("%s: rules differ:\n here %v\n host %v", c.name, lr, rr)
		}
		if strings.Contains(string(rm.Rule), e.remoteRoot) || strings.Contains(rb, e.remoteRoot) {
			t.Errorf("%s: the mask's description names a host path: %s", c.name, rb)
		}
		made[c.name] = [2]hpMask{lm, rm}
	}
	if _, err := os.Stat(filepath.Join(e.base, "PWNED")); err == nil {
		t.Fatal("a mask name ran a command on the host")
	}
	if made["regions"][1].Name != strings.TrimSpace(hostile) && !strings.Contains(made["regions"][1].Name, `"east"`) {
		t.Errorf("the name was not kept as typed: %q", made["regions"][1].Name)
	}
	// the files are on the host, in its derived folder, and their identity is the host's: nothing new here but the local masks
	files := derivedFiles(t, hostDerived)
	if len(files) != len(cases) {
		t.Fatalf("host derived folder: %v", files)
	}
	for _, f := range files {
		if strings.HasPrefix(f, ".part-") {
			t.Errorf("a temporary file was left on the host: %s", f)
		}
	}
	for _, c := range cases {
		rm := made[c.name][1]
		p := hostMaskFile(t, hostDerived, rm.Mask)
		var row masks.Mask
		_, lb := e.get(fmt.Sprintf("/api/datasets/%d/masks", rem.ID))
		var list struct{ Masks []hpMask }
		json.Unmarshal(lb, &list)
		for _, m := range list.Masks {
			if m.ID == rm.ID {
				row = m.Mask
			}
		}
		if row.SHA256 != fileSHA(t, p) || row.SHA256 == "" {
			t.Errorf("%s: the recorded identity is not the host file's: %s vs %s", c.name, row.SHA256, fileSHA(t, p))
		}
		if row.HostID != e.h.ID {
			t.Errorf("%s: the row does not record the host: %+v", c.name, row)
		}
	}
	if after := derivedFiles(t, e.derivedDir); len(after) != len(before)+len(cases) {
		t.Errorf("only the local masks may land in the local derived folder: %v -> %v", before, after)
	}

	// the hatch: one plane of each mask, the same bytes here and from the host
	for _, c := range cases {
		lm, rm := made[c.name][0], made[c.name][1]
		for _, ch := range []int{0, 31} {
			c1, a := e.get(fmt.Sprintf("/api/masks/%d/plane?channel=%d", lm.ID, ch))
			c2, b := e.get(fmt.Sprintf("/api/masks/%d/plane?channel=%d", rm.ID, ch))
			if c1 != 200 || c2 != 200 {
				t.Fatalf("%s plane: %d %d %s", c.name, c1, c2, b)
			}
			_, pa := subFrame(t, a)
			_, pb := subFrame(t, b)
			if !bytes.Equal(pa, pb) || len(pa) != 32*24 {
				t.Fatalf("%s plane %d: the host's hatch differs from the local one", c.name, ch)
			}
		}
	}

	// region spectrum and region histogram with masks: the host's worker applies the masks next to the file
	q := func(c string, pol string, m [2]hpMask, which int) string {
		return fmt.Sprintf("&mask=%d:%s", m[which].ID, pol) + c
	}
	box := "shape=rect&x0=2&y0=2&x1=20&y1=15"
	for _, pair := range [][2]string{{"saturated", "exclude"}, {"regions", "exclude"}, {"missing", "exclude"}, {"import", "keep"}} {
		m := made[pair[0]]
		c1, sa := e.get(fmt.Sprintf("/api/datasets/%d/spectrum?%s%s", loc.ID, box, q("", pair[1], m, 0)))
		c2, sb := e.get(fmt.Sprintf("/api/datasets/%d/spectrum?%s%s", rem.ID, box, q("", pair[1], m, 1)))
		if c1 != 200 || c2 != 200 {
			t.Fatalf("%s spectrum: %d %d %s", pair[0], c1, c2, sb)
		}
		ha, pa := subFrame(t, sa)
		hb, pb := subFrame(t, sb)
		if !bytes.Equal(pa, pb) || fmt.Sprint(ha["excluded_by_mask"]) != fmt.Sprint(hb["excluded_by_mask"]) || fmt.Sprint(ha["no_value"]) != fmt.Sprint(hb["no_value"]) {
			t.Fatalf("%s spectrum differs:\n here %v\n host %v", pair[0], ha, hb)
		}
		if strings.Contains(string(sb), e.remoteRoot) {
			t.Errorf("%s: the spectrum names a host path", pair[0])
		}
		hq := "&channel=30&min=0&max=2&bins=8"
		c1, ha2 := e.get(fmt.Sprintf("/api/datasets/%d/region-histogram?%s%s%s", loc.ID, box, hq, q("", pair[1], m, 0)))
		c2, hb2 := e.get(fmt.Sprintf("/api/datasets/%d/region-histogram?%s%s%s", rem.ID, box, hq, q("", pair[1], m, 1)))
		if c1 != 200 || c2 != 200 || string(ha2) != strings.ReplaceAll(string(hb2), fmt.Sprintf(`"id": %d`, m[1].ID), fmt.Sprintf(`"id": %d`, m[0].ID)) {
			t.Fatalf("%s histogram differs: %d %d\n here %s\n host %s", pair[0], c1, c2, ha2, hb2)
		}
	}
	// a mask of the host's file cannot be applied to the local copy, and the reverse (each is another dataset; and it lives elsewhere)
	if c, b := e.get(fmt.Sprintf("/api/datasets/%d/spectrum?%s&mask=%d:exclude", loc.ID, box, made["saturated"][1].ID)); c != 422 {
		t.Errorf("a host mask on the local dataset: %d %s", c, b)
	}

	// statistics as a job on the host, with masks: the script names the mask files by path, type and policy only, and the result is the
	// local one
	uses := func(i int) []map[string]any {
		return []map[string]any{{"id": made["saturated"][i].ID, "policy": "exclude"}, {"id": made["regions"][i].ID, "policy": "exclude"}}
	}
	code, lsb := e.call("POST", "/api/analysis", map[string]any{"op": "stats", "datasetId": loc.ID, "masks": uses(0)})
	if code != 200 {
		t.Fatalf("local stats: %d %s", code, lsb)
	}
	code, jb := e.call("POST", "/api/analysis/job", map[string]any{"op": "stats", "datasetId": rem.ID, "masks": uses(1)})
	if code != 201 {
		t.Fatalf("stats job with masks: %d %s", code, jb)
	}
	var job jobs.Job
	json.Unmarshal(jb, &job)
	if !strings.Contains(job.Script, "--masks '[{") || !strings.Contains(job.Script, hostDerived) || strings.Contains(job.Script, "east") || strings.Contains(job.Script, "datasheet") {
		t.Fatalf("the script must name the masks by path, type and policy only:\n%s", job.Script)
	}
	if !strings.Contains(job.Params, `"masks"`) || strings.Contains(job.Params, e.remoteRoot) {
		t.Errorf("the job's record keeps the id-free, path-free masks: %s", job.Params)
	}
	e.runJob(job, nil)
	code, rsb := e.get(fmt.Sprintf("/api/jobs/%d/result", job.ID))
	if code != 200 {
		t.Fatalf("job result: %d %s", code, rsb)
	}
	var ls, rs struct {
		Summary map[string]any `json:"summary"`
	}
	json.Unmarshal(lsb, &ls)
	json.Unmarshal(rsb, &rs)
	for _, k := range []string{"mean", "median", "std", "min", "max", "valid_pixels", "count"} {
		if fmt.Sprint(ls.Summary[k]) != fmt.Sprint(rs.Summary[k]) {
			t.Errorf("stats %s: here %v, host job %v", k, ls.Summary[k], rs.Summary[k])
		}
	}
	if ls.Summary["masks"] == nil || rs.Summary["masks"] == nil {
		t.Errorf("both summaries say which masks were applied: %v / %v", ls.Summary["masks"], rs.Summary["masks"])
	}

	// refusals kept, each with what to do instead
	for name, c := range map[string]struct {
		body map[string]any
		code int
		want string
	}{
		"noise map on another machine": {map[string]any{"method": "low_snr", "k": 3.0, "sigma": map[string]any{"kind": "map", "datasetId": locFlags.ID}}, 409, "same machine"},
		"mask file on another machine": {map[string]any{"method": "import", "type": "invalid", "importDatasetId": locFlags.ID, "nonzero": "flagged"}, 409, "same machine"},
	} {
		if code, _, b := e.mask(rem.ID, c.body); code != c.code || !strings.Contains(b, c.want) {
			t.Errorf("%s: %d %s", name, code, b)
		}
	}
	code, jb = e.call("POST", "/api/analysis/job", map[string]any{"op": "cutout", "datasetId": rem.ID, "params": map[string]string{"x0": "0", "x1": "5", "y0": "0", "y1": "5"},
		"carryMasks": []map[string]any{{"id": made["saturated"][1].ID, "carry": true}}})
	if code != 422 || !strings.Contains(string(jb), "make the masks again on the result") {
		t.Errorf("carrying in a host job: %d %s", code, jb)
	}

	// a mask file that changed on the host is refused everywhere it would be applied or drawn, and says what to do
	sat := made["saturated"][1]
	p := hostMaskFile(t, hostDerived, sat.Mask)
	later := time.Now().Add(time.Hour)
	os.Chtimes(p, later, later)
	if c, b := e.get(fmt.Sprintf("/api/datasets/%d/spectrum?%s&mask=%d:exclude", rem.ID, box, sat.ID)); c != 409 || !strings.Contains(string(b), "make it again") {
		t.Errorf("a changed host mask: %d %s", c, b)
	}
	if c, b := e.get(fmt.Sprintf("/api/masks/%d/plane?channel=0", sat.ID)); c != 409 || !strings.Contains(string(b), "make the mask again") {
		t.Errorf("a changed host mask's plane: %d %s", c, b)
	}
	// the host does not answer: a sentence, nothing recorded, no file
	os.WriteFile(filepath.Join(e.remoteHome, "DOWN"), nil, 0o644)
	e.mgr.Forget(e.h.ID)
	if c, _, b := e.mask(rem.ID, map[string]any{"method": "missing"}); c != 502 || !strings.Contains(b, "VPN") {
		t.Errorf("an unreachable host: %d %s", c, b)
	}
	os.Remove(filepath.Join(e.remoteHome, "DOWN"))
	if got, _ := filepath.Glob(filepath.Join(hostDerived, "mask-*")); len(got) != len(cases) { // the statistics job has its own folder there too
		t.Errorf("an unreachable host left files: %v", got)
	}
}

// A virtual subset of a file on a host is read through the host's worker with the same readouts as the same subset of a local copy, and
// written as a file on the host, in its derived folder, registered as a dataset there with the same pixels and the same derived record.
func TestSubsetsOnAHostMatchTheSameSubsetHere(t *testing.T) {
	t.Parallel()
	e := newHostParity(t)
	loc, rem := e.pair("hi_cube.fits")
	// the subset forms refuse double quotes, backslashes and backticks; what is left of shell syntax must still reach the host as text (stdin)
	note := "the core's every second pixel; for $(touch " + filepath.Join(e.base, "PWNED") + ") & it's; '$(id)'"
	def := map[string]any{"name": "Core 'east'; $(id)", "reason": note, "box": map[string]any{"x0": 3, "x1": 20, "y0": 2, "y1": 15}, "step": 2, "channels": map[string]any{"c0": 10, "c1": 40}}
	mk := func(d catalog.Dataset) subsetView {
		code, b := e.call("POST", fmt.Sprintf("/api/datasets/%d/subsets", d.ID), def)
		if code != 201 {
			t.Fatalf("subset of %d: %d %s", d.ID, code, b)
		}
		var doc subDoc
		json.Unmarshal(b, &doc)
		return doc.Subset
	}
	ls, rs := mk(loc), mk(rem)
	lp, rp := fmt.Sprintf("/api/subsets/%d", ls.ID), fmt.Sprintf("/api/subsets/%d", rs.ID)
	same := func(what, a, b string) {
		t.Helper()
		c1, x := e.get(lp + a)
		c2, y := e.get(rp + b)
		if c1 != 200 || c2 != 200 {
			t.Fatalf("%s: %d %d %s", what, c1, c2, y)
		}
		if len(x) > 4 && x[0] != '{' {
			hx, px := subFrame(t, x)
			hy, py := subFrame(t, y)
			if !bytes.Equal(px, py) || fmt.Sprint(hx["window"]) != fmt.Sprint(hy["window"]) {
				t.Fatalf("%s differs: %v / %v", what, hx, hy)
			}
			return
		}
		var mx, my map[string]any
		json.Unmarshal(x, &mx)
		json.Unmarshal(y, &my)
		for _, k := range []string{"width", "height", "channels", "window", "sky", "spectral", "wcs", "value"} {
			if fmt.Sprint(mx[k]) != fmt.Sprint(my[k]) {
				t.Fatalf("%s %s: here %v, host %v", what, k, mx[k], my[k])
			}
		}
		if bytes.Contains(y, []byte(e.remoteRoot)) && what != "view" {
			t.Errorf("%s names a host path", what)
		}
	}
	same("view", "/view", "/view")
	for _, ch := range []string{"0", "15", "30"} {
		same("plane "+ch, "/plane?channel="+ch, "/plane?channel="+ch)
	}
	same("coord", "/coord?x=4&y=3&channel=12", "/coord?x=4&y=3&channel=12")
	same("spectrum", "/spectrum?shape=rect&x0=1&y0=1&x1=4&y1=3", "/spectrum?shape=rect&x0=1&y0=1&x1=4&y1=3")

	// a mask of the source that is not carried is left behind on the host, and the record says so (carrying: its own test)
	if code, _, b := e.mask(rem.ID, map[string]any{"method": "saturated", "threshold": 0.5, "source": "datasheet"}); code != 201 {
		t.Fatalf("mask: %d %s", code, b)
	}
	_, lb := e.get(fmt.Sprintf("/api/datasets/%d/masks", rem.ID))
	var list struct{ Masks []hpMask }
	json.Unmarshal(lb, &list)

	code, lb2 := e.call("POST", lp+"/materialise", map[string]any{})
	if code != 201 {
		t.Fatalf("materialise here: %d %s", code, lb2)
	}
	code, rb := e.call("POST", rp+"/materialise", map[string]any{})
	if code != 201 {
		t.Fatalf("materialise on the host: %d %s", code, rb)
	}
	if _, err := os.Stat(filepath.Join(e.base, "PWNED")); err == nil {
		t.Fatal("a subset's text ran a command on the host")
	}
	var lm, rm subDoc
	json.Unmarshal(lb2, &lm)
	json.Unmarshal(rb, &rm)
	hostDerived := filepath.Join(e.remoteRoot, "vesqira_derived")
	if rm.Dataset.HostID != e.h.ID || filepath.Dir(rm.Dataset.Path) != hostDerived || !strings.HasPrefix(filepath.Base(rm.Dataset.Path), "subset-") {
		t.Fatalf("the file is written and registered on the host: %+v", rm.Dataset)
	}
	for _, f := range derivedFiles(t, e.derivedDir) {
		if strings.HasPrefix(f, fmt.Sprintf("subset-%d-", rs.ID)) {
			t.Errorf("the host's subset was written here: %s", f)
		}
	}
	for _, f := range derivedFiles(t, hostDerived) {
		if strings.HasPrefix(f, ".part-") {
			t.Errorf("a temporary file was left on the host: %s", f)
		}
	}
	for _, ch := range []string{"0", "15", "30"} {
		c1, a := e.get(fmt.Sprintf("/api/datasets/%d/plane?channel=%s", lm.Dataset.ID, ch))
		c2, b := e.get(fmt.Sprintf("/api/datasets/%d/plane?channel=%s", rm.Dataset.ID, ch))
		if c1 != 200 || c2 != 200 {
			t.Fatalf("plane of the files: %d %d", c1, c2)
		}
		_, pa := subFrame(t, a)
		_, pb := subFrame(t, b)
		if !bytes.Equal(pa, pb) {
			t.Fatalf("channel %s of the file written on the host differs from the one written here", ch)
		}
	}
	_, ld := e.get(fmt.Sprintf("/api/datasets/%d/derived", lm.Dataset.ID))
	_, rd := e.get(fmt.Sprintf("/api/datasets/%d/derived", rm.Dataset.ID))
	var lrec, rrec derived.Record
	json.Unmarshal(ld, &lrec)
	json.Unmarshal(rd, &rrec)
	var lpm, rpm map[string]any
	json.Unmarshal(lrec.Params, &lpm)
	json.Unmarshal(rrec.Params, &rpm)
	if rrec.Op != "subset" || fmt.Sprint(lpm["subset"]) != fmt.Sprint(rpm["subset"]) || rpm["source"].(map[string]any)["name"] != "hi_cube.fits" {
		t.Errorf("derived records differ:\n here %s\n host %s", ld, rd)
	}
	if !strings.Contains(string(rd), `"leftBehind":[{`) || !strings.Contains(string(rd), "datasheet") {
		t.Errorf("the host's file records the mask left behind: %s", rd)
	}
	// the same version is reused, not written twice
	if code, b := e.call("POST", rp+"/materialise", map[string]any{}); code != 200 || !strings.Contains(string(b), `"reused":true`) {
		t.Errorf("reuse on the host: %d %s", code, b)
	}
	if n := len(derivedFiles(t, hostDerived)); n != 2 { // the mask and the one subset file
		t.Errorf("host derived folder holds %d files", n)
	}

	// the reviewer: a published figure of the host's file names neither a host path nor the derived folder, and the review server has no
	// route to a mask or a subset at all
	_, fb := e.call("POST", "/api/figures", map[string]any{"datasetId": rm.Dataset.ID, "title": "Core", "view": map[string]any{"kind": "plane", "channel": 0}, "previewPng": testPNG(t)})
	var fig boards.Figure
	json.Unmarshal(fb, &fig)
	_, bb := e.call("POST", "/api/boards", map[string]any{"title": "Host"})
	var b boards.Board
	json.Unmarshal(bb, &b)
	e.call("POST", fmt.Sprintf("/api/boards/%d/items", b.ID), map[string]any{"figureId": fig.ID})
	_, pub := e.call("POST", fmt.Sprintf("/api/boards/%d/publish", b.ID), map[string]any{"hours": 1})
	var tok struct{ Token string }
	json.Unmarshal(pub, &tok)
	if tok.Token == "" {
		t.Fatalf("publish: %s", pub)
	}
	_, rbody := get(t, e.rv.URL+"/review-api/"+tok.Token)
	for _, leak := range []string{e.remoteRoot, "vesqira_derived", e.remoteHome} { // not e.base: the subset's reason, which the reviewer reads, names it
		if strings.Contains(string(rbody), leak) {
			t.Errorf("the reviewer's response names %q", leak)
		}
	}
	for _, route := range []string{fmt.Sprintf("/api/masks/%d/plane?channel=0", list.Masks[0].ID), fmt.Sprintf("/api/subsets/%d/view", rs.ID),
		fmt.Sprintf("/api/datasets/%d/masks", rem.ID), "/review-api/" + tok.Token + fmt.Sprintf("/masks/%d/plane", list.Masks[0].ID)} {
		if r, body := get(t, e.rv.URL+route); r.StatusCode == 200 || bytes.Contains(body, []byte(e.remoteRoot)) {
			t.Errorf("the reviewer reached %s: %d", route, r.StatusCode)
		}
	}
}

// Corrections reach a file on a host: the host's worker applies them and echoes their hash, an older worker that does not is refused, a
// subset written on the host carries them, and an analysis job is given them and must echo them before its result is registered.
func TestCorrectionsThroughAHostMatchTheSameCorrectionsHere(t *testing.T) {
	t.Parallel()
	e := newHostParity(t)
	loc, rem := e.pair("hi_cube.fits")
	set := func(d catalog.Dataset, unit string, crval2 float64) {
		_, lb := e.get(fmt.Sprintf("/api/datasets/%d/corrections", d.ID))
		var l struct {
			Last int64 `json:"last"`
		}
		json.Unmarshal(lb, &l)
		code, b := e.call("POST", fmt.Sprintf("/api/datasets/%d/corrections", d.ID), map[string]any{"action": "set", "unit": unit, "wcs": map[string]any{"CRVAL2": crval2},
			"reason": "pointing correction from the observing log", "expect": l.Last})
		if code != 201 {
			t.Fatalf("correction: %d %s", code, b)
		}
	}
	set(loc, "Jy/beam", 2.05)
	set(rem, "Jy/beam", 2.05)
	for _, q := range []string{"/view", "/coord?x=4&y=3&channel=12", "/header-cards"} {
		c1, a := e.get(fmt.Sprintf("/api/datasets/%d%s", loc.ID, q))
		c2, b := e.get(fmt.Sprintf("/api/datasets/%d%s", rem.ID, q))
		if c1 != 200 || c2 != 200 {
			t.Fatalf("%s: %d %d %s", q, c1, c2, b)
		}
		var ma, mb map[string]any
		json.Unmarshal(a, &ma)
		json.Unmarshal(b, &mb)
		for _, k := range []string{"sky", "spectral", "bunit", "corrections", "cards", "wcs"} {
			if fmt.Sprint(ma[k]) != fmt.Sprint(mb[k]) {
				t.Fatalf("%s %s: here %v, host %v", q, k, ma[k], mb[k])
			}
		}
		if q == "/coord?x=4&y=3&channel=12" && !strings.Contains(string(b), `"wcs_corrected": ["CRVAL2"]`) {
			t.Fatalf("the coordinate is not the corrected one: %s", b)
		}
	}
	// the view says which unit is in force and that it is the researcher's (the header cards stay the file's own, here as there)
	if _, v := e.get(fmt.Sprintf("/api/datasets/%d/view", rem.ID)); !strings.Contains(string(v), `"bunit": "Jy/beam"`) || !strings.Contains(string(v), `"corrected"`) {
		t.Fatalf("the host's view does not carry the corrected unit: %s", v)
	}

	// an analysis job is given the corrections, applies them and echoes them; the result is the local run's
	code, lb := e.call("POST", "/api/analysis", map[string]any{"op": "moment", "datasetId": loc.ID, "params": map[string]string{"order": "1"}})
	if code != 201 {
		t.Fatalf("local moment: %d %s", code, lb)
	}
	code, jb := e.call("POST", "/api/analysis/job", map[string]any{"op": "moment", "datasetId": rem.ID, "params": map[string]string{"order": "1"}})
	if code != 201 {
		t.Fatalf("moment job of a corrected file: %d %s", code, jb)
	}
	var job jobs.Job
	json.Unmarshal(jb, &job)
	if !strings.Contains(job.Script, "--corrections '{") || !strings.Contains(job.Script, `"CRVAL2":2.05`) {
		t.Fatalf("the script must carry the corrections:\n%s", job.Script)
	}
	e.runJob(job, nil)
	code, regb := e.call("POST", fmt.Sprintf("/api/jobs/%d/register", job.ID), map[string]any{})
	if code != 201 {
		t.Fatalf("register: %d %s", code, regb)
	}
	var lres struct{ Dataset catalog.Dataset }
	json.Unmarshal(lb, &lres)
	var out catalog.Dataset
	json.Unmarshal(regb, &out)
	for _, q := range []string{"/plane?channel=0", "/coord?x=4&y=3&channel=0"} {
		c1, a := e.get(fmt.Sprintf("/api/datasets/%d%s", lres.Dataset.ID, q))
		c2, b := e.get(fmt.Sprintf("/api/datasets/%d%s", out.ID, q))
		if c1 != 200 || c2 != 200 {
			t.Fatalf("%s of the results: %d %d %s", q, c1, c2, b)
		}
		if a[0] == '{' {
			var ma, mb map[string]any
			json.Unmarshal(a, &ma)
			json.Unmarshal(b, &mb)
			if fmt.Sprint(ma["sky"]) != fmt.Sprint(mb["sky"]) || ma["sky"] == nil {
				t.Fatalf("the job's result does not carry the corrected WCS: %s vs %s", a, b)
			}
			continue
		}
		_, pa := subFrame(t, a)
		_, pb := subFrame(t, b)
		if !bytes.Equal(pa, pb) {
			t.Fatal("the job's moment map differs from the local one")
		}
	}
	_, rd := e.get(fmt.Sprintf("/api/datasets/%d/derived", out.ID))
	if !strings.Contains(string(rd), `"corrections":[{`) || !strings.Contains(string(rd), "pointing correction") || strings.Contains(string(rd), e.remoteRoot) {
		t.Errorf("the result records the corrections it was made with, without paths: %s", rd)
	}
	_, oc := e.get(fmt.Sprintf("/api/datasets/%d/header-cards", out.ID))
	if !strings.Contains(string(oc), "VESQWCSC") {
		t.Errorf("the result's header does not say it carries corrected values: %s", oc)
	}

	// a job whose program did not echo the corrections is not registered
	code, jb = e.call("POST", "/api/analysis/job", map[string]any{"op": "moment", "datasetId": rem.ID, "params": map[string]string{"order": "0"}})
	if code != 201 {
		t.Fatalf("second job: %d %s", code, jb)
	}
	json.Unmarshal(jb, &job)
	e.runJob(job, func(outDir string) {
		p := filepath.Join(outDir, "result.json")
		var res map[string]any
		raw, _ := os.ReadFile(p)
		json.Unmarshal(raw, &res)
		delete(res, "corrections_applied")
		raw, _ = json.Marshal(res)
		os.WriteFile(p, raw, 0o644)
	})
	if code, b := e.call("POST", fmt.Sprintf("/api/jobs/%d/register", job.ID), map[string]any{}); code != 502 || !strings.Contains(string(b), "did not apply the researcher's corrections") {
		t.Errorf("a job that ignored the corrections: %d %s", code, b)
	}

	// a subset written on the host carries the corrected header, like one written here
	mk := func(d catalog.Dataset) string {
		code, b := e.call("POST", fmt.Sprintf("/api/datasets/%d/subsets", d.ID), map[string]any{"name": "w", "reason": "r", "box": map[string]any{"x0": 0, "x1": 9, "y0": 0, "y1": 9}})
		if code != 201 {
			t.Fatalf("subset: %d %s", code, b)
		}
		var doc subDoc
		json.Unmarshal(b, &doc)
		code, b = e.call("POST", fmt.Sprintf("/api/subsets/%d/materialise", doc.Subset.ID), map[string]any{})
		if code != 201 {
			t.Fatalf("materialise: %d %s", code, b)
		}
		json.Unmarshal(b, &doc)
		_, c := e.get(fmt.Sprintf("/api/datasets/%d/coord?x=4&y=3&channel=12", doc.Dataset.ID))
		return string(c)
	}
	if a, b := mk(loc), mk(rem); a != b || !strings.Contains(b, "sky") {
		t.Errorf("the subsets' coordinates differ:\n here %s\n host %s", a, b)
	}

	// an older worker on the host, one that ignores the corrections, is refused: nothing uncorrected is shown
	deployed := filepath.Join(e.remoteHome, ".cache", "vesqira", e.mgr.Hash(), "plane_worker.py")
	src, err := os.ReadFile(deployed)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(string(src), `head = {**head, "corrections_applied": sorted(st["hash"] for st in applied.values())}`, `pass`, 1)
	if old == string(src) {
		t.Fatal("could not make an older worker: the echo line moved")
	}
	os.WriteFile(deployed, []byte(old), 0o644)
	e.mgr.Forget(e.h.ID)
	if code, b := e.get(fmt.Sprintf("/api/datasets/%d/coord?x=5&y=5&channel=1", rem.ID)); code != 502 || !strings.Contains(string(b), "uncorrected") {
		t.Errorf("an older worker on the host: %d %s", code, b)
	}
}
