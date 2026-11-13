package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/notify"
	"github.com/unsound-mint/vesqira/services/api/internal/sweeps"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// A ROHSA fit that lives on a host (ADR 0046, remote fits): imported by the host's worker, indexed there, auto-imported when its
// job completes. These tests use the stand-in host of jobwatch_test.go (ssh runs the remote command locally with $HOME moved to
// a scratch "remote home", and logs every command) and the real worker on both sides.

// writeROHSA writes the fixture fit under dir: <dir>/region_a/rohsa_n3.dat beside its crop, traces and product tree. rmse is
// the declared voxel_rmse, so two fits can be told apart in a sweep.
func (e *watchEnv) writeROHSA(dir, rmse string) {
	e.t.Helper()
	gen := exec.Command("python3", "-c", "import sys, pathlib, generate; generate.write_rohsa_run(pathlib.Path(sys.argv[1]))", dir)
	gen.Dir = filepath.Join(e.repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		e.t.Fatalf("%v\n%s", err, out)
	}
	if rmse != "" {
		f := filepath.Join(dir, "region_a", "rohsa_n3_products_trace.toml")
		b, _ := os.ReadFile(f)
		os.WriteFile(f, []byte(strings.Replace(string(b), "voxel_rmse = 0.05", "voxel_rmse = "+rmse, 1)), 0o644)
	}
}

func (e *watchEnv) importROHSAFromHost(path string, want int) (rohsaRunJSON, string) {
	e.t.Helper()
	r, b := postJSON(e.t, e.api("/api/runs/import"), map[string]any{"hostId": e.host.ID, "path": path})
	if r.StatusCode != want {
		e.t.Fatalf("import %s from the host: %d %s", path, r.StatusCode, b)
	}
	var run rohsaRunJSON
	json.Unmarshal(b, &run)
	return run, string(b)
}

func (e *watchEnv) cacheFiles() []string {
	ents, _ := os.ReadDir(filepath.Join(e.home, ".cache", "vesqira", "rohsa"))
	var out []string
	for _, x := range ents {
		out = append(out, x.Name())
	}
	return out
}

func (e *watchEnv) auditCount(action string) int {
	n := 0
	if es, err := e.srv.Audit.List(workspace.Local, "", 0, 500); err == nil {
		for _, a := range es {
			if a.Action == action {
				n++
			}
		}
	}
	return n
}

