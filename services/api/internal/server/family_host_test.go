package server

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
)

// A family of maps on a host is compared where the maps are (ADR 0070): the host's copy of the program writes the comparison maps in its
// derived folder, only the summary comes back, and the numbers equal those of the same family on this computer.
func TestAFamilyOfMapsOnAHostIsComparedThereAndMatchesTheSameFamilyHere(t *testing.T) {
	t.Parallel()
	e := newHostParity(t)
	py, _ := exec.LookPath("python3")
	// two more versions of the cube (scaled, and scaled with a different offset), the same file on both sides
	mk := exec.Command(py, "-c", `import sys, numpy as np
from astropy.io import fits
src, a, b, c, d = sys.argv[1:]
with fits.open(src) as h:
    data, hdr = h[0].data.astype('f4'), h[0].header
for out, k, off in ((a, 1.1, 0.0), (b, 1.0, 0.25), (c, 1.1, 0.0), (d, 1.0, 0.25)):
    fits.PrimaryHDU((data * k + off).astype('f4'), header=hdr).writeto(out)`,
		filepath.Join(e.localRoot, "hi_cube.fits"), filepath.Join(e.localRoot, "hi_x11.fits"), filepath.Join(e.localRoot, "hi_p25.fits"),
		filepath.Join(e.remoteRoot, "hi_x11.fits"), filepath.Join(e.remoteRoot, "hi_p25.fits"))
	if out, err := mk.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var loc, rem []catalog.Dataset
	for _, n := range []string{"hi_cube.fits", "hi_x11.fits", "hi_p25.fits"} {
		l, r := e.pair(n)
		loc, rem = append(loc, l), append(rem, r)
	}
	ids := func(ds []catalog.Dataset) []int64 { return []int64{ds[0].ID, ds[1].ID, ds[2].ID} }
	type result struct {
		Maps []struct {
			Role    string
			Member  int
			Dataset catalog.Dataset
		}
		Summary struct {
			Members []struct {
				Compared int      `json:"pixels_compared"`
				Mean     *float64 `json:"mean"`
				RMS      *float64 `json:"rms"`
			}
			PairwiseRMS [][]*float64 `json:"pairwise_rms"`
			Unit        string
			Labels      []string
		}
	}
	run := func(d []catalog.Dataset) (int, result, string) {
		code, b := e.call("POST", "/api/analysis/family", map[string]any{"datasetIds": ids(d), "referenceId": d[0].ID, "channel": 20, "labels": []string{"native", "x1.1", "+0.25 K"}})
		var r result
		json.Unmarshal(b, &r)
		return code, r, string(b)
	}
	hostDerived := filepath.Join(e.remoteRoot, "vesqira_derived")
	beforeLocal := derivedFiles(t, e.derivedDir)

	code, onHost, hb := run(rem)
	if code != 201 {
		t.Fatalf("on the host: %d %s", code, hb)
	}
	if got := derivedFiles(t, e.derivedDir); len(got) != len(beforeLocal) {
		t.Errorf("nothing may land in the local derived folder for a family on a host: %v -> %v", beforeLocal, got)
	}
	files := derivedFiles(t, hostDerived)
	if len(files) != 3 {
		t.Fatalf("the host's derived folder holds %v: want the spread and two comparison maps", files)
	}
	for _, f := range files {
		if strings.HasPrefix(f, ".part-") {
			t.Errorf("a temporary file was left on the host: %s", f)
		}
	}
	var sumOnly struct {
		Summary json.RawMessage `json:"summary"`
	}
	json.Unmarshal([]byte(hb), &sumOnly)
	if len(sumOnly.Summary) == 0 || strings.Contains(string(sumOnly.Summary), e.remoteRoot) {
		t.Errorf("the summary is missing or names a host path: %s", sumOnly.Summary)
	}
	if len(onHost.Maps) != 3 {
		t.Fatalf("maps: %s", hb)
	}
	for _, m := range onHost.Maps {
		if m.Dataset.HostID != e.h.ID {
			t.Errorf("a result map is not registered on the host: %+v", m.Dataset)
		}
		var rec derived.Record
		dc, db := e.get(fmt.Sprintf("/api/datasets/%d/derived", m.Dataset.ID))
		json.Unmarshal(db, &rec)
		if dc != 200 || rec.Op != "family_agreement" || len(rec.Sources) != 3 || !strings.Contains(string(rec.Params), fmt.Sprintf(`"hostId":%d`, e.h.ID)) {
			t.Errorf("record of %d: %d %+v %s", m.Dataset.ID, dc, rec, rec.Params)
		}
		// the viewer's plane endpoint serves it from the host
		if c, _ := e.get(fmt.Sprintf("/api/datasets/%d/plane?channel=0", m.Dataset.ID)); c != 200 {
			t.Errorf("plane of a result on the host: %d", c)
		}
	}
	code, here, lb := run(loc)
	if code != 201 {
		t.Fatalf("here: %d %s", code, lb)
	}
	// the numbers are the same as for the same maps here (float32 maps through the same program)
	if fmt.Sprint(here.Summary.Labels) != fmt.Sprint(onHost.Summary.Labels) || here.Summary.Unit != onHost.Summary.Unit {
		t.Errorf("labels or unit differ: %v %v", here.Summary, onHost.Summary)
	}
	for i := range here.Summary.Members {
		a, b := here.Summary.Members[i], onHost.Summary.Members[i]
		if a.Compared != b.Compared || (a.Mean == nil) != (b.Mean == nil) || (a.Mean != nil && (*a.Mean != *b.Mean || *a.RMS != *b.RMS)) {
			t.Errorf("member %d differs: here %+v host %+v", i, a, b)
		}
	}
	if fmt.Sprint(here.Summary.PairwiseRMS[1][2]) == "" || *here.Summary.PairwiseRMS[1][2] != *onHost.Summary.PairwiseRMS[1][2] {
		t.Errorf("pairwise rms differs")
	}
	// the +0.25 K member differs from the native cube by exactly 0.25 K where both have a value (golden, from how the file was made)
	if m := onHost.Summary.Members[2]; m.Mean == nil || *m.Mean < 0.2499 || *m.Mean > 0.2501 {
		t.Errorf("mean difference of the offset member: %+v", m)
	}

	// maps in two places are not compared, and nothing is copied
	if code, b := e.call("POST", "/api/analysis/family", map[string]any{"datasetIds": []int64{loc[0].ID, rem[1].ID}, "channel": 20}); code != 409 || !strings.Contains(b2s(b), "does not copy maps") {
		t.Errorf("mixed places: %d %s", code, b)
	}
	// a host that cannot be reached is a clear refusal, with nothing recorded and no file made
	n := len(derivedFiles(t, hostDerived))
	os.WriteFile(filepath.Join(e.remoteHome, "DOWN"), nil, 0o644)
	if code, b := e.call("POST", "/api/analysis/family", map[string]any{"datasetIds": ids(rem), "channel": 20}); code != 502 || !strings.Contains(b2s(b), "not reach") && !strings.Contains(b2s(b), "VPN") {
		t.Errorf("an unreachable host: %d %s", code, b)
	}
	os.Remove(filepath.Join(e.remoteHome, "DOWN"))
	if len(derivedFiles(t, hostDerived)) != n {
		t.Errorf("a refused run left files on the host")
	}
	// a member outside the host's roots or a bad channel is refused by the host's own program, not computed
	if code, b := e.call("POST", "/api/analysis/family", map[string]any{"datasetIds": ids(rem), "channel": 9999}); code != 422 || !strings.Contains(b2s(b), "channel") {
		t.Errorf("a channel out of range on the host: %d %s", code, b)
	}
	if len(derivedFiles(t, hostDerived)) != n {
		t.Errorf("a refused run left files on the host")
	}
}

func b2s(b []byte) string { return string(b) }
