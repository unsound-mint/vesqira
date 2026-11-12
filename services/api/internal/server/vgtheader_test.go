package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"net/http/httptest"
)

// What a run import declares for the psi map depends on what its header says (ADR 0014, amendment): PSIFRAME wins over the adapter's
// convention and is labelled as the file's; an old output without it keeps the labelled convention; a person's declaration is kept and a
// disagreement with the header is listed, never resolved silently.
func TestRunImportReadsTheAngleFrameFromTheHeader(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	hdr := headerWorker(t, "python3", repo)
	base := t.TempDir()
	root := filepath.Join(base, "root")
	os.MkdirAll(root, 0o755)
	rs, _ := sourceroot.New([]string{root})
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	rn, err := runs.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	au, _ := audit.Open(cat.DB())
	ts := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: hdr, Runs: rn, Audit: au}).Handler())
	defer ts.Close()
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	psiOf := func(dir string) catalog.Dataset {
		t.Helper()
		r, out := postJSON(t, ts.URL+"/api/runs/import", map[string]any{"path": filepath.Join(root, dir)})
		if r.StatusCode != 201 {
			t.Fatalf("import %s: %d %s", dir, r.StatusCode, out)
		}
		var run struct {
			Outputs []struct {
				Role      string
				DatasetID int64
			}
			Params []struct{ Key, Value string }
		}
		json.Unmarshal(out, &run)
		for _, o := range run.Outputs {
			if o.Role == "psi" {
				_, pb := get(t, ts.URL+"/api/datasets/"+itoa(int(o.DatasetID)))
				var d catalog.Dataset
				json.Unmarshal(pb, &d)
				return d
			}
		}
		t.Fatalf("no psi output: %s", out)
		return catalog.Dataset{}
	}

	// current VGT output: PSIFRAME=GALACTIC on an ICRS grid, run_metadata.toml with project name "VGT" and without the retired keys
	cur := psiOf("vgt_run_current")
	in := cur.Interpretation
	if in == nil || in.DeclaredBy != catalog.FileHeaderAuthor || in.Tier != catalog.TierDeclared || in.Unit != "rad" ||
		in.Frame == nil || *in.Frame != "sky" || in.SkyFrame == nil || *in.SkyFrame != "galactic" || in.Quantity == nil || *in.Quantity != "b_field" {
		t.Fatalf("header declaration: %+v", in)
	}
	if in.Stale {
		t.Errorf("a declaration just read from the header is not stale")
	}
	if cur.VGT == nil || len(cur.VGT.Conflicts) != 0 || cur.VGT.Orientation == nil || cur.VGT.Orientation.Basis["frame"] != "file" {
		t.Errorf("header view: %+v", cur.VGT)
	}

	// an older output keeps the adapter's convention, labelled as before, and nothing is inferred from the WCS
	old := psiOf("vgt_run_demo")
	if old.Interpretation == nil || old.Interpretation.DeclaredBy != "vgt-run-adapter" || old.Interpretation.SkyFrame != nil ||
		old.Interpretation.Frame == nil || *old.Interpretation.Frame != "pixel" || old.VGT != nil {
		t.Fatalf("fallback: %+v vgt=%+v", old.Interpretation, old.VGT)
	}

	// a person's own declaration is kept; the disagreement with the header is shown with the header's words
	r, b := postJSON(t, ts.URL+"/api/datasets/"+itoa(int(cur.ID))+"/interpretation", map[string]any{"kind": "axial_orientation", "unit": "rad", "frame": "pixel", "quantity": "b_field"})
	if r.StatusCode != 200 {
		t.Fatalf("declare: %d %s", r.StatusCode, b)
	}
	cur2 := psiOf("vgt_run_current")
	if cur2.Interpretation.DeclaredBy != "user" || *cur2.Interpretation.Frame != "pixel" {
		t.Fatalf("a re-import overwrote the person's declaration: %+v", cur2.Interpretation)
	}
	if cur2.VGT == nil || len(cur2.VGT.Conflicts) != 1 || !strings.Contains(cur2.VGT.Conflicts[0], "Galactic") || !strings.Contains(cur2.VGT.Conflicts[0], "pixel grid") {
		t.Fatalf("conflict not shown: %+v", cur2.VGT)
	}
	// "sky" by the person means the grid's own frame (ICRS here): that also disagrees with a Galactic header
	postJSON(t, ts.URL+"/api/datasets/"+itoa(int(cur.ID))+"/interpretation", map[string]any{"kind": "axial_orientation", "unit": "rad", "frame": "sky", "quantity": "b_field"})
	_, pb := get(t, ts.URL+"/api/datasets/"+itoa(int(cur.ID)))
	var d3 catalog.Dataset
	json.Unmarshal(pb, &d3)
	if d3.VGT == nil || len(d3.VGT.Conflicts) != 1 || !strings.Contains(d3.VGT.Conflicts[0], "not this grid's own frame") {
		t.Fatalf("sky-in-own-frame against GALACTIC: %+v", d3.VGT)
	}
	// adopting the file's declaration is explicit, and cannot be mixed with values sent by hand
	if r, _ := postJSON(t, ts.URL+"/api/datasets/"+itoa(int(cur.ID))+"/interpretation", map[string]any{"kind": "axial_orientation", "fromFile": true, "unit": "deg"}); r.StatusCode != 400 {
		t.Errorf("fromFile with a unit: %d, want 400", r.StatusCode)
	}
	r, b = postJSON(t, ts.URL+"/api/datasets/"+itoa(int(cur.ID))+"/interpretation", map[string]any{"kind": "axial_orientation", "fromFile": true})
	var d4 catalog.Dataset
	json.Unmarshal(b, &d4)
	if r.StatusCode != 200 || d4.Interpretation.DeclaredBy != catalog.FileHeaderAuthor || d4.Interpretation.SkyFrame == nil || len(d4.VGT.Conflicts) != 0 {
		t.Fatalf("adopt: %d %s", r.StatusCode, b)
	}
	// a file with no PSIFRAME has nothing to adopt
	_, ob := get(t, ts.URL+"/api/datasets/"+itoa(int(old.ID)))
	_ = ob
	if r, _ := postJSON(t, ts.URL+"/api/datasets/"+itoa(int(old.ID))+"/interpretation", map[string]any{"kind": "axial_orientation", "fromFile": true}); r.StatusCode != 422 {
		t.Errorf("fromFile without a header declaration: %d, want 422", r.StatusCode)
	}
}

