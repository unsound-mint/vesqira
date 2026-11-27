package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// rangePlanes answers the axial op with the extremes a test sets per file name, the way the real worker does (numbers only).
type rangePlanes struct {
	mu     sync.Mutex
	ranges map[string][2]float64
	calls  []string
}

func (f *rangePlanes) Do(_ context.Context, req map[string]any) (worker.Reply, error) {
	p, _ := req["path"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, filepath.Base(p)+":"+fmt.Sprint(req["op"]))
	r, ok := f.ranges[filepath.Base(p)]
	if !ok {
		return worker.Reply{Head: []byte(`{"stats":{"value_min":null,"value_max":null}}`)}, nil
	}
	return worker.Reply{Head: []byte(fmt.Sprintf(`{"stats":{"value_min":%g,"value_max":%g}}`, r[0], r[1]))}, nil
}

type applyEnv struct {
	t      *testing.T
	srv    *Server
	ts     *httptest.Server
	cat    *catalog.Store
	rn     *runs.Store
	pj     *projects.Store
	planes *rangePlanes
	root   string
}

const applyProfile = "VGT.jl@1"

func newApplyEnv(t *testing.T) *applyEnv {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(base, "data")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	rs, err := sourceroot.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	rn, err := runs.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	pj, err := projects.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	au, err := audit.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	pl := &rangePlanes{ranges: map[string][2]float64{}}
	srv := &Server{Roots: rs, Store: cat, Worker: &fakeWorker{}, Runs: rn, Projects: pj, Planes: pl, Cache: NewPlaneCache(1 << 20), Audit: au}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &applyEnv{t: t, srv: srv, ts: ts, cat: cat, rn: rn, pj: pj, planes: pl, root: root}
}

