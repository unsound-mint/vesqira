package boards

import (
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

func TestJobPanelPinsInPlaceAndKeepsItsNote(t *testing.T) {
	s, _ := newStore(t)
	b, _ := s.CreateBoard(workspace.Local, "local-owner", "B")
	f1 := fig(t, s, "one", 10)
	f2 := fig(t, s, "two", 20)
	s.AddItem(workspace.Local, b.ID, f1.ID)
	if err := s.AddJobItem(workspace.Local, b.ID, 42); err != nil {
		t.Fatal(err)
	}
	s.AddItem(workspace.Local, b.ID, f1.ID)
	s.SetNote(workspace.Local, b.ID, 1, "waiting on this", nil)
	got, _ := s.Board(workspace.Local, b.ID)
	if got.Items[1].Kind != KindJob || got.Items[1].Job.ID != 42 || got.Items[1].Figure != nil || got.Items[0].Kind != KindFigure {
		t.Fatalf("%+v", got.Items)
	}
	// moves and removal work for job panels as for figures
	if err := s.MoveItem(workspace.Local, b.ID, 1, -1, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveItem(workspace.Local, b.ID, 0, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.PinItem(workspace.Local, b.ID, 0, f2.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("a figure panel cannot be pinned over: %v", err)
	}
	if err := s.PinItem(workspace.Local, b.ID, 1, f2.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Board(workspace.Local, b.ID)
	if got.Items[1].Kind != KindFigure || got.Items[1].Figure.ID != f2.ID || got.Items[1].Note != "waiting on this" || got.Items[1].Job != nil {
		t.Fatalf("%+v", got.Items[1])
	}
	if err := s.PinItem(workspace.Local, b.ID, 1, f1.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("pinning twice must not swap the figure: %v", err)
	}
	if err := s.PinItem(workspace.Local, b.ID, 9, f1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	s.AddJobItem(workspace.Local, b.ID, 43)
	if err := s.PinItem(workspace.Local, b.ID, 3, 9999); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown figure is refused: %v", err)
	}
	if err := s.RemoveItem(workspace.Local, b.ID, 3, nil); err != nil {
		t.Fatal(err)
	}
}

func keys(t *testing.T, raw []byte) []string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestAFrozenJobPanelHasExactlyKindTitleStateAtNote(t *testing.T) {
	s, _ := newStore(t)
	b, _ := s.CreateBoard(workspace.Local, "local-owner", "B")
	f := fig(t, s, "fig", 10)
	s.AddItem(workspace.Local, b.ID, f.ID)
	s.AddJobItem(workspace.Local, b.ID, 7)
	s.SetNote(workspace.Local, b.ID, 1, "check the ker", nil)
	jobs := map[int64]JobSnapshot{7: {Name: "vgt-ker16", State: "running"}}
	pf, err := s.Preflight(workspace.Local, b.ID, "q", jobs)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range pf.Warnings {
		found = found || w == "Panel 2 follows a run that has not finished; the snapshot shows it as running and will not change. Publish a new revision when it finishes."
	}
	if !found {
		t.Fatalf("the preflight must list the running panel: %v", pf.Warnings)
	}
	_, tok, err := s.PublishWith(workspace.Local, "local-owner", b.ID, PublishOptions{Label: "L", TTL: time.Hour, Jobs: jobs})
	if err != nil {
		t.Fatal(err)
	}
	rv, err := s.Review(tok)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rv.Snapshot.Items[1])
	if got := keys(t, raw); len(got) != 5 || got[0] != "at" || got[1] != "kind" || got[2] != "note" || got[3] != "state" || got[4] != "title" {
		t.Fatalf("a frozen job panel has exactly kind, title, state, at, note: %s", raw)
	}
	if rv.Snapshot.Items[1].State != "running" || rv.Snapshot.Items[1].Title != "vgt-ker16" || rv.Snapshot.Items[1].Note != "check the ker" || rv.Snapshot.Items[1].At == nil {
		t.Fatalf("%+v", rv.Snapshot.Items[1])
	}
	// a figure panel is frozen as before: no job fields appear
	raw0, _ := json.Marshal(rv.Snapshot.Items[0])
	for _, k := range keys(t, raw0) {
		if k == "kind" || k == "state" || k == "at" {
			t.Fatalf("figure panel gained %q: %s", k, raw0)
		}
	}
	// the snapshot never changes with the job: pin the panel and read it again
	s.PinItem(workspace.Local, b.ID, 1, fig(t, s, "result", 99).ID)
	rv2, _ := s.Review(tok)
	if rv2.Snapshot.Items[1].Kind != KindJob || rv2.Snapshot.Items[1].State != "running" {
		t.Fatal("a published snapshot must not follow the board")
	}
	// nothing can be commented on or previewed through a job panel
	if _, err := s.AddComment(tok, CommentInput{FigureRevision: "", Body: "hi"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a job panel is not a comment target: %v", err)
	}
	if _, err := s.ReviewPreview(tok, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestAFinishedButUnpinnedPanelIsWarnedAboutDifferently(t *testing.T) {
	s, _ := newStore(t)
	b, _ := s.CreateBoard(workspace.Local, "local-owner", "B")
	s.AddJobItem(workspace.Local, b.ID, 7)
	pf, err := s.Preflight(workspace.Local, b.ID, "", map[int64]JobSnapshot{7: {Name: "x", State: "completed"}})
	if err != nil {
		t.Fatal(err)
	}
	ok := false
	for _, w := range pf.Warnings {
		ok = ok || len(w) > 8 && w[:8] == "Panel 1 " && w != ""
	}
	if !ok {
		t.Fatalf("%v", pf.Warnings)
	}
}
