package server

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

type rohsaEnv struct {
	ts    *httptest.Server
	root  string
	cache string
	srv   *Server
}

func newROHSAEnv(t *testing.T) rohsaEnv {
	t.Helper()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	pw := headerWorker(t, "python3", repo)
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	rs, _ := sourceroot.New([]string{root})
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	rn, err := runs.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cache := filepath.Join(base, "cache")
	srv := &Server{Roots: rs, Store: cat, Worker: pw, Planes: pw, Cache: NewPlaneCache(8 << 20), Runs: rn, CacheDir: cache}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return rohsaEnv{ts, root, cache, srv}
}

type rohsaRunJSON struct {
	ID      int64
	Name    string
	Kind    string
	Params  []runs.Param
	Metrics []runs.Metric
	Outputs []struct {
		Role      string
		DatasetID int64
	}
	Rohsa *runs.RohsaInfo
}

func (e rohsaEnv) importRun(t *testing.T, path string, want int) (rohsaRunJSON, string) {
	t.Helper()
	r, out := postJSON(t, e.ts.URL+"/api/runs/import", map[string]any{"path": path})
	if r.StatusCode != want {
		t.Fatalf("import %s: %d %s", path, r.StatusCode, out)
	}
	var run rohsaRunJSON
	json.Unmarshal(out, &run)
	return run, string(out)
}

func TestROHSAImportDeclaresSettingsLinksProductsAndNeverGuessesUnits(t *testing.T) {
	t.Parallel()
	e := newROHSAEnv(t)
	dir := filepath.Join(e.root, "rohsa_run_demo", "region_a")
	run, raw := e.importRun(t, dir, 201) // a folder with one fit is enough
	if run.Kind != "rohsa" || run.Name != "rohsa_n3" || run.Rohsa == nil {
		t.Fatalf("run: %s", raw)
	}
	if run.Rohsa.NGauss != 3 || run.Rohsa.NGaussBasis != "declared by the run trace" || run.Rohsa.RegionBasis != "declared" || run.Rohsa.CropDatasetID == nil {
		t.Fatalf("decomposition record: %+v", run.Rohsa)
	}
	// settings are declared by the pipeline's own records; locations of the machine it ran on never come through
	got := map[string]string{}
	for _, p := range run.Params {
		got[p.Key] = p.Source
	}
	for _, k := range []string{"lambda_amp", "lb_sig", "init_option", "n_gauss"} {
		if got[k] == "" {
			t.Errorf("setting %s missing: %v", k, got)
		}
	}
	for _, bad := range []string{"/shared/home", "someone", "filename", "fileout", "rohsa_output"} {
		if strings.Contains(raw, bad) {
			t.Errorf("%q must not appear in the run: %s", bad, raw)
		}
	}
	if len(run.Metrics) != 2 || run.Metrics[0].Status != "declared" {
		t.Errorf("fit metrics are declared by the trace: %+v", run.Metrics)
	}
	ids := map[string]int64{}
	for _, o := range run.Outputs {
		ids[o.Role] = o.DatasetID
	}
	for _, role := range []string{"component_g1_amp_k", "component_g1_mu_kms", "component_g1_sigma_kms", "model_nhi_cm2", "residual_moment0_kkms", "cube_model"} {
		if ids[role] == 0 {
			t.Errorf("output %s not linked: %v", role, ids)
		}
	}
	for _, id := range ids {
		if id == *run.Rohsa.CropDatasetID {
			t.Errorf("the crop is the run's input, not one of its outputs")
		}
	}
	// units: no BUNIT -> a labelled proposal from the name; BUNIT that contradicts the name -> the file's value stays, with a note
	var mu, sig, amp catalog.Dataset
	_, b := get(t, e.ts.URL+"/api/datasets/"+itoa(int(ids["component_g1_mu_kms"])))
	json.Unmarshal(b, &mu)
	if mu.UnitDeclaration == nil || mu.UnitDeclaration.Unit != "km/s" || mu.UnitDeclaration.DeclaredBy != "rohsa-run-adapter" {
		t.Errorf("mu map unit proposal: %+v", mu.UnitDeclaration)
	}
	_, b = get(t, e.ts.URL+"/api/datasets/"+itoa(int(ids["component_g1_sigma_kms"])))
	json.Unmarshal(b, &sig)
	if sig.UnitDeclaration != nil {
		t.Errorf("a file that states a unit is never given a declaration: %+v", sig.UnitDeclaration)
	}
	if n := run.Rohsa.UnitNotes["component_g1_sigma_kms"]; !strings.Contains(n, "file says K") || !strings.Contains(n, "km/s") {
		t.Errorf("conflict note: %q", n)
	}
	if n := run.Rohsa.UnitNotes["component_g1_mu_kms"]; !strings.Contains(n, "proposed from the file name") {
		t.Errorf("proposal note: %q", n)
	}
	if _, ok := run.Rohsa.UnitNotes["component_g1_amp_k"]; ok {
		t.Errorf("a file that agrees with its name needs no note")
	}
	_, b = get(t, e.ts.URL+"/api/datasets/"+itoa(int(ids["component_g1_amp_k"])))
	json.Unmarshal(b, &amp)
	if amp.UnitDeclaration != nil {
		t.Errorf("amp: %+v", amp.UnitDeclaration)
	}
	// re-import refreshes the same run; the .dat itself imports the same way
	if again, _ := e.importRun(t, filepath.Join(dir, "rohsa_n3.dat"), 201); again.ID != run.ID {
		t.Errorf("re-import made a new run: %d vs %d", again.ID, run.ID)
	}
	// a person's own unit declaration is never replaced by a later import
	postJSON(t, e.ts.URL+"/api/datasets/"+itoa(int(ids["component_g1_mu_kms"]))+"/unit", map[string]any{"unit": "m/s"})
	e.importRun(t, dir, 201)
	_, b = get(t, e.ts.URL+"/api/datasets/"+itoa(int(ids["component_g1_mu_kms"])))
	json.Unmarshal(b, &mu)
	if mu.UnitDeclaration == nil || mu.UnitDeclaration.DeclaredBy != "user" {
		t.Errorf("the user's declaration was replaced: %+v", mu.UnitDeclaration)
	}
	// running a ROHSA run again is offered (ADR 0046, 0048 follow-up); what it says is tested in rohsa_rerun_test.go
	if r, _ := get(t, e.ts.URL+"/api/runs/"+itoa(int(run.ID))+"/rerun"); r.StatusCode != 200 {
		t.Errorf("rerun of a ROHSA run: %d", r.StatusCode)
	}
}