func TestROHSAImportFromAHostRegistersRemoteDatasetsAndReadsNoBulkData(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	e.writeROHSA(filepath.Join(e.remoteRoot, "fit"), "")
	dir := filepath.Join(e.remoteRoot, "fit", "region_a")
	run, raw := e.importROHSAFromHost(dir, 201) // the folder is enough
	if run.Kind != "rohsa" || run.Name != "rohsa_n3" || run.Rohsa == nil || run.Rohsa.CropDatasetID == nil || run.Rohsa.HostID != e.host.ID {
		t.Fatalf("run: %s", raw)
	}
	if run.Rohsa.NGauss != 3 || run.Rohsa.NGaussBasis != "declared by the run trace" || len(run.Metrics) != 2 || run.Metrics[0].Status != "declared" {
		t.Errorf("settings and metrics are the trace's, declared: %+v %+v", run.Rohsa, run.Metrics)
	}
	for _, bad := range []string{e.remoteRoot, "/shared/home", "someone"} {
		if strings.Contains(raw, bad) {
			t.Errorf("%q must not appear in the run: %s", bad, raw)
		}
	}
	// the crop and every product are datasets that live on the host, as other remote FITS files do
	ids := map[string]int64{"crop": *run.Rohsa.CropDatasetID}
	for _, o := range run.Outputs {
		ids[o.Product] = o.DatasetID
	}
	for _, role := range []string{"crop", "component_g1_amp_k", "component_g1_mu_kms", "component_g1_sigma_kms", "model_nhi_cm2", "residual_moment0_kkms", "cube_model"} {
		if ids[role] == 0 {
			t.Fatalf("%s not registered: %v", role, ids)
		}
		d, err := e.srv.Store.Get(workspace.Local, ids[role])
		if err != nil || d.HostID != e.host.ID || !strings.HasPrefix(d.Path, e.remoteRoot) {
			t.Errorf("%s: host %d path %q (%v), want a dataset on host %d", role, d.HostID, d.Path, err, e.host.ID)
		}
	}
	// ADR 0046's unit rules apply to the remote products as to local ones
	mu, _ := e.srv.Store.Get(workspace.Local, ids["component_g1_mu_kms"])
	if mu.UnitDeclaration == nil || mu.UnitDeclaration.Unit != "km/s" || mu.UnitDeclaration.DeclaredBy != "rohsa-run-adapter" {
		t.Errorf("mu map unit proposal: %+v", mu.UnitDeclaration)
	}
	if sig, _ := e.srv.Store.Get(workspace.Local, ids["component_g1_sigma_kms"]); sig.UnitDeclaration != nil {
		t.Errorf("a file that states a unit is never given a declaration: %+v", sig.UnitDeclaration)
	}
	if n := run.Rohsa.UnitNotes["component_g1_sigma_kms"]; !strings.Contains(n, "file says K") {
		t.Errorf("conflict note: %q", n)
	}
	// the .dat itself was not read, indexed or copied: no sidecar exists anywhere
	if files := e.cacheFiles(); len(files) != 0 {
		t.Errorf("importing wrote %v on the host", files)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.home, ".cache", "vesqira")); len(ents) != 1 {
		t.Errorf("only the deployed worker should be under the host's cache: %v", ents)
	}
	// the .dat gives the same run (one key per fit and host); another host's, or a local, path is another key
	again, _ := e.importROHSAFromHost(filepath.Join(dir, "rohsa_n3.dat"), 201)
	if again.ID != run.ID {
		t.Errorf("re-import by the .dat made a new run: %d vs %d", again.ID, run.ID)
	}
	if rs, _ := e.runs.List(workspace.Local); len(rs) != 1 {
		t.Errorf("%d runs", len(rs))
	}
	// refusals: outside the host's roots, not a fit, several fits, nothing there, an unknown host
	outside := filepath.Join(filepath.Dir(e.remoteRoot), "elsewhere")
	os.MkdirAll(outside, 0o755)
	e.writeROHSA(outside, "")
	e.importROHSAFromHost(filepath.Join(outside, "region_a"), 403)
	os.WriteFile(filepath.Join(dir, "rms_map.dat"), []byte("1 2 3\n"), 0o644)
	e.importROHSAFromHost(filepath.Join(dir, "rms_map.dat"), 422)
	os.Symlink(filepath.Join(outside, "region_a", "rohsa_n3.dat"), filepath.Join(dir, "rohsa_link.dat"))
	e.importROHSAFromHost(filepath.Join(dir, "rohsa_link.dat"), 403) // a link out of the roots
	os.Remove(filepath.Join(dir, "rohsa_link.dat"))
	os.WriteFile(filepath.Join(dir, "rohsa_n4.dat"), []byte("1 2 3\n"), 0o644)
	if _, b := e.importROHSAFromHost(dir, 422); !strings.Contains(b, "several ROHSA fits") || !strings.Contains(b, "rohsa_n4.dat") {
		t.Errorf("several fits: %s", b)
	}
	os.MkdirAll(filepath.Join(e.remoteRoot, "empty"), 0o755)
	e.importROHSAFromHost(filepath.Join(e.remoteRoot, "empty"), 422)
	if r, _ := postJSON(t, e.api("/api/runs/import"), map[string]any{"hostId": 9999, "path": dir}); r.StatusCode != 404 {
		t.Errorf("unknown host: %d", r.StatusCode)
	}
	if e.auditCount("rohsa run imported from host") != 2 {
		t.Errorf("imports from a host are audited")
	}
}

