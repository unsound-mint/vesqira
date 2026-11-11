package server

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
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
	"github.com/unsound-mint/vesqira/services/api/internal/lifecycle"
	"github.com/unsound-mint/vesqira/services/api/internal/masks"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

type maskEnv struct {
	ts   *httptest.Server
	srv  *Server
	cat  *catalog.Store
	root string
	dir  string
	reg  func(name string) catalog.Dataset
}

// maskServer starts the API with the real worker, on generated fixtures and a data file made here with real zeros and real NaNs.
func maskServer(t *testing.T) *maskEnv {
	t.Helper()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy, reproject").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy and reproject is required")
		}
		t.Skip("python3 with astropy and reproject not available")
	}
	py, _ := exec.LookPath("python3")
	base := t.TempDir()
	root, derivedDir := filepath.Join(base, "local"), filepath.Join(base, "derived")
	for _, d := range []string{root, derivedDir} {
		os.MkdirAll(d, 0o755)
	}
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	mk := exec.Command("python3", "-c", `
import sys, numpy as np
from astropy.io import fits
a = np.full((6, 8), 3.0, dtype='<f4')
a[1, 1] = 0.0; a[2, 2] = 0.0          # zeros are values
a[4, 5] = np.nan; a[0, 7] = np.nan    # holes
a[3, 3] = 100.0
h = fits.Header(); h['BUNIT'] = 'K'
fits.PrimaryHDU(a, header=h).writeto(sys.argv[1])
`, filepath.Join(root, "zeros_and_holes.fits"))
	if out, err := mk.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cat, _ := catalog.Open("file:" + filepath.Join(base, "t.db"))
	t.Cleanup(func() { cat.Close() })
	ds, _ := derived.Open(cat.DB())
	ms, err := masks.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	rg, _ := regions.Open(cat.DB())
	rs, err := sourceroot.New([]string{root, derivedDir})
	if err != nil {
		t.Fatal(err)
	}
	pw := &worker.Persistent{Command: []string{py, "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	srv := &Server{Roots: rs, Store: cat, Worker: headerWorker(t, py, repo), Planes: pw, Cache: NewPlaneCache(8 << 20), Derived: ds, Masks: ms, Regions: rg,
		Analysis: &AnalysisConfig{Python: py, Script: filepath.Join(repo, "services/scientific-worker/analysis.py"), DerivedDir: derivedDir, Timeout: 2 * time.Minute}}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	e := &maskEnv{ts: ts, srv: srv, cat: cat, root: root, dir: derivedDir}
	e.reg = func(name string) catalog.Dataset {
		var d catalog.Dataset
		json.NewDecoder(post(t, ts, filepath.Join(root, name), nil).Body).Decode(&d)
		return d
	}
	return e
}

func (e *maskEnv) make(t *testing.T, id int64, body map[string]any) (int, masks.Mask, string) {
	t.Helper()
	st, b := callJSON(t, "POST", e.ts.URL+"/api/datasets/"+itoa(int(id))+"/masks", body)
	var m struct {
		masks.Mask
		Stale bool `json:"stale"`
	}
	json.Unmarshal(b, &m)
	return st, m.Mask, string(b)
}

func (e *maskEnv) stats(t *testing.T, id int64, uses []map[string]any) (int, map[string]any, string) {
	t.Helper()
	body := map[string]any{"op": "stats", "datasetId": id}
	if uses != nil {
		body["masks"] = uses
	}
	st, b := callJSON(t, "POST", e.ts.URL+"/api/analysis", body)
	var out struct {
		Summary map[string]any `json:"summary"`
	}
	json.Unmarshal(b, &out)
	return st, out.Summary, string(b)
}

func num(m map[string]any, k string) float64 { f, _ := m[k].(float64); return f }

