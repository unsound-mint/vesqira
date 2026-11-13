package server

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/access"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
)

func skyEnv(t *testing.T) *subEnv {
	return newSubEnv(t, nil, func(s *Server, st *catalog.Store) {
		a, err := access.Open(st.DB())
		if err != nil {
			t.Fatal(err)
		}
		s.Access = a
	})
}

func skyBody(png string, over map[string]any) map[string]any {
	m := map[string]any{"title": "Fields on the sky", "previewPng": png, "frame": "icrs", "centre": map[string]any{"lon": 150.0, "lat": 2.0}, "fovDeg": 1.5,
		"footprints": []map[string]any{}}
	for k, v := range over {
		m[k] = v
	}
	return m
}

// The sky panel's view saved as a figure (ADR 0072). The server reads every footprint itself, through the panel's own route; the
// reviewer-safe provenance carries the survey and each outline by file name and WCS basis and no id; the survey is context and the
// words say so; and the labels of the datasets drawn apply to publishing it, also when a dataset is drawn only as a window of a subset.
func TestSkyViewFigureRecordsWhatWasDrawnAndStaysContext(t *testing.T) {
	t.Parallel()
	e := skyEnv(t)
	src := e.register("hi_cube.fits")
	other := e.register("hi_cube_rotated.fits")
	sub := e.mustCreate(src.ID, boxDef)
	png := testPNG(t)
	post := func(over map[string]any) (int, []byte) { return e.call("POST", "/api/figures/sky", skyBody(png, over)) }
	fps := []map[string]any{{"kind": "dataset", "id": other.ID}, {"kind": "subset", "id": sub.ID}}

	code, b := post(map[string]any{"footprints": fps, "survey": "CDS/P/DSS2/color"})
	if code != 201 {
		t.Fatalf("save: %d %s", code, b)
	}
	var f boards.Figure
	json.Unmarshal(b, &f)
	if f.Kind != boards.KindSky || f.DatasetID != nil {
		t.Fatalf("kind %q dataset %v", f.Kind, f.DatasetID)
	}
	// the owner-only description names the datasets (the subset's source, with the subset and its version) and the survey, as the server wrote them
	var spec skyViewSpec
	if err := json.Unmarshal(f.Scene, &spec); err != nil || spec.Schema != SkyViewSchema || len(spec.Footprints) != 2 {
		t.Fatalf("description: %s", f.Scene)
	}
	if spec.Footprints[0].DatasetID != other.ID || spec.Footprints[1].DatasetID != src.ID || spec.Footprints[1].SubsetID != sub.ID || spec.Footprints[1].SubsetVersion != 1 {
		t.Errorf("footprints: %+v", spec.Footprints)
	}
	for _, fp := range spec.Footprints {
		if fp.FromBasis == "" || fp.FromStatus == "" || fp.ExtentDeg <= 0 {
			t.Errorf("a footprint without its WCS basis or extent: %+v", fp)
		}
	}
	if spec.Survey == nil || spec.Survey.ID != "CDS/P/DSS2/color" || spec.Survey.Provider != "CDS, Strasbourg" || spec.Survey.Role != "context" || spec.Survey.Credit == "" {
		t.Errorf("survey: %+v", spec.Survey)
	}
	// the reviewer-safe provenance: names and bases, the survey as context, no id of a dataset or subset
	prov := string(f.Provenance)
	for _, want := range []string{`"kind":"sky"`, "hi_cube.fits", "hi_cube_rotated.fits", `"window":true`, "CDS/P/DSS2/color", "context only", "never sampled into a number", "no file"} {
		if !strings.Contains(prov, want) {
			t.Errorf("provenance lacks %q: %s", want, prov)
		}
	}
	for _, banned := range []string{"datasetId", "subsetId", "footprints\":[{\"datasetId", fmt.Sprintf(`"id":%d`, src.ID)} {
		if strings.Contains(prov, banned) {
			t.Errorf("provenance holds %q: %s", banned, prov)
		}
	}
	var pv struct {
		Sky *struct {
			Background *struct{ Role string }
			Footprints []struct{ Name string }
		}
	}
	json.Unmarshal(f.Provenance, &pv)
	if pv.Sky == nil || pv.Sky.Background == nil || pv.Sky.Background.Role != "context" || len(pv.Sky.Footprints) != 2 {
		t.Errorf("sky in provenance: %s", prov)
	}

	// without the background the picture is the plain grid and the provenance says so
	code, b = post(map[string]any{"footprints": fps[:1]})
	if code != 201 {
		t.Fatalf("plain: %d %s", code, b)
	}
	var plain boards.Figure
	json.Unmarshal(b, &plain)
	if !strings.Contains(string(plain.Provenance), "No survey image") || strings.Contains(string(plain.Provenance), "HiPS") || !strings.Contains(string(plain.Scene), `"survey":null`) {
		t.Errorf("plain view: %s / %s", plain.Provenance, plain.Scene)
	}

	// refusals leave no figure
	n := countFigures(t, e)
	for label, over := range map[string]map[string]any{
		"a survey that is not offered": {"footprints": fps, "survey": "CDS/P/Mystery"},
		"no footprints":                {"footprints": []map[string]any{}},
		"a dataset twice":              {"footprints": []map[string]any{fps[0], fps[0]}},
		"an unknown dataset":           {"footprints": []map[string]any{{"kind": "dataset", "id": 9999}}},
		"an unknown subset":            {"footprints": []map[string]any{{"kind": "subset", "id": 9999}}},
		"a bad frame":                  {"footprints": fps, "frame": "fk5"},
		"a centre off the sphere":      {"footprints": fps, "centre": map[string]any{"lon": 10.0, "lat": 95.0}},
		"no field":                     {"footprints": fps, "fovDeg": 0},
		"a server-written field":       {"footprints": fps, "surveyName": "x"},
	} {
		if code, b := post(over); code < 400 {
			t.Errorf("%s was accepted: %d %s", label, code, b)
		}
	}
	if code, b := e.call("POST", "/api/figures/sky", skyBody("bm90IGEgcG5n", map[string]any{"footprints": fps})); code < 400 {
		t.Errorf("a picture that is not a PNG: %d %s", code, b)
	}
	if countFigures(t, e) != n {
		t.Error("a refused save left a figure")
	}
	// a dataset with no celestial WCS has no outline to record
	flat := e.register("vgt_psi_deg.fits")
	_ = flat
	// a sky view is not drawn again from the catalog
	_, rb := e.get(fmt.Sprintf("/api/figures/%d/redraw-check", f.ID))
	if !strings.Contains(string(rb), `"kind":"sky"`) || !strings.Contains(string(rb), `"state":"none"`) {
		t.Errorf("redraw-check: %s", rb)
	}
	// the view cannot be sent as a dataset figure's view
	if code, _ := e.call("POST", "/api/figures", map[string]any{"datasetId": src.ID, "title": "x", "previewPng": png, "view": map[string]any{"kind": "sky"}}); code < 400 {
		t.Errorf("a dataset figure with a sky view was accepted: %d", code)
	}
}

