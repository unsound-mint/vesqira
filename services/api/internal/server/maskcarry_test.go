package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/masks"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// The masks of hi_cube.fits (64 channels of 32 x 24, with a NaN box): a per-channel saturated mask, a region mask of one plane that
// applies to every channel, and the no-value mask computed from the data.
type carryFixture struct {
	e           *maskEnv
	cube        catalog.Dataset
	sat, reg, m masks.Mask
}

func newCarryFixture(t *testing.T) *carryFixture {
	t.Helper()
	e := maskServer(t)
	cube := e.reg("hi_cube.fits")
	st, sat, b := e.make(t, cube.ID, map[string]any{"method": "saturated", "threshold": 0.0, "source": "a test threshold", "name": "Bright"})
	if st != 201 {
		t.Fatalf("saturated: %d %s", st, b)
	}
	var rg regions.Region
	_, rb := callJSON(t, "POST", e.ts.URL+"/api/datasets/"+itoa(int(cube.ID))+"/regions", map[string]any{"name": "knot", "shape": map[string]any{"shape": "circle", "cx": 10, "cy": 8, "r": 3}})
	json.Unmarshal(rb, &rg)
	st, reg, b := e.make(t, cube.ID, map[string]any{"method": "regions", "regionIds": []int64{rg.ID}, "name": "Knot"})
	if st != 201 {
		t.Fatalf("regions: %d %s", st, b)
	}
	st, miss, b := e.make(t, cube.ID, map[string]any{"method": "missing"})
	if st != 201 {
		t.Fatalf("missing: %d %s", st, b)
	}
	return &carryFixture{e: e, cube: cube, sat: sat, reg: reg, m: miss}
}

func (f *carryFixture) analyze(t *testing.T, body map[string]any) (int, struct {
	Dataset catalog.Dataset `json:"dataset"`
	Summary map[string]any  `json:"summary"`
}, string) {
	t.Helper()
	st, b := callJSON(t, "POST", f.e.ts.URL+"/api/analysis", body)
	var out struct {
		Dataset catalog.Dataset `json:"dataset"`
		Summary map[string]any  `json:"summary"`
	}
	json.Unmarshal(b, &out)
	return st, out, string(b)
}

func (f *carryFixture) masksOf(t *testing.T, id int64) map[string]masks.Mask {
	t.Helper()
	ms, err := f.e.srv.Masks.ForDataset(workspace.Local, id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]masks.Mask{}
	for _, m := range ms {
		out[m.Type+"/"+m.Name] = m
	}
	return out
}

var cutoutBox = map[string]string{"x0": "4", "x1": "20", "y0": "2", "y1": "15"}