// Pixels with no value are the type `missing`, computed from the data and never declared; zero is a value and no mask flags it unless
// its stated rule does. Each type records its rule, its tier and what it was made from; the dataset's file is not touched.
func TestMasksAreTypedComputedFromTheDataAndNeverTreatZeroAsMissing(t *testing.T) {
	e := maskServer(t)
	d := e.reg("zeros_and_holes.fits")
	before, _ := os.ReadFile(filepath.Join(e.root, "zeros_and_holes.fits"))
	doc := loadSpec(t)

	st, miss, b := e.make(t, d.ID, map[string]any{"method": "missing"})
	if st != 201 {
		t.Fatalf("missing: %d %s", st, b)
	}
	conforms(t, responseSchemaAt(t, doc, "/api/datasets/{id}/masks", "POST", 201, "application/json"), "create mask", []byte(b))
	if miss.Type != "missing" || miss.Tier != "verified" || miss.Method != "computed" || miss.Flagged != 2 || miss.Total != 48 || miss.Author != "user" {
		t.Fatalf("missing mask: %+v", miss)
	}
	if miss.SHA256 == "" || !strings.HasPrefix(miss.SourceIdentity, "sha256:") || !strings.Contains(string(miss.Rule), "NaN") {
		t.Fatalf("identity and rule: %+v %s", miss, miss.Rule)
	}
	// the dataset's own file is byte-identical
	if after, _ := os.ReadFile(filepath.Join(e.root, "zeros_and_holes.fits")); string(after) != string(before) {
		t.Fatal("the data file changed")
	}
	// the plane the viewer draws: ones exactly where the file has no value, and not at the two zeros
	r, fb := get(t, e.ts.URL+"/api/masks/"+itoa(int(miss.ID))+"/plane?channel=0")
	if r.StatusCode != 200 {
		t.Fatalf("plane: %d %s", r.StatusCode, fb)
	}
	n := binary.LittleEndian.Uint32(fb)
	var head struct{ Width, Height int }
	json.Unmarshal(fb[4:4+n], &head)
	px := fb[4+n:]
	if head.Width != 8 || head.Height != 6 || len(px) != 48 {
		t.Fatalf("frame: %+v %d", head, len(px))
	}
	for y := 0; y < 6; y++ {
		for x := 0; x < 8; x++ {
			want := byte(0)
			if (y == 4 && x == 5) || (y == 0 && x == 7) {
				want = 1
			}
			if px[y*8+x] != want {
				t.Fatalf("pixel (%d,%d) = %d, want %d (zeros at (1,1) and (2,2) are values)", x, y, px[y*8+x], want)
			}
		}
	}
	// the type missing cannot be declared, and a mask of type other needs a description
	if st, _, b := e.make(t, d.ID, map[string]any{"method": "import", "type": "missing", "importDatasetId": d.ID, "nonzero": "flagged"}); st != 422 {
		t.Fatalf("declaring missing: %d %s", st, b)
	}
	// saturated: a threshold and its source
	if st, _, b := e.make(t, d.ID, map[string]any{"method": "saturated", "threshold": 100.0}); st != 422 || !strings.Contains(b, "where") {
		t.Fatalf("saturated without a source: %d %s", st, b)
	}
	st, sat, b := e.make(t, d.ID, map[string]any{"method": "saturated", "threshold": 100.0, "source": "detector datasheet"})
	if st != 201 || sat.Flagged != 1 || sat.Tier != "declared" || sat.Type != "saturated" {
		t.Fatalf("saturated: %d %s", st, b)
	}
	// low SNR with the robust estimate: the rule and estimator are recorded; no pixel with no value is flagged
	st, lo, b := e.make(t, d.ID, map[string]any{"method": "low_snr", "k": 3.0, "sigma": map[string]any{"kind": "value", "value": 2.0}})
	if st != 201 || lo.Type != "low_snr" {
		t.Fatalf("low snr: %d %s", st, b)
	}
	// value / 2 < 3 flags every finite value below 6: the 3.0 background, and the two zeros; not the 100, not the two holes
	if lo.Flagged != 48-2-1 {
		t.Fatalf("low snr flagged %d, want %d: %s", lo.Flagged, 48-2-1, lo.Rule)
	}
	// a saved region: the circle's pixels by the region pixel rule
	reg := regions.Region{}
	_, rb := callJSON(t, "POST", e.ts.URL+"/api/datasets/"+itoa(int(d.ID))+"/regions", map[string]any{"name": "knot", "shape": map[string]any{"shape": "circle", "cx": 2, "cy": 2, "r": 1}})
	json.Unmarshal(rb, &reg)
	st, ux, b := e.make(t, d.ID, map[string]any{"method": "regions", "regionIds": []int64{reg.ID}})
	if st != 201 || ux.Type != "user_excluded" || ux.Flagged != 5 || ux.Channels != 0 {
		t.Fatalf("regions: %d %s", st, b)
	}
	// a mask of another dataset's region is refused
	if st, _, b := e.make(t, d.ID, map[string]any{"method": "regions", "regionIds": []int64{99999}}); st != 422 {
		t.Fatalf("a foreign region: %d %s", st, b)
	}

	// statistics: each mask gets its own explicit policy, recorded in the result
	policy := func(m masks.Mask, p string) map[string]any { return map[string]any{"id": m.ID, "policy": p} }
	_, base, _ := e.stats(t, d.ID, nil)
	if num(base, "finite") != 46 || num(base, "no_value") != 2 || num(base, "min") != 0 {
		t.Fatalf("base stats: %v", base)
	}
	st, kept, b := e.stats(t, d.ID, []map[string]any{policy(sat, "keep"), policy(miss, "exclude")})
	if st != 200 || num(kept, "finite") != 46 || num(kept, "max") != 100 {
		t.Fatalf("keep: %d %s", st, b)
	}
	st, excl, b := e.stats(t, d.ID, []map[string]any{policy(sat, "exclude")})
	if st != 200 || num(excl, "finite") != 45 || num(excl, "max") != 3 || num(excl, "no_value") != 2 || num(excl, "excluded_by_mask") != 1 {
		t.Fatalf("exclude: %d %s", st, b)
	}
	got := excl["masks"].([]any)[0].(map[string]any)
	if got["policy"] != "exclude" || got["type"] != "saturated" || got["tier"] != "declared" || num(got, "id") != float64(sat.ID) {
		t.Fatalf("recorded mask: %v", got)
	}
	// excluding the no-value mask removes nothing that has a value: zero is not missing
	st, ex2, b := e.stats(t, d.ID, []map[string]any{policy(miss, "exclude")})
	if st != 200 || num(ex2, "finite") != 46 || num(ex2, "min") != 0 || num(ex2, "excluded_by_mask") != 0 {
		t.Fatalf("exclude missing: %d %s", st, b)
	}
	// no policy, an unknown policy, weight where there are no weights, and keeping a hole are refused
	for name, uses := range map[string][]map[string]any{
		"no policy":       {{"id": sat.ID}},
		"unknown policy":  {{"id": sat.ID, "policy": "ignore"}},
		"weight":          {{"id": sat.ID, "policy": "weight", "weight": 0.5}},
		"keep a no-value": {{"id": miss.ID, "policy": "keep"}},
		"twice":           {policy(sat, "keep"), policy(sat, "exclude")},
		"not a mask":      {{"id": 987654, "policy": "keep"}},
	} {
		if st, _, b := e.stats(t, d.ID, uses); st != 422 {
			t.Errorf("%s: %d %s", name, st, b)
		}
	}
	// another dataset's mask is not this dataset's to apply
	other := e.reg("hi_cube.fits")
	if st, _, b := e.stats(t, other.ID, []map[string]any{policy(sat, "exclude")}); st != 422 || !strings.Contains(b, "another dataset") {
		t.Errorf("a foreign mask: %d %s", st, b)
	}
	// a mask whose dataset changed is stale and is not applied
	list, lb := get(t, e.ts.URL+"/api/datasets/"+itoa(int(d.ID))+"/masks")
	if list.StatusCode != 200 || !strings.Contains(string(lb), `"stale":false`) {
		t.Fatalf("list: %d %s", list.StatusCode, lb)
	}
	conforms(t, responseSchemaAt(t, doc, "/api/datasets/{id}/masks", "GET", 200, "application/json"), "mask list", lb)
}

