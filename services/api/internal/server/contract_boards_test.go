package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

// byNameWorker reports a header with a BUNIT for files named with-unit and one without for the rest, as the real worker
// does for a file that declares none, so both shapes of a figure's provenance are exercised.
type byNameWorker struct{}

func (byNameWorker) Header(ctx context.Context, p string) (json.RawMessage, error) {
	if strings.Contains(p, "with-unit") {
		return hdrWorker{}.Header(ctx, p)
	}
	return unitlessWorker{}.Header(ctx, p)
}

// A request body the spec describes must be one the spec accepts, and the response one the spec describes: both are
// checked on the real handlers (capabilities, figures, boards, publish).
func TestResponsesMatchTheSpecForCapabilitiesFiguresAndBoards(t *testing.T) {
	doc := loadSpec(t)
	base := t.TempDir()
	root := filepath.Join(base, "data")
	os.MkdirAll(root, 0o755)
	rs, _ := sourceroot.New([]string{root})
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	bs, err := boards.Open(cat.DB(), base)
	if err != nil {
		t.Fatal(err)
	}
	js, err := jobs.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Roots: rs, Store: cat, Worker: byNameWorker{}, Boards: bs, Jobs: js}
	srv.progress.read = func(ctx context.Context, j jobs.Job) (string, error) { return chunkLog(4, 16), nil }
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// call sends a body that must conform to the operation's request schema and checks the answer against the response
	// schema the spec declares for the status that came back.
	call := func(method, specPath, url string, in any, wantStatus int) []byte {
		t.Helper()
		var resp *http.Response
		var out []byte
		if in != nil {
			raw, _ := json.Marshal(in)
			conforms(t, requestSchema(t, doc, specPath, method), method+" "+specPath+" request", raw)
			resp, out = postJSON(t, url, in)
		} else {
			resp, out = get(t, url)
		}
		if resp.StatusCode != wantStatus {
			t.Fatalf("%s %s: %d, want %d: %s", method, url, resp.StatusCode, wantStatus, out)
		}
		conforms(t, responseSchemaAt(t, doc, specPath, method, wantStatus, "application/json"), method+" "+specPath, out)
		return out
	}
	api := ts.URL

	call("GET", "/api/capabilities", api+"/api/capabilities", nil, 200)

	// two datasets: one whose header has a BUNIT, one without (the author declares its unit and nature)
	register := func(name string) int64 {
		f := filepath.Join(root, name)
		os.WriteFile(f, []byte("x"), 0o644)
		var d catalog.Dataset
		json.NewDecoder(post(t, ts, f, nil).Body).Decode(&d)
		return d.ID
	}
	withUnit, noUnit := register("with-unit.fits"), register("no-unit.fits")
	if r, b := postJSON(t, api+"/api/datasets/"+itoa(int(noUnit))+"/unit", map[string]any{"unit": "Jy/beam"}); r.StatusCode != 200 {
		t.Fatalf("declare unit: %d %s", r.StatusCode, b)
	}
	if r, b := postJSON(t, api+"/api/datasets/"+itoa(int(noUnit))+"/nature", map[string]any{"nature": "simulated"}); r.StatusCode != 200 {
		t.Fatalf("nature: %d %s", r.StatusCode, b)
	}

	// figures: a plain plane view, and a fully described one with a region and limits in the declared unit
	call("GET", "/api/figures", api+"/api/figures", nil, 200) // empty list
	f1 := map[string]any{"datasetId": withUnit, "title": "Peak channel", "caption": "H I", "previewPng": testPNG(t),
		"view": map[string]any{"kind": "plane", "channel": 0, "colormap": "viridis", "stretch": "linear", "range": "percentile"}}
	var fig1, fig2 boards.Figure
	json.Unmarshal(call("POST", "/api/figures", api+"/api/figures", f1, 201), &fig1)
	f2 := map[string]any{"datasetId": noUnit, "title": "Declared", "previewPng": testPNG(t),
		"view": map[string]any{"kind": "plane", "channel": 1, "spectralValue": 8.5, "spectralUnit": "km/s", "colormap": "gray", "stretch": "asinh", "range": "custom",
			"limits": []float64{0, 2}, "limitsUnit": "Jy/beam", "showLines": true,
			"region": map[string]any{"shape": "rect", "x0": 1, "y0": 1, "x1": 4, "y1": 4}},
		// the scene/1 it was drawn from: the stored Figure.scene is validated against the Scene schema on the way out
		"scene": map[string]any{"schema": "scene/1",
			"base": map[string]any{"datasetId": noUnit, "channel": 1, "spectral": map[string]any{"value": 8.5, "unit": "km/s"},
				"display": map[string]any{"mapping": "scalar", "colormap": "gray", "stretch": "asinh", "range": "custom", "limits": []float64{0, 2}, "limitsUnit": "Jy/beam"}},
			"overlays": []any{map[string]any{"id": "regions", "kind": "regions", "datasetId": noUnit, "visible": true,
				"active": map[string]any{"shape": "rect", "x0": 1, "y0": 1, "x1": 4, "y1": 4}, "saved": []any{}}},
			"view": map[string]any{"zoom": 1},
			// how the base raster was drawn, so the figure can be drawn again
			"render": map[string]any{"canvas": map[string]any{"width": 16, "height": 12}, "nodata": []any{[]any{27, 34, 44}, []any{85, 98, 117}}, "figureWidth": 720}}}
	json.Unmarshal(call("POST", "/api/figures", api+"/api/figures", f2, 201), &fig2)
	if len(fig2.Scene) == 0 {
		t.Fatal("the figure did not keep its scene")
	}
	// "draw again": the check before drawing, and a redrawing saved as a new revision linked to the original; both conform
	var check redrawResult
	json.Unmarshal(call("GET", "/api/figures/{id}/redraw-check", api+"/api/figures/"+itoa(int(fig2.ID))+"/redraw-check", nil, 200), &check)
	if check.State != "ready" || len(check.Sources) != 1 {
		t.Fatalf("redraw check: %+v", check)
	}
	call("GET", "/api/figures/{id}/redraw-check", api+"/api/figures/"+itoa(int(fig1.ID))+"/redraw-check", nil, 200) // a figure with no description: state none
	f3 := map[string]any{}
	for k, v := range f2 {
		f3[k] = v
	}
	f3["redrawnFrom"] = fig2.ID
	var fig3 boards.Figure
	json.Unmarshal(call("POST", "/api/figures", api+"/api/figures", f3, 201), &fig3)
	if fig3.ID == fig2.ID || !strings.Contains(string(fig3.Identity), `"redrawnFrom"`) {
		t.Fatalf("the redrawing: %+v", fig3)
	}
	call("GET", "/api/figures", api+"/api/figures", nil, 200)
	call("POST", "/api/figures/{id}/text", api+"/api/figures/"+itoa(int(fig1.ID))+"/text", map[string]any{"title": "Peak", "caption": "edited"}, 200)

	for _, route := range []string{"preview", "download"} {
		r, b := get(t, api+"/api/figures/"+itoa(int(fig1.ID))+"/"+route)
		want := doc.Paths.Find("/api/figures/{id}/" + route).GetOperation("GET").Responses.Status(200).Value.Content.Get("image/png")
		if want == nil || r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" || len(b) < 8 || string(b[1:4]) != "PNG" {
			t.Errorf("figure %s: %d %q (spec describes image/png: %v)", route, r.StatusCode, r.Header.Get("Content-Type"), want != nil)
		}
	}

	// boards: create, add figures and a job, then edit; every reply is the board
	var b boards.Board
	json.Unmarshal(call("POST", "/api/boards", api+"/api/boards", map[string]any{"title": "For review"}, 201), &b)
	bid := itoa(int(b.ID))
	call("GET", "/api/boards", api+"/api/boards", nil, 200)
	items := api + "/api/boards/" + bid + "/items"
	call("POST", "/api/boards/{id}/items", items, map[string]any{"figureId": fig1.ID}, 200)
	call("POST", "/api/boards/{id}/items", items, map[string]any{"figureId": fig2.ID}, 200)
	j, err := js.CreateDraft(jobs.Job{HostID: 1, Name: "vgt-ker16", Template: "vgt", Script: "#!/bin/bash\n", OutputDir: "/x", Params: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	js.MarkSubmitted(j.ID, "1")
	js.SetState(j.ID, jobs.StateRunning, "running")
	call("POST", "/api/boards/{id}/items", items, map[string]any{"jobId": j.ID}, 200)
	live := call("GET", "/api/boards/{id}", api+"/api/boards/"+bid, nil, 200) // reads the job log, so the panel carries progress
	if !strings.Contains(string(live), `"progress"`) {
		t.Errorf("the job panel should carry progress so that branch of the spec is exercised: %s", live)
	}
	call("GET", "/api/boards", api+"/api/boards", nil, 200)
	item := func(pos int, op string) string { return api + "/api/boards/" + bid + "/items/" + itoa(pos) + "/" + op }
	call("POST", "/api/boards/{id}/items/{pos}/note", item(0, "note"), map[string]any{"note": "look here"}, 200)
	call("POST", "/api/boards/{id}/items/{pos}/move", item(1, "move"), map[string]any{"delta": -1}, 200)
	call("POST", "/api/boards/{id}/items/{pos}/pin", item(2, "pin"), map[string]any{"figureId": fig1.ID}, 200)
	call("POST", "/api/boards/{id}/items/{pos}/remove", item(2, "remove"), map[string]any{}, 200)
	// a job panel left on the board shows up as a frozen placeholder in the preflight and the snapshot
	call("POST", "/api/boards/{id}/items", items, map[string]any{"jobId": j.ID}, 200)

	pf := call("GET", "/api/boards/{id}/preflight", api+"/api/boards/"+bid+"/preflight?question=Is+it+real", nil, 200)
	if !strings.Contains(string(pf), `"kind":"job"`) {
		t.Errorf("the preflight should freeze the job panel: %s", pf)
	}
	call("GET", "/api/boards/{id}/snapshots", api+"/api/boards/"+bid+"/snapshots", nil, 200) // none yet
	var pub struct {
		Snapshot boards.Snapshot `json:"snapshot"`
		Token    string          `json:"token"`
	}
	json.Unmarshal(call("POST", "/api/boards/{id}/publish", api+"/api/boards/"+bid+"/publish", map[string]any{"label": "v1", "hours": 24, "question": "Is it real?"}, 201), &pub)
	call("POST", "/api/boards/{id}/publish", api+"/api/boards/"+bid+"/publish", map[string]any{"label": "v2", "hours": 24, "supersedes": 1, "reason": "rescaled"}, 201)
	var snaps struct{ Snapshots []json.RawMessage }
	json.Unmarshal(call("GET", "/api/boards/{id}/snapshots", api+"/api/boards/"+bid+"/snapshots", nil, 200), &snaps)
	if len(snaps.Snapshots) != 2 {
		t.Fatalf("want 2 snapshots, got %d", len(snaps.Snapshots))
	}

	// the author's side of a review: read the reviewers' comments, answer in a thread, close it, end the link
	sid := itoa(int(pub.Snapshot.ID))
	call("GET", "/api/snapshots/{id}/comments", api+"/api/snapshots/"+sid+"/comments", nil, 200) // none yet: an empty list, not null
	first, err := bs.AddComment(pub.Token, boards.CommentInput{FigureRevision: fig1.Revision, Author: "Sam", Body: "Is this a ring?", Anchor: &boards.Anchor{Kind: "point", X: 0.2, Y: 0.3}})
	if err != nil {
		t.Fatal(err)
	}
	call("POST", "/api/snapshots/{id}/comments", api+"/api/snapshots/"+sid+"/comments", map[string]any{"parentId": first.ID, "body": "An artefact"}, 201)
	list := call("GET", "/api/snapshots/{id}/comments", api+"/api/snapshots/"+sid+"/comments", nil, 200)
	if !strings.Contains(string(list), `"fromOwner":true`) {
		t.Errorf("the author's reply should be listed: %s", list)
	}
	call("POST", "/api/comments/{id}/resolve", api+"/api/comments/"+itoa(int(first.ID))+"/resolve", map[string]any{"resolved": true}, 200)
	// revoke takes no body (the web client sends {}), so it does not go through call
	rr, rb := postJSON(t, api+"/api/snapshots/"+sid+"/revoke", map[string]any{})
	if rr.StatusCode != 200 {
		t.Fatalf("revoke: %d %s", rr.StatusCode, rb)
	}
	conforms(t, responseSchema(t, doc, "/api/snapshots/{id}/revoke", "POST", "application/json"), "POST /api/snapshots/{id}/revoke", rb)

	// the spec names the Error shape for failures of these routes too
	for _, u := range []string{"/api/boards/999"} {
		r, out := get(t, api+u)
		if r.StatusCode != 404 {
			t.Fatalf("%s: %d", u, r.StatusCode)
		}
		conforms(t, doc.Components.Schemas["Error"], u+" 404 body", out)
	}
}