// The index of a remote .dat is built by the host's own worker in the account's cache folder, never next to the data, and a pixel
// is then a seek there. The answer equals what the local path gives for the same fixture, byte for byte.
func TestROHSAIndexIsBuiltOnTheHostCachedOffTheDataAndPixelsEqualTheLocalPath(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	e.srv.Cache = NewPlaneCache(8 << 20)
	e.srv.CacheDir = filepath.Join(filepath.Dir(e.home), "local-cache")
	e.writeROHSA(filepath.Join(e.remoteRoot, "fit"), "")
	e.writeROHSA(filepath.Join(filepath.Dir(e.remoteRoot), "localroot", "fit"), "") // the same fixture, on this machine
	dir := filepath.Join(e.remoteRoot, "fit", "region_a")
	remote, _ := e.importROHSAFromHost(dir, 201)
	r, b := postJSON(t, e.api("/api/runs/import"), map[string]any{"path": filepath.Join(filepath.Dir(e.remoteRoot), "localroot", "fit", "region_a")})
	var local rohsaRunJSON
	json.Unmarshal(b, &local)
	if r.StatusCode != 201 || local.ID == remote.ID || local.Rohsa.HostID != 0 {
		t.Fatalf("local import: %d %s", r.StatusCode, b)
	}
	rbase := e.api("/api/datasets/" + itoa(int(*remote.Rohsa.CropDatasetID)))
	lbase := e.api("/api/datasets/" + itoa(int(*local.Rohsa.CropDatasetID)))

	// the list answers for a crop on a host
	_, lb := get(t, rbase+"/rohsa")
	if !strings.Contains(string(lb), `"usable":true`) || !strings.Contains(string(lb), `"isCrop":true`) {
		t.Fatalf("runs for the remote crop: %s", lb)
	}
	// no index yet: refused, not a whole-file scan on the host
	if r, _ := get(t, rbase+"/rohsa/components?run="+itoa(int(remote.ID))+"&x=2&y=1"); r.StatusCode != 409 {
		t.Errorf("no index yet: %d, want 409", r.StatusCode)
	}
	r, ib := postJSON(t, rbase+"/rohsa/index", map[string]any{"runId": remote.ID})
	var idx map[string]any
	json.Unmarshal(ib, &idx)
	if r.StatusCode != 200 || idx["state"] != "ready" || idx["rows"].(float64) != 6*4*3 || idx["where"] != "host" {
		t.Fatalf("index: %d %s", r.StatusCode, ib)
	}
	for _, k := range []string{"cache_file", "created", "settled"} {
		if _, ok := idx[k]; ok {
			t.Errorf("%s reaches the page: %s", k, ib)
		}
	}
	if strings.Contains(string(ib), e.home) {
		t.Errorf("a location on the host reaches the page: %s", ib)
	}
	// the sidecar is in the host's own cache folder (private), and nothing is next to the data or in the local cache folder
	files := e.cacheFiles()
	if len(files) != 2 {
		t.Fatalf("host cache folder: %v", files)
	}
	if fi, err := os.Stat(filepath.Join(e.home, ".cache", "vesqira", "rohsa")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("the host's cache folder must be private: %v %v", fi, err)
	}
	ents, _ := os.ReadDir(dir)
	for _, x := range ents {
		if strings.HasSuffix(x.Name(), ".offs") || strings.HasSuffix(x.Name(), ".tmp") || (strings.HasSuffix(x.Name(), ".json") && x.Name() != "") {
			t.Errorf("nothing is written next to the data: %s", x.Name())
		}
	}
	if ents, _ := os.ReadDir(e.srv.CacheDir); len(ents) != 0 {
		t.Errorf("the local cache folder holds the host's index: %v", ents)
	}
	// audited once when it started and once when it settled; asking again (ready) adds nothing
	postJSON(t, rbase+"/rohsa/index", map[string]any{"runId": remote.ID})
	if e.auditCount("rohsa index started on host") != 1 || e.auditCount("rohsa index ready on host") != 1 {
		es, _ := e.srv.Audit.List(workspace.Local, "", 0, 50)
		t.Errorf("audit of the cache write: %+v", es)
	}
	es, _ := e.srv.Audit.List(workspace.Local, "run", remote.ID, 50)
	started := ""
	for _, a := range es {
		if a.Action == "rohsa index started on host" {
			started = a.Detail
		}
	}
	if !strings.Contains(started, filepath.Join(e.home, ".cache", "vesqira", "rohsa")) || !strings.Contains(started, "never in the data's folder") {
		t.Errorf("the audit says where the host was written: %q", started)
	}
	postJSON(t, lbase+"/rohsa/index", map[string]any{"runId": local.ID})

	// every pixel of the remote fit equals the local path's: head (components, axis, conventions) and curves
	for _, xy := range [][2]int{{0, 0}, {2, 1}, {5, 3}} {
		q := "&x=" + itoa(xy[0]) + "&y=" + itoa(xy[1])
		rr, rf := get(t, rbase+"/rohsa/components?run="+itoa(int(remote.ID))+q)
		lr, lf := get(t, lbase+"/rohsa/components?run="+itoa(int(local.ID))+q)
		if rr.StatusCode != 200 || lr.StatusCode != 200 {
			t.Fatalf("pixel %v: remote %d %s, local %d", xy, rr.StatusCode, rf, lr.StatusCode)
		}
		if !bytes.Equal(rf, lf) {
			n := int(binary.LittleEndian.Uint32(rf))
			m := int(binary.LittleEndian.Uint32(lf))
			t.Errorf("pixel %v differs between the host and the local path:\nremote %s\nlocal  %s", xy, rf[4:4+n], lf[4:4+m])
		}
	}
	if r, _ := get(t, rbase+"/rohsa/components?run="+itoa(int(remote.ID))+"&x=6&y=0"); r.StatusCode != 400 {
		t.Errorf("a pixel outside the crop: %d", r.StatusCode)
	}
	// a changed fit is a different file: its pixels are not served from what was indexed or cached
	dat := filepath.Join(dir, "rohsa_n3.dat")
	b2, _ := os.ReadFile(dat)
	os.WriteFile(dat, bytes.Replace(b2, []byte("1.6"), []byte("1.7"), 1), 0o644)
	if r, _ := get(t, rbase+"/rohsa/components?run="+itoa(int(remote.ID))+"&x=2&y=1"); r.StatusCode != 409 {
		t.Errorf("a changed .dat is indexed again before it is read: %d", r.StatusCode)
	}
	// the host is down: a clear answer, no stale pixel
	os.WriteFile(filepath.Join(e.home, "DOWN"), nil, 0o644)
	e.srv.Remote.Forget(e.host.ID)
	e.srv.Cache = NewPlaneCache(8 << 20)
	if r, b := get(t, rbase+"/rohsa/components?run="+itoa(int(remote.ID))+"&x=2&y=1"); r.StatusCode != 502 || !strings.Contains(string(b), "not reachable") {
		t.Errorf("host down: %d %s", r.StatusCode, b)
	}
}