// A moment map made with masks carries them: the owner's record has the ids and the policy, the reviewer's provenance has the type,
// rule, policy, tier and fingerprint and no id, and it survives the mask being forgotten.
func TestAResultCarriesTheMasksThatWereAppliedAndTheReviewerSeesTheirTiersWithoutIds(t *testing.T) {
	e := maskServer(t)
	cube := e.reg("hi_cube.fits")
	st, sat, b := e.make(t, cube.ID, map[string]any{"method": "saturated", "threshold": 0.0, "source": "a test threshold"})
	if st != 201 {
		t.Fatalf("%d %s", st, b)
	}
	if sat.Channels != 64 || sat.Width != 32 || sat.Height != 24 {
		t.Fatalf("a per-channel mask: %+v", sat)
	}
	run := func(p string) (catalog.Dataset, map[string]any) {
		t.Helper()
		st, b := callJSON(t, "POST", e.ts.URL+"/api/analysis", map[string]any{"op": "moment", "datasetId": cube.ID, "params": map[string]string{"order": "0"}, "masks": []map[string]any{{"id": sat.ID, "policy": p}}})
		if st != 201 {
			t.Fatalf("moment %s: %d %s", p, st, b)
		}
		var out struct {
			Dataset catalog.Dataset `json:"dataset"`
			Summary map[string]any  `json:"summary"`
		}
		json.Unmarshal(b, &out)
		return out.Dataset, out.Summary
	}
	keepDS, _ := run("keep")
	exclDS, exclSum := run("exclude")
	if keepDS.ID == exclDS.ID {
		t.Fatal("two results")
	}
	rec, err := e.srv.Derived.Get(exclDS.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ownerSum struct {
		Masks []struct {
			ID     int64  `json:"id"`
			Policy string `json:"policy"`
		} `json:"masks"`
	}
	json.Unmarshal(rec.Summary, &ownerSum)
	if len(ownerSum.Masks) != 1 || ownerSum.Masks[0].ID != sat.ID || ownerSum.Masks[0].Policy != "exclude" {
		t.Fatalf("the owner's record carries the mask's id and policy: %s", rec.Summary)
	}
	if exclSum["masks"] == nil {
		t.Fatalf("summary: %v", exclSum)
	}
	// the reviewer-safe account
	der := e.srv.derivedSummary(exclDS.ID)
	d, _ := e.cat.Get(exclDS.ID)
	raw, err := buildProvenance(d, ViewParams{}, nil, der, nil)
	if err != nil {
		t.Fatal(err)
	}
	var pr struct {
		Tiers []safeTier `json:"tiers"`
	}
	json.Unmarshal(raw, &pr)
	var found *safeTier
	for i := range pr.Tiers {
		if strings.HasPrefix(pr.Tiers[i].What, "mask: ") {
			found = &pr.Tiers[i]
		}
	}
	if found == nil || found.Tier != "declared" || !strings.Contains(found.Note, "excluded") {
		t.Fatalf("tiers: %+v", pr.Tiers)
	}
	if s := string(raw); strings.Contains(s, fmt.Sprintf(`"id":%d`, sat.ID)) || strings.Contains(s, e.dir) || strings.Contains(s, "mask-") {
		t.Fatalf("the reviewer's provenance leaks an id or a path: %s", s)
	}
	if !strings.Contains(string(raw), "sha256:"+sat.SHA256) {
		t.Fatalf("the fingerprint of the mask is recorded: %s", raw)
	}

	// forgetting the mask keeps the file and the result's own record
	stored, err := e.srv.Masks.Get(sat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st, b := callJSON(t, "POST", e.ts.URL+"/api/masks/"+itoa(int(sat.ID))+"/delete", map[string]any{}); st != 200 {
		t.Fatalf("delete: %d %s", st, b)
	}
	if _, err := os.Stat(stored.Path); err != nil {
		t.Fatalf("the mask file must stay: %v", err)
	}
	if der2 := e.srv.derivedSummary(exclDS.ID); !strings.Contains(string(der2.Params), "sha256:"+sat.SHA256) {
		t.Fatalf("the result keeps its own record of the mask: %s", der2.Params)
	}
	// masks go with their dataset, and the impact preview says so
	st, m2, b := e.make(t, cube.ID, map[string]any{"method": "missing"})
	if st != 201 {
		t.Fatalf("%d %s", st, b)
	}
	storedM2, _ := e.srv.Masks.Get(m2.ID)
	svc := lifecycle.New(e.cat.DB())
	im, err := svc.Impact(lifecycle.KindDataset, cube.ID)
	if err != nil {
		t.Fatal(err)
	}
	var said bool
	for _, g := range im.Groups {
		if g.Key == "masks" && g.Effect == "removed" && g.Count == 1 {
			said = true
		}
	}
	if !said {
		t.Fatalf("impact: %+v", im.Groups)
	}
	if _, err := svc.Delete(lifecycle.KindDataset, cube.ID, im.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.Masks.Get(m2.ID); err != masks.ErrNotFound {
		t.Fatalf("the mask goes with its dataset: %v", err)
	}
	if _, err := os.Stat(storedM2.Path); err != nil {
		t.Fatalf("its file is never deleted: %v", err)
	}
}

// An imported mask file keeps the declared type and the meaning of nonzero; a different grid is refused.
func TestImportedMaskKeepsItsDeclaredTypeAndMeaning(t *testing.T) {
	e := maskServer(t)
	d := e.reg("zeros_and_holes.fits")
	mk := exec.Command("python3", "-c", `
import sys, numpy as np
from astropy.io import fits
m = np.zeros((6, 8), dtype='<i2'); m[0, 0:3] = 1
fits.PrimaryHDU(m).writeto(sys.argv[1])
fits.PrimaryHDU(np.zeros((5, 5), dtype='<i2')).writeto(sys.argv[2])
`, filepath.Join(e.root, "their_mask.fits"), filepath.Join(e.root, "other_grid.fits"))
	if out, err := mk.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	theirs, wrong := e.reg("their_mask.fits"), e.reg("other_grid.fits")
	st, m, b := e.make(t, d.ID, map[string]any{"method": "import", "type": "other", "importDatasetId": theirs.ID, "nonzero": "flagged", "meaning": "ghosting from the bright star"})
	if st != 201 || m.Type != "other" || m.Flagged != 3 || m.Tier != "declared" || m.Meaning != "ghosting from the bright star" || m.Method != "import" || !strings.Contains(string(m.Rule), "flagged") || !strings.Contains(string(m.Rule), "pixel shape only") {
		t.Fatalf("import: %d %s", st, b)
	}
	st, inv, b := e.make(t, d.ID, map[string]any{"method": "import", "type": "invalid", "importDatasetId": theirs.ID, "nonzero": "valid"})
	if st != 201 || inv.Flagged != 45 || !strings.Contains(string(inv.Rule), "zero is flagged") {
		t.Fatalf("import with nonzero = valid: %d %s", st, b)
	}
	for name, body := range map[string]map[string]any{
		"no meaning for nonzero": {"method": "import", "type": "invalid", "importDatasetId": theirs.ID},
		"type other, no text":    {"method": "import", "type": "other", "importDatasetId": theirs.ID, "nonzero": "flagged"},
		"another grid":           {"method": "import", "type": "invalid", "importDatasetId": wrong.ID, "nonzero": "flagged"},
		"itself":                 {"method": "import", "type": "invalid", "importDatasetId": d.ID, "nonzero": "flagged"},
	} {
		if st, _, b := e.make(t, d.ID, body); st < 400 || st >= 500 {
			t.Errorf("%s: %d %s", name, st, b)
		}
	}
}

// A mask made from other bytes than the dataset has now is stale and is refused rather than applied.
func TestAStaleMaskIsNotApplied(t *testing.T) {
	e := maskServer(t)
	d := e.reg("zeros_and_holes.fits")
	_, sat, _ := e.make(t, d.ID, map[string]any{"method": "saturated", "threshold": 100.0, "source": "x"})
	if _, err := e.cat.DB().Exec(`UPDATE masks SET source_size = source_size + 1 WHERE id=?`, sat.ID); err != nil {
		t.Fatal(err)
	}
	if st, _, b := e.stats(t, d.ID, []map[string]any{{"id": sat.ID, "policy": "exclude"}}); st != 409 || !strings.Contains(b, "make it again") {
		t.Fatalf("stale: %d %s", st, b)
	}
	_, lb := get(t, e.ts.URL+"/api/datasets/"+itoa(int(d.ID))+"/masks")
	if !strings.Contains(string(lb), `"stale":true`) {
		t.Fatalf("the list says so: %s", lb)
	}
	_ = math.NaN
}