// Nothing is carried unless the researcher chooses it. A chosen mask becomes a new mask of the result: its type, its tier, a rule that
// says what was done, the fingerprint of its own file, fresh against the result. What was left behind is recorded; the no-value mask is
// computed from the result, not carried. The sources are byte-identical afterwards.
func TestACutoutCarriesOnlyTheMasksChosenAndRecordsTheRest(t *testing.T) {
	t.Parallel()
	f := newCarryFixture(t)
	srcFiles := []string{filepath.Join(f.e.root, "hi_cube.fits")}
	for _, m := range []masks.Mask{f.sat, f.reg, f.m} {
		stored, _ := f.e.srv.Masks.Get(workspace.Local, m.ID)
		srcFiles = append(srcFiles, stored.Path)
	}
	before := map[string]string{}
	for _, p := range srcFiles {
		before[p] = fileSHA(t, p)
	}

	// nothing chosen: the result has only its own no-value mask, and both masks are recorded as left behind
	st, none, b := f.analyze(t, map[string]any{"op": "cutout", "datasetId": f.cube.ID, "params": cutoutBox})
	if st != 201 {
		t.Fatalf("cutout: %d %s", st, b)
	}
	got := f.masksOf(t, none.Dataset.ID)
	if len(got) != 1 || got["missing/No value (NaN, BLANK)"].Method != masks.MethodComputed || got["missing/No value (NaN, BLANK)"].Tier != "verified" {
		t.Fatalf("a cutout carries nothing by default: %+v", got)
	}

	st, res, b := f.analyze(t, map[string]any{"op": "cutout", "datasetId": f.cube.ID, "params": cutoutBox,
		"carryMasks": []map[string]any{{"id": f.reg.ID, "carry": true}, {"id": f.sat.ID, "carry": false}}})
	if st != 201 {
		t.Fatalf("cutout: %d %s", st, b)
	}
	got = f.masksOf(t, res.Dataset.ID)
	if len(got) != 2 {
		t.Fatalf("the region mask and the recomputed no-value mask, and not the saturated one: %+v", got)
	}
	c := got["user_excluded/Knot"]
	if c.Type != masks.UserExcluded || c.Tier != f.reg.Tier || c.Method != masks.MethodCarried || c.Name != f.reg.Name || c.Channels != 0 {
		t.Fatalf("a carried mask keeps its type, tier and name: %+v", c)
	}
	if c.Width != 17 || c.Height != 14 || c.Flagged != f.reg.Flagged {
		t.Fatalf("the knot is wholly inside the box, so it is the same pixels in the cut window: %+v (source flagged %d)", c, f.reg.Flagged)
	}
	var rule struct {
		Rule        string `json:"rule"`
		FlagsFollow string `json:"flagsFollow"`
		CarriedFrom struct {
			Fingerprint string `json:"fingerprint"`
			Tier        string `json:"tier"`
		} `json:"carriedFrom"`
	}
	json.Unmarshal(c.Rule, &rule)
	if want := `carried from mask "Knot" of hi_cube.fits by cutout x 4-20, y 2-15`; !strings.HasPrefix(rule.Rule, want) {
		t.Fatalf("rule: %s", c.Rule)
	}
	if rule.CarriedFrom.Fingerprint != "sha256:"+f.reg.SHA256 || rule.CarriedFrom.Tier != f.reg.Tier {
		t.Fatalf("the rule names the source mask's fingerprint and tier: %s", c.Rule)
	}
	stored, _ := f.e.srv.Masks.Get(workspace.Local, c.ID)
	if c.SHA256 == "" || c.SHA256 != fileSHA(t, stored.Path) || c.SHA256 == f.reg.SHA256 {
		t.Fatalf("the fingerprint is the new file's own: %s", c.SHA256)
	}
	// a carried mask is fresh against the result (not stale at birth), so an analysis of the result can apply it
	if st, _, b := f.e.stats(t, res.Dataset.ID, []map[string]any{{"id": c.ID, "policy": "exclude"}}); st != 200 {
		t.Fatalf("applying a carried mask to the result: %d %s", st, b)
	}
	// the result's no-value mask is the NaN box of the window, counted from the result's own values
	miss := got["missing/No value (NaN, BLANK)"]
	if miss.Method != masks.MethodComputed || miss.Flagged == 0 || miss.Width != 17 {
		t.Fatalf("no-value mask: %+v", miss)
	}

	// the record: carried and left behind, with fingerprints, tiers and no ids; the key `masks` (applied) is not used
	der := f.e.srv.derivedSummary(workspace.Local, res.Dataset.ID)
	var pm struct {
		Masks     json.RawMessage `json:"masks"`
		MaskCarry struct {
			Carried []struct {
				Name, Type, Tier                string
				Fingerprint, CarriedFingerprint string
			} `json:"carried"`
			LeftBehind []struct{ Name, Type, Tier, Fingerprint string } `json:"leftBehind"`
			Recomputed []string                                         `json:"recomputed"`
		} `json:"maskCarry"`
	}
	json.Unmarshal(der.Params, &pm)
	if len(pm.Masks) != 0 || len(pm.MaskCarry.Carried) != 1 || pm.MaskCarry.Carried[0].CarriedFingerprint != "sha256:"+c.SHA256 ||
		pm.MaskCarry.Carried[0].Fingerprint != "sha256:"+f.reg.SHA256 || len(pm.MaskCarry.LeftBehind) != 1 || pm.MaskCarry.LeftBehind[0].Name != "Bright" ||
		pm.MaskCarry.LeftBehind[0].Fingerprint != "sha256:"+f.sat.SHA256 || len(pm.MaskCarry.Recomputed) != 1 {
		t.Fatalf("the derived record: %s", der.Params)
	}

	// the reviewer sees types, names, tiers and fingerprints; never an id or a path
	d, _ := f.e.cat.Get(workspace.Local, res.Dataset.ID)
	raw, err := buildProvenance(d, ViewParams{}, nil, der, nil)
	if err != nil {
		t.Fatal(err)
	}
	var pr struct {
		Tiers []safeTier `json:"tiers"`
	}
	json.Unmarshal(raw, &pr)
	var carriedLine, leftLine, missLine bool
	for _, ti := range pr.Tiers {
		carriedLine = carriedLine || (strings.HasPrefix(ti.What, "mask carried into this result: excluded by the researcher (Knot)") && ti.Tier == f.reg.Tier)
		leftLine = leftLine || (strings.HasPrefix(ti.What, "mask left behind: saturated (Bright)") && strings.Contains(ti.Note, "not carried"))
		missLine = missLine || (strings.HasPrefix(ti.What, "mask: no value (computed again)") && ti.Tier == "verified")
	}
	if !carriedLine || !leftLine || !missLine {
		t.Fatalf("the reviewer's tier lines: %+v", pr.Tiers)
	}
	if s := string(raw); strings.Contains(s, f.e.dir) || strings.Contains(s, "mask-carried") || strings.Contains(s, fmt.Sprintf(`"id":%d`, c.ID)) {
		t.Fatalf("the reviewer's provenance leaks a path or an id: %s", s)
	}

	for p, h := range before {
		if fileSHA(t, p) != h {
			t.Errorf("%s was changed", p)
		}
	}
}

