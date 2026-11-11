package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A host that stops answering during an import is not a host with no trace, no crop and no maps: nothing is saved, a good run
// under the same key is not overwritten by a degraded one, and an interrupted request saves nothing either.
func TestROHSAImportFromAHostThatGoesSilentSavesNothingAndKeepsTheGoodRun(t *testing.T) {
	e := newWatchEnv(t)
	e.writeROHSA(filepath.Join(e.remoteRoot, "fit"), "")
	dir := filepath.Join(e.remoteRoot, "fit", "region_a")
	dat := filepath.Join(dir, "rohsa_n3.dat")
	down := func() {
		os.WriteFile(filepath.Join(e.home, "DOWN"), nil, 0o644)
		e.srv.Remote.Forget(e.host.ID)
	}
	up := func() {
		os.Remove(filepath.Join(e.home, "DOWN"))
		e.srv.Remote.Forget(e.host.ID)
	}
	// the host is silent after the target was found: every read fails
	down()
	if _, code, msg := e.srv.importROHSA(context.Background(), newRemoteROHSA(e.srv, e.host, dat)); code != 502 || !strings.Contains(msg, "not reachable") {
		t.Errorf("a silent host: %d %q", code, msg)
	}
	if rs, _ := e.runs.List(); len(rs) != 0 {
		t.Errorf("a run built from silence was saved: %+v", rs)
	}
	e.importROHSAFromHost(dir, 502) // through the route: the fit cannot even be found
	up()
	good, _ := e.importROHSAFromHost(dir, 201)
	if good.Rohsa == nil || good.Rohsa.CropDatasetID == nil || len(good.Outputs) < 6 || len(good.Metrics) != 2 {
		t.Fatalf("good run: %+v", good)
	}
	// the same import again while the host is silent leaves the good run exactly as it was
	down()
	if _, code, _ := e.srv.importROHSA(context.Background(), newRemoteROHSA(e.srv, e.host, dat)); code != 502 {
		t.Errorf("a silent host on re-import: %d", code)
	}
	up()
	after, err := e.runs.Get(good.ID)
	if err != nil || len(after.Outputs) != len(good.Outputs) || len(after.Metrics) != 2 {
		t.Errorf("the good run was changed: %+v %v", after, err)
	}
	if rr, err := e.runs.ROHSA(good.ID); err != nil || rr.Info.CropDatasetID == nil || len(rr.Info.Notes) != len(good.Rohsa.Notes) {
		t.Errorf("the good run's record was changed: %+v %v", rr, err)
	}
	// a request that was cancelled (a closed tab) saves nothing and changes nothing
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, code, _ := e.srv.importROHSA(ctx, newRemoteROHSA(e.srv, e.host, dat)); code != 502 {
		t.Errorf("a cancelled import: %d", code)
	}
	if rs, _ := e.runs.List(); len(rs) != 1 {
		t.Errorf("%d runs", len(rs))
	}
}

// writeBigROHSA writes a fit large enough that the host's index takes more than one chunk (the pass reads 32 MiB at a time): a
// 600 x 600 crop of two channels with one Gaussian per pixel is 360 000 rows of 103 bytes.
func (e *watchEnv) writeBigROHSA(dir string) {
	e.t.Helper()
	const py = `
import sys, numpy as np
from pathlib import Path
from astropy.io import fits
d = Path(sys.argv[1]); d.mkdir(parents=True, exist_ok=True)
n = 600
h = fits.PrimaryHDU(np.zeros((2, n, n), dtype=np.float32)).header
for i, (ct, cv, cd, cp) in enumerate([("RA---TAN", 10.0, -0.01, 300.5), ("DEC--TAN", 20.0, 0.01, 300.5), ("VRAD", -1000.0, 800.0, 1.0)], start=1):
    h["CTYPE%d" % i], h["CRVAL%d" % i], h["CDELT%d" % i], h["CRPIX%d" % i] = ct, cv, cd, cp
h["BUNIT"] = "K"
fits.PrimaryHDU(np.ones((2, n, n), dtype=np.float32), header=h).writeto(d / "big.fits", overwrite=True)
yy, xx = np.divmod(np.arange(n * n), n)
with open(d / "rohsa_n1.dat", "w") as f:
    f.write(" # \n # i, j, A, mean, sigma\n")
    f.write("".join("%12d%12d%26.17g%26.17g%26.17g\n" % (y, x, 1.0 + 0.001 * x, 1.0 + (x % 2), 1.5) for y, x in zip(yy.tolist(), xx.tolist())))
`
	if out, err := exec.Command("python3", "-c", py, dir).CombinedOutput(); err != nil {
		e.t.Fatalf("%v\n%s", err, out)
	}
}

