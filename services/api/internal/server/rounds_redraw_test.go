package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/review"
)

// A figure drawn again through the real save route (ADR 0042) is the same figure in the next review round (ADR 0073), and the
// owner-side link that says so never reaches the reviewer's document.
func TestADrawAgainSaveIsARedrawnFigureInTheNextRoundAndTheLinkStaysOwnerSide(t *testing.T) {
	t.Parallel()
	e := newIDEnv(t)
	d := e.mustRegister(e.write("map.fits", []byte("0123456789abcdef")))
	_, orig, b := e.saveFigure(d, renderedScene(d.ID, true), nil)
	if orig.ID == 0 {
		t.Fatalf("%s", b)
	}
	code, bb := e.call("POST", "/api/boards", map[string]any{"title": "Board"})
	if code/100 != 2 {
		t.Fatalf("board: %d %s", code, bb)
	}
	var board boards.Board
	json.Unmarshal(bb, &board)
	base := "/api/boards/" + itoa(int(board.ID))
	if code, out := e.call("POST", base+"/items", map[string]any{"figureId": orig.ID}); code/100 != 2 {
		t.Fatalf("add: %d %s", code, out)
	}
	publish := func(label string) string {
		code, out := e.call("POST", base+"/publish", map[string]any{"label": label, "hours": 24})
		if code != 201 {
			t.Fatalf("publish %s: %d %s", label, code, out)
		}
		var pub struct{ Token string }
		json.Unmarshal(out, &pub)
		return pub.Token
	}
	publish("v1")

	code, re, b := e.saveFigure(d, renderedScene(d.ID, true), map[string]any{"redrawnFrom": orig.ID})
	if code != 201 {
		t.Fatalf("redrawing: %d %s", code, b)
	}
	if code, out := e.call("POST", base+"/items/0/remove", map[string]any{}); code/100 != 2 {
		t.Fatalf("remove: %d %s", code, out)
	}
	if code, out := e.call("POST", base+"/items", map[string]any{"figureId": re.ID}); code/100 != 2 {
		t.Fatalf("add redrawing: %d %s", code, out)
	}
	tok2 := publish("v2")

	rv := httptest.NewServer((&review.Handler{Store: e.srv.Boards}).Handler())
	defer rv.Close()
	r, raw := get(t, rv.URL+"/review-api/"+tok2)
	if r.StatusCode != 200 {
		t.Fatalf("review: %d %s", r.StatusCode, raw)
	}
	var doc struct {
		Snapshot struct {
			Round struct {
				Number int
				Since  struct {
					Items []struct{ State string }
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Snapshot.Round.Number != 2 || len(doc.Snapshot.Round.Since.Items) != 1 || doc.Snapshot.Round.Since.Items[0].State != "redrawn" {
		t.Errorf("the redrawing should be one redrawn figure, not a removal and an addition: %s", raw)
	}
	for _, secret := range []string{"redrawnFrom", orig.Revision} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("%q reached the reviewer: %s", secret, raw)
		}
	}
}