// The mask a carry is refused for: another dataset's, the no-value mask (computed again), a stale one, a listed-twice one; a stale mask can
// still be left behind. Align and convolve want every mask decided, never a silent default.
func TestCarryingRefusesWhatItMust(t *testing.T) {
	t.Parallel()
	f := newCarryFixture(t)
	other := f.e.reg("hi_cube_coarser.fits")
	_, foreign, _ := f.e.make(t, other.ID, map[string]any{"method": "missing"})
	box := func(c ...map[string]any) map[string]any {
		return map[string]any{"op": "cutout", "datasetId": f.cube.ID, "params": cutoutBox, "carryMasks": c}
	}
	if st, _, b := f.analyze(t, box(map[string]any{"id": foreign.ID, "carry": true})); st != 422 || !strings.Contains(b, "not one of this dataset's") {
		t.Errorf("another dataset's mask: %d %s", st, b)
	}
	if st, _, b := f.analyze(t, box(map[string]any{"id": f.m.ID, "carry": true})); st != 422 || !strings.Contains(b, "never carried") {
		t.Errorf("the no-value mask: %d %s", st, b)
	}
	if st, _, b := f.analyze(t, box(map[string]any{"id": f.reg.ID, "carry": true}, map[string]any{"id": f.reg.ID, "carry": false})); st != 422 || !strings.Contains(b, "twice") {
		t.Errorf("listed twice: %d %s", st, b)
	}
	if st, _, b := f.analyze(t, map[string]any{"op": "moment", "datasetId": f.cube.ID, "carryMasks": []map[string]any{{"id": f.reg.ID, "carry": true}}}); st != 422 {
		t.Errorf("an analysis that does not carry masks: %d %s", st, b)
	}
	if _, err := f.e.cat.DB().Exec(`UPDATE masks SET source_size = source_size + 1 WHERE id=?`, f.reg.ID); err != nil {
		t.Fatal(err)
	}
	before := fileCount(t, f.e.dir)
	if st, _, b := f.analyze(t, box(map[string]any{"id": f.reg.ID, "carry": true})); st != 409 || !strings.Contains(b, "make it again") {
		t.Errorf("a stale mask: %d %s", st, b)
	}
	if after := fileCount(t, f.e.dir); after != before {
		t.Errorf("a refusal leaves no file behind: %d before, %d after", before, after)
	}
	if st, _, b := f.analyze(t, box(map[string]any{"id": f.reg.ID, "carry": false})); st != 201 {
		t.Errorf("a stale mask can be left behind: %d %s", st, b)
	}

	// align and convolve: every mask decided
	conv := map[string]any{"op": "convolve", "datasetId": f.cube.ID, "params": map[string]string{"targetBmaj": "144", "targetBmin": "144", "targetBpa": "0"}}
	st, _, b := f.analyze(t, conv)
	if st != 422 || !strings.Contains(b, "choose") || !strings.Contains(b, "Bright") || !strings.Contains(b, "Knot") || strings.Contains(b, "No value") {
		t.Errorf("a convolution with undecided masks: %d %s", st, b)
	}
	conv["carryMasks"] = []map[string]any{{"id": f.sat.ID, "carry": true}, {"id": f.reg.ID, "carry": false}}
	if st, _, b := f.analyze(t, conv); st != 201 {
		t.Fatalf("convolve with every mask decided: %d %s", st, b)
	}
}