func countFigures(t *testing.T, e *subEnv) int {
	t.Helper()
	_, b := e.get("/api/figures")
	var l []json.RawMessage
	if json.Unmarshal(b, &l) != nil {
		var o struct{ Figures []json.RawMessage }
		json.Unmarshal(b, &o)
		return len(o.Figures)
	}
	return len(l)
}

// A label on a dataset that is only drawn as an outline still governs publishing the figure, as a dataset and as a window of a subset.
func TestSkyViewFigureIsGatedByTheLabelsOfTheDatasetsDrawn(t *testing.T) {
	t.Parallel()
	e := skyEnv(t)
	src := e.register("hi_cube.fits")
	other := e.register("hi_cube_rotated.fits")
	sub := e.mustCreate(src.ID, boxDef)
	png := testPNG(t)
	save := func(fps ...map[string]any) boards.Figure {
		code, b := e.call("POST", "/api/figures/sky", skyBody(png, map[string]any{"footprints": fps, "title": fmt.Sprintf("view %d", len(fps))}))
		if code != 201 {
			t.Fatalf("save: %d %s", code, b)
		}
		var f boards.Figure
		json.Unmarshal(b, &f)
		return f
	}
	viaSubset := save(map[string]any{"kind": "subset", "id": sub.ID}, map[string]any{"kind": "dataset", "id": other.ID})
	plain := save(map[string]any{"kind": "dataset", "id": other.ID})

	share := func(id int64, confirm bool) (int, string) {
		c, b := e.call("POST", fmt.Sprintf("/api/figures/%d/share", id), map[string]any{"hours": 24, "confirmAccess": confirm})
		return c, string(b)
	}
	if c, b := share(viaSubset.ID, false); c != 201 {
		t.Fatalf("an unlabelled view shares: %d %s", c, b)
	}
	// only the subset's source is labelled; the figure draws it as a window and never as a dataset of its own
	if c, b := e.call("POST", fmt.Sprintf("/api/datasets/%d/access", src.ID), map[string]any{"label": "unreleased", "reason": "not approved"}); c != 200 {
		t.Fatalf("label: %d %s", c, b)
	}
	if c, b := share(viaSubset.ID, false); c != 409 || !strings.Contains(b, "access_confirmation_required") || !strings.Contains(b, "hi_cube.fits") {
		t.Errorf("sharing a view that draws a labelled subset: %d %s", c, b)
	}
	if c, b := share(plain.ID, false); c != 201 {
		t.Errorf("a view that draws only an unlabelled dataset shares: %d %s", c, b)
	}
	if c, b := share(viaSubset.ID, true); c != 201 || !strings.Contains(b, "unreleased") {
		t.Errorf("confirmed: %d %s", c, b)
	}
	// on a board too, and the figure list states the label
	_, bb := e.call("POST", "/api/boards", map[string]any{"title": "Fields"})
	var board boards.Board
	json.Unmarshal(bb, &board)
	e.call("POST", fmt.Sprintf("/api/boards/%d/items", board.ID), map[string]any{"figureId": viaSubset.ID})
	if c, b := e.call("POST", fmt.Sprintf("/api/boards/%d/publish", board.ID), map[string]any{"label": "v1", "hours": 24}); c != 409 || !strings.Contains(string(b), "access_confirmation_required") {
		t.Errorf("publishing a board with that view: %d %s", c, b)
	}
	_, lb := e.get("/api/figures")
	if !regexp.MustCompile(`"access"`).Match(lb) {
		t.Errorf("the figure list does not state the label: %s", lb)
	}
	// deleting a drawn dataset lists the view among what is made from it, and the view stays
	_, ib := e.get(fmt.Sprintf("/api/datasets/%d/impact", src.ID))
	if !strings.Contains(string(ib), "view 2") {
		t.Errorf("impact does not list the sky view: %s", ib)
	}
}

// The surveys the server will name are the ones the panel offers, word for word.
func TestSkySurveyListMatchesThePanel(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../../../apps/web/src/sky/surveys.ts")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`\{ id: "([^"]+)", name: "([^"]+)", provider: CDS, host: HOST, url: "[^"]+", credit: "([^"]+)" \}`)
	found := re.FindAllStringSubmatch(string(raw), -1)
	if len(found) != len(skySurveys) {
		t.Fatalf("surveys.ts lists %d, the server %d", len(found), len(skySurveys))
	}
	for i, m := range found {
		if skySurveys[i].ID != m[1] || skySurveys[i].Name != m[2] || skySurveys[i].Credit != m[3] {
			t.Errorf("survey %d differs: web %q %q %q, server %+v", i, m[1], m[2], m[3], skySurveys[i])
		}
	}
	if !strings.Contains(string(raw), `const CDS = "`+skySurveyProvider+`"`) {
		t.Error("the provider named by the server is not the panel's")
	}
}