// A request cannot move the host's index: the host's worker takes the folder from its launcher, not from the request, and
// refuses to follow a link in the cache folder.
func TestROHSAHostWorkerIgnoresACacheFolderFromTheRequest(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	e.writeROHSA(filepath.Join(e.remoteRoot, "fit"), "")
	run, _ := e.importROHSAFromHost(filepath.Join(e.remoteRoot, "fit", "region_a"), 201)
	crop, _ := e.srv.Store.Get(workspace.Local, *run.Rohsa.CropDatasetID)
	elsewhere := filepath.Join(filepath.Dir(e.home), "elsewhere-cache")
	req := map[string]any{"op": "rohsa_index", "path": crop.Path, "dat_path": filepath.Join(filepath.Dir(crop.Path), "rohsa_n3.dat"), "n_gauss": 3,
		"cache_dir": elsewhere, "budget_s": 5}
	if _, err := e.srv.Remote.Do(context.Background(), e.host, req); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(elsewhere); err == nil {
		t.Errorf("the worker wrote where the request said")
	}
	if len(e.cacheFiles()) != 2 {
		t.Errorf("the index belongs in the host's cache folder: %v", e.cacheFiles())
	}
	// a link where a sidecar belongs is never followed
	for _, f := range e.cacheFiles() {
		os.Remove(filepath.Join(e.home, ".cache", "vesqira", "rohsa", f))
	}
	target := filepath.Join(filepath.Dir(e.home), "victim")
	os.WriteFile(target, []byte("do not touch"), 0o644)
	req2 := func() error { _, err := e.srv.Remote.Do(context.Background(), e.host, req); return err }
	// the sidecar's name is a hash of path, size and mtime: make a link under every name the worker could use by linking after one run
	if err := req2(); err != nil {
		t.Fatal(err)
	}
	for _, f := range e.cacheFiles() {
		p := filepath.Join(e.home, ".cache", "vesqira", "rohsa", f)
		os.Remove(p)
		os.Symlink(target, p)
	}
	if err := req2(); err == nil {
		t.Error("an index written through a link")
	}
	if b, _ := os.ReadFile(target); string(b) != "do not touch" {
		t.Errorf("the link's target was changed: %q", b)
	}
}