func fileCount(t *testing.T, dir string) int {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(es)
}

// Convolution flags what the kernel footprint reaches; a per-channel mask stays per channel; the result's no-value mask is its own.
func TestConvolveCarriesAMaskThroughTheKernelFootprint(t *testing.T) {
	t.Parallel()
	f := newCarryFixture(t)
	st, res, b := f.analyze(t, map[string]any{"op": "convolve", "datasetId": f.cube.ID, "params": map[string]string{"targetBmaj": "144", "targetBmin": "144", "targetBpa": "0"},
		"carryMasks": []map[string]any{{"id": f.reg.ID, "carry": true}, {"id": f.sat.ID, "carry": false}}})
	if st != 201 {
		t.Fatalf("%d %s", st, b)
	}
	half, _ := res.Summary["kernel_half_width_pixels"].(float64)
	if half < 1 {
		t.Fatalf("a real kernel: %v", res.Summary)
	}
	c := f.masksOf(t, res.Dataset.ID)["user_excluded/Knot"]
	// the knot is the disc of radius 3 at (10, 8); the footprint is the square of half-width `half` around each of its pixels, so the
	// flagged area is the bounding box of the disc grown by `half` (a square structuring element on a convex disc)
	n := int(half)
	want := int64((2*3 + 1 + 2*n) * (2*3 + 1 + 2*n))
	if c.Flagged < f.reg.Flagged || c.Flagged > want || c.Width != 32 || c.Height != 24 {
		t.Fatalf("flagged %d, between the knot (%d) and its grown box (%d): %+v", c.Flagged, f.reg.Flagged, want, c)
	}
	var rule struct {
		Rule        string `json:"rule"`
		FlagsFollow string `json:"flagsFollow"`
	}
	json.Unmarshal(c.Rule, &rule)
	if !strings.Contains(rule.FlagsFollow, "convolution kernel") || !strings.Contains(rule.Rule, "carried from mask \"Knot\" of hi_cube.fits by convolve") {
		t.Fatalf("the rule states the footprint: %s", c.Rule)
	}
	if got := f.masksOf(t, res.Dataset.ID); len(got) != 2 {
		t.Fatalf("the carried mask and the recomputed no-value mask: %+v", got)
	}
}