func TestROHSAWithoutItsCropImportsWithANoteAndNoCalibration(t *testing.T) {
	t.Parallel()
	e := newROHSAEnv(t)
	dir := filepath.Join(e.root, "rohsa_run_demo", "region_a")
	if err := os.Remove(filepath.Join(dir, "region_a.fits")); err != nil {
		t.Fatal(err)
	}
	run, raw := e.importRun(t, filepath.Join(dir, "rohsa_n3.dat"), 201)
	if run.Rohsa == nil || run.Rohsa.CropDatasetID != nil || !strings.Contains(strings.Join(run.Rohsa.Notes, " "), "no spectral calibration") {
		t.Fatalf("no crop, no calibration, and a note saying so: %s", raw)
	}
	// without a crop nothing can be drawn over any dataset, and no other FITS in the folder is guessed to be the crop
	_, b := get(t, e.ts.URL+"/api/datasets/"+itoa(int(run.Outputs[0].DatasetID))+"/rohsa")
	if !strings.Contains(string(b), `"runs":[]`) {
		t.Errorf("runs: %s", b)
	}
	// only rohsa_*.dat files are fits; others in the folder are refused
	if r, _ := postJSON(t, e.ts.URL+"/api/runs/import", map[string]any{"path": filepath.Join(dir, "rohsa_n3.txt")}); r.StatusCode == 201 {
		t.Errorf("a non-.dat file must not import as a ROHSA run")
	}
	os.WriteFile(filepath.Join(dir, "rms_map.dat"), []byte("1 2 3\n"), 0o644)
	if r, _ := postJSON(t, e.ts.URL+"/api/runs/import", map[string]any{"path": filepath.Join(dir, "rms_map.dat")}); r.StatusCode != 422 {
		t.Errorf("rms_map.dat is not a ROHSA fit: %d", r.StatusCode)
	}
}

