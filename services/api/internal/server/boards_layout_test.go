package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/review"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

// The layout route (ADR 0060): one write for the whole board, guarded by the panels it was computed for, answering 409 with the
// board as it is when they differ; the owner's board and the reviewer's frozen copy both carry the place; and the reviewer's
// listener has no way to write one.
func TestBoardLayoutRouteSavesGuardsAndReachesTheReviewerReadOnly(t *testing.T) {
	base := t.TempDir()
	rs, _ := sourceroot.New([]string{base})
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	bs, _ := boards.Open(cat.DB(), base)
	owner := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: hdrWorker{}, Boards: bs}).Handler())
	defer owner.Close()
	rv := httptest.NewServer((&review.Handler{Store: bs}).Handler())
	defer rv.Close()

	png, _ := base64.StdEncoding.DecodeString(testPNG(t))
	b, _ := bs.CreateBoard("B")
	var expect []map[string]any
	for i, name := range []string{"A", "B"} {
		f, err := bs.CreateFigure(boards.NewFigure{DatasetID: 1, Title: name, Provenance: json.RawMessage(`{"i":` + itoa(i) + `}`)}, png)
		if err != nil {
			t.Fatal(err)
		}
		bs.AddItem(b.ID, f.ID)
		expect = append(expect, map[string]any{"kind": "figure", "id": f.ID})
	}
	url := owner.URL + "/api/boards/" + itoa(int(b.ID)) + "/layout"
	rect := func(x, y, w, h int) map[string]any { return map[string]any{"x": x, "y": y, "w": w, "h": h} }
	good := []any{rect(0, 0, 6, 7), rect(6, 0, 6, 7)}

	var saved boards.Board
	if r, out := postJSON(t, url, map[string]any{"layout": good, "expect": expect, "from": []any{nil, nil}}); r.StatusCode != 200 {
		t.Fatalf("save: %d %s", r.StatusCode, out)
	} else if json.Unmarshal(out, &saved); saved.Items[1].Layout == nil || *saved.Items[1].Layout != (boards.Rect{X: 6, Y: 0, W: 6, H: 7}) {
		t.Fatalf("the reply does not carry the layout: %s", out)
	}
	// a stale second window: it still believes there is no layout
	r, out := postJSON(t, url, map[string]any{"layout": []any{rect(0, 0, 12, 7), rect(0, 7, 12, 7)}, "expect": expect, "from": []any{nil, nil}})
	var conflict struct {
		Error string
		Board boards.Board
	}
	json.Unmarshal(out, &conflict)
	if r.StatusCode != 409 || conflict.Board.Items[0].Layout == nil || conflict.Board.Items[0].Layout.W != 6 {
		t.Fatalf("stale from: %d %s", r.StatusCode, out)
	}
	if r, _ := postJSON(t, url, map[string]any{"layout": []any{rect(0, 0, 6, 7), rect(3, 0, 6, 7)}, "expect": expect}); r.StatusCode != 422 {
		t.Fatalf("overlap: %d", r.StatusCode)
	}
	if r, _ := postJSON(t, url, map[string]any{"layout": good}); r.StatusCode != 400 {
		t.Fatalf("expect is required: %d", r.StatusCode)
	}
	if r, _ := postJSON(t, url, map[string]any{"layout": good, "expect": expect, "extra": 1}); r.StatusCode != 400 {
		t.Fatalf("unknown field: %d", r.StatusCode)
	}
	if r, _ := postJSON(t, owner.URL+"/api/boards/999/layout", map[string]any{"layout": []any{}, "expect": []any{}}); r.StatusCode != 404 {
		t.Fatalf("unknown board: %d", r.StatusCode)
	}

	// the reviewer gets the layout in the frozen copy and has no route to change it
	pr, pb := postJSON(t, owner.URL+"/api/boards/"+itoa(int(b.ID))+"/publish", map[string]any{"label": "v1", "hours": 24})
	if pr.StatusCode != 201 && pr.StatusCode != 200 {
		t.Fatalf("publish: %d %s", pr.StatusCode, pb)
	}
	var pub struct{ ReviewPath string }
	json.Unmarshal(pb, &pub)
	var tok string
	if i := strings.LastIndex(pub.ReviewPath, "/"); i >= 0 {
		tok = pub.ReviewPath[i+1:]
	}
	rr, rb := get(t, rv.URL+"/review-api/"+tok)
	if rr.StatusCode != 200 || !strings.Contains(string(rb), `"layout":{"x":6,"y":0,"w":6,"h":7}`) {
		t.Fatalf("reviewer copy: %d %s", rr.StatusCode, rb)
	}
	for _, p := range []string{"/review-api/" + tok + "/layout", "/api/boards/" + itoa(int(b.ID)) + "/layout"} {
		if r, _ := postJSON(t, rv.URL+p, map[string]any{"layout": good, "expect": expect}); r.StatusCode != 404 && r.StatusCode != 405 {
			t.Fatalf("the reviewer listener answered %s with %d", p, r.StatusCode)
		}
	}
}
