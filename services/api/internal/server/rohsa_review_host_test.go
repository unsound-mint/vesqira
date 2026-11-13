package server

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/runs/runstest"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// The decomposition review of a fit kept on a host (ADR 0068, slice 4 follow-up): the sigma-v diagram, the click on a cell and the class
// maps run by the host's own copy of the program, on the stand-in host of jobwatch_test.go with the real workers on both sides. The expected
// numbers are closed forms (fixtureWeights, truth), never the program's.

func reviewHostEnv(t *testing.T) *watchEnv {
	t.Helper()
	e := newWatchEnv(t)
	e.srv.Runs.Profiles = runstest.WithSyntheticMaps(t)
	e.srv.Analysis = &AnalysisConfig{Timeout: 2 * time.Minute} // the budget a local review has; the program itself is the host's
	ds, err := derived.Open(e.srv.Store.DB())
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Derived = ds
	// the review routes exist only with the analysis dependencies, which the stand-in's first handler was built without
	ts := httptest.NewServer(e.srv.Handler())
	t.Cleanup(ts.Close)
	e.api = func(p string) string { return ts.URL + p }
	return e
}

func (e *watchEnv) hostDerived() string { return filepath.Join(e.remoteRoot, "vesqira_derived") }

// readMap reads a FITS image with astropy: rows of values, null for NaN, and its header cards VESQCLS and BUNIT.
func readMap(t *testing.T, path string) (rows [][]*float64, cls int, bunit string) {
	t.Helper()
	py := `import sys, json, math
from astropy.io import fits
with fits.open(sys.argv[1]) as h:
    d = h[0].data
    print(json.dumps({"rows": [[None if math.isnan(v) else float(v) for v in r] for r in d.tolist()], "cls": h[0].header.get("VESQCLS", -1), "bunit": h[0].header.get("BUNIT", "")}))`
	out, err := exec.Command("python3", "-c", py, path).Output()
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m struct {
		Rows  [][]*float64
		Cls   int
		Bunit string
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	return m.Rows, m.Cls, m.Bunit
}

type reviewMaps struct {
	Maps []struct {
		Dataset catalog.Dataset
		Class   struct {
			Index      int
			Components int
		}
	}
}

func TestReviewOfAnImportedDatRunOnAHostReadsItThereAndWritesTheMapsThere(t *testing.T) {
	t.Parallel()
	e := reviewHostEnv(t)
	e.writeROHSA(filepath.Join(e.remoteRoot, "fit"), "")
	dir := filepath.Join(e.remoteRoot, "fit", "region_a")
	run, raw := e.importROHSAFromHost(dir, 201)
	if run.Rohsa == nil || run.Rohsa.HostID != e.host.ID || run.Rohsa.CropDatasetID == nil {
		t.Fatalf("run %s", raw)
	}
	crop := *run.Rohsa.CropDatasetID
	base := e.api("/api/datasets/" + itoa(int(crop)) + "/rohsa")
	total, narrow, rest := fixtureWeights()
	bins := map[string]any{"velocityBins": map[string]any{"min": -14.0, "max": 10.0, "n": 12}, "sigmaBins": map[string]any{"min": 0.0, "max": 4.0, "n": 8}}
	req := map[string]any{"runId": run.ID, "weight": "integrated"}
	for k, v := range bins {
		req[k] = v
	}

	resp, body := postJSON(t, base+"/sigmav", req)
	if resp.StatusCode != 200 {
		t.Fatalf("sigmav %d %s", resp.StatusCode, body)
	}
	var d struct {
		Components int                        `json:"components"`
		Total      float64                    `json:"weight_total"`
		Cells      [][]any                    `json:"cells"`
		PixelIndex *struct{ Rows, Bytes int } `json:"pixel_index"`
	}
	json.Unmarshal(body, &d)
	if d.Components != 72 || math.Abs(d.Total/total-1) > 1e-12 || d.PixelIndex == nil || d.PixelIndex.Rows != 72 || d.PixelIndex.Bytes != 144 || len(d.Cells) == 0 {
		t.Fatalf("diagram: %s", body)
	}
	for _, where := range []string{e.remoteRoot, e.home, "/.cache/"} {
		if strings.Contains(string(body), where) {
			t.Errorf("the reply names a folder on the host (%s): %s", where, body)
		}
	}
	// the index is in the host's fixed cache folder, once, and never beside the data
	var cells []string
	for _, f := range e.cacheFiles() {
		if strings.HasSuffix(f, ".cells") {
			cells = append(cells, f)
		}
	}
	if len(cells) != 1 {
		t.Fatalf("the host's cache folder holds %v, want one .cells index", e.cacheFiles())
	}
	if ents, _ := os.ReadDir(dir); len(cellFiles(ents)) != 0 {
		t.Errorf("an index beside the data: %v", ents)
	}

	// the click reads the index on the host, not the fit
	cell := d.Cells[0]
	creq := map[string]any{"runId": run.ID, "weight": "integrated", "cell": map[string]any{"s": int(cell[0].(float64)), "v": int(cell[1].(float64))}}
	for k, v := range bins {
		creq[k] = v
	}
	resp, body = postJSON(t, base+"/sigmav/cell", creq)
	var m struct {
		Components, Pixels int
		Source             string
		Mask               struct{ Width, Height int }
	}
	json.Unmarshal(body, &m)
	if resp.StatusCode != 200 || m.Source != "pixel index written by the diagram" || m.Components != int(cell[2].(float64)) || m.Pixels < 1 || m.Mask.Width != 6 || m.Mask.Height != 4 {
		t.Fatalf("cell %d %s", resp.StatusCode, body)
	}
	// bins that were never drawn have no index: a sentence, and the fit is not parsed on the host for it
	creq["velocityBins"] = map[string]any{"min": -14.0, "max": 10.0, "n": 6}
	resp, body = postJSON(t, base+"/sigmav/cell", creq)
	if resp.StatusCode != 422 || !strings.Contains(string(body), "draw the diagram again") {
		t.Errorf("undrawn bins: %d %s", resp.StatusCode, body)
	}

	// class maps: written on the host, in its derived folder, one dataset of that host per class
	mreq := map[string]any{"runId": run.ID, "weight": "integrated", "edges": []float64{2.0}}
	resp, body = postJSON(t, base+"/class-maps", mreq)
	if resp.StatusCode != 201 {
		t.Fatalf("class maps %d %s", resp.StatusCode, body)
	}
	var cm reviewMaps
	json.Unmarshal(body, &cm)
	if len(cm.Maps) != 2 || cm.Maps[0].Class.Components != narrow || cm.Maps[1].Class.Components != rest {
		t.Fatalf("classes: %s", body)
	}
	ents, _ := os.ReadDir(e.hostDerived())
	if len(ents) != 2 {
		t.Errorf("the host's derived folder holds %v, want the two class maps and nothing else", ents)
	}
	for i, mp := range cm.Maps {
		ds := mp.Dataset
		if ds.HostID != e.host.ID || filepath.Dir(ds.Path) != e.hostDerived() || !strings.Contains(ds.Path, "rohsa-classes-") || !strings.HasSuffix(ds.Path, "-class"+itoa(i)+".fits") {
			t.Errorf("class %d is %+v, want a dataset of the host in its derived folder", i, ds)
		}
		rec, err := e.srv.Derived.Get(ds.ID)
		if err != nil || rec.Op != "rohsa_class_map" || len(rec.Sources) != 1 || rec.Sources[0] != crop {
			t.Fatalf("record of class %d: %+v %v", i, rec, err)
		}
		all := string(rec.Params) + string(rec.Summary)
		for _, bad := range []string{e.remoteRoot, e.home} {
			if strings.Contains(all, bad) {
				t.Errorf("the record of class %d names a folder on the host: %s", i, all)
			}
		}
		rows, cls, bunit := readMap(t, ds.Path)
		if len(rows) != 4 || len(rows[0]) != 6 || cls != i || bunit != "K km/s" {
			t.Errorf("class %d file: %d rows, VESQCLS %d, BUNIT %q", i, len(rows), cls, bunit)
		}
	}
	// the crop and the fit are as they were; nothing was copied here
	if ents, _ := os.ReadDir(dir); len(ents) == 0 {
		t.Fatal("the fit's folder is empty")
	}
	if e.auditCount("ROHSA width class maps") != 1 {
		t.Errorf("class maps are audited once")
	}
}

// pixelsOfCell is the closed form: the pixels with a component of the synthetic maps fit in cell (s, v) of the diagram of reviewBins.
func pixelsOfCell(s, v int) map[[2]int]bool {
	out := map[[2]int]bool{}
	for k := 0; k < dnComp; k++ {
		for y := 0; y < dyPix; y++ {
			for x := 0; x < dxPix; x++ {
				if _, c, w, ok := truth(k, x, y); ok {
					if int(math.Floor(w/fwhmPerSigma/0.5)) == s && int(math.Floor((c+20)/5)) == v {
						out[[2]int{x, y}] = true
					}
				}
			}
		}
	}
	return out
}

func TestReviewOfAMapsRunOnAHostEqualsTheClosedFormAndTheIndexIsInvalidatedByAChangedMap(t *testing.T) {
	t.Parallel()
	e := reviewHostEnv(t)
	fit := filepath.Join(e.remoteRoot, "fit")
	e.writeSynthDecomp(fit)
	run, raw := e.importROHSAFromHost(fit, 201)
	if run.Rohsa == nil || run.Rohsa.HostID != e.host.ID || run.Rohsa.CropDatasetID == nil {
		t.Fatalf("run %s", raw)
	}
	base := e.api("/api/datasets/" + itoa(int(*run.Rohsa.CropDatasetID)) + "/rohsa")
	bins := map[string]any{"velocityBins": map[string]any{"min": -20.0, "max": 30.0, "n": 10}, "sigmaBins": map[string]any{"min": 0.0, "max": 4.0, "n": 8}}
	diagram := map[string]any{"runId": run.ID, "weight": "count"}
	for k, v := range bins {
		diagram[k] = v
	}
	resp, body := postJSON(t, base+"/sigmav", diagram)
	if resp.StatusCode != 200 || strings.Contains(string(body), `"pixel_index":null`) || !strings.Contains(string(body), `"pixel_index"`) {
		t.Fatalf("sigmav %d %s", resp.StatusCode, body)
	}
	for _, f := range e.cacheFiles() {
		if !strings.HasSuffix(f, ".cells") {
			t.Errorf("only the index is written in the host's cache folder, got %s", f)
		}
	}
	if ents, _ := os.ReadDir(fit); len(cellFiles(ents)) != 0 {
		t.Errorf("an index in the fit's own folder")
	}
	clicked := 0
	for s := 0; s < 8 && clicked < 4; s++ {
		for v := 0; v < 10 && clicked < 4; v++ {
			want := pixelsOfCell(s, v)
			if len(want) == 0 {
				continue
			}
			clicked++
			cell := map[string]any{"runId": run.ID, "weight": "count", "cell": map[string]any{"s": s, "v": v}}
			for k, val := range bins {
				cell[k] = val
			}
			r, b := postJSON(t, base+"/sigmav/cell", cell)
			var m struct {
				Pixels int
				Source string
			}
			json.Unmarshal(b, &m)
			if r.StatusCode != 200 || m.Pixels != len(want) || m.Source != "pixel index written by the diagram" {
				t.Errorf("cell (%d, %d): %d %s, want %d pixels", s, v, r.StatusCode, b, len(want))
			}
		}
	}
	if clicked == 0 {
		t.Fatal("no cell has a pixel")
	}

	// class maps by the closed form: a pixel holds the number of its valid components in the class (count weight), [lo, hi) in sigma
	mreq := map[string]any{"runId": run.ID, "weight": "count", "edges": []float64{2.0}}
	resp, body = postJSON(t, base+"/class-maps", mreq)
	if resp.StatusCode != 201 {
		t.Fatalf("class maps %d %s", resp.StatusCode, body)
	}
	var cm reviewMaps
	json.Unmarshal(body, &cm)
	if len(cm.Maps) != 2 {
		t.Fatalf("classes: %s", body)
	}
	for i, mp := range cm.Maps {
		if mp.Dataset.HostID != e.host.ID || filepath.Dir(mp.Dataset.Path) != e.hostDerived() {
			t.Errorf("class %d is %+v", i, mp.Dataset)
		}
		rows, cls, _ := readMap(t, mp.Dataset.Path)
		if len(rows) != dyPix || cls != i {
			t.Fatalf("class %d file: %d rows, VESQCLS %d", i, len(rows), cls)
		}
		for y := 0; y < dyPix; y++ {
			for x := 0; x < dxPix; x++ {
				want := 0.0
				for k := 0; k < dnComp; k++ {
					if _, _, w, ok := truth(k, x, y); ok && (w/fwhmPerSigma >= 2.0) == (i == 1) {
						want++
					}
				}
				if got := rows[y][x]; got == nil || *got != want {
					t.Errorf("class %d pixel (%d, %d) = %v, want %v", i, x, y, got, want)
				}
			}
		}
	}

	// a map that changed on the host after the diagram was drawn: the click says to draw it again
	rewriteAmplitudes(t, e, filepath.Join(fit, "synth_decomp_amp.fits"), 3)
	cell := map[string]any{"runId": run.ID, "weight": "count", "cell": map[string]any{"s": 1, "v": 1}}
	for k, v := range bins {
		cell[k] = v
	}
	if r, b := postJSON(t, base+"/sigmav/cell", cell); r.StatusCode != 422 || !strings.Contains(string(b), "draw the diagram again") {
		t.Errorf("a click after a map changed: %d %s", r.StatusCode, b)
	}
}

// stubManager is a manager for the same stand-in host whose hostrun.py is the given script: a worker too old to know the review, or one that
// never answers.
func stubManager(t *testing.T, e *watchEnv, hostrun string) *hosts.Manager {
	t.Helper()
	files := workerFiles(t, e.repo)
	files["hostrun.py"] = []byte(hostrun)
	m := hosts.NewManager(e.srv.Remote.Ssh, files)
	m.ControlDir = t.TempDir()
	t.Cleanup(m.Close)
	return m
}

const oldHostrun = `import sys, json
sys.stdin.read()
print(json.dumps({"ok": False, "error": "bad request"}))
sys.exit(2)
`

func TestReviewOnAHostRefusesAnOldWorkerAndStopsWaitingForASilentOne(t *testing.T) {
	t.Parallel()
	e := reviewHostEnv(t)
	fit := filepath.Join(e.remoteRoot, "fit")
	e.writeSynthDecomp(fit)
	run, _ := e.importROHSAFromHost(fit, 201)
	base := e.api("/api/datasets/" + itoa(int(*run.Rohsa.CropDatasetID)) + "/rohsa")
	diagram := map[string]any{"runId": run.ID, "weight": "count", "velocityBins": map[string]any{"min": -20.0, "max": 30.0, "n": 10}, "sigmaBins": map[string]any{"min": 0.0, "max": 4.0, "n": 8}}
	maps := map[string]any{"runId": run.ID, "weight": "count", "edges": []float64{2.0}}
	registered := func() int {
		ds, _ := e.srv.Store.List(workspace.Local)
		return len(ds)
	}
	before := registered()

	// a worker that does not know the operation: refused with a sentence that says what to do, nothing written or registered
	good := e.srv.Remote
	e.srv.Remote = stubManager(t, e, oldHostrun)
	for path, body := range map[string]map[string]any{"/sigmav": diagram, "/class-maps": maps} {
		r, b := postJSON(t, base+path, body)
		if r.StatusCode != 409 || !strings.Contains(string(b), "older than this version") || !strings.Contains(string(b), "Hosts, Check") {
			t.Errorf("old worker %s: %d %s", path, r.StatusCode, b)
		}
	}
	if registered() != before || len(e.cacheFiles()) != 0 {
		t.Errorf("an old worker left something: %d datasets, cache %v", registered(), e.cacheFiles())
	}
	if _, err := os.Stat(e.hostDerived()); err == nil {
		t.Errorf("the derived folder was made by a refused review")
	}

	// a host that accepts the request and never answers: Vesqira stops at the budget, and says so
	e.srv.Remote = stubManager(t, e, "import time\ntime.sleep(120)\n")
	e.srv.Analysis.Timeout = 2 * time.Second
	t0 := time.Now()
	r, b := postJSON(t, base+"/sigmav", diagram)
	if r.StatusCode != 504 || !strings.Contains(string(b), "stopped waiting") || time.Since(t0) > 8*time.Second {
		t.Errorf("a silent host: %d %s after %s", r.StatusCode, b, time.Since(t0))
	}

	// a host that is down: not reachable, and the person is told to check the VPN; the host is marked unreachable
	e.srv.Remote = good
	e.srv.Analysis.Timeout = 2 * time.Minute
	e.srv.Remote.Forget(e.host.ID)
	os.WriteFile(filepath.Join(e.home, "DOWN"), nil, 0o644)
	r, b = postJSON(t, base+"/sigmav", diagram)
	if r.StatusCode != 502 || !strings.Contains(string(b), "not reachable") {
		t.Errorf("a host that is down: %d %s", r.StatusCode, b)
	}
	os.Remove(filepath.Join(e.home, "DOWN"))
	e.srv.Remote.Forget(e.host.ID)
	if r, b := postJSON(t, base+"/sigmav", diagram); r.StatusCode != 200 {
		t.Errorf("after the host is back: %d %s", r.StatusCode, b)
	}
}

func TestReviewOnAHostStopsWhenTheRequestIsCancelled(t *testing.T) {
	t.Parallel()
	e := reviewHostEnv(t)
	h := e.host
	// the request goes to the host's program and the person leaves right after: the call returns at once with the budget error the caller
	// maps to "request cancelled", and does not wait for the host
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	t0 := time.Now()
	_, err := e.srv.Remote.HostReview(ctx, h, "rohsa_sigmav", "", []string{"--input", filepath.Join(e.remoteRoot, "x.fits"), "--params", "{}"}, 2*time.Minute)
	if err != nil && !errors.Is(err, hosts.ErrHostRunTimeout) {
		t.Logf("returned %v before the cancel", err) // a refusal that came back first is also fine: the point is not to wait
	}
	if time.Since(t0) > 10*time.Second {
		t.Errorf("the call waited %s after the person left", time.Since(t0))
	}
	// and the server maps it to 499, whatever the host was doing
	code, msg := 0, ""
	_, _, code, msg = e.srv.hostCall(ctx, h, "rohsa_sigmav", nil, "%s", func([]string, time.Duration) ([]byte, error) { return nil, hosts.ErrHostRunTimeout })
	if code != 499 || !strings.Contains(msg, "cancelled") {
		t.Errorf("cancelled request: %d %s", code, msg)
	}
}