func TestROHSAComponentsRequireTheSameGridAndAnIndex(t *testing.T) {
	t.Parallel()
	e := newROHSAEnv(t)
	dir := filepath.Join(e.root, "rohsa_run_demo", "region_a")
	run, _ := e.importRun(t, dir, 201)
	crop := *run.Rohsa.CropDatasetID
	base := e.ts.URL + "/api/datasets/" + itoa(int(crop))

	_, b := get(t, base+"/rohsa")
	var list struct {
		Runs []rohsaRunView
	}
	json.Unmarshal(b, &list)
	if len(list.Runs) != 1 || !list.Runs[0].Usable || !list.Runs[0].IsCrop || list.Runs[0].CropBunit == nil || *list.Runs[0].CropBunit != "K" {
		t.Fatalf("the crop itself is usable: %s", b)
	}
	// before an index exists a pixel is refused, not computed by a whole-file scan
	if r, _ := get(t, base+"/rohsa/components?run="+itoa(int(run.ID))+"&x=2&y=1"); r.StatusCode != 409 {
		t.Errorf("no index yet: %d, want 409", r.StatusCode)
	}
	r, ib := postJSON(t, base+"/rohsa/index", map[string]any{"runId": run.ID})
	var idx struct {
		State string
		Rows  int
	}
	json.Unmarshal(ib, &idx)
	if r.StatusCode != 200 || idx.State != "ready" || idx.Rows != 6*4*3 {
		t.Fatalf("index: %d %s", r.StatusCode, ib)
	}
	if ents, _ := os.ReadDir(e.cache); len(ents) != 2 {
		t.Errorf("the index sidecar goes in the cache folder: %v", ents)
	}
	if ents, _ := os.ReadDir(dir); func() bool {
		for _, x := range ents {
			if strings.HasSuffix(x.Name(), ".offs") || strings.HasSuffix(x.Name(), ".json") {
				return true
			}
		}
		return false
	}() {
		t.Errorf("nothing is written next to the data")
	}
	resp, frame := get(t, base+"/rohsa/components?run="+itoa(int(run.ID))+"&x=2&y=1")
	if resp.StatusCode != 200 {
		t.Fatalf("components: %d %s", resp.StatusCode, frame)
	}
	n := int(binary.LittleEndian.Uint32(frame))
	var head struct {
		Channels   int
		Components []struct {
			Amp, Centre, Sigma float64
			Valid              bool
		}
		Axis struct {
			Kind, Unit string
			UnitStatus string `json:"unit_status"`
		}
	}
	if err := json.Unmarshal(frame[4:4+n], &head); err != nil {
		t.Fatal(err)
	}
	payload := frame[4+n:]
	if len(head.Components) != 3 || head.Axis.Kind != "velocity" || head.Axis.Unit != "km/s" || head.Axis.UnitStatus != "inferred" || len(payload) != 8*head.Channels*(2+3) {
		t.Fatalf("components head %+v payload %d", head, len(payload))
	}
	// x=2, y=1: amp = 1 + 0.5k + 0.1x + 0.01y; the centre of the first is the file's mean (8 + .25*2 + .5*1 + 1) minus 1, on the WCS axis
	wantCentre := (-12000.0 + (8+0.5+0.5-3+1)*800.0) / 1000.0 // channel 9.0 is pixel 10: CRVAL + (10 - CRPIX) * CDELT
	if math.Abs(head.Components[0].Centre-wantCentre) > 1e-9 || math.Abs(head.Components[0].Amp-1.21) > 1e-12 || !head.Components[0].Valid {
		t.Errorf("first component %+v, want centre %v amp 1.21", head.Components[0], wantCentre)
	}
	if r, _ := get(t, base+"/rohsa/components?run="+itoa(int(run.ID))+"&x=6&y=0"); r.StatusCode != 400 {
		t.Errorf("a pixel outside the crop: %d", r.StatusCode)
	}

	// a byte copy of the crop has the same shape but its grid is not confirmed (no CUNIT3 on either): refused, with the reason
	b, _ = os.ReadFile(filepath.Join(dir, "region_a.fits"))
	cp := filepath.Join(e.root, "copy.fits")
	os.WriteFile(cp, b, 0o644)
	var d catalog.Dataset
	json.NewDecoder(post(t, e.ts, cp, nil).Body).Decode(&d)
	_, lb := get(t, e.ts.URL+"/api/datasets/"+itoa(int(d.ID))+"/rohsa")
	json.Unmarshal(lb, &list)
	if len(list.Runs) != 1 || list.Runs[0].Usable || list.Runs[0].Refusal == nil || !strings.Contains(*list.Runs[0].Refusal, "not known to be on the run's grid") {
		t.Fatalf("a cube whose grid cannot be confirmed is refused with a reason: %s", lb)
	}
	r2, body := get(t, e.ts.URL+"/api/datasets/"+itoa(int(d.ID))+"/rohsa/components?run="+itoa(int(run.ID))+"&x=0&y=0")
	if r2.StatusCode != 409 || !strings.Contains(string(body), "grid") {
		t.Errorf("components over a cube off the grid: %d %s", r2.StatusCode, body)
	}
	// an unrelated cube lists nothing at all
	cube := filepath.Join(e.root, "hi_cube.fits")
	json.NewDecoder(post(t, e.ts, cube, nil).Body).Decode(&d)
	_, lb = get(t, e.ts.URL+"/api/datasets/"+itoa(int(d.ID))+"/rohsa")
	if !strings.Contains(string(lb), `"runs":[]`) {
		t.Errorf("unrelated cube: %s", lb)
	}
}