func TestSourceBeamAcceptedFromTheFilesOwnKeywordsOnly(t *testing.T) {
	t.Parallel()
	good := catalog.Dataset{VGT: &catalog.VGTHeader{Resolution: json.RawMessage(`{"status":"approximate","gaussian_arcsec":{"bmaj":680.5,"bmin":679.25,"bpa":30}}`)}}
	opts := map[string]string{"source-beam-basis": "vgt_approximate", "source-bmaj": "1"} // numbers a client sent are replaced by the file's
	if code, msg := acceptSourceBeam("convolve", good, opts); code != 0 || opts["source-bmaj"] != "680.5" || opts["source-bmin"] != "679.25" || opts["source-bpa"] != "30" {
		t.Fatalf("%d %s %v", code, msg, opts)
	}
	notEstablished := catalog.Dataset{VGT: &catalog.VGTHeader{Resolution: json.RawMessage(`{"status":"not_established","reasons":["x"]}`)}}
	if code, msg := acceptSourceBeam("convolve", notEstablished, map[string]string{"source-beam-basis": "vgt_approximate"}); code != 422 || !strings.Contains(msg, "not established") {
		t.Errorf("not established must be refused: %d %s", code, msg)
	}
	if code, _ := acceptSourceBeam("convolve", catalog.Dataset{}, map[string]string{"source-beam-basis": "vgt_approximate"}); code != 422 {
		t.Errorf("a file with no VGT keywords has no approximation to accept: %d", code)
	}
	if code, _ := acceptSourceBeam("align", good, map[string]string{"source-beam-basis": "user", "source-bmaj": "1", "source-bmin": "1"}); code != 422 {
		t.Errorf("only a convolution takes a source resolution: %d", code)
	}
	if code, _ := acceptSourceBeam("convolve", good, map[string]string{"source-bmaj": "5"}); code != 422 {
		t.Errorf("a resolution without its basis: %d", code)
	}
	if code, _ := acceptSourceBeam("convolve", good, map[string]string{}); code != 0 {
		t.Errorf("no source resolution asked: %d", code)
	}
}

func TestAxialMapsStatedDifferentlyAreNotSubtracted(t *testing.T) {
	px := &catalog.Interpretation{Frame: strp("pixel")}
	sky := &catalog.Interpretation{Frame: strp("sky")}
	gal := &catalog.Interpretation{Frame: strp("sky"), SkyFrame: strp("galactic")}
	for _, c := range []struct {
		a, b *catalog.Interpretation
		same bool
	}{{px, px, true}, {sky, sky, true}, {gal, gal, true}, {px, sky, false}, {sky, gal, false}, {px, gal, false}, {&catalog.Interpretation{}, gal, true}} {
		if got := axialStatedDifferently(c.a, c.b); (got == "") != c.same {
			t.Errorf("%v vs %v: %q", c.a, c.b, got)
		}
	}
	if why := axialStatedDifferently(gal, px); !strings.Contains(why, "Galactic") || !strings.Contains(why, "relative orientation") {
		t.Errorf("the refusal should name the frames and the right tool: %q", why)
	}
}