// Align carries a mask through the exact (area-overlap) reprojection and names the reference; the choice is required.
func TestAlignCarriesAMaskAndNamesTheReference(t *testing.T) {
	t.Parallel()
	f := newCarryFixture(t)
	ref := f.e.reg("hi_cube_coarser.fits")
	body := map[string]any{"op": "align", "datasetId": f.cube.ID, "referenceId": ref.ID}
	if st, _, b := f.analyze(t, body); st != 422 || !strings.Contains(b, "choose") {
		t.Fatalf("undecided: %d %s", st, b)
	}
	body["carryMasks"] = []map[string]any{{"id": f.sat.ID, "carry": true}, {"id": f.reg.ID, "carry": true}}
	st, res, b := f.analyze(t, body)
	if st != 201 {
		t.Fatalf("%d %s", st, b)
	}
	got := f.masksOf(t, res.Dataset.ID)
	reg := got["user_excluded/Knot"]
	if reg.Method != masks.MethodCarried || reg.Flagged == 0 || !strings.Contains(string(reg.Rule), "reference: hi_cube_coarser.fits") || !strings.Contains(string(reg.Rule), "exact") {
		t.Fatalf("carried through the reprojection: %+v %s", reg, reg.Rule)
	}
	if sat := got["saturated/Bright"]; sat.Channels != 64 || sat.Tier != f.sat.Tier {
		t.Fatalf("a per-channel mask follows the channels: %+v", sat)
	}
}

// A virtual subset written as a file carries the masks chosen, cut by the same window; a request with another choice is another file.
func TestAMaterialisedSubsetCarriesMasksByItsWindowAndIsReusedOnlyForTheSameChoice(t *testing.T) {
	t.Parallel()
	f := newCarryFixture(t)
	code, b := callJSON(t, "POST", fmt.Sprintf("%s/api/datasets/%d/subsets", f.e.ts.URL, f.cube.ID), map[string]any{"name": "Core", "reason": "the source",
		"box": map[string]any{"x0": 3, "x1": 20, "y0": 2, "y1": 15}, "step": 2, "channels": map[string]any{"c0": 10, "c1": 40}})
	if code != 201 {
		t.Fatalf("create: %d %s", code, b)
	}
	var sub struct {
		Subset struct {
			ID int64 `json:"id"`
		} `json:"subset"`
	}
	json.Unmarshal(b, &sub)
	url := fmt.Sprintf("%s/api/subsets/%d/materialise", f.e.ts.URL, sub.Subset.ID)
	mat := func(c ...map[string]any) (int, catalog.Dataset, bool) {
		st, rb := callJSON(t, "POST", url, map[string]any{"carryMasks": c})
		var out struct {
			Dataset catalog.Dataset `json:"dataset"`
			Reused  bool            `json:"reused"`
		}
		json.Unmarshal(rb, &out)
		if st != 200 && st != 201 {
			t.Fatalf("materialise: %d %s", st, rb)
		}
		return st, out.Dataset, out.Reused
	}
	_, plain, _ := mat()
	_, withMasks, reused := mat(map[string]any{"id": f.sat.ID, "carry": true}, map[string]any{"id": f.reg.ID, "carry": false})
	if reused || withMasks.ID == plain.ID {
		t.Fatal("a request with a mask to carry is not answered by the file written without one")
	}
	_, again, reused := mat(map[string]any{"id": f.sat.ID, "carry": true}, map[string]any{"id": f.reg.ID, "carry": false})
	if !reused || again.ID != withMasks.ID {
		t.Fatalf("the same choice is answered by the same file: %v %d %d", reused, again.ID, withMasks.ID)
	}
	got := f.masksOf(t, withMasks.ID)
	sat := got["saturated/Bright"]
	// 9 columns (3..19 step 2), 7 rows (2..14 step 2), 31 channels (10..40) of the source's 64
	if sat.Width != 9 || sat.Height != 7 || sat.Channels != 31 || sat.Method != masks.MethodCarried {
		t.Fatalf("the mask follows the window and the channel range: %+v", sat)
	}
	if !strings.Contains(string(sat.Rule), "subset window x 3-19") {
		t.Fatalf("rule: %s", sat.Rule)
	}
	// nothing is carried by default into the file written without a choice
	if plainMasks := f.masksOf(t, plain.ID); len(plainMasks) != 1 {
		t.Fatalf("only the no-value mask: %+v", plainMasks)
	}
}
