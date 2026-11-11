package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

// planeScene is the scene/1 the web client sends with a plane figure of channel 3 in viridis, linear, percentile.
func planeScene(id int64) map[string]any {
	return map[string]any{
		"schema":   "scene/1",
		"base":     map[string]any{"datasetId": id, "channel": 3, "display": map[string]any{"mapping": "scalar", "colormap": "viridis", "stretch": "linear", "range": "percentile"}},
		"overlays": []any{map[string]any{"id": "regions", "kind": "regions", "datasetId": id, "visible": true, "saved": []any{}}},
		"view":     map[string]any{"zoom": 2, "centre": map[string]any{"x": 4, "y": 4.5}},
	}
}

// A figure's scene is held to the scene/1 contract and to the figure it describes. A refused scene saves nothing.
func TestAFigureSceneIsValidatedAgainstTheContractAndTheFigure(t *testing.T) {
	base := t.TempDir()
	rs, _ := sourceroot.New([]string{base})
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	bs, err := boards.Open(cat.DB(), base)
	if err != nil {
		t.Fatal(err)
	}
	owner := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: hdrWorker{}, Boards: bs}).Handler())
	defer owner.Close()
	file := filepath.Join(base, "cube.fits")
	os.WriteFile(file, []byte("x"), 0o644)
	var d, other catalog.Dataset
	json.NewDecoder(post(t, owner, file, nil).Body).Decode(&d)
	otherFile := filepath.Join(base, "co.fits")
	os.WriteFile(otherFile, []byte("y"), 0o644)
	json.NewDecoder(post(t, owner, otherFile, nil).Body).Decode(&other)
	view := map[string]any{"kind": "plane", "channel": 3, "colormap": "viridis", "stretch": "linear", "range": "percentile"}
	fig := func(sc any, v map[string]any) map[string]any {
		return map[string]any{"datasetId": d.ID, "title": "t", "view": v, "previewPng": testPNG(t), "scene": sc}
	}
	with := func(edit func(s map[string]any)) map[string]any {
		s := planeScene(d.ID)
		edit(s)
		return s
	}
	ov := func(s map[string]any) []any { return s["overlays"].([]any) }
	bse := func(s map[string]any) map[string]any { return s["base"].(map[string]any) }
	many := make([]any, 17)
	for i := range many {
		many[i] = map[string]any{"id": "r" + itoa(i), "kind": "regions", "datasetId": d.ID, "visible": true, "saved": []any{}}
	}
	for name, body := range map[string]map[string]any{
		"unknown overlay kind": fig(with(func(s map[string]any) {
			s["overlays"] = append(ov(s), map[string]any{"id": "x", "kind": "heatmap", "datasetId": d.ID, "visible": true})
		}), view),
		"a catalog overlay naming columns itself (the declaration owns them)": fig(with(func(s map[string]any) {
			s["overlays"] = append(ov(s), map[string]any{"id": "c", "kind": "catalog", "datasetId": d.ID, "visible": true, "columns": map[string]any{"lon": "RA", "lat": "DEC"}})
		}), view),
		"contours from a dataset that is not registered": fig(with(func(s map[string]any) {
			s["overlays"] = append(ov(s), map[string]any{"id": "c", "kind": "contours", "datasetId": d.ID + 50, "visible": true, "channel": 0, "levels": map[string]any{"mode": "list", "values": []any{1}}, "unit": "K"})
		}), view),
		"orientation lines from a map not declared axial": fig(with(func(s map[string]any) {
			s["overlays"] = append(ov(s), map[string]any{"id": "o", "kind": "orientation", "datasetId": other.ID, "visible": true, "sampling": map[string]any{"mode": "step", "step": 2}, "length": "fixed", "opacity": "uniform"})
		}), view),
		"a grid of another dataset": fig(with(func(s map[string]any) {
			s["overlays"] = append(ov(s), map[string]any{"id": "g", "kind": "grid", "datasetId": other.ID, "visible": true, "frame": "icrs"})
		}), view),
		"17 overlays":                      fig(with(func(s map[string]any) { s["overlays"] = many }), view),
		"null zoom (NaN from a client)":    fig(with(func(s map[string]any) { s["view"] = map[string]any{"zoom": nil} }), view),
		"another dataset as the base":      fig(with(func(s map[string]any) { bse(s)["datasetId"] = d.ID + 1 }), view),
		"an overlay from another file":     fig(with(func(s map[string]any) { ov(s)[0].(map[string]any)["datasetId"] = d.ID + 1 }), view),
		"a channel the view does not show": fig(with(func(s map[string]any) { bse(s)["channel"] = 2 }), view),
		"a colormap the view does not use": fig(with(func(s map[string]any) { bse(s)["display"].(map[string]any)["colormap"] = "gray" }), view),
		"an axial mapping on a scalar map": fig(with(func(s map[string]any) { bse(s)["display"].(map[string]any)["mapping"] = "axial" }), view),
		"a centre outside the image": fig(with(func(s map[string]any) {
			s["view"] = map[string]any{"zoom": 2, "centre": map[string]any{"x": 9, "y": 1}}
		}), view),
		"a scene on a spectrum figure": fig(planeScene(d.ID), map[string]any{"kind": "spectrum", "channel": 3}),
		"a region the view does not have": fig(with(func(s map[string]any) {
			ov(s)[0].(map[string]any)["active"] = map[string]any{"shape": "circle", "cx": 1, "cy": 1, "r": 1}
		}), view),
		"orientation lines on a map not declared axial": fig(with(func(s map[string]any) {
			s["overlays"] = append(ov(s), map[string]any{"id": "o", "kind": "orientation", "datasetId": d.ID, "visible": true, "sampling": map[string]any{"mode": "step", "step": 2}, "length": "fixed", "opacity": "uniform"})
		}), view),
	} {
		if r, out := postJSON(t, owner.URL+"/api/figures", body); r.StatusCode != 400 {
			t.Errorf("%s: %d %s, want 400", name, r.StatusCode, out)
		}
	}
	// a NaN token is not JSON: the whole request is refused before the scene is read
	id := itoa(int(d.ID))
	raw := `{"datasetId":` + id + `,"title":"t","view":{"kind":"plane","channel":3},"previewPng":"` + testPNG(t) + `","scene":{"schema":"scene/1","base":{"datasetId":` + id + `,"channel":3,"display":{"mapping":"scalar","colormap":"viridis","stretch":"linear","range":"percentile"}},"overlays":[],"view":{"zoom":NaN}}}`
	if r, err := http.Post(owner.URL+"/api/figures", "application/json", strings.NewReader(raw)); err != nil || r.StatusCode != 400 {
		t.Errorf("a NaN token: %v %v, want 400", r, err)
	}
	if fs, _ := bs.Figures(); len(fs) != 0 {
		t.Fatalf("a refused scene saved a figure: %d", len(fs))
	}

	// a matching scene is stored as the server read it; the same picture and view without a scene is a different
	// figure, and a figure without a scene has none
	region := map[string]any{"shape": "rect", "x0": 1, "y0": 1, "x1": 3, "y1": 2}
	r, out := postJSON(t, owner.URL+"/api/figures", fig(with(func(s map[string]any) { ov(s)[0].(map[string]any)["active"] = region }),
		map[string]any{"kind": "plane", "channel": 3, "colormap": "viridis", "stretch": "linear", "range": "percentile", "region": region}))
	if r.StatusCode != 201 {
		t.Fatalf("a matching scene: %d %s", r.StatusCode, out)
	}
	var f boards.Figure
	json.Unmarshal(out, &f)
	if !strings.Contains(string(f.Scene), `"active":{"shape":"rect","x0":1,"y0":1,"x1":3,"y1":2}`) {
		t.Fatalf("scene not kept: %s", f.Scene)
	}
	if got, _ := bs.Figure(f.ID); string(got.Scene) != string(f.Scene) {
		t.Fatalf("scene not read back: %s", got.Scene)
	}
	r, out = postJSON(t, owner.URL+"/api/figures", map[string]any{"datasetId": d.ID, "title": "t", "view": view, "previewPng": testPNG(t)})
	var plain boards.Figure
	json.Unmarshal(out, &plain)
	if r.StatusCode != 201 || plain.Scene != nil || strings.Contains(string(out), `"scene"`) {
		t.Fatalf("a figure without a scene: %d %s", r.StatusCode, out)
	}
	// contours and a beam from another registered file, and the base's own grid, are kept as read (every setting), and the
	// provenance names the other file by name, never by id
	layered := with(func(s map[string]any) {
		s["overlays"] = append(ov(s),
			map[string]any{"id": "co", "kind": "contours", "datasetId": other.ID, "visible": true, "channel": 0, "levels": map[string]any{"mode": "sigma", "multiples": []any{3, 5}, "sigma": 0.17}, "unit": "K", "color": "amber", "width": 1.5},
			map[string]any{"id": "beam", "kind": "beam", "datasetId": other.ID, "visible": true, "corner": "top-left"},
			map[string]any{"id": "grid", "kind": "grid", "datasetId": d.ID, "visible": true, "frame": "fk5", "dash": "dashed"})
	})
	r, out = postJSON(t, owner.URL+"/api/figures", fig(layered, view))
	var lf boards.Figure
	json.Unmarshal(out, &lf)
	if r.StatusCode != 201 {
		t.Fatalf("overlays from another file: %d %s", r.StatusCode, out)
	}
	for _, want := range []string{`"levels":{"mode":"sigma","multiples":[3,5],"sigma":0.17}`, `"color":"amber"`, `"width":1.5`, `"corner":"top-left"`, `"frame":"fk5"`, `"dash":"dashed"`} {
		if !strings.Contains(string(lf.Scene), want) {
			t.Errorf("stored scene lost %s: %s", want, lf.Scene)
		}
	}
	if !strings.Contains(string(out), "Overlay drawn from another file, co.fits") || strings.Contains(string(lf.Provenance), `"datasetId"`) {
		t.Errorf("provenance of the overlay file: %s", lf.Provenance)
	}
	r, out = postJSON(t, owner.URL+"/api/figures", fig(planeScene(d.ID), view))
	var withScene boards.Figure
	json.Unmarshal(out, &withScene)
	if r.StatusCode != 201 || withScene.Revision == plain.Revision {
		t.Fatalf("the scene is part of the evidence: %d %s", r.StatusCode, out)
	}
}
