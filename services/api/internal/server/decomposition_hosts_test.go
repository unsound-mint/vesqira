package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/runs/runstest"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// FITS-map decompositions found by HDU name, kept on a host, and the pixel index behind a click on a cell of the diagram (ADR 0068,
// slice 4 follow-up). The expected numbers are the closed form `truth` of decomposition_test.go, never the reader's.

type pixelComponent struct {
	Amp, Centre, Sigma float64
	Present, Valid     bool
}

// componentsAt asks the components route for one pixel and decodes the JSON head of the frame.
func componentsAt(t *testing.T, url string) (int, []pixelComponent, string) {
	t.Helper()
	resp, frame := get(t, url)
	if resp.StatusCode != 200 {
		return resp.StatusCode, nil, string(frame)
	}
	n := int(binary.LittleEndian.Uint32(frame))
	var head struct {
		Components []pixelComponent
		Drawn      bool
	}
	if err := json.Unmarshal(frame[4:4+n], &head); err != nil || !head.Drawn {
		t.Fatalf("components head: %v %s", err, frame[4:4+n])
	}
	return 200, head.Components, ""
}

func checkAgainstTruth(t *testing.T, what string, comps []pixelComponent, x, y int, ampScale float64) {
	t.Helper()
	if len(comps) != dnComp {
		t.Fatalf("%s: %d components", what, len(comps))
	}
	for k, c := range comps {
		amp, centre, fwhm, ok := truth(k, x, y)
		if c.Present != ok {
			t.Errorf("%s: slot %d present %v, want %v", what, k, c.Present, ok)
			continue
		}
		if ok && (math.Abs(c.Amp-ampScale*amp) > 1e-11 || math.Abs(c.Centre-centre) > 1e-9 || math.Abs(c.Sigma-fwhm/fwhmPerSigma) > 1e-9) {
			t.Errorf("%s: slot %d = %+v, want amp %v centre %v sigma %v", what, k, c, ampScale*amp, centre, fwhm/fwhmPerSigma)
		}
	}
}

func TestNamedHDUsAreReadByNameAndAClickOnACellUsesThePixelIndex(t *testing.T) {
	t.Parallel()
	e, _, _ := reviewEnv(t)
	e.srv.Runs.Profiles = runstest.WithSyntheticMaps(t)
	folder := filepath.Join(e.root, "synth_decomp_demo", "named_hdus")
	run, raw := e.importRun(t, folder, 201)
	if run.Kind != "synthdecomp" || run.Rohsa == nil || run.Rohsa.NGauss != dnComp || run.Rohsa.CropDatasetID == nil || run.Rohsa.HostID != 0 {
		t.Fatalf("run %s", raw)
	}
	base := e.ts.URL + "/api/datasets/" + itoa(int(*run.Rohsa.CropDatasetID)) + "/rohsa"
	for _, p := range [][2]int{{0, 0}, {3, 2}, {1, 3}, {6, 4}} {
		code, comps, msg := componentsAt(t, base+"/components?run="+itoa(int(run.ID))+"&x="+itoa(p[0])+"&y="+itoa(p[1]))
		if code != 200 {
			t.Fatalf("components %v: %d %s", p, code, msg)
		}
		checkAgainstTruth(t, "named HDUs", comps, p[0], p[1], 1) // AMP EXTVER 1, not the decoy (10 x) stored ahead of it
	}

	bins := map[string]any{"velocityBins": map[string]any{"min": -20.0, "max": 30.0, "n": 10}, "sigmaBins": map[string]any{"min": 0.0, "max": 4.0, "n": 8}}
	diagram := map[string]any{"runId": run.ID, "weight": "count"}
	cell := map[string]any{"runId": run.ID, "weight": "count"}
	for k, v := range bins {
		diagram[k], cell[k] = v, v
	}
	if r, b := postJSON(t, base+"/sigmav", diagram); r.StatusCode != 200 {
		t.Fatalf("sigmav %d %s", r.StatusCode, b)
	} else if !strings.Contains(string(b), `"pixel_index"`) || strings.Contains(string(b), `"pixel_index":null`) {
		t.Errorf("the diagram of a maps run writes the pixel index: %s", b)
	}
	if ents, _ := os.ReadDir(e.cache); len(cellFiles(ents)) != 1 {
		t.Errorf("one .cells index in the cache folder, got %v", ents)
	}

	// the pixels behind each cell, by a plain loop over the closed form
	want := map[[2]int]map[[2]int]bool{}
	for k := 0; k < dnComp; k++ {
		for y := 0; y < dyPix; y++ {
			for x := 0; x < dxPix; x++ {
				if _, c, w, ok := truth(k, x, y); ok {
					key := [2]int{int(math.Floor(w / fwhmPerSigma / 0.5)), int(math.Floor((c + 20) / 5))}
					if key[0] < 8 && key[1] >= 0 && key[1] < 10 {
						if want[key] == nil {
							want[key] = map[[2]int]bool{}
						}
						want[key][[2]int{x, y}] = true
					}
				}
			}
		}
	}
	clicked := 0
	for key, pixels := range want {
		cell["cell"] = map[string]any{"s": key[0], "v": key[1]}
		r, b := postJSON(t, base+"/sigmav/cell", cell)
		if r.StatusCode != 200 {
			t.Fatalf("cell %v: %d %s", key, r.StatusCode, b)
		}
		var m struct {
			Mask   struct{ Width, Height int }
			Pixels int
			Source string
		}
		json.Unmarshal(b, &m)
		if m.Source != "pixel index written by the diagram" || m.Pixels != len(pixels) {
			t.Errorf("cell %v: %s, want %d pixels from the index", key, b, len(pixels))
		}
		if clicked++; clicked == 4 {
			break
		}
	}

	// a map that changed after the diagram was drawn invalidates the index: the click says to draw it again
	amp := filepath.Join(folder, "synth_decomp_ext.fits")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(amp, later, later); err != nil {
		t.Fatal(err)
	}
	r, b := postJSON(t, base+"/sigmav/cell", cell)
	if r.StatusCode < 400 || !strings.Contains(string(b), "draw the diagram again") {
		t.Errorf("a click after a map changed: %d %s", r.StatusCode, b)
	}
}