// header is a 4 x 4 map on one grid; extra keys (the file's own cards) are merged over it.
func gridHeader(side int, extra map[string]any) map[string]any {
	ax := func(ct string, crval, cdelt float64) map[string]any {
		return map[string]any{"length": side, "ctype": ct, "cunit": "deg", "crval": crval, "crpix": 2.5, "cdelt": cdelt}
	}
	h := map[string]any{"axes": []any{ax("RA---TAN", 150, -0.01), ax("DEC--TAN", 2, 0.01)}, "radesys": "ICRS"}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

func (e *applyEnv) dataset(ws workspace.ID, name string, h map[string]any) catalog.Dataset {
	e.t.Helper()
	p := filepath.Join(e.root, name)
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	st, _ := os.Stat(p)
	raw, _ := json.Marshal(h)
	d, err := e.cat.Upsert(ws, "local-owner", catalog.Dataset{Path: p, Name: name, SizeBytes: st.Size(), ModifiedAt: st.ModTime(), Header: raw})
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

// run records a run of the profile whose orientation output is psi and, when rel is not nil, whose reliability output is rel.
func (e *applyEnv) run(ws workspace.ID, dir, profile string, psi catalog.Dataset, rel *catalog.Dataset) runs.Run {
	e.t.Helper()
	outs := []runs.Output{{Role: runs.RoleOrientation, Product: "psi", DatasetID: psi.ID, Name: psi.Name}}
	if rel != nil {
		outs = append(outs, runs.Output{Role: runs.RoleReliability, Product: "reliability", DatasetID: rel.ID, Name: rel.Name})
	}
	r, err := e.rn.Save(ws, "local-owner", dir, runs.Parsed{Run: runs.Run{Name: filepath.Base(dir), Kind: "vgt", Profile: profile}}, outs)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// declareSource declares psi as the user would through the single-declaration route's store call.
func (e *applyEnv) declareSource(d catalog.Dataset, unit string, rel *int64) {
	e.t.Helper()
	frame, quantity := "pixel", "b_field"
	if err := e.cat.SetInterpretation(d, catalog.Interpretation{Kind: "axial_orientation", Unit: unit, ReliabilityID: rel, Frame: &frame, Quantity: &quantity, DeclaredBy: "user"}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *applyEnv) preview(src int64, scope string) (int, applyPreview, []byte) {
	e.t.Helper()
	q := ""
	if scope != "" {
		q = "?scope=" + scope
	}
	resp, b := get(e.t, e.ts.URL+"/api/datasets/"+itoa(int(src))+"/interpretation/apply-preview"+q)
	var out applyPreview
	json.Unmarshal(b, &out)
	return resp.StatusCode, out, b
}

type declApplyResult struct {
	ApplyID string         `json:"applyId"`
	Scope   string         `json:"scope"`
	Rows    []applyRow     `json:"rows"`
	Counts  map[string]int `json:"counts"`
}

func (e *applyEnv) apply(src int64, scope string, ids ...int64) (int, declApplyResult, []byte) {
	e.t.Helper()
	r, b := postJSON(e.t, e.ts.URL+"/api/interpretations/apply", map[string]any{"sourceId": src, "scope": scope, "targetIds": ids, "confirm": true})
	var out declApplyResult
	json.Unmarshal(b, &out)
	return r.StatusCode, out, b
}

func (e *applyEnv) get(id int64) catalog.Dataset {
	e.t.Helper()
	d, err := e.cat.Get(workspace.Local, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

func rowOf(t *testing.T, rows []applyRow, id int64) applyRow {
	t.Helper()
	for _, r := range rows {
		if r.DatasetID == id {
			return r
		}
	}
	t.Fatalf("no row for dataset %d in %+v", id, rows)
	return applyRow{}
}

// cardHeader is a header carrying VGT.jl's own cards (PSIFRAME, PSIANGLE, BUNIT) read by the profile.
func cardHeader(side int) map[string]any {
	return gridHeader(side, map[string]any{
		"vgt":         map[string]any{"psiframe": "GALACTIC", "psiangle": "BFIELD", "vgtprod": "PSI"},
		"orientation": map[string]any{"label": "profile:VGT.jl@1", "frame": "sky", "sky_frame": "galactic", "quantity": "b_field", "unit": "rad", "unresolved": []string{}},
	})
}

// cardlessHeader is an older VGT output: the profile reads it as the psi product and places none of frame, quantity or unit.
func cardlessHeader(side int) map[string]any {
	return gridHeader(side, map[string]any{
		"vgt":         map[string]any{"vgtprod": "PSI"},
		"orientation": map[string]any{"label": "profile:VGT.jl@1", "unresolved": []string{"frame", "quantity", "unit"}},
	})
}

type fleet struct {
	src, srcRel   catalog.Dataset
	good, goodRel catalog.Dataset // a target with its own reliability map
	noRel         catalog.Dataset // a target whose run has no reliability output
	declared      catalog.Dataset // already declared by the researcher
	carded        catalog.Dataset // its header states the convention
	cube          catalog.Dataset // not a single plane
	wide          catalog.Dataset // values beyond 2 pi
	other         catalog.Dataset // a run read by another tool profile
	outside       catalog.Dataset // a run outside the folder
}

// newFleet builds one project folder of runs: the source (declared by the user in radians) and one run per outcome.
func newFleet(t *testing.T, e *applyEnv) fleet {
	t.Helper()
	var f fleet
	proj := filepath.Join(e.root, "vgt")
	runDir := func(n string) string { return filepath.Join(proj, "runs", n) }
	f.src, f.srcRel = e.dataset(1, "psi_src.fits", cardlessHeader(4)), e.dataset(1, "rel_src.fits", gridHeader(4, nil))
	f.good, f.goodRel = e.dataset(1, "psi_good.fits", cardlessHeader(4)), e.dataset(1, "rel_good.fits", gridHeader(4, nil))
	f.noRel = e.dataset(1, "psi_norel.fits", gridHeader(4, nil))
	f.declared = e.dataset(1, "psi_declared.fits", gridHeader(4, nil))
	f.carded = e.dataset(1, "psi_carded.fits", cardHeader(4))
	f.cube = e.dataset(1, "psi_cube.fits", func() map[string]any {
		h := gridHeader(4, nil)
		h["axes"] = append(h["axes"].([]any), map[string]any{"length": 5, "ctype": "VRAD", "cunit": "m/s", "crval": 0, "crpix": 1, "cdelt": 1000})
		return h
	}())
	f.wide = e.dataset(1, "psi_wide.fits", gridHeader(4, nil))
	f.other = e.dataset(1, "psi_other.fits", gridHeader(4, nil))
	f.outside = e.dataset(1, "psi_outside.fits", gridHeader(4, nil))
	e.run(1, runDir("src"), applyProfile, f.src, &f.srcRel)
	e.run(1, runDir("good"), applyProfile, f.good, &f.goodRel)
	e.run(1, runDir("norel"), applyProfile, f.noRel, nil)
	e.run(1, runDir("declared"), applyProfile, f.declared, nil)
	e.run(1, runDir("carded"), applyProfile, f.carded, nil)
	e.run(1, runDir("cube"), applyProfile, f.cube, nil)
	e.run(1, runDir("wide"), applyProfile, f.wide, nil)
	e.run(1, runDir("other"), "ROHSA@1", f.other, nil)
	e.run(1, filepath.Join(e.root, "elsewhere", "run"), applyProfile, f.outside, nil)
	if _, err := e.pj.Touch(1, "local-owner", 0, proj, "VGT project"); err != nil {
		t.Fatal(err)
	}
	e.declareSource(f.src, "rad", &f.srcRel.ID)
	e.declareSource(f.declared, "deg", nil)
	e.planes.ranges["psi_good.fits"] = [2]float64{0, 3.1}
	e.planes.ranges["psi_norel.fits"] = [2]float64{-1.5, 1.5}
	e.planes.ranges["psi_wide.fits"] = [2]float64{0, 179.9}
	return f
}

func TestApplyPreviewListsOnlyTheSameRoleAndProfileInScopeAndNamesWhyTheRestAreLeftAlone(t *testing.T) {
	t.Parallel()
	e := newApplyEnv(t)
	f := newFleet(t, e)
	code, pv, raw := e.preview(f.src.ID, "")
	if code != 200 || !pv.Eligible || pv.Source == nil || pv.Source.Profile != applyProfile || pv.Source.Unit != "rad" {
		t.Fatalf("preview: %d %s", code, raw)
	}
	if pv.Scope != "project" || len(pv.Scopes) != 2 || pv.Scopes[0].Scope != "project" || pv.Scopes[1].Scope != "folder" {
		t.Fatalf("scopes: %+v", pv.Scopes)
	}
	// the project holds every run of the profile under it; the folder is the runs folder: the same ones here, the run elsewhere is in neither
	for _, s := range pv.Scopes {
		if s.Runs != 6 {
			t.Errorf("scope %s reaches %d runs, want 6 (every run of the profile in it but the source)", s.Scope, s.Runs)
		}
	}
	ids := map[int64]bool{}
	for _, r := range pv.Rows {
		ids[r.DatasetID] = true
	}
	if ids[f.other.ID] || ids[f.outside.ID] || ids[f.src.ID] || ids[f.srcRel.ID] || ids[f.goodRel.ID] {
		t.Errorf("the preview reaches another profile's run, a run outside the scope, the source or a reliability output: %+v", pv.Rows)
	}
	want := map[int64]string{f.good.ID: "declare", f.noRel.ID: "declare", f.wide.ID: "declare", f.declared.ID: "skipped", f.carded.ID: "skipped", f.cube.ID: "refused"}
	for id, o := range want {
		if got := rowOf(t, pv.Rows, id).Outcome; got != o {
			t.Errorf("dataset %d: %s, want %s", id, got, o)
		}
	}
	if r := rowOf(t, pv.Rows, f.declared.ID); r.Reason != "already declared by you" {
		t.Errorf("declared: %q", r.Reason)
	}
	if r := rowOf(t, pv.Rows, f.carded.ID); !strings.Contains(r.Reason, "own header states the convention") {
		t.Errorf("carded: %q", r.Reason)
	}
	if r := rowOf(t, pv.Rows, f.cube.ID); !strings.Contains(r.Reason, "single-plane") {
		t.Errorf("cube: %q", r.Reason)
	}
	if pv.Counts["declare"] != 3 || pv.Counts["skipped"] != 2 || pv.Counts["refused"] != 1 {
		t.Errorf("counts: %+v", pv.Counts)
	}
	if len(e.planes.calls) != 0 {
		t.Errorf("a preview reads no file, but the worker was asked: %v", e.planes.calls)
	}
	// the folder scope can be asked for by name, and an unknown one is refused
	if code, pf, _ := e.preview(f.src.ID, "folder"); code != 200 || pf.Scope != "folder" || len(pf.Rows) != 6 {
		t.Errorf("folder preview: %d %+v", code, pf)
	}
	if code, _, _ := e.preview(f.src.ID, "everywhere"); code != 400 {
		t.Errorf("unknown scope: %d", code)
	}
}

func TestApplyDeclaresWithTheTargetsOwnReliabilityAndRecordsTheBasisTruthfully(t *testing.T) {
	t.Parallel()
	e := newApplyEnv(t)
	f := newFleet(t, e)
	code, res, raw := e.apply(f.src.ID, "project", f.good.ID, f.noRel.ID, f.wide.ID, f.declared.ID, f.carded.ID, f.cube.ID, f.other.ID, f.outside.ID, 99999)
	if code != 200 {
		t.Fatalf("apply: %d %s", code, raw)
	}
	if res.Counts["declared"] != 2 || res.Counts["skipped"] != 2 || res.Counts["refused"] != 5 {
		t.Fatalf("counts: %+v\n%s", res.Counts, raw)
	}
	good := rowOf(t, res.Rows, f.good.ID)
	if good.Outcome != "declared" || good.Reliability == nil || good.Reliability.DatasetID != f.goodRel.ID {
		t.Errorf("good: %+v", good)
	}
	d := e.get(f.good.ID)
	in := d.Interpretation
	if in == nil || in.Kind != "axial_orientation" || in.Unit != "rad" || in.Frame == nil || *in.Frame != "pixel" || in.Quantity == nil || *in.Quantity != "b_field" {
		t.Fatalf("what was copied: %+v", in)
	}
	// the reliability map is the target run's own, never the source's
	if in.ReliabilityID == nil || *in.ReliabilityID != f.goodRel.ID || *in.ReliabilityID == f.srcRel.ID {
		t.Errorf("reliability %v, want the target's own %d", in.ReliabilityID, f.goodRel.ID)
	}
	// the basis: the user's declaration, applied from the source, by the account; not verified and not inferred
	if in.DeclaredBy != "user" || in.Tier != catalog.TierDeclared || in.AppliedFrom == nil || *in.AppliedFrom != f.src.ID || in.AppliedBy != authz.LocalOwnerID || in.ApplyID != res.ApplyID || in.ApplyID == "" {
		t.Errorf("provenance: %+v", in)
	}
	// a target whose run has no reliability output is declared without one, and says so
	nr := rowOf(t, res.Rows, f.noRel.ID)
	if nr.Outcome != "declared" || nr.Reliability != nil || !strings.Contains(nr.Note, "no reliability output") {
		t.Errorf("no reliability: %+v", nr)
	}
	if got := e.get(f.noRel.ID).Interpretation; got == nil || got.ReliabilityID != nil {
		t.Errorf("no-reliability target: %+v", got)
	}
	// never overwritten: the researcher's own declaration (degrees) stands
	if got := e.get(f.declared.ID).Interpretation; got.Unit != "deg" || got.AppliedFrom != nil || rowOf(t, res.Rows, f.declared.ID).Outcome != "skipped" {
		t.Errorf("an existing declaration was touched: %+v", got)
	}
	// the file's own statement wins; a cube is refused; a value range beyond 2 pi is refused for radians with the reason
	if e.get(f.carded.ID).Interpretation != nil || rowOf(t, res.Rows, f.carded.ID).Outcome != "skipped" {
		t.Errorf("a file whose header states the convention was declared")
	}
	if e.get(f.cube.ID).Interpretation != nil || rowOf(t, res.Rows, f.cube.ID).Outcome != "refused" {
		t.Errorf("a cube was declared")
	}
	w := rowOf(t, res.Rows, f.wide.ID)
	if w.Outcome != "refused" || !strings.Contains(w.Reason, "beyond 2π") || !strings.Contains(w.Reason, "179.9") || e.get(f.wide.ID).Interpretation != nil {
		t.Errorf("wide: %+v", w)
	}
	// another tool's run, a run outside the scope and an unknown id are all refused as not being in this scope
	for _, id := range []int64{f.other.ID, f.outside.ID, 99999} {
		if r := rowOf(t, res.Rows, id); r.Outcome != "refused" || !strings.Contains(r.Reason, "not an orientation output of another run in this scope") {
			t.Errorf("dataset %d: %+v", id, r)
		}
	}
	for _, id := range []int64{f.other.ID, f.outside.ID} {
		if e.get(id).Interpretation != nil {
			t.Errorf("dataset %d out of scope was declared", id)
		}
	}
	// the values were read only for what would be declared in radians, as numbers (the axial op), never as a plane
	for _, c := range e.planes.calls {
		if !strings.HasSuffix(c, ":axial") {
			t.Errorf("the worker was asked for more than the range: %s", c)
		}
	}
	if len(e.planes.calls) != 3 {
		t.Errorf("worker calls: %v", e.planes.calls)
	}
	// a second apply declares nothing new: every target is now declared and is skipped, whoever it was declared by
	_, again, _ := e.apply(f.src.ID, "project", f.good.ID, f.noRel.ID)
	if again.Counts["declared"] != 0 || again.Counts["skipped"] != 2 || !strings.Contains(rowOf(t, again.Rows, f.good.ID).Reason, "copied from dataset") {
		t.Errorf("repeat: %+v", again)
	}
}

func TestApplyOfDegreesNeedsNoRangeReadAndOnlyAnExplicitConfirmWritesAnything(t *testing.T) {
	t.Parallel()
	e := newApplyEnv(t)
	f := newFleet(t, e)
	e.declareSource(f.src, "deg", nil)
	e.planes.ranges["psi_good.fits"] = [2]float64{0, 3.1} // looks like radians; degrees are not refused on a guess (ADR 0010: it is a warning)
	r, b := postJSON(t, e.ts.URL+"/api/interpretations/apply", map[string]any{"sourceId": f.src.ID, "scope": "project", "targetIds": []int64{f.good.ID}})
	if r.StatusCode != 400 || e.get(f.good.ID).Interpretation != nil {
		t.Fatalf("without confirm: %d %s", r.StatusCode, b)
	}
	code, res, raw := e.apply(f.src.ID, "project", f.good.ID)
	if code != 200 || res.Counts["declared"] != 1 || e.get(f.good.ID).Interpretation.Unit != "deg" {
		t.Fatalf("%d %s", code, raw)
	}
	if len(e.planes.calls) != 0 {
		t.Errorf("degrees need no read: %v", e.planes.calls)
	}
	if r, _ := postJSON(t, e.ts.URL+"/api/interpretations/apply", map[string]any{"sourceId": f.src.ID, "scope": "project", "targetIds": []int64{}, "confirm": true}); r.StatusCode != 400 {
		t.Errorf("no targets: %d", r.StatusCode)
	}
	if code, _, _ := e.apply(f.src.ID, "galaxy", f.good.ID); code != 400 {
		t.Errorf("unknown scope: %d", code)
	}
}

func TestApplyRefusesASourceThatIsNotTheUsersOwnDirectDeclarationOfARunsOrientationOutput(t *testing.T) {
	t.Parallel()
	e := newApplyEnv(t)
	f := newFleet(t, e)
	e.apply(f.src.ID, "project", f.good.ID)
	// a copy is not itself applied onward
	if code, pv, _ := e.preview(f.good.ID, ""); code != 200 || pv.Eligible || !strings.Contains(pv.Reason, "itself copied from dataset") {
		t.Errorf("copy as source: %d %+v", code, pv)
	}
	if code, _, _ := e.apply(f.good.ID, "project", f.noRel.ID); code != 409 {
		t.Errorf("copy as source: %d", code)
	}
	// not declared; declared by the file's header or a rule; a reliability output; no run
	if _, pv, _ := e.preview(f.noRel.ID, ""); pv.Eligible || !strings.Contains(pv.Reason, "not declared") {
		t.Errorf("undeclared: %+v", pv)
	}
	frame, q := "pixel", "b_field"
	for _, by := range []string{"profile:VGT.jl@1", "rule:folder", "vgt-run-adapter"} {
		e.cat.ClearInterpretation(f.noRel.ID)
		if err := e.cat.SetInterpretation(f.noRel, catalog.Interpretation{Kind: "axial_orientation", Unit: "rad", Frame: &frame, Quantity: &q, DeclaredBy: by}); err != nil {
			t.Fatal(err)
		}
		if _, pv, _ := e.preview(f.noRel.ID, ""); pv.Eligible || !strings.Contains(pv.Reason, "only a declaration you made yourself") {
			t.Errorf("%s: %+v", by, pv)
		}
	}
	e.declareSource(f.srcRel, "deg", nil)
	if _, pv, _ := e.preview(f.srcRel.ID, ""); pv.Eligible || !strings.Contains(pv.Reason, "reliability output") {
		t.Errorf("reliability output: %+v", pv)
	}
	loose := e.dataset(1, "psi_loose.fits", gridHeader(4, nil))
	e.declareSource(loose, "deg", nil)
	if _, pv, _ := e.preview(loose.ID, ""); pv.Eligible || !strings.Contains(pv.Reason, "not an output of an imported run") {
		t.Errorf("no run: %+v", pv)
	}
	// declared again by hand: no longer a copy, so it can be a source (and its own apply id is gone)
	e.declareSource(f.good, "rad", nil)
	if got := e.get(f.good.ID).Interpretation; got.ApplyID != "" || got.AppliedFrom != nil {
		t.Errorf("a direct declaration kept its apply marks: %+v", got)
	}
	if _, pv, _ := e.preview(f.good.ID, ""); !pv.Eligible {
		t.Errorf("re-declared: %+v", pv)
	}
}

func TestApplyStaysInsideTheCallersWorkspace(t *testing.T) {
	t.Parallel()
	e := newApplyEnv(t)
	f := newFleet(t, e)
	if _, err := e.cat.DB().Exec(`INSERT INTO workspaces(id,name,created_at) VALUES(2,'Lab',1)`); err != nil {
		t.Fatal(err)
	}
	// a second workspace has its own source and an identical run layout; the first has the fleet
	psi2, rel2 := e.dataset(2, "psi_ws2.fits", gridHeader(4, nil)), e.dataset(2, "rel_ws2.fits", gridHeader(4, nil))
	other2 := e.dataset(2, "psi_ws2b.fits", gridHeader(4, nil))
	dir := filepath.Join(e.root, "vgt", "runs")
	e.run(2, filepath.Join(dir, "ws2-src"), applyProfile, psi2, &rel2)
	e.run(2, filepath.Join(dir, "ws2-b"), applyProfile, other2, nil)
	frame, q := "pixel", "b_field"
	if err := e.cat.SetInterpretation(psi2, catalog.Interpretation{Kind: "axial_orientation", Unit: "deg", Frame: &frame, Quantity: &q, DeclaredBy: "user"}); err != nil {
		t.Fatal(err)
	}
	asWS2 := func(handler http.HandlerFunc, method, target string, body any) (int, []byte) {
		var rd *strings.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = strings.NewReader(string(b))
		} else {
			rd = strings.NewReader("")
		}
		req := httptest.NewRequest(method, target, rd)
		req.SetPathValue("id", itoa(int(psi2.ID)))
		req = req.WithContext(authz.WithActor(req.Context(), authz.Actor{ID: "acct-2", Via: authz.ViaSession, Workspace: 2, Role: authz.RoleResearcher}))
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	// workspace 2's apply reaches workspace 2's runs only, and a dataset id of workspace 1 is refused by name
	code, b := asWS2(e.srv.applyRoute, "POST", "/api/interpretations/apply", map[string]any{"sourceId": psi2.ID, "scope": "folder", "targetIds": []int64{other2.ID, f.good.ID}, "confirm": true})
	var res declApplyResult
	json.Unmarshal(b, &res)
	if code != 200 || rowOf(t, res.Rows, other2.ID).Outcome != "declared" || rowOf(t, res.Rows, f.good.ID).Outcome != "refused" {
		t.Fatalf("workspace 2 apply: %d %s", code, b)
	}
	if e.get(f.good.ID).Interpretation != nil {
		t.Errorf("a dataset of another workspace was declared")
	}
	// workspace 1's source is not found from workspace 2, and workspace 2's apply cannot be undone from workspace 1
	if code, _ := asWS2(e.srv.applyRoute, "POST", "/api/interpretations/apply", map[string]any{"sourceId": f.src.ID, "scope": "folder", "targetIds": []int64{f.good.ID}, "confirm": true}); code != 404 {
		t.Errorf("another workspace's source: %d", code)
	}
	if code, b := asWS2(e.srv.applyPreviewRoute, "GET", "/api/datasets/1/interpretation/apply-preview", nil); code != 200 {
		t.Errorf("own preview: %d %s", code, b)
	}
	r, ub := postJSON(t, e.ts.URL+"/api/interpretations/apply/"+res.ApplyID+"/undo", map[string]any{})
	var undone struct{ Removed int }
	json.Unmarshal(ub, &undone)
	if r.StatusCode != 200 || undone.Removed != 0 {
		t.Errorf("undo from workspace 1 of workspace 2's apply: %d %s", r.StatusCode, ub)
	}
	if got, err := e.cat.Get(2, other2.ID); err != nil || got.Interpretation == nil {
		t.Errorf("workspace 2's declaration was removed by workspace 1: %+v %v", got.Interpretation, err)
	}
}

func TestUndoRemovesExactlyThisAppliesUneditedDeclarations(t *testing.T) {
	t.Parallel()
	e := newApplyEnv(t)
	f := newFleet(t, e)
	_, first, _ := e.apply(f.src.ID, "project", f.good.ID, f.noRel.ID, f.wide.ID)
	if first.Counts["declared"] != 2 {
		t.Fatalf("first: %+v", first.Counts)
	}
	// the researcher edits one copy by hand (the ordinary route), after which it is theirs and an undo leaves it
	r, b := postJSON(t, e.ts.URL+"/api/datasets/"+itoa(int(f.good.ID))+"/interpretation", map[string]any{"kind": "axial_orientation", "unit": "rad", "reliabilityId": nil, "frame": "sky", "quantity": "gradient"})
	if r.StatusCode != 200 {
		t.Fatalf("edit: %d %s", r.StatusCode, b)
	}
	// a declaration of another apply and one made directly are never touched by this undo
	e.declareSource(f.wide, "deg", nil)
	_, second, _ := e.apply(f.src.ID, "folder", f.wide.ID, f.cube.ID)
	if second.Counts["declared"] != 0 {
		t.Fatalf("second: %+v", second)
	}
	ur, ub := postJSON(t, e.ts.URL+"/api/interpretations/apply/"+first.ApplyID+"/undo", map[string]any{})
	var undone struct {
		ApplyID string
		Removed int
	}
	json.Unmarshal(ub, &undone)
	if ur.StatusCode != 200 || undone.Removed != 1 || undone.ApplyID != first.ApplyID {
		t.Fatalf("undo: %d %s", ur.StatusCode, ub)
	}
	if e.get(f.noRel.ID).Interpretation != nil {
		t.Errorf("an unedited copy of the apply is still there")
	}
	edited := e.get(f.good.ID).Interpretation
	if edited == nil || *edited.Quantity != "gradient" || edited.ApplyID != "" {
		t.Errorf("the edited declaration was removed or kept its apply marks: %+v", edited)
	}
	if got := e.get(f.wide.ID).Interpretation; got == nil || got.Unit != "deg" {
		t.Errorf("a direct declaration was removed: %+v", got)
	}
	if got := e.get(f.src.ID).Interpretation; got == nil {
		t.Errorf("the source declaration was removed")
	}
	// repeatable, and a malformed id is refused
	_, ub2 := postJSON(t, e.ts.URL+"/api/interpretations/apply/"+first.ApplyID+"/undo", map[string]any{})
	if !strings.Contains(string(ub2), `"removed":0`) {
		t.Errorf("second undo: %s", ub2)
	}
	if r, _ := postJSON(t, e.ts.URL+"/api/interpretations/apply/not-an-id/undo", map[string]any{}); r.StatusCode != 400 {
		t.Errorf("bad id: %d", r.StatusCode)
	}
}

func TestAnApplyNeverWritesOverADeclarationMadeAfterThePreview(t *testing.T) {
	t.Parallel()
	e := newApplyEnv(t)
	f := newFleet(t, e)
	d := e.get(f.good.ID)
	mine := catalog.Interpretation{Kind: "axial_orientation", Unit: "deg"}
	if err := e.cat.SetInterpretation(d, catalog.Interpretation{Kind: "axial_orientation", Unit: "deg", DeclaredBy: "user"}); err != nil {
		t.Fatal(err)
	}
	// the insert an apply makes after its own checks passed: the row is there, so it is not replaced
	wrote, err := e.cat.ApplyInterpretation(d, mine, "00000000000000000000000000000001", f.src.ID, "someone")
	if err != nil || wrote {
		t.Fatalf("wrote over a declaration: %v %v", wrote, err)
	}
	if got := e.get(f.good.ID).Interpretation; got.Unit != "deg" || got.AppliedFrom != nil {
		t.Errorf("declaration changed: %+v", got)
	}
}

func TestApplyReliabilityIsPairedOnlyWhenTheRunsOwnMapSharesTheGrid(t *testing.T) {
	t.Parallel()
	e := newApplyEnv(t)
	f := newFleet(t, e)
	// a second target whose run's reliability map is on another grid, and one whose run lists two reliability outputs
	offGrid := e.dataset(1, "psi_offgrid.fits", gridHeader(4, nil))
	badRel := e.dataset(1, "rel_offgrid.fits", gridHeader(8, nil))
	two := e.dataset(1, "psi_two.fits", gridHeader(4, nil))
	relA, relB := e.dataset(1, "rel_a.fits", gridHeader(4, nil)), e.dataset(1, "rel_b.fits", gridHeader(4, nil))
	proj := filepath.Join(e.root, "vgt", "runs")
	e.run(1, filepath.Join(proj, "offgrid"), applyProfile, offGrid, &badRel)
	r := e.run(1, filepath.Join(proj, "two"), applyProfile, two, &relA)
	if _, err := e.cat.DB().Exec(`INSERT INTO run_outputs(run_id,role,product,dataset_id,name) VALUES(?,?,?,?,?)`, r.ID, runs.RoleReliability, "reliability2", relB.ID, relB.Name); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"psi_offgrid.fits", "psi_two.fits"} {
		e.planes.ranges[n] = [2]float64{0, 3}
	}
	_, res, raw := e.apply(f.src.ID, "project", offGrid.ID, two.ID)
	for _, id := range []int64{offGrid.ID, two.ID} {
		row := rowOf(t, res.Rows, id)
		if row.Outcome != "declared" || row.Reliability != nil || !strings.Contains(row.Note, "declared without a reliability map") {
			t.Errorf("dataset %d: %+v\n%s", id, row, raw)
		}
		if got := e.get(id).Interpretation; got == nil || got.ReliabilityID != nil {
			t.Errorf("dataset %d was paired with a reliability map: %+v", id, got)
		}
	}
	if !strings.Contains(rowOf(t, res.Rows, offGrid.ID).Note, "not known to share") || !strings.Contains(rowOf(t, res.Rows, two.ID).Note, "several reliability outputs") {
		t.Errorf("notes: %+v", res.Rows)
	}
}

func TestApplyRefusesWhenTheValuesCannotBeReadAndNeverDeclaresUnchecked(t *testing.T) {
	t.Parallel()
	e := newApplyEnv(t)
	f := newFleet(t, e)
	delete(e.planes.ranges, "psi_good.fits") // the worker answers without extremes (an older worker, or a map with no value)
	_, res, raw := e.apply(f.src.ID, "project", f.good.ID)
	row := rowOf(t, res.Rows, f.good.ID)
	if row.Outcome != "refused" || !strings.Contains(row.Reason, "unit was not checked") || e.get(f.good.ID).Interpretation != nil {
		t.Errorf("%+v\n%s", row, raw)
	}
	// a file that changed since it was registered cannot be checked either
	if err := os.WriteFile(filepath.Join(e.root, "psi_norel.fits"), []byte("changed bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, res2, _ := e.apply(f.src.ID, "project", f.noRel.ID)
	if r := rowOf(t, res2.Rows, f.noRel.ID); r.Outcome != "refused" || !strings.Contains(r.Reason, "could not be read to check") || e.get(f.noRel.ID).Interpretation != nil {
		t.Errorf("%+v", r)
	}
}

func TestRangeRefusalIsOnlyForRadiansBeyondTwoPi(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		unit   string
		lo, hi float64
		refuse bool
	}{
		{"rad", 0, 3.1415927, false}, {"rad", -1.5708, 1.5708, false}, {"rad", 0, 6.2831853, false}, {"rad", 0, 6.2835, false},
		{"rad", 0, 6.3, true}, {"rad", 0, 179.9, true}, {"rad", -7, 0, true},
		{"deg", 0, 179.9, false}, {"deg", 0, 3.1, false}, {"deg", 0, 400, false},
	} {
		if got := rangeRefusal(c.unit, c.lo, c.hi) != ""; got != c.refuse {
			t.Errorf("%s %g..%g: refused %v, want %v", c.unit, c.lo, c.hi, got, c.refuse)
		}
	}
}

func TestApplyAnswersMatchTheContractAndAreAudited(t *testing.T) {
	t.Parallel()
	e := newApplyEnv(t)
	f := newFleet(t, e)
	doc := loadSpec(t)
	_, _, praw := e.preview(f.src.ID, "")
	conforms(t, responseSchemaAt(t, doc, "/api/datasets/{id}/interpretation/apply-preview", "GET", 200, "application/json"), "preview", praw)
	_, _, ineligible := e.preview(f.noRel.ID, "")
	conforms(t, responseSchemaAt(t, doc, "/api/datasets/{id}/interpretation/apply-preview", "GET", 200, "application/json"), "ineligible preview", ineligible)
	code, res, araw := e.apply(f.src.ID, "project", f.good.ID, f.noRel.ID, f.declared.ID, f.wide.ID)
	if code != 200 {
		t.Fatalf("%d %s", code, araw)
	}
	conforms(t, responseSchemaAt(t, doc, "/api/interpretations/apply", "POST", 200, "application/json"), "apply result", araw)
	conforms(t, requestSchema(t, doc, "/api/interpretations/apply", "POST"), "apply request", mustJSON(t, map[string]any{"sourceId": 1, "scope": "project", "targetIds": []int64{2}, "confirm": true}))
	_, ub := postJSON(t, e.ts.URL+"/api/interpretations/apply/"+res.ApplyID+"/undo", map[string]any{})
	conforms(t, responseSchemaAt(t, doc, "/api/interpretations/apply/{applyId}/undo", "POST", 200, "application/json"), "undo result", ub)
	// the dataset the copy is recorded on still matches the Dataset schema with the new fields
	e.apply(f.src.ID, "project", f.good.ID)
	_, db := get(t, e.ts.URL+"/api/datasets/"+itoa(int(f.good.ID)))
	var ds struct {
		Interpretation json.RawMessage `json:"interpretation"`
	}
	json.Unmarshal(db, &ds)
	conforms(t, doc.Components.Schemas["Interpretation"], "an applied declaration", ds.Interpretation)
	entries, err := e.srv.Audit.List(workspace.Local, "", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var sawApply, sawUndo bool
	for _, a := range entries {
		sawApply = sawApply || strings.Contains(a.Detail, "apply "+res.ApplyID+": 2 declared")
		sawUndo = sawUndo || strings.Contains(a.Detail, "apply "+res.ApplyID+": 2 removed")
	}
	if !sawApply || !sawUndo {
		t.Errorf("audit: apply %v, undo %v: %+v", sawApply, sawUndo, entries)
	}
}

// The range comes from the real worker's axial reply: a degrees file declared as radians is refused by the extremes alone.
func TestValueRangeIsReadFromTheRealWorkersAxialReply(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	py, _ := exec.LookPath("python3")
	e := newApplyEnv(t)
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), e.root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	e.srv.Planes = headerWorker(t, py, repo)
	e.srv.Worker = headerWorker(t, py, repo)
	reg := func(name string) catalog.Dataset {
		var d catalog.Dataset
		json.NewDecoder(post(t, e.ts, filepath.Join(e.root, name), nil).Body).Decode(&d)
		if d.ID == 0 {
			t.Fatalf("%s was not registered", name)
		}
		return d
	}
	deg, rad := reg("vgt_psi_deg.fits"), reg("vgt_psi_rad.fits")
	for _, c := range []struct {
		d      catalog.Dataset
		unit   string
		refuse bool
	}{{deg, "rad", true}, {deg, "deg", false}, {rad, "rad", false}} {
		lo, hi, why := e.srv.valueRange(context.Background(), c.d, c.unit)
		if why != "" {
			t.Fatalf("%s as %s: %s", c.d.Name, c.unit, why)
		}
		if got := rangeRefusal(c.unit, *lo, *hi) != ""; got != c.refuse {
			t.Errorf("%s declared %s (values %g to %g): refused %v, want %v", c.d.Name, c.unit, *lo, *hi, got, c.refuse)
		}
	}
}