// A job's output folder holds the fit, so the fit is imported when the job completes, once, linked to the job; Vesqira's own
// run.json beside it is written for ROHSA jobs too, and a rescan lists the job and its fit as one run.
func TestROHSAJobIsImportedOnceWhenItCompletesAndRescanListsOneRun(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	ctx := context.Background()
	j := e.submit(e.rohsaDraft("rohsa-a", map[string]string{"n_gauss": "3"}, nil), nil)
	if !j.AutoImport {
		t.Fatal("a ROHSA job imports its fit by default")
	}
	e.setSlurm(j.SlurmID, "RUNNING")
	w := newJobWatcher(MinWatchInterval)
	t0 := time.Now()
	e.srv.watchOnce(ctx, w, t0)
	e.writeROHSA(j.OutputDir, "") // the job writes <output>/region_a/rohsa_n3.dat and the rest
	e.setSlurm(j.SlurmID, "COMPLETED")
	e.srv.watchOnce(ctx, w, t0.Add(2*MinWatchInterval))
	e.srv.waitRecords()
	e.srv.waitImports()
	got := e.job(j.ID)
	if got.State != "completed" || got.RunID == nil {
		t.Fatalf("job after completion: state %s run %v detail %q", got.State, got.RunID, got.Detail)
	}
	var imported []notify.Notification
	for _, n := range e.notifications() {
		if n.Kind == notify.KindImported {
			imported = append(imported, n)
		}
	}
	if len(imported) != 1 || imported[0].RunID == nil || *imported[0].RunID != *got.RunID {
		t.Fatalf("notifications: %+v", e.notifications())
	}
	rs, _ := e.runs.List(workspace.Local)
	if len(rs) != 1 || rs[0].Kind != "rohsa" {
		t.Fatalf("runs: %+v", rs)
	}
	if rr, err := e.runs.ROHSA(workspace.Local, *got.RunID); err != nil || rr.Info.HostID != e.host.ID || rr.Info.CropDatasetID == nil {
		t.Errorf("the run's record: %+v %v", rr, err)
	}
	if l, _ := e.jobs.Lineages(); l[*got.RunID].JobID != j.ID {
		t.Errorf("the run is not linked to its job: %+v", l)
	}
	// looking again, from every direction, imports nothing more
	postJSON(t, e.api("/api/jobs/"+itoa(int(j.ID))+"/refresh"), map[string]any{})
	e.srv.autoImport(ctx, j.ID, e.host)
	e.srv.watchOnce(ctx, w, t0.Add(time.Hour))
	e.srv.waitImports()
	if rs, _ := e.runs.List(workspace.Local); len(rs) != 1 || len(e.notifications()) != 2 {
		t.Errorf("a second look changed things: %d runs, %d notifications", len(rs), len(e.notifications()))
	}
	// the manual button gives the same run (one key per fit)
	if r, b := postJSON(t, e.api("/api/jobs/"+itoa(int(j.ID))+"/register"), map[string]any{}); r.StatusCode != 201 {
		t.Errorf("register: %d %s", r.StatusCode, b)
	}
	if rs, _ := e.runs.List(workspace.Local); len(rs) != 1 {
		t.Errorf("the button made a second run: %d", len(rs))
	}

	// run.json is written beside the job's outputs and carries what discovery needs to recognise the fit as the job's
	rec, err := os.ReadFile(filepath.Join(j.OutputDir, jobs.RunRecordName))
	if err != nil {
		t.Fatalf("no run.json for a ROHSA job: %v", err)
	}
	var doc struct {
		Settings map[string]string
		Vesqira  struct {
			Record string
			Job    struct {
				ID        int64
				Template  string
				OutputDir string `json:"output_dir"`
			}
		}
	}
	if err := json.Unmarshal(rec, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Vesqira.Job.ID != j.ID || doc.Vesqira.Job.Template != "rohsa" || doc.Vesqira.Job.OutputDir != j.OutputDir || doc.Vesqira.Record != jobs.RunRecordMarker {
		t.Errorf("the record does not name its job: %+v", doc.Vesqira)
	}
	if doc.Settings["rohsa.n_gauss"] != "3" || doc.Settings["rohsa.lambda_amp"] == "" || doc.Settings["slurm.cpus-per-task"] != "32" {
		t.Errorf("the record's settings are the submitted config's: %v", doc.Settings)
	}
	// a rescan of the project folder finds one run, the fit, which names the job: not the record and the fit as two
	res := discover.Scan(ctx, discover.LocalFS{Root: e.remoteRoot}, discover.Options{})
	rel, _ := filepath.Rel(e.remoteRoot, j.OutputDir)
	if len(res.Runs) != 1 || res.Runs[0].RecordKind != "rohsa" || res.Runs[0].Path != filepath.Join(rel, "region_a") {
		t.Fatalf("a rescan lists %d runs: %+v", len(res.Runs), res.Runs)
	}
	var job string
	for _, f := range res.Runs[0].Fields {
		if f.Key == "job" {
			job = f.Value + " / " + f.Status
		}
	}
	if job != "Vesqira job "+itoa(int(j.ID))+" / declared" {
		t.Errorf("the fit does not name its job: %q", job)
	}
}

func TestAnAutoImportOfAROHSAJobWithNoFitOrTwoFitsIsOneNotificationAndNothingIsGuessed(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	ctx := context.Background()
	w := newJobWatcher(MinWatchInterval)
	t0 := time.Now()
	none := e.submit(e.rohsaDraft("rohsa-none", nil, nil), nil)
	two := e.submit(e.rohsaDraft("rohsa-two", nil, nil), nil)
	os.MkdirAll(none.OutputDir, 0o755)
	e.writeROHSA(two.OutputDir, "")
	os.WriteFile(filepath.Join(two.OutputDir, "region_a", "rohsa_n4.dat"), []byte("1 2 3\n"), 0o644)
	for _, j := range []jobs.Job{none, two} {
		e.setSlurm(j.SlurmID, "COMPLETED")
	}
	e.srv.watchOnce(ctx, w, t0)
	e.srv.waitRecords()
	e.srv.waitImports()
	if rs, _ := e.runs.List(workspace.Local); len(rs) != 0 {
		t.Errorf("%d runs were imported", len(rs))
	}
	for _, c := range []struct {
		j    jobs.Job
		want string
	}{{none, "no ROHSA fit"}, {two, "several ROHSA fits"}} {
		var failed []notify.Notification
		for _, n := range e.notifications() {
			if n.Kind == notify.KindImportFailed && n.JobID != nil && *n.JobID == c.j.ID {
				failed = append(failed, n)
			}
		}
		if len(failed) != 1 || !strings.Contains(failed[0].Body, c.want) || !strings.Contains(failed[0].Body, "not retried") {
			t.Errorf("job %s: %+v", c.j.Name, failed)
		}
		if e.job(c.j.ID).RunID != nil {
			t.Errorf("a failed import linked a run")
		}
	}
	// the two-fit folder imports by choosing one .dat, and that run can then be linked by hand through the button's rule: refused
	if r, b := postJSON(t, e.api("/api/jobs/"+itoa(int(two.ID))+"/register"), map[string]any{}); r.StatusCode != 422 || !strings.Contains(string(b), "several ROHSA fits") {
		t.Errorf("register: %d %s", r.StatusCode, b)
	}
}

// The sweep's grid shows the metrics each imported ROHSA run declared, as it does for VGT, and still names no best run.
func TestROHSASweepGridShowsTheImportedRunsMetrics(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	base := e.rohsaDraft("rohsa-base", map[string]string{"lambda": "10"}, nil)
	sw := e.createSweep(map[string]any{"name": "rsweep", "baseJobId": base.ID, "params": []map[string]any{{"key": "n_gauss", "label": "N", "values": []string{"4", "8"}}}})
	for _, m := range sw.Jobs {
		if !e.job(m.JobID).AutoImport {
			t.Fatal("a ROHSA sweep's jobs import their fits by themselves")
		}
	}
	sw = e.approve(sw.ID, 2, false)
	rmse := []string{"0.05", "0.09"}
	for i := 0; i < 2; i++ {
		j := e.job(sw.Jobs[i].JobID)
		e.writeROHSA(j.OutputDir, rmse[i])
		e.end(j.ID, "COMPLETED")
		if e.job(j.ID).RunID == nil {
			t.Fatalf("job %d was not imported (detail %q)", i, e.job(j.ID).Detail)
		}
	}
	code, b := getBody(t, e.api("/api/sweeps/"+itoa(int(sw.ID))+"/grid?metric=voxel_rmse&x=n_gauss"))
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	var g struct {
		Rows    []sweeps.Row
		Metrics []sweeps.MetricInfo
		Plot    *sweeps.Plot
	}
	json.Unmarshal(b, &g)
	if len(g.Rows) != 2 {
		t.Fatalf("rows %+v", g.Rows)
	}
	for i, r := range g.Rows {
		if r.RunID == nil || len(r.Metrics) != 2 {
			t.Fatalf("row %d shows no metrics: %+v", i, r)
		}
		got := map[string]float64{}
		for _, m := range r.Metrics {
			if m.Value != nil {
				got[m.Key] = *m.Value
			}
		}
		if want := map[string]float64{"0.05": 0.05, "0.09": 0.09}[rmse[i]]; got["voxel_rmse"] != want {
			t.Errorf("row %d voxel_rmse %v, want %v", i, got["voxel_rmse"], want)
		}
	}
	if g.Plot == nil || len(g.Plot.Lines) != 1 || len(g.Plot.Lines[0].Points) != 2 {
		t.Errorf("plot %+v", g.Plot)
	}
	if strings.Contains(strings.ToLower(string(b)), "best") {
		t.Error("the grid labels something best")
	}
}

func TestROHSAMemoryBudgetIsEditableInAJobDraft(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	j := e.rohsaDraft("rohsa-mem", map[string]string{"n_gauss": "24", "memory_gb": "140"}, nil)
	cfg := configOf(t, j.Script)
	if !strings.Contains(cfg, "memory_gb = 140\n") || strings.Contains(cfg, "140.0") || !strings.Contains(cfg, "n_gauss = [24]") {
		t.Errorf("the pipeline's memory preflight is raised with N, as typed:\n%s", cfg)
	}
	if r, _ := postJSON(t, e.api("/api/jobs"), map[string]any{"hostId": e.host.ID, "template": "rohsa", "name": "x", "rohsa": map[string]any{"repoDir": "/shared/home/u/vgt", "configText": rohsaCfg, "set": map[string]string{"memory_gb": "lots"}, "cpus": 1, "mem": "1G", "time": "01:00:00"}}); r.StatusCode != 422 {
		t.Errorf("a word that is not a number or auto: %d", r.StatusCode)
	}
}
