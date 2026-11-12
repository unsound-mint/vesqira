package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

// The undo of a board edit names the panel it expects at a position. The server applies the edit only if that panel is still
// there, answers 409 with the board as it is otherwise, and still serves clients that send no expectation.
func TestBoardItemEditsHonourTheExpectedPanel(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	rs, _ := sourceroot.New([]string{base})
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	bs, _ := boards.Open(cat.DB(), base)
	ts := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: hdrWorker{}, Boards: bs}).Handler())
	defer ts.Close()

	png, _ := base64.StdEncoding.DecodeString(testPNG(t))
	var figs []boards.Figure
	for i, name := range []string{"A", "B", "C"} {
		f, err := bs.CreateFigure(boards.NewFigure{DatasetID: 1, Title: name, Provenance: json.RawMessage(`{"i":` + itoa(i) + `}`)}, png)
		if err != nil {
			t.Fatal(err)
		}
		figs = append(figs, f)
	}
	b, _ := bs.CreateBoard("B")
	for _, f := range figs {
		bs.AddItem(b.ID, f.ID)
	}
	item := func(op string, pos int) string {
		return ts.URL + "/api/boards/" + itoa(int(b.ID)) + "/items/" + itoa(pos) + "/" + op
	}
	titles := func() string {
		cur, _ := bs.Board(b.ID)
		s := ""
		for _, it := range cur.Items {
			s += it.Figure.Title
		}
		return s
	}
	exp := func(f boards.Figure, extra map[string]any) map[string]any {
		e := map[string]any{"kind": "figure", "id": f.ID}
		for k, v := range extra {
			e[k] = v
		}
		return e
	}

	// another window moved B before A since this client read the board: its undo of "move A down" expects A at position 1
	bs.MoveItem(b.ID, 1, -1, nil)                                                                    // BAC
	r, out := postJSON(t, item("move", 1), map[string]any{"delta": -1, "expect": exp(figs[0], nil)}) // A is at 1
	if r.StatusCode != 200 || titles() != "ABC" {
		t.Fatalf("matching expect: %d %s %s", r.StatusCode, out, titles())
	}
	r, out = postJSON(t, item("move", 1), map[string]any{"delta": 1, "expect": exp(figs[0], nil)}) // B is at 1 now
	var got struct {
		Error string
		Board boards.Board
	}
	json.Unmarshal(out, &got)
	if r.StatusCode != 409 || titles() != "ABC" || got.Error == "" || len(got.Board.Items) != 3 || got.Board.Items[1].Figure == nil || got.Board.Items[1].Figure.Title != "B" {
		t.Fatalf("mismatch: %d %s %s", r.StatusCode, out, titles())
	}

	if r, _ := postJSON(t, item("remove", 2), map[string]any{"expect": exp(figs[1], nil)}); r.StatusCode != 409 || titles() != "ABC" {
		t.Fatalf("remove mismatch: %d %s", r.StatusCode, titles())
	}
	if r, _ := postJSON(t, item("note", 0), map[string]any{"note": "x", "expect": exp(figs[0], map[string]any{"note": "stale"})}); r.StatusCode != 409 {
		t.Fatalf("note mismatch: %d", r.StatusCode)
	}
	if cur, _ := bs.Board(b.ID); cur.Items[0].Note != "" {
		t.Fatal("a refused note edit changed the note")
	}
	if r, out := postJSON(t, item("note", 0), map[string]any{"note": "x", "expect": exp(figs[0], map[string]any{"note": ""})}); r.StatusCode != 200 {
		t.Fatalf("note match: %d %s", r.StatusCode, out)
	}
	if r, out := postJSON(t, item("remove", 2), map[string]any{"expect": exp(figs[2], nil)}); r.StatusCode != 200 || titles() != "AB" {
		t.Fatalf("remove match: %d %s %s", r.StatusCode, out, titles())
	}

	// clients that send no expectation keep working, including an empty remove body
	if r, _ := postJSON(t, item("move", 0), map[string]any{"delta": 1}); r.StatusCode != 200 || titles() != "BA" {
		t.Fatalf("old move: %d %s", r.StatusCode, titles())
	}
	if r, _ := postJSON(t, item("remove", 0), map[string]any{}); r.StatusCode != 200 || titles() != "A" {
		t.Fatalf("old remove: %d %s", r.StatusCode, titles())
	}
	if r, _ := postJSON(t, item("remove", 5), map[string]any{}); r.StatusCode != 404 {
		t.Fatalf("unknown position: %d", r.StatusCode)
	}
	if r, _ := postJSON(t, item("remove", 5), map[string]any{"expect": exp(figs[0], nil)}); r.StatusCode != 409 {
		t.Fatalf("guarded unknown position: %d", r.StatusCode)
	}
}