func cellFiles(ents []os.DirEntry) []string {
	var out []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".cells") {
			out = append(out, e.Name())
		}
	}
	return out
}

func (e *watchEnv) writeSynthDecomp(dir string) {
	e.t.Helper()
	gen := exec.Command("python3", "-c", "import sys, pathlib, generate; generate.write_synth_decomp_run(pathlib.Path(sys.argv[1]))", dir)
	gen.Dir = filepath.Join(e.repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		e.t.Fatalf("%v\n%s", err, out)
	}
}

func TestADecompositionWrittenAsMapsIsImportedAndReadOnTheHost(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	e.srv.Runs.Profiles = runstest.WithSyntheticMaps(t)
	e.srv.Cache = NewPlaneCache(8 << 20)
	e.writeSynthDecomp(filepath.Join(e.remoteRoot, "fit"))
	for name, dir := range map[string]string{
		"one cube per parameter": filepath.Join(e.remoteRoot, "fit"),
		"named HDUs":             filepath.Join(e.remoteRoot, "fit", "named_hdus"),
		"one map per component":  filepath.Join(e.remoteRoot, "fit", "per_component"),
	} {
		run, raw := e.importROHSAFromHost(dir, 201)
		if run.Kind != "synthdecomp" || run.Rohsa == nil || run.Rohsa.HostID != e.host.ID || run.Rohsa.CropDatasetID == nil || run.Rohsa.NGauss != dnComp {
			t.Fatalf("%s: run %s", name, raw)
		}
		if strings.Contains(raw, e.remoteRoot) || strings.Contains(raw, "/scratch/someone") {
			t.Errorf("%s: no location comes through in the run: %s", name, raw)
		}
		if rec := map[string]string{"one cube per parameter": "synth_decomp_run.toml", "named HDUs": "synth_decomp_hdus_run.toml"}[name]; rec != "" {
			if again, _ := e.importROHSAFromHost(filepath.Join(dir, rec), 201); again.ID != run.ID { // the record file names the same run
				t.Errorf("%s: importing by its record made run %d, not %d", name, again.ID, run.ID)
			}
		}
		crop, err := e.srv.Store.Get(workspace.Local, *run.Rohsa.CropDatasetID)
		if err != nil || crop.HostID != e.host.ID || !strings.HasPrefix(crop.Path, e.remoteRoot) {
			t.Fatalf("%s: the crop lives on the host: %+v %v", name, crop, err)
		}
		base := e.api("/api/datasets/" + itoa(int(crop.ID)) + "/rohsa")
		for _, p := range [][2]int{{0, 0}, {3, 2}, {1, 3}, {6, 4}} {
			code, comps, msg := componentsAt(t, base+"/components?run="+itoa(int(run.ID))+"&x="+itoa(p[0])+"&y="+itoa(p[1]))
			if code != 200 {
				t.Fatalf("%s: components %v: %d %s", name, p, code, msg)
			}
			checkAgainstTruth(t, name+" on the host", comps, p[0], p[1], 1)
		}
		if r, b := postJSON(t, base+"/index", map[string]any{"runId": run.ID}); r.StatusCode != 200 || !strings.Contains(string(b), `"ready"`) || !strings.Contains(string(b), `"host"`) {
			t.Errorf("%s: index %d %s", name, r.StatusCode, b)
		}
		// the maps are read where they are: nothing is written on the host for them, no index, no cache folder
		if files := e.cacheFiles(); len(files) != 0 {
			t.Errorf("%s: reading maps wrote %v on the host", name, files)
		}
		// a map that changes on the host is read again, never served from the cache
		if name == "one cube per parameter" {
			amp := filepath.Join(dir, "synth_decomp_amp.fits")
			rewriteAmplitudes(t, e, amp, 3)
			code, comps, msg := componentsAt(t, base+"/components?run="+itoa(int(run.ID))+"&x=3&y=2")
			if code != 200 {
				t.Fatalf("after the change: %d %s", code, msg)
			}
			checkAgainstTruth(t, "changed map", comps, 3, 2, 3)
		}
	}
}

