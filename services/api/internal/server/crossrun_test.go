package server

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/review"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

const (
	canaryName  = "CANARY-run-name-7731"
	canaryValue = 0.123456789
	crossDef    = "Axial agreement with Planck"
)

type crossEnv struct {
	t     *testing.T
	owner *httptest.Server
	rv    *httptest.Server
	runs  *runs.Store
	ids   map[string]int64
}

// newCrossEnv serves the owner API with the real worker (the conversion is verified through it) and, apart, the reviewer's listener.
func newCrossEnv(t *testing.T) *crossEnv {
	t.Helper()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	py, _ := exec.LookPath("python3")
	base := t.TempDir()
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	bs, err := boards.Open(cat.DB(), base)
	if err != nil {
		t.Fatal(err)
	}
	rn, err := runs.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	au, err := audit.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	pw := &worker.Persistent{Command: []string{py, "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	owner := httptest.NewServer((&Server{Store: cat, Boards: bs, Runs: rn, Planes: pw, Audit: au, ManagedDir: filepath.Join(base, "managed")}).Handler())
	t.Cleanup(owner.Close)
	rv := httptest.NewServer((&review.Handler{Store: bs}).Handler())
	t.Cleanup(rv.Close)
	e := &crossEnv{t: t, owner: owner, rv: rv, runs: rn, ids: map[string]int64{}}
	m := func(unit, def string, v float64) []runs.Metric {
		return []runs.Metric{{Key: "am", Value: f64(v), Unit: unit, Uncertainty: f64(0.01), UncertaintyKind: "standard_error", Definition: def, Status: "declared"}}
	}
	for _, c := range []struct {
		name    string
		block   string
		metrics []runs.Metric
	}{
		{canaryName, "8", m("rad", crossDef, canaryValue)},
		{"run-b", "16", m("rad", crossDef, 0.6)},
		{"run-deg", "32", m("deg", crossDef, 30)},
		{"run-other-def", "64", m("deg", "Something else entirely", 40)},
		{"run-no-am", "128", nil},
	} {
		p := runs.Parsed{Run: runs.Run{Name: c.name, Kind: "vgt", GeneratedAt: "2026-08-01T10:00:00Z", GitCommit: "abc1234",
			Params: []runs.Param{{Key: "block_size", Value: c.block, Source: "config"}}, Metrics: c.metrics}}
		saved, err := rn.Save("/runs/"+c.name, p, nil)
		if err != nil {
			t.Fatal(err)
		}
		e.ids[c.name] = saved.ID
	}
	return e
}

func (e *crossEnv) request(over map[string]any) (int, []byte) {
	e.t.Helper()
	req := map[string]any{"title": "am across the sweep", "caption": "", "previewPng": testPNG(e.t), "query": "block_size:8..64", "y": "am", "x": "block_size", "xKind": "param", "by": "",
		"runIds": []int64{e.ids[canaryName], e.ids["run-b"], e.ids["run-deg"]}}
	for k, v := range over {
		if v == nil {
			delete(req, k)
		} else {
			req[k] = v
		}
	}
	r, b := postJSON(e.t, e.owner.URL+"/api/figures/runs", req)
	return r.StatusCode, b
}

func (e *crossEnv) conversion() map[string]any {
	return map[string]any{"to": "rad", "entries": []map[string]any{{"runId": e.ids["run-deg"], "factor": math.Pi / 180}}}
}

// A cross-run plot is a figure of its own kind: no dataset, a description of the runs written from the catalog's own rows with the
// recorded values beside the converted ones, and a reviewer-safe provenance that holds the words and counts and no run.
func TestCrossRunFigureHasNoDatasetAndKeepsWhatTheRunsRecorded(t *testing.T) {
	e := newCrossEnv(t)
	code, b := e.request(map[string]any{"conversion": e.conversion()})
	if code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	conforms(t, responseSchemaAt(t, loadSpec(t), "/api/figures/runs", "POST", 201, "application/json"), "created", b)
	var f struct {
		ID         int64           `json:"id"`
		DatasetID  *int64          `json:"datasetId"`
		Kind       string          `json:"kind"`
		Scene      json.RawMessage `json:"scene"`
		Provenance json.RawMessage `json:"provenance"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.Kind != "crossrun" || f.DatasetID != nil {
		t.Fatalf("kind %q dataset %v", f.Kind, f.DatasetID)
	}
	var sc crossRunSpec
	if err := json.Unmarshal(f.Scene, &sc); err != nil || sc.Schema != CrossRunSchema || len(sc.Runs) != 3 {
		t.Fatalf("%v %s", err, f.Scene)
	}
	for _, m := range sc.Runs {
		if m.Name == "run-deg" {
			if m.Y.Value != 30 || m.Y.Unit != "deg" || m.Converted == nil || math.Abs(m.Converted.Value-math.Pi/6) > 1e-12 {
				t.Errorf("the recorded 30 deg must stay beside the converted value: %+v", m)
			}
			if m.Converted.Uncertainty == nil || math.Abs(*m.Converted.Uncertainty-0.01*math.Pi/180) > 1e-12 {
				t.Errorf("uncertainty converted by the same factor: %+v", m.Converted)
			}
		} else if m.Converted != nil {
			t.Errorf("%s was not converted: %+v", m.Name, m.Converted)
		}
	}
	if sc.Conversion == nil || sc.Conversion.To != "rad" || len(sc.Conversion.From) != 1 || sc.Conversion.From[0].Unit != "deg" || sc.Conversion.From[0].Count != 1 {
		t.Errorf("conversion record: %+v", sc.Conversion)
	}
	// the reviewer-safe provenance has the words and the counts and nothing that names a run
	var pr map[string]any
	_ = json.Unmarshal(f.Provenance, &pr)
	rp, _ := pr["runPlot"].(map[string]any)
	if rp == nil || rp["runs"] != float64(3) || rp["y"] != "am" || rp["query"] != "block_size:8..64" || rp["conversion"] == nil {
		t.Errorf("runPlot: %v", pr["runPlot"])
	}
	for _, leak := range []string{canaryName, "run-deg", "0.1234", "datasetId", `"id"`} {
		if strings.Contains(string(f.Provenance), leak) {
			t.Errorf("the provenance, which a reviewer receives, contains %q: %s", leak, f.Provenance)
		}
	}
	// it is listed with the other figures, and its redraw check does not pretend it is a file
	_, lb := get(t, e.owner.URL+"/api/figures")
	if !strings.Contains(string(lb), `"kind":"crossrun"`) {
		t.Errorf("the figure list: %s", lb)
	}
	_, cb := get(t, e.owner.URL+"/api/figures/"+itoa(int(f.ID))+"/redraw-check")
	if !strings.Contains(string(cb), `"kind":"cross-run"`) || !strings.Contains(string(cb), `"state":"ready"`) {
		t.Errorf("redraw check: %s", cb)
	}
}

func TestCrossRunFigureRefusesWhatTheCatalogDoesNotHold(t *testing.T) {
	e := newCrossEnv(t)
	ok := []int64{e.ids[canaryName], e.ids["run-b"], e.ids["run-deg"]}
	cases := map[string]struct {
		over map[string]any
		code int
	}{
		"a run that does not exist":         {map[string]any{"runIds": []int64{e.ids[canaryName], 99999}}, 400},
		"a run named twice":                 {map[string]any{"runIds": []int64{e.ids[canaryName], e.ids[canaryName]}}, 400},
		"a run that did not record y":       {map[string]any{"runIds": []int64{e.ids[canaryName], e.ids["run-no-am"]}}, 400},
		"an x that the runs did not record": {map[string]any{"x": "nonexistent"}, 400},
		"no runs":                           {map[string]any{"runIds": []int64{}}, 400},
		"an x kind that is not one":         {map[string]any{"xKind": "other"}, 400},
		"a picture that is not a PNG":       {map[string]any{"previewPng": "bm90IGEgcG5n"}, 422},
		"a wrong factor":                    {map[string]any{"conversion": map[string]any{"to": "rad", "entries": []map[string]any{{"runId": e.ids["run-deg"], "factor": 0.02}}}}, 409},
		"a run defined differently":         {map[string]any{"runIds": append(append([]int64{}, ok...), e.ids["run-other-def"]), "conversion": map[string]any{"to": "rad", "entries": []map[string]any{{"runId": e.ids["run-other-def"], "factor": math.Pi / 180}}}}, 400},
		"a run already in the target unit":  {map[string]any{"conversion": map[string]any{"to": "rad", "entries": []map[string]any{{"runId": e.ids["run-b"], "factor": 1}}}}, 400},
		"a conversion the worker refuses":   {map[string]any{"conversion": map[string]any{"to": "Jy/beam", "entries": []map[string]any{{"runId": e.ids["run-deg"], "factor": 1}}}}, 400},
		"an unknown field":                  {map[string]any{"datasetId": 1}, 400},
	}
	for label, c := range cases {
		if code, b := e.request(c.over); code != c.code {
			t.Errorf("%s: %d %s", label, code, b)
		}
	}
	// a redrawing must come from a cross-run plot
	if code, b := e.request(map[string]any{"redrawnFrom": 99999}); code != 400 {
		t.Errorf("redrawnFrom a figure that does not exist: %d %s", code, b)
	}
	_, lb := get(t, e.owner.URL+"/api/figures")
	if strings.Contains(string(lb), `"crossrun"`) {
		t.Errorf("a refused request left a figure behind: %s", lb)
	}
}

// A redrawing is a figure of its own, linked to the original and unable to be linked to anything else; the original is not touched.
func TestCrossRunRedrawIsANewLinkedFigure(t *testing.T) {
	e := newCrossEnv(t)
	code, b := e.request(nil)
	if code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	var first boards.Figure
	_ = json.Unmarshal(b, &first)
	code, b = e.request(map[string]any{"redrawnFrom": first.ID, "runIds": []int64{e.ids[canaryName], e.ids["run-b"]}})
	if code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	var second boards.Figure
	_ = json.Unmarshal(b, &second)
	if second.ID == first.ID || second.Revision == first.Revision {
		t.Errorf("a redrawing is a figure of its own: %d %d", first.ID, second.ID)
	}
	if !strings.Contains(string(b), `"redrawnFrom":{"figureId":`+itoa(int(first.ID))) {
		t.Errorf("the board card says which figure a redrawing redraws, as for any figure: %s", b)
	}
	if again, _ := e.request(nil); again != 201 {
		t.Errorf("saving the first evidence again: %d", again)
	}
}

// The reviewer's listener holds the boards store and nothing else: a published cross-run plot is its frozen picture and its words.
// No run name, value or id, no description of the runs and no owner route is reachable, by the wire.
func TestReviewerSeesOnlyTheFrozenPictureOfACrossRunPlot(t *testing.T) {
	e := newCrossEnv(t)
	code, b := e.request(map[string]any{"conversion": e.conversion()})
	if code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	var f boards.Figure
	_ = json.Unmarshal(b, &f)
	// positive control: the owner's own view of the figure does hold the run data the reviewer must not get
	if !strings.Contains(string(b), canaryName) {
		t.Fatal("the owner's figure should describe its runs")
	}
	_, bb := postJSON(t, e.owner.URL+"/api/boards", map[string]any{"title": "Sweep"})
	var bd boards.Board
	_ = json.Unmarshal(bb, &bd)
	if r, out := postJSON(t, e.owner.URL+"/api/boards/"+itoa(int(bd.ID))+"/items", map[string]any{"figureId": f.ID}); r.StatusCode != 200 && r.StatusCode != 201 {
		t.Fatalf("add to board: %d %s", r.StatusCode, out)
	}
	r, pb := postJSON(t, e.owner.URL+"/api/boards/"+itoa(int(bd.ID))+"/publish", map[string]any{"label": "v1", "hours": 24})
	if r.StatusCode != 201 {
		t.Fatalf("publish: %d %s", r.StatusCode, pb)
	}
	var pub struct{ Token string }
	_ = json.Unmarshal(pb, &pub)
	if strings.Contains(string(pb), canaryName) {
		t.Errorf("the publish reply names a run: %s", pb)
	}
	rr, body := get(t, e.rv.URL+"/review-api/"+pub.Token)
	if rr.StatusCode != 200 {
		t.Fatalf("review: %d %s", rr.StatusCode, body)
	}
	for _, leak := range []string{canaryName, "run-deg", "run-b", "0.1234", `"scene"`, `"datasetId"`, `"runIds"`, `"identity":{`, "block_size\":\"8"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the reviewer's document contains %q: %s", leak, body)
		}
	}
	var rev struct {
		Snapshot struct {
			Items []struct {
				PreviewSHA string `json:"previewSha"`
				Provenance struct {
					RunPlot struct {
						Runs int `json:"runs"`
					} `json:"runPlot"`
				} `json:"provenance"`
			} `json:"items"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal(body, &rev); err != nil || len(rev.Snapshot.Items) != 1 || rev.Snapshot.Items[0].Provenance.RunPlot.Runs != 3 {
		t.Fatalf("%v %s", err, body)
	}
	// the frozen picture is served; no run, no figure list and no owner route is
	if pr, _ := get(t, e.rv.URL+"/review-api/"+pub.Token+"/preview/"+rev.Snapshot.Items[0].PreviewSHA); pr.StatusCode != 200 {
		t.Errorf("preview: %d", pr.StatusCode)
	}
	for _, path := range []string{"/api/runs", "/api/runs/" + itoa(int(e.ids[canaryName])), "/api/figures", "/api/figures/" + itoa(int(f.ID)), "/api/run-views"} {
		if pr, _ := get(t, e.rv.URL+path); pr.StatusCode == http.StatusOK {
			t.Errorf("the reviewer's listener serves %s", path)
		}
	}
	if pr, _ := postJSON(t, e.rv.URL+"/api/runs/convert-values", map[string]any{"to": "deg", "values": []map[string]any{{"value": 1, "from": "rad"}}}); pr.StatusCode == http.StatusOK {
		t.Error("the reviewer's listener serves the unit conversion")
	}
}

// Deleting or archiving a run says which saved cross-run plots drew it; those plots stay, with their picture and what they recorded.
func TestRunImpactNamesTheCrossRunPlotsThatDrewIt(t *testing.T) {
	e := newCrossEnv(t)
	code, b := e.request(nil)
	if code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	var f boards.Figure
	_ = json.Unmarshal(b, &f)
	id := e.ids[canaryName]
	_, ib := get(t, e.owner.URL+"/api/runs/"+itoa(int(id))+"/impact")
	var im impactDoc
	if err := json.Unmarshal(ib, &im); err != nil {
		t.Fatal(err)
	}
	if effect, n := im.group("crossrun_figures"); n != 1 || effect != "stays" {
		t.Fatalf("impact of a run in a cross-run plot: %s", ib)
	}
	if effect, n := im.group("figures"); n != 1 || effect != "stays" {
		t.Errorf("the plot is pinned evidence that stays: %s", ib)
	}
	// a run that is in no plot has no such group
	_, ob := get(t, e.owner.URL+"/api/runs/"+itoa(int(e.ids["run-other-def"]))+"/impact")
	var other impactDoc
	_ = json.Unmarshal(ob, &other)
	if _, n := other.group("crossrun_figures"); n != 0 {
		t.Errorf("run-other-def is in no plot: %s", ob)
	}
	// deleting the run leaves the figure
	r, db := postJSON(t, e.owner.URL+"/api/runs/"+itoa(int(id))+"/delete", map[string]any{"impactDigest": im.Digest})
	if r.StatusCode != 200 {
		t.Fatalf("delete: %d %s", r.StatusCode, db)
	}
	_, lb := get(t, e.owner.URL+"/api/figures")
	if !strings.Contains(string(lb), canaryName) { // the owner-side description still names what it drew
		t.Errorf("the figure must stay after the run is deleted: %s", lb)
	}
	// and a dataset's own impact queries are not confused by a figure with no dataset
	cat := e.owner.URL + "/api/datasets"
	if r, _ := get(t, cat); r.StatusCode != 200 {
		t.Errorf("datasets: %d", r.StatusCode)
	}
}
