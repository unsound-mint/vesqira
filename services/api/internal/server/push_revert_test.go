package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/pushed"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Putting a figure's caption back and pushing again makes the earlier version current again on the server, under the same
// lineage: push A, push B, push A again, and the head is A. Nothing is duplicated, the history reads A, B, A, and the local queue
// shows what the server now shows.
func TestPushingAnEarlierVersionAgainMakesItCurrent(t *testing.T) {
	g, ts, ps := pushGroup(t)
	full, _ := g.setupAdmin("ada")
	token := g.mintPush(full)
	l := newPushLocal(t, ts)
	l.link(t, g.origin, token)
	caption := func(c string) {
		t.Helper()
		if st, b := callJSON(t, "POST", l.ts.URL+"/api/figures/"+itoa(int(l.figure))+"/text", map[string]string{"title": "Peak channel", "caption": c}); st != 200 {
			t.Fatalf("caption: %d %s", st, b)
		}
	}
	push := func() string {
		t.Helper()
		e := l.enqueue(t, "figure", l.figure)
		l.s.OutboxPass(context.Background())
		if q := l.queue(t); q[0].Status != "pushed" {
			t.Fatalf("queue: %+v", q[0])
		}
		return e.Hash
	}
	caption("version A")
	a := push()
	caption("version B")
	b := push()
	caption("version A")
	if again := push(); again != a {
		t.Fatalf("the same content froze to another hash: %s %s", again, a)
	}
	link, _ := l.s.Outbox.Link()
	heads, _ := ps.Heads(workspace.Local, g.mustAccount("ada").ID, link.Instance)
	if len(heads) != 1 || heads[0].Hash != a {
		t.Fatalf("the head is not A: %+v (B is %s)", heads, b)
	}
	if ps.Count(workspace.Local, a) != 1 || ps.Count(workspace.Local, b) != 1 {
		t.Fatal("a version was stored twice")
	}
	var det struct {
		Item pushed.Detail `json:"item"`
	}
	json.Unmarshal(g.do(req{path: "/api/pushed/" + itoa(int(heads[0].ID)), cookie: full}).Body.Bytes(), &det)
	if n := len(det.Item.Lineage); n != 3 || det.Item.Lineage[0].Hash != a || det.Item.Lineage[1].Hash != b || det.Item.Lineage[2].Hash != a || det.Item.SupersededBy != 0 {
		t.Fatalf("history: %+v", det.Item.Lineage)
	}
	if kind, id, err := l.s.Outbox.Origin(det.Item.Item.Origin); err != nil || kind != "figure" || id != l.figure {
		t.Fatal("the lineage does not lead back to the figure")
	}
	var head string
	_ = l.s.Store.DB().QueryRow(`SELECT head_hash FROM push_lineage WHERE origin=?`, det.Item.Item.Origin).Scan(&head)
	if head != a {
		t.Fatalf("the local lineage head is %s, the server's is A", head)
	}
}
