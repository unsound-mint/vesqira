package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
)

// A figure records what its picture shows: an outline outside the field is left out, name included, so a label on a dataset that is
// not in the picture does not gate it, and a view that shows no outline is refused.
func TestSkyViewFigureRecordsOnlyOutlinesInTheField(t *testing.T) {
	t.Parallel()
	e := skyEnv(t)
	near := e.register("hi_cube.fits")
	far := e.register("hi_cube_galactic.fits")
	png := testPNG(t)
	fps := []map[string]any{{"kind": "dataset", "id": near.ID}, {"kind": "dataset", "id": far.ID}}
	code, b := e.call("POST", "/api/figures/sky", skyBody(png, map[string]any{"footprints": fps, "centre": map[string]any{"lon": 150.0, "lat": 2.0}, "fovDeg": 1.5}))
	if code != 201 {
		t.Fatalf("save: %d %s", code, b)
	}
	var f boards.Figure
	json.Unmarshal(b, &f)
	var spec skyViewSpec
	json.Unmarshal(f.Scene, &spec)
	if len(spec.Footprints) != 1 || spec.Footprints[0].DatasetID != near.ID {
		t.Fatalf("recorded: %+v", spec.Footprints)
	}
	if strings.Contains(string(f.Provenance), "hi_cube_galactic") || !strings.Contains(string(f.Provenance), "sky view of 1 datasets") {
		t.Errorf("provenance names what is not in the picture: %s", f.Provenance)
	}
	// a label on the dataset outside the field does not gate this figure
	if c, b := e.call("POST", fmt.Sprintf("/api/datasets/%d/access", far.ID), map[string]any{"label": "unreleased", "reason": "not approved"}); c != 200 {
		t.Fatalf("label: %d %s", c, b)
	}
	if c, b := e.call("POST", fmt.Sprintf("/api/figures/%d/share", f.ID), map[string]any{"hours": 24}); c != 201 {
		t.Errorf("a label on a dataset outside the field gated the figure: %d %s", c, b)
	}
	// a view that shows no outline is refused
	n := countFigures(t, e)
	code, b = e.call("POST", "/api/figures/sky", skyBody(png, map[string]any{"footprints": fps, "centre": map[string]any{"lon": 20.0, "lat": -40.0}, "fovDeg": 1.5}))
	if code != 422 || countFigures(t, e) != n {
		t.Errorf("an empty field: %d %s", code, b)
	}
}
