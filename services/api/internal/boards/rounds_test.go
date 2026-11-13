package boards

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

const ws1 = workspace.Local

// provFig saves a figure with the given provenance document.
func provFig(t *testing.T, s *Store, title string, shade uint8, prov string) Figure {
	t.Helper()
	f, err := s.CreateFigure(ws1, "local-owner", NewFigure{DatasetID: 7, Title: title, Caption: "c", Provenance: json.RawMessage(prov)}, pngBytes(t, 8, 6, shade))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// redraw saves a drawing of `of` with other pixels, linked the way "draw again" links it (ADR 0042).
func redraw(t *testing.T, s *Store, of Figure, shade uint8, prov string) Figure {
	t.Helper()
	id := fmt.Sprintf(`{"redrawnFrom":{"figureId":%d,"revision":%q}}`, of.ID, of.Revision)
	f, err := s.CreateFigure(ws1, "local-owner", NewFigure{DatasetID: 7, Title: of.Title, Caption: of.Caption, Provenance: json.RawMessage(prov),
		Identity: json.RawMessage(id), RedrawnFrom: of.Revision}, pngBytes(t, 8, 6, shade))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func publish(t *testing.T, s *Store, b Board, label string) (Snapshot, string) {
	t.Helper()
	sn, tok, err := s.Publish(ws1, "local-owner", b.ID, label, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return sn, tok
}

func setItems(t *testing.T, s *Store, b Board, figs ...Figure) {
	t.Helper()
	cur, _ := s.Board(ws1, b.ID)
	for range cur.Items {
		if err := s.RemoveItem(ws1, b.ID, 0, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range figs {
		if err := s.AddItem(ws1, b.ID, f.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func stateOf(r Review, title string) (ItemChange, bool) {
	for i, it := range r.Snapshot.Items {
		if it.Title == title && r.Snapshot.Round != nil && r.Snapshot.Round.Since != nil {
			return r.Snapshot.Round.Since.Items[i], true
		}
	}
	return ItemChange{}, false
}

// roundsFixture is a board published twice: A (caption edited later), B (removed), C (redrawn), D (added).
type roundsFixture struct {
	s                  *Store
	b                  Board
	a, bFig, c, c2, d  Figure
	sn1, sn2           Snapshot
	tok1, tok2         string
	aComment, cComment Comment
}

func newRounds(t *testing.T) roundsFixture {
	t.Helper()
	s, _ := newStore(t)
	x := roundsFixture{s: s}
	x.b, _ = s.CreateBoard(ws1, "local-owner", "Phase separation")
	x.a, x.bFig, x.c = fig(t, s, "A", 1), fig(t, s, "B", 2), fig(t, s, "C", 3)
	setItems(t, s, x.b, x.a, x.bFig, x.c)
	x.sn1, x.tok1 = publish(t, s, x.b, "round one")
	var err error
	x.aComment, err = s.AddComment(x.tok1, CommentInput{FigureRevision: x.a.Revision, Author: "Supervisor", Body: "Is the stretch linear?", Anchor: &Anchor{Kind: "point", X: .5, Y: .5}})
	if err != nil {
		t.Fatal(err)
	}
	x.cComment, err = s.AddComment(x.tok1, CommentInput{FigureRevision: x.c.Revision, Author: "Supervisor", Body: "The ridge looks shifted.", Anchor: &Anchor{Kind: "point", X: .25, Y: .25}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OwnerReply(ws1, x.sn1.ID, CommentInput{ParentID: x.aComment.ID, Body: "Yes, linear."}); err != nil {
		t.Fatal(err)
	}
	// the author edits A's caption, drops B, draws C again and adds D
	if err := s.SetText(ws1, x.a.ID, "A", "A, now with a caption"); err != nil {
		t.Fatal(err)
	}
	x.c2 = redraw(t, s, x.c, 30, `{"dataset":"a.fits","channel":3,"redrawn":true}`)
	x.d = fig(t, s, "D", 4)
	a, _ := s.Figure(ws1, x.a.ID)
	setItems(t, s, x.b, a, x.c2, x.d)
	x.sn2, x.tok2 = publish(t, s, x.b, "round two")
	return x
}

func TestARoundSaysWhatChangedSinceTheRoundBefore(t *testing.T) {
	x := newRounds(t)
	r2, err := x.s.Review(x.tok2)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Snapshot.Round == nil || r2.Snapshot.Round.Number != 2 || r2.Snapshot.Round.Since == nil {
		t.Fatalf("round 2 has no comparison: %+v", r2.Snapshot.Round)
	}
	since := r2.Snapshot.Round.Since
	if since.Label != "round one" || since.At.IsZero() || len(since.Items) != len(r2.Snapshot.Items) {
		t.Fatalf("%+v", since)
	}
	want := map[string]struct {
		state   string
		changed []string
	}{
		"A": {StateUnchanged, nil},
		"C": {StateRedrawn, nil}, // same title, new revision, linked to C
		"D": {StateNew, nil},
	}
	for title, w := range want {
		ch, ok := stateOf(r2, title)
		if !ok || ch.State != w.state {
			t.Errorf("%s: %+v (want %s)", title, ch, w.state)
		}
	}
	// A's caption was edited: the same revision, so the same picture, and the old text is recorded
	ch, _ := stateOf(r2, "A")
	if len(ch.Changed) != 1 || ch.Changed[0] != "caption" || ch.Previous == nil || ch.Previous.Caption != "c" || ch.Previous.PreviewSHA != "" {
		t.Errorf("A: %+v %+v", ch, ch.Previous)
	}
	// C was redrawn: the earlier picture is named
	ch, _ = stateOf(r2, "C")
	if ch.Previous == nil || ch.Previous.PreviewSHA != x.c.PreviewSHA {
		t.Errorf("C: %+v", ch.Previous)
	}
	if len(since.Removed) != 1 || since.Removed[0].Title != "B" || since.RemovedCount != 1 {
		t.Errorf("removed: %+v", since.Removed)
	}
	var kinds []string
	for _, l := range since.Lines {
		kinds = append(kinds, l.Kind+":"+l.Title)
	}
	if got := strings.Join(kinds, ","); got != "caption:A,redrawn:C,added:D,removed:B" {
		t.Errorf("lines: %s", got)
	}
	// round 1 stays as it was published: no round comparison, and nothing was written back to it
	r1, _ := x.s.Review(x.tok1)
	if r1.Snapshot.Round == nil || r1.Snapshot.Round.Number != 1 || r1.Snapshot.Round.Since != nil || r1.Previous != nil {
		t.Errorf("round 1: %+v", r1.Snapshot.Round)
	}
	if r1.Snapshot.Items[0].Caption != "c" {
		t.Error("an edited caption changed a published snapshot")
	}
}

func TestACompleteRedrawIsNotAddedAndRemovedWhenNoLinkExists(t *testing.T) {
	s, _ := newStore(t)
	b, _ := s.CreateBoard(ws1, "local-owner", "b")
	c := fig(t, s, "C", 3)
	setItems(t, s, b, c)
	publish(t, s, b, "1")
	other := fig(t, s, "C", 9) // same title, other evidence, no draw-again link: a different figure
	setItems(t, s, b, other)
	_, tok := publish(t, s, b, "2")
	r, _ := s.Review(tok)
	ch, _ := stateOf(r, "C")
	if ch.State != StateNew || r.Snapshot.Round.Since.RemovedCount != 1 {
		t.Errorf("a figure is never matched by its title: %+v", ch)
	}
}

func TestHistoryListsRoundsWithTheirChanges(t *testing.T) {
	x := newRounds(t)
	list, err := x.s.Snapshots(ws1, x.b.ID)
	if err != nil || len(list) != 2 {
		t.Fatal(err, list)
	}
	if list[0].ID != x.sn2.ID || list[0].Round != 2 || list[1].Round != 1 {
		t.Fatalf("order or numbers: %+v", list)
	}
	if list[1].Changes != nil || list[0].Changes == nil || len(list[0].Changes.Lines) != 4 || list[0].Changes.Carried == 0 || list[0].Changes.SinceLabel != "round one" {
		t.Errorf("%+v", list[0].Changes)
	}
}

func TestSnapshotsFromBeforeRoundsAreComparedByRevisionOnly(t *testing.T) {
	prev := Frozen{Label: "old", CreatedAt: time.Unix(100, 0).UTC(), Items: []FrozenItem{{Revision: "r1", Title: "A"}, {Revision: "r2", Title: "B"}}}
	cur := Frozen{Items: []FrozenItem{{Revision: "r1", Title: "A"}, {Revision: "r3", Title: "B"}}}
	ch := roundChanges(cur, &prev)
	if ch == nil || ch.SinceLabel != "old" || len(ch.Lines) != 2 || ch.Lines[0].Kind != "added" || ch.Lines[1].Kind != "removed" {
		t.Errorf("%+v", ch)
	}
	if roundChanges(cur, nil) != nil {
		t.Error("the first snapshot has nothing to compare with")
	}
	if roundChanges(Frozen{Round: &Round{Number: 1}}, &prev) != nil {
		t.Error("round 1 of a board is not compared")
	}
}

func TestCommentsCarryToTheSameFigureAndLabelTheirRound(t *testing.T) {
	x := newRounds(t)
	r2, _ := x.s.Review(x.tok2)
	got := map[string][]CarriedComment{}
	for _, c := range r2.Snapshot.Round.Since.Carried {
		got[c.FigureRevision] = append(got[c.FigureRevision], c)
	}
	// A: unchanged picture, so the mark stays; the author's reply comes along
	ac := got[x.a.Revision]
	if len(ac) != 2 || ac[0].Round != 1 || ac[0].Body != "Is the stretch linear?" || ac[0].Anchor == nil || ac[0].PictureChanged || !ac[1].FromOwner || ac[1].ParentID != ac[0].ID {
		t.Errorf("A: %+v", ac)
	}
	// C: redrawn, so the comment is on the redrawn figure's revision and the mark (placed on the old drawing) is dropped
	cc := got[x.c2.Revision]
	if len(cc) != 1 || cc[0].Anchor != nil || !cc[0].PictureChanged || cc[0].Round != 1 {
		t.Errorf("C: %+v", cc)
	}
	if len(got[x.c.Revision]) != 0 || len(got[x.bFig.Revision]) != 0 || len(got[x.d.Revision]) != 0 {
		t.Errorf("comments carried to a figure that is not the same: %+v", got)
	}
	if len(r2.Comments) != 0 {
		t.Error("carried comments are not this round's own")
	}
	// a third round carries round 1's comment on again, still labelled round 1, next to round 2's own
	if _, err := x.s.AddComment(x.tok2, CommentInput{FigureRevision: x.a.Revision, Author: "Supervisor", Body: "Better."}); err != nil {
		t.Fatal(err)
	}
	_, tok3 := publish(t, x.s, x.b, "round three")
	r3, _ := x.s.Review(tok3)
	rounds := []int{}
	for _, c := range r3.Snapshot.Round.Since.Carried {
		if c.FigureRevision == x.a.Revision {
			rounds = append(rounds, c.Round)
		}
	}
	if fmt.Sprint(rounds) != "[1 1 2]" {
		t.Errorf("rounds of A's carried comments: %v", rounds)
	}
}

func TestThePreviousPictureIsServedOnlyWhileThePreviousLinkIsValid(t *testing.T) {
	x := newRounds(t)
	if r, _ := x.s.Review(x.tok2); r.Previous == nil || !r.Previous.Available {
		t.Fatalf("%+v", r.Previous)
	}
	if _, err := x.s.ReviewPreview(x.tok2, x.c.PreviewSHA); err != nil {
		t.Fatalf("the earlier picture of a redrawn figure: %v", err)
	}
	// B was removed: its picture is not part of this link, redrawn or not
	if _, err := x.s.ReviewPreview(x.tok2, x.bFig.PreviewSHA); !errors.Is(err, ErrNotFound) {
		t.Errorf("a removed figure's picture: %v", err)
	}
	// expired: the picture goes, the frozen text stays
	if _, err := x.s.db.Exec(`UPDATE snapshots SET expires_at=? WHERE id=?`, time.Now().Add(-time.Minute).Unix(), x.sn1.ID); err != nil {
		t.Fatal(err)
	}
	r, _ := x.s.Review(x.tok2)
	if r.Previous == nil || r.Previous.Available {
		t.Errorf("expired: %+v", r.Previous)
	}
	if ch, _ := stateOf(r, "A"); ch.Previous == nil || ch.Previous.Caption != "c" || len(r.Snapshot.Round.Since.Carried) == 0 {
		t.Error("an expired round's frozen text should stay with the newer snapshot")
	}
	if _, err := x.s.ReviewPreview(x.tok2, x.c.PreviewSHA); !errors.Is(err, ErrNotFound) {
		t.Errorf("an expired round's picture was served: %v", err)
	}
	if _, err := x.s.ReviewPreview(x.tok2, x.c2.PreviewSHA); err != nil {
		t.Errorf("this round's own picture: %v", err)
	}
}

func TestARevokedRoundIsNotReachableFromTheNewLink(t *testing.T) {
	x := newRounds(t)
	if err := x.s.Revoke(ws1, x.sn1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := x.s.Review(x.tok1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the revoked link: %v", err)
	}
	r, err := x.s.Review(x.tok2)
	if err != nil {
		t.Fatal(err)
	}
	if r.Previous == nil || r.Previous.Available {
		t.Errorf("%+v", r.Previous)
	}
	since := r.Snapshot.Round.Since
	if len(since.Carried) != 0 || len(since.Removed) != 0 || since.RemovedCount != 1 {
		t.Errorf("a revoked round's comments or titles reached the new link: %+v", since)
	}
	for i, ch := range since.Items {
		if ch.Previous != nil {
			t.Errorf("item %d keeps text or a picture of the revoked round: %+v", i, ch.Previous)
		}
	}
	for _, l := range since.Lines {
		if l.Kind == "removed" || l.Detail != "" {
			t.Errorf("line from the revoked round: %+v", l)
		}
	}
	raw, _ := json.Marshal(r)
	for _, secret := range []string{"Is the stretch linear?", "The ridge looks shifted.", "Yes, linear.", x.c.PreviewSHA, x.bFig.PreviewSHA} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the new link's document holds %q from the revoked round", secret)
		}
	}
	// what the new round holds is still marked
	if ch, _ := stateOf(r, "C"); ch.State != StateRedrawn {
		t.Errorf("C: %+v", ch)
	}
	if _, err := x.s.ReviewPreview(x.tok2, x.c.PreviewSHA); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked round's picture: %v", err)
	}
}

func TestPublishingAgainstARevokedRoundCopiesNothingFromIt(t *testing.T) {
	x := newRounds(t)
	x.s.Revoke(ws1, x.sn2.ID)
	if _, err := x.s.AddComment(x.tok1, CommentInput{FigureRevision: x.a.Revision, Author: "Supervisor", Body: "late"}); err != nil {
		t.Fatal(err)
	}
	_, tok3 := publish(t, x.s, x.b, "three")
	r3, _ := x.s.Review(tok3)
	since := r3.Snapshot.Round.Since
	if since.Label != "" || len(since.Carried) != 0 { // the revoked round's label is withheld too
		t.Errorf("%+v", since)
	}
	raw, _ := json.Marshal(r3)
	if strings.Contains(string(raw), "Is the stretch linear?") {
		t.Error("round 1's comment came through a revoked round 2")
	}
}

func TestAFigureShareIsInNoBoardsHistory(t *testing.T) {
	x := newRounds(t)
	shareSn, shareTok, err := x.s.PublishFigure(ws1, "local-owner", x.a.ID, PublishOptions{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if shareSn.Round != 0 {
		t.Errorf("a figure share was numbered: %+v", shareSn)
	}
	r, _ := x.s.Review(shareTok)
	if r.Snapshot.Round != nil || r.Previous != nil {
		t.Errorf("a figure share has a round: %+v", r.Snapshot.Round)
	}
	list, _ := x.s.Snapshots(ws1, x.b.ID)
	if len(list) != 2 {
		t.Errorf("the share is in the board's history: %d", len(list))
	}
	shares, _ := x.s.FigureShares(ws1, "")
	if len(shares) != 1 || shares[0].Round != 0 || shares[0].Changes != nil {
		t.Errorf("%+v", shares)
	}
	// a second share of the same figure compares with nothing, and the next round of the board skips both shares
	if _, _, err := x.s.PublishFigure(ws1, "local-owner", x.a.ID, PublishOptions{TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	_, tok3 := publish(t, x.s, x.b, "three")
	r3, _ := x.s.Review(tok3)
	if r3.Snapshot.Round.Number != 3 || r3.Snapshot.Round.Since.Label != "round two" {
		t.Errorf("shares changed the round history: %+v", r3.Snapshot.Round)
	}
}

func TestRoundsStayInsideTheirWorkspace(t *testing.T) {
	x := newRounds(t)
	// another workspace's board with the same id space has its own history
	b2, _ := x.s.CreateBoard(otherWS, "someone", "mine")
	f, err := x.s.CreateFigure(otherWS, "someone", NewFigure{DatasetID: 7, Title: "A", Provenance: json.RawMessage(`{"dataset":"a.fits","channel":3}`)}, pngBytes(t, 8, 6, 1))
	if err != nil {
		t.Fatal(err)
	}
	x.s.AddItem(otherWS, b2.ID, f.ID)
	_, tok, err := x.s.Publish(otherWS, "someone", b2.ID, "first", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := x.s.Review(tok)
	if r.Snapshot.Round.Number != 1 || r.Snapshot.Round.Since != nil {
		t.Errorf("%+v", r.Snapshot.Round)
	}
	if list, _ := x.s.Snapshots(otherWS, x.b.ID); len(list) != 0 {
		t.Error("history of another workspace's board listed")
	}
	// a redraw link into another workspace's figure never makes a match
	if root := x.s.rootRevision(otherWS, x.c2.Revision); root != x.c2.Revision {
		t.Errorf("the redraw chain was followed into another workspace: %s", root)
	}
}

func TestRedrawChainsAreFollowedToTheFirstDrawing(t *testing.T) {
	s, _ := newStore(t)
	a := fig(t, s, "A", 1)
	b := redraw(t, s, a, 2, `{"v":2}`)
	c := redraw(t, s, b, 3, `{"v":3}`)
	if s.rootRevision(ws1, c.Revision) != a.Revision || s.rootRevision(ws1, a.Revision) != a.Revision || s.rootRevision(ws1, "unknown") != "unknown" {
		t.Error("lineage")
	}
	board, _ := s.CreateBoard(ws1, "o", "b")
	setItems(t, s, board, b)
	publish(t, s, board, "1")
	setItems(t, s, board, c)
	_, tok := publish(t, s, board, "2")
	r, _ := s.Review(tok)
	if ch, _ := stateOf(r, "A"); ch.State != StateRedrawn {
		t.Errorf("%+v", ch)
	}
}

func TestCorrectionsAndMasksAreComparedFromTheFrozenProvenanceOnly(t *testing.T) {
	cases := []struct {
		name, a, b, corr, mask string
	}{
		{"same", `{"corrections":[{"what":"BUNIT","value":"K"}]}`, `{"corrections":[{"what":"BUNIT","value":"K"}]}`, "", ""},
		{"added", `{}`, `{"corrections":[{"what":"CRVAL3","value":1}]}`, "Corrections: CRVAL3 added", ""},
		{"value changed", `{"corrections":[{"what":"BUNIT","value":"K"}]}`, `{"corrections":[{"what":"BUNIT","value":"Jy"}]}`, "Corrections: BUNIT changed", ""},
		{"removed", `{"corrections":[{"what":"BUNIT","value":"K"},{"what":"CRVAL3","value":1}]}`, `{"corrections":[{"what":"BUNIT","value":"K"}]}`, "Corrections: CRVAL3 removed", ""},
		{"mask", `{"tiers":[{"what":"brightness unit","tier":"verified","note":"x"}]}`, `{"tiers":[{"what":"brightness unit","tier":"verified","note":"x"},{"what":"mask shown: low SNR (m)","tier":"declared","note":"n"}]}`, "", "Masks: mask shown: low SNR (m) added"},
		{"unit tier is not a mask", `{"tiers":[{"what":"brightness unit","tier":"verified","note":"x"}]}`, `{"tiers":[{"what":"brightness unit","tier":"declared","note":"y"}]}`, "", ""},
		{"unreadable", `not json`, `{"corrections":[{"what":"A"}]}`, "Corrections: A added", ""},
	}
	for _, c := range cases {
		corr, mask := provenanceChanges(json.RawMessage(c.a), json.RawMessage(c.b))
		if corr != c.corr || mask != c.mask {
			t.Errorf("%s: %q %q", c.name, corr, mask)
		}
	}
	// through a round: a redrawn figure whose provenance gained a correction says so; the old round is not consulted
	s, _ := newStore(t)
	a := provFig(t, s, "A", 1, `{"dataset":"a.fits"}`)
	b := redraw(t, s, a, 2, `{"dataset":"a.fits","corrections":[{"what":"BUNIT","value":"K"}],"tiers":[{"what":"mask shown: x (m)","tier":"declared","note":"n"}]}`)
	board, _ := s.CreateBoard(ws1, "o", "b")
	setItems(t, s, board, a)
	publish(t, s, board, "1")
	setItems(t, s, board, b)
	_, tok := publish(t, s, board, "2")
	r, _ := s.Review(tok)
	ch, _ := stateOf(r, "A")
	if ch.State != StateRedrawn || len(ch.Changed) != 2 || ch.Changed[0] != "corrections" || ch.Changed[1] != "masks" {
		t.Errorf("%+v", ch)
	}
	var details []string
	for _, l := range r.Snapshot.Round.Since.Lines {
		details = append(details, l.Detail)
	}
	if got := strings.Join(details, "|"); got != "|Corrections: BUNIT added|Masks: mask shown: x (m) added" {
		t.Errorf("details: %q", got)
	}
}

func TestAnAccessLabelChangeIsAChangeAndAJobPanelMatchesByName(t *testing.T) {
	prev := []FrozenItem{{Revision: "r1", Title: "A", Access: "collaboration"}, {Kind: KindJob, Title: "Run 1", State: "running"}}
	cur := []FrozenItem{{Revision: "r1", Title: "A"}, {Kind: KindJob, Title: "Run 1", State: "completed"}}
	changes, prevOf, removed := compareRounds(prev, cur, nil)
	if len(removed) != 0 || prevOf[0] != 0 || prevOf[1] != 1 {
		t.Fatal(prevOf, removed)
	}
	if fmt.Sprint(changes[0].Changed) != "[access]" || fmt.Sprint(changes[1].Changed) != "[state]" {
		t.Errorf("%+v", changes)
	}
	lines := changeLines(prev, cur, changes, prevOf, removed)
	if lines[0].Detail != "access label collaboration before, none now" || lines[1].Detail != "run running before, completed now" {
		t.Errorf("%+v", lines)
	}
}

// A board published twice with the same figure twice: matching is one to one and in order.
func TestDuplicatePanelsMatchOneToOne(t *testing.T) {
	prev := []FrozenItem{{Revision: "r1", Title: "A"}, {Revision: "r1", Title: "A"}}
	cur := []FrozenItem{{Revision: "r1", Title: "A"}, {Revision: "r1", Title: "A"}, {Revision: "r1", Title: "A"}}
	changes, _, removed := compareRounds(prev, cur, nil)
	if changes[0].State != StateUnchanged || changes[1].State != StateUnchanged || changes[2].State != StateNew || len(removed) != 0 {
		t.Errorf("%+v %v", changes, removed)
	}
}

func TestRoundTypesAreAWhitelist(t *testing.T) {
	allowed := map[reflect.Type]map[string]bool{
		reflect.TypeOf(Round{}):          {"number": true, "since": true},
		reflect.TypeOf(Since{}):          {"label": true, "at": true, "items": true, "removed": true, "removedCount": true, "lines": true, "carried": true},
		reflect.TypeOf(ItemChange{}):     {"state": true, "changed": true, "previous": true},
		reflect.TypeOf(PreviousItem{}):   {"title": true, "caption": true, "note": true, "previewSha": true, "width": true, "height": true},
		reflect.TypeOf(RemovedItem{}):    {"title": true},
		reflect.TypeOf(ChangeLine{}):     {"kind": true, "title": true, "detail": true},
		reflect.TypeOf(PreviousStatus{}): {"available": true},
		// ids of the earlier snapshot's comments are kept so that a reply finds its thread; they name nothing outside the comments
		reflect.TypeOf(CarriedComment{}): {"id": true, "parentId": true, "round": true, "figureRevision": true, "author": true, "fromOwner": true, "body": true, "anchor": true, "pictureChanged": true, "createdAt": true, "resolvedAt": true},
	}
	for typ, ok := range allowed {
		for i := 0; i < typ.NumField(); i++ {
			tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if !ok[tag] {
				t.Errorf("%s exposes %q: a new reviewer-visible field needs a deliberate review", typ.Name(), tag)
			}
		}
	}
	// the round holds no snapshot id, board id, workspace or link to the redrawing
	raw, _ := json.Marshal(Round{Number: 2, Since: &Since{}})
	for _, banned := range []string{"snapshotId", "boardId", "workspace", "redrawnFrom", "datasetId", "path", "token"} {
		if strings.Contains(string(raw), `"`+banned+`"`) {
			t.Errorf("round document has %q", banned)
		}
	}
}

func TestTheRedrawLinkNeverReachesTheSnapshot(t *testing.T) {
	x := newRounds(t)
	r, _ := x.s.Review(x.tok2)
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "redrawnFrom") {
		t.Error("the owner-side redraw link is in the reviewer document")
	}
}

func TestPreflightShowsTheRoundAPublishWouldMake(t *testing.T) {
	x := newRounds(t)
	p, err := x.s.Preflight(ws1, x.b.ID, "q", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Frozen.Round == nil || p.Frozen.Round.Number != 3 || p.Frozen.Round.Since == nil || p.Frozen.Round.Since.Label != "round two" {
		t.Errorf("%+v", p.Frozen.Round)
	}
	if list, _ := x.s.Snapshots(ws1, x.b.ID); len(list) != 2 {
		t.Error("a preflight made a snapshot")
	}
}
