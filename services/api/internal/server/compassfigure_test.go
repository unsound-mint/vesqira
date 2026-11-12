package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
)

// A figure that drew north and east arrows records them in its scene and says so in the reviewer's provenance, with the WCS state they
// rest on. A figure without them (the option off, no celestial WCS, or saved before the option) has no such line. A compass that is not a
// reading (an angle outside [0, 360), a keyword that is not a FITS name) is refused and saves nothing.
func TestAFigureRecordsTheArrowsItDrewAndSaysWhatTheyRestOn(t *testing.T) {
	t.Parallel()
	f := newCarryFixture(t)
	view := map[string]any{"kind": "plane", "channel": 3, "colormap": "viridis", "stretch": "linear", "range": "percentile"}
	save := func(compass any) (int, boards.Figure, string) {
		sc := planeScene(f.cube.ID)
		if compass != nil {
			sc["compass"] = compass
		}
		st, b := callJSON(t, "POST", f.e.ts.URL+"/api/figures", map[string]any{"datasetId": f.cube.ID, "title": "t", "view": view, "previewPng": testPNG(t), "scene": sc})
		var fig boards.Figure
		json.Unmarshal(b, &fig)
		return st, fig, string(b)
	}
	lines := func(fig boards.Figure) (tier string, statements []string) {
		var pr struct {
			Tiers      []safeTier `json:"tiers"`
			Statements []string   `json:"statements"`
		}
		json.Unmarshal(fig.Provenance, &pr)
		for _, ti := range pr.Tiers {
			if ti.What == "north and east arrows (WCS state)" {
				tier = ti.Tier + ": " + ti.Note
			}
		}
		for _, s := range pr.Statements {
			if strings.Contains(s, "North (N) and east (E) arrows") {
				statements = append(statements, s)
			}
		}
		return
	}

	for name, bad := range map[string]any{
		"an angle that is not below 360": map[string]any{"x": 1, "y": 1, "northDeg": 360, "eastDeg": 90},
		"a keyword that is not a name":   map[string]any{"x": 1, "y": 1, "northDeg": 90, "eastDeg": 0, "corrected": []string{"x y"}},
		"a missing direction":            map[string]any{"x": 1, "y": 1, "northDeg": 90},
		"an invented key":                map[string]any{"x": 1, "y": 1, "northDeg": 90, "eastDeg": 0, "up": true},
	} {
		if st, _, b := save(bad); st != 400 {
			t.Errorf("%s: %d %s", name, st, b)
		}
	}
	if figs, _ := f.e.srv.Boards.Figures(); len(figs) != 0 {
		t.Fatalf("a refused figure saves nothing: %d", len(figs))
	}

	// the file's own WCS
	st, fig, b := save(map[string]any{"x": 31.5, "y": 23.5, "northDeg": 120, "eastDeg": 210, "frame": "icrs"})
	if st != 201 {
		t.Fatalf("save: %d %s", st, b)
	}
	if !strings.Contains(string(fig.Scene), `"compass":{"x":31.5,"y":23.5,"northDeg":120,"eastDeg":210,"frame":"icrs"}`) {
		t.Fatalf("the scene keeps the arrows: %s", fig.Scene)
	}
	tier, statements := lines(fig)
	if !strings.HasPrefix(tier, "declared: north 120.0") || !strings.Contains(tier, "east 210.0") || !strings.Contains(tier, "pixel (31.5, 23.5)") || !strings.Contains(tier, "icrs frame") || !strings.Contains(tier, "the file's own WCS") || strings.Contains(tier, "corrected") {
		t.Fatalf("the tier line: %q", tier)
	}
	if len(statements) != 1 || !strings.Contains(statements[0], "north is not assumed up") {
		t.Fatalf("the statement: %q", statements)
	}

	// corrections applied: the marker is in the provenance too
	_, fig, _ = save(map[string]any{"x": 31.5, "y": 23.5, "northDeg": 100, "eastDeg": 190, "corrected": []string{"PC1_2", "PC2_1"}})
	if tier, _ = lines(fig); !strings.Contains(tier, "corrections (PC1_2, PC2_1): corrected") {
		t.Fatalf("a corrected WCS is named: %q", tier)
	}

	// no arrows, no line
	_, fig, _ = save(nil)
	if tier, statements = lines(fig); tier != "" || len(statements) != 0 || strings.Contains(string(fig.Scene), "compass") {
		t.Fatalf("a figure without arrows claims none: %q %q %s", tier, statements, fig.Scene)
	}
}
