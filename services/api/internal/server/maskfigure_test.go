package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// A figure saved with masks hatched records which ones, by type, name, tier and fingerprint; the server writes those from its own rows.
// The reviewer's provenance has a line for each (type, name, tier, rule, fingerprint) and never an id or a path. A mask that is not the
// dataset's, is stale or has another fingerprint is refused. "Draw again" announces a mask that changed or was forgotten first.
func TestAFigureRecordsTheMasksItHatchedAndRedrawAnnouncesAChangedOne(t *testing.T) {
	t.Parallel()
	f := newCarryFixture(t)
	view := map[string]any{"kind": "plane", "channel": 3, "colormap": "viridis", "stretch": "linear", "range": "percentile"}
	save := func(masksField any) (int, boards.Figure, string) {
		sc := planeScene(f.cube.ID)
		sc["render"] = map[string]any{"canvas": map[string]any{"width": 64, "height": 48}, "nodata": []any{[]any{10, 10, 10}, []any{40, 40, 40}}, "figureWidth": 720}
		if masksField != nil {
			sc["masks"] = masksField
		}
		st, b := callJSON(t, "POST", f.e.ts.URL+"/api/figures", map[string]any{"datasetId": f.cube.ID, "title": "t", "view": view, "previewPng": testPNG(t), "scene": sc})
		var fig boards.Figure
		json.Unmarshal(b, &fig)
		return st, fig, string(b)
	}
	entry := func(id int64, typ, name, tier, sha string) map[string]any {
		return map[string]any{"id": id, "type": typ, "name": name, "tier": tier, "fingerprint": "sha256:" + sha}
	}

	// what is refused saves nothing
	other := f.e.reg("hi_cube_coarser.fits")
	_, foreign, _ := f.e.make(t, other.ID, map[string]any{"method": "missing"})
	for name, tc := range map[string]struct {
		in   []any
		want int
		text string
	}{
		"another dataset's mask":              {[]any{entry(foreign.ID, "missing", "x", "verified", foreign.SHA256)}, 422, "belongs to another dataset"},
		"a fingerprint that is not the row's": {[]any{entry(f.reg.ID, "user_excluded", "Knot", "declared", strings.Repeat("a", 64))}, 409, "fingerprint differs"},
		"a mask that is not in the catalog":   {[]any{entry(99999, "missing", "x", "verified", strings.Repeat("a", 64))}, 422, "not in the catalog"},
		"a mask listed twice":                 {[]any{entry(f.reg.ID, "user_excluded", "Knot", "declared", f.reg.SHA256), entry(f.reg.ID, "user_excluded", "Knot", "declared", f.reg.SHA256)}, 400, "twice"},
		"an unknown type":                     {[]any{entry(f.reg.ID, "good", "Knot", "declared", f.reg.SHA256)}, 400, "needs its id"},
	} {
		if st, _, b := save(tc.in); st != tc.want || !strings.Contains(b, tc.text) {
			t.Errorf("%s: %d %s", name, st, b)
		}
	}
	figs, _ := f.e.srv.Boards.Figures(workspace.Local)
	if len(figs) != 0 {
		t.Fatalf("a refused figure saves nothing: %d", len(figs))
	}

	// the name and tier the client wrote are not trusted: the row's are stored
	st, fig, b := save([]any{entry(f.reg.ID, "user_excluded", "A name the client made up", "verified", f.reg.SHA256), entry(f.sat.ID, "saturated", "Bright", "declared", f.sat.SHA256)})
	if st != 201 {
		t.Fatalf("save: %d %s", st, b)
	}
	var stored struct {
		Masks []struct {
			ID          int64  `json:"id"`
			Type, Name  string `json:"-"`
			Fingerprint string `json:"fingerprint"`
		} `json:"masks"`
	}
	if err := json.Unmarshal(fig.Scene, &stored); err != nil || len(stored.Masks) != 2 || stored.Masks[0].Fingerprint != "sha256:"+f.reg.SHA256 {
		t.Fatalf("the scene's masks: %s", fig.Scene)
	}
	if !strings.Contains(string(fig.Scene), `"name":"Knot"`) || !strings.Contains(string(fig.Scene), `"tier":"declared"`) || strings.Contains(string(fig.Scene), "made up") {
		t.Fatalf("the server's own name and tier are stored: %s", fig.Scene)
	}

	// the reviewer's provenance: a line per mask shown, no id and no path
	var pr struct {
		Tiers []safeTier `json:"tiers"`
	}
	json.Unmarshal(fig.Provenance, &pr)
	var knot, bright bool
	for _, ti := range pr.Tiers {
		if ti.What == "mask shown: excluded by the researcher (Knot)" && ti.Tier == "declared" && strings.Contains(ti.Note, "sha256:"+f.reg.SHA256) && strings.Contains(ti.Note, "rule: pixels whose centre is inside a region") {
			knot = true
		}
		bright = bright || (ti.What == "mask shown: saturated (Bright)" && strings.Contains(ti.Note, "sha256:"+f.sat.SHA256))
	}
	if !knot || !bright {
		t.Fatalf("tiers: %+v", pr.Tiers)
	}
	if s := string(fig.Provenance); strings.Contains(s, f.e.dir) || strings.Contains(s, fmt.Sprintf(`"id":%d`, f.reg.ID)) || strings.Contains(s, "mask-") {
		t.Fatalf("the reviewer's provenance leaks an id or a path: %s", s)
	}

	check := func() redrawResult {
		st, rb := get(t, f.e.ts.URL+fmt.Sprintf("/api/figures/%d/redraw-check", fig.ID))
		if st.StatusCode != 200 {
			t.Fatalf("redraw-check: %d %s", st.StatusCode, rb)
		}
		var r redrawResult
		json.Unmarshal(rb, &r)
		if strings.Contains(string(rb), f.e.dir) || strings.Contains(string(rb), fmt.Sprintf(`"id":%d`, f.reg.ID)) {
			t.Fatalf("the redraw check shows an id or a path: %s", rb)
		}
		return r
	}
	if r := check(); r.State != "ready" || len(r.Masks) != 2 || r.Masks[0].Status != "unchanged" {
		t.Fatalf("unchanged masks do not ask: %+v", r)
	}
	// made from other bytes than the dataset has now: changed
	if _, err := f.e.cat.DB().Exec(`UPDATE masks SET source_size = source_size + 1 WHERE id=?`, f.reg.ID); err != nil {
		t.Fatal(err)
	}
	r := check()
	if r.State != "ask" || !strings.Contains(r.Prompt, "mask") || r.Masks[0].Status != "changed" || r.Masks[1].Status != "unchanged" || r.Masks[0].Name != "Knot" || r.Masks[0].Type != "user_excluded" {
		t.Fatalf("a changed mask is announced: %+v", r)
	}
	// forgotten
	if st, b := callJSON(t, "POST", f.e.ts.URL+fmt.Sprintf("/api/masks/%d/delete", f.sat.ID), map[string]any{}); st != 200 {
		t.Fatalf("forget: %d %s", st, b)
	}
	if r := check(); r.State != "ask" || r.Masks[1].Status != "forgotten" || !strings.Contains(r.Masks[1].Text, "forgotten") {
		t.Fatalf("a forgotten mask is announced: %+v", r)
	}
	// a figure without masks is unchanged by all this
	if st, _, b := save(nil); st != 201 {
		t.Fatalf("a figure without masks: %d %s", st, b)
	}
}