// The host's index of a fit bigger than one chunk is built over several bounded calls and resumed, through a worker that
// enforces roots, and stays in the host's cache folder.
func TestROHSAHostIndexOfAFitBiggerThanOneChunkIsBuiltInResumableCalls(t *testing.T) {
	e := newWatchEnv(t)
	dir := filepath.Join(e.remoteRoot, "big")
	e.writeBigROHSA(dir)
	if fi, _ := os.Stat(filepath.Join(dir, "rohsa_n1.dat")); fi == nil || fi.Size() < 33<<20 {
		t.Fatalf("the fixture must exceed one 32 MiB chunk")
	}
	ask := func(op string, extra map[string]any) (map[string]any, error) {
		req := map[string]any{"op": op, "path": filepath.Join(dir, "big.fits"), "dat_path": filepath.Join(dir, "rohsa_n1.dat"), "n_gauss": 1}
		for k, v := range extra {
			req[k] = v
		}
		rep, err := e.srv.Remote.Do(context.Background(), e.host, req)
		if err != nil {
			return nil, err
		}
		var h map[string]any
		json.Unmarshal(rep.Head, &h)
		return h, nil
	}
	if _, err := ask("rohsa_pixel", map[string]any{"x": 5, "y": 5}); err == nil {
		t.Fatal("a pixel before any index must be refused")
	}
	first, err := ask("rohsa_index", map[string]any{"budget_s": 0.001}) // one chunk, then out of time
	if err != nil {
		t.Fatal(err)
	}
	if first["state"] != "building" || first["created"] != true || first["settled"] != false || first["done_bytes"].(float64) <= 0 || first["done_bytes"].(float64) >= first["size_bytes"].(float64) {
		t.Fatalf("first call: %+v", first)
	}
	steps := 1
	last := first
	for ; last["state"] == "building" && steps < 20; steps++ {
		if last, err = ask("rohsa_index", map[string]any{"budget_s": 0.001}); err != nil {
			t.Fatal(err)
		}
		if last["created"] == true {
			t.Fatal("a resumed call began a second index")
		}
	}
	if last["state"] != "ready" || last["settled"] != true || steps < 2 || last["rows"].(float64) != 600*600 {
		t.Fatalf("after %d calls: %+v", steps, last)
	}
	files := e.cacheFiles()
	if len(files) != 2 {
		t.Fatalf("host cache folder: %v", files)
	}
	for _, f := range files {
		if strings.HasSuffix(f, ".offs") {
			if fi, _ := os.Stat(filepath.Join(e.home, ".cache", "vesqira", "rohsa", f)); fi.Size() != 600*600*8 {
				t.Errorf("index size %d, want 8 bytes per pixel", fi.Size())
			}
		}
	}
	ents, _ := os.ReadDir(dir)
	for _, x := range ents {
		if strings.HasSuffix(x.Name(), ".offs") || strings.HasSuffix(x.Name(), ".json") || strings.HasSuffix(x.Name(), ".tmp") {
			t.Errorf("nothing is written next to the data: %s", x.Name())
		}
	}
	rep, err := e.srv.Remote.Do(context.Background(), e.host, map[string]any{"op": "rohsa_pixel", "path": filepath.Join(dir, "big.fits"), "dat_path": filepath.Join(dir, "rohsa_n1.dat"), "n_gauss": 1, "x": 599, "y": 3})
	if err != nil {
		t.Fatal(err)
	}
	var head struct {
		Components []struct{ Amp float64 }
	}
	json.Unmarshal(rep.Head, &head)
	if len(head.Components) != 1 || head.Components[0].Amp != 1.0+0.001*599 {
		t.Errorf("pixel (599, 3): %+v", head)
	}
}
