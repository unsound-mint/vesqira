package server

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// The unit conversion of the cross-run plot is the real worker (astropy) behind the real route: values and uncertainties are
// scaled by the dimensional factor, a conversion that would need an equivalency is a refusal with a sentence (never a number),
// and malformed requests are 400s. The worker is the oracle's subject here; the factors are closed-form.
func TestRunValueConversionRoute(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	py, _ := exec.LookPath("python3")
	doc := loadSpec(t)
	cat, err := catalog.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	rn, _ := runs.Open(cat.DB())
	pw := &worker.Persistent{Command: []string{py, "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	ts := httptest.NewServer((&Server{Store: cat, Runs: rn, Planes: pw}).Handler())
	defer ts.Close()
	url := ts.URL + "/api/runs/convert-values"
	check := func(status int, label string, b []byte) {
		t.Helper()
		conforms(t, responseSchemaAt(t, doc, "/api/runs/convert-values", "POST", status, "application/json"), label, b)
	}
	e := 0.01
	r, b := postJSON(t, url, map[string]any{"to": "deg", "values": []map[string]any{{"value": math.Pi / 2, "uncertainty": e, "from": "rad"}, {"value": 30, "from": "deg"}}})
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	check(200, "converted", b)
	var ok struct {
		Refused bool
		Results []struct {
			Value       float64
			Uncertainty *float64
		}
	}
	if err := json.Unmarshal(b, &ok); err != nil || ok.Refused || len(ok.Results) != 2 {
		t.Fatalf("%v %s", err, b)
	}
	if math.Abs(ok.Results[0].Value-90) > 1e-9 || ok.Results[0].Uncertainty == nil || math.Abs(*ok.Results[0].Uncertainty-e*180/math.Pi) > 1e-9 {
		t.Errorf("rad to deg: %+v", ok.Results[0])
	}
	if ok.Results[1].Uncertainty != nil {
		t.Errorf("no uncertainty must stay none, not zero: %+v", ok.Results[1])
	}
	// refusals are answers: 200 with a reason and no numbers
	for _, c := range []struct{ to, from string }{{"Jy/beam", "K"}, {"Hz", "km/s"}, {"rad", "1"}, {"deg", "parsecs-per-fortnight"}} {
		r, b := postJSON(t, url, map[string]any{"to": c.to, "values": []map[string]any{{"value": 1, "from": c.from}}})
		var no struct {
			Refused bool
			Reason  string
			Results any
		}
		if r.StatusCode != 200 || json.Unmarshal(b, &no) != nil || !no.Refused || no.Reason == "" || no.Results != nil {
			t.Errorf("%s to %s should be refused with a reason: %d %s", c.from, c.to, r.StatusCode, b)
		}
		check(200, "refused "+c.from, b)
	}
	// the dry run: one answer per pair, a refusal of one does not hide the next, no number is converted
	r, b = postJSON(t, url, map[string]any{"pairs": []map[string]any{{"from": "deg", "to": "rad"}, {"from": "K", "to": "Jy/beam"}, {"from": "mK", "to": "K"}}})
	check(200, "dry run", b)
	var dry struct {
		Refused bool
		Results any
		Pairs   []struct {
			From, To, Reason string
			Refused          bool
			Factor           *float64
		}
	}
	if r.StatusCode != 200 || json.Unmarshal(b, &dry) != nil || dry.Refused || dry.Results != nil || len(dry.Pairs) != 3 {
		t.Fatalf("dry run: %d %s", r.StatusCode, b)
	}
	if p := dry.Pairs[0]; p.Refused || p.Factor == nil || math.Abs(*p.Factor-math.Pi/180) > 1e-12 {
		t.Errorf("deg to rad: %+v", p)
	}
	if p := dry.Pairs[1]; !p.Refused || p.Reason == "" || p.Factor != nil || p.From != "K" || p.To != "Jy/beam" {
		t.Errorf("K to Jy/beam must be refused with a reason: %+v", p)
	}
	if p := dry.Pairs[2]; p.Refused || p.Factor == nil || math.Abs(*p.Factor-1e-3) > 1e-12 {
		t.Errorf("mK to K: %+v", p)
	}
	for label, body := range map[string]any{
		"dry run, no pairs":     map[string]any{"pairs": []any{}},
		"dry run with values":   map[string]any{"pairs": []map[string]any{{"from": "deg", "to": "rad"}}, "values": []map[string]any{{"value": 1, "from": "deg"}}},
		"dry run with a target": map[string]any{"pairs": []map[string]any{{"from": "deg", "to": "rad"}}, "to": "deg"},
		"dry run, half a pair":  map[string]any{"pairs": []map[string]any{{"from": "deg"}}},
		"dry run, too many":     map[string]any{"pairs": make([]map[string]any, 201)},
		"no values":             map[string]any{"to": "deg", "values": []any{}},
		"no target":             map[string]any{"values": []map[string]any{{"value": 1, "from": "deg"}}},
		"no from":               map[string]any{"to": "deg", "values": []map[string]any{{"value": 1}}},
		"no value":              map[string]any{"to": "deg", "values": []map[string]any{{"from": "deg"}}}, // absent is not zero
		"negative sigma":        map[string]any{"to": "deg", "values": []map[string]any{{"value": 1, "uncertainty": -1, "from": "deg"}}},
		"unknown field":         map[string]any{"to": "deg", "values": []map[string]any{{"value": 1, "from": "deg"}}, "spectral": "radio"},
		"unit too long":         map[string]any{"to": "deg", "values": []map[string]any{{"value": 1, "from": string(make([]byte, 100))}}},
		"a flag as value":       map[string]any{"to": "deg", "values": []map[string]any{{"value": "x", "from": "deg"}}},
	} {
		if r, b := postJSON(t, url, body); r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s", label, r.StatusCode, b)
		}
	}
}

// The operation is classed as an interactive request: a person waits on the plot, so it queues with planes and readouts and
// never behind a heavy analysis.
func TestConvertValuesIsAnInteractiveOperation(t *testing.T) {
	t.Parallel()
	if worker.ClassOf("convert_values") != worker.Interactive {
		t.Error("convert_values must be interactive")
	}
}