// rewriteAmplitudes multiplies the amplitude map by k in place (a new size is not needed: the modification time moves).
func rewriteAmplitudes(t *testing.T, e *watchEnv, file string, k int) {
	t.Helper()
	py := "import sys; from astropy.io import fits; h = fits.open(sys.argv[1], mode='update'); h[0].data *= float(sys.argv[2]); h.close()"
	if out, err := exec.Command("python3", "-c", py, file, itoa(k)).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
}

func TestMapsOnAHostKeepTheFolderRulesAndAHostThatIsSilentSavesNothing(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	e.srv.Runs.Profiles = runstest.WithSyntheticMaps(t)
	fit := filepath.Join(e.remoteRoot, "fit")
	e.writeSynthDecomp(fit)

	// outside the host's roots: refused like any other path there
	outside := filepath.Join(filepath.Dir(e.remoteRoot), "elsewhere")
	e.writeSynthDecomp(outside)
	e.importROHSAFromHost(outside, 403)

	// a map that is a link to a file elsewhere inside the roots: the folder is not where it really is, so it is not read
	other := filepath.Join(e.remoteRoot, "other")
	os.MkdirAll(other, 0o755)
	real := filepath.Join(other, "amp.fits")
	link := filepath.Join(fit, "synth_decomp_amp.fits")
	b, _ := os.ReadFile(link)
	os.WriteFile(real, b, 0o644)
	os.Remove(link)
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	run, raw := e.importROHSAFromHost(fit, 201)
	crop := *run.Rohsa.CropDatasetID
	resp, body := get(t, e.api("/api/datasets/"+itoa(int(crop))+"/rohsa/components?run="+itoa(int(run.ID))+"&x=0&y=0"))
	if resp.StatusCode != 403 || !strings.Contains(string(body), "outside the run's folder") {
		t.Errorf("a map linked from outside the folder: %d %s (import: %s)", resp.StatusCode, body, raw)
	}

	// a host that does not answer is not a folder with no maps: nothing is saved
	n := len(mustRuns(t, e))
	os.WriteFile(filepath.Join(e.home, "DOWN"), nil, 0o644)
	e.srv.Remote.Forget(e.host.ID)
	e.importROHSAFromHost(filepath.Join(fit, "per_component"), 502)
	if _, code, _ := e.srv.importFrom(context.Background(), remoteRun{s: e.srv, h: e.host, dir: filepath.Join(fit, "per_component"), file: "synth_decomp_2d_run.toml"}); code != 502 {
		t.Errorf("a silent host during the import: %d", code)
	}
	os.Remove(filepath.Join(e.home, "DOWN"))
	e.srv.Remote.Forget(e.host.ID)
	if got := len(mustRuns(t, e)); got != n {
		t.Errorf("a silent host saved a run: %d runs, had %d", got, n)
	}
}

func mustRuns(t *testing.T, e *watchEnv) []struct{} {
	t.Helper()
	rs, err := e.runs.List(workspace.Local)
	if err != nil {
		t.Fatal(err)
	}
	return make([]struct{}, len(rs))
}
