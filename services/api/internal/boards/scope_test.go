package boards

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

const otherWS workspace.ID = 2

// ADR 0050 slice 3: a figure, a board and a review of one workspace look missing from another, whatever the id, and the same
// evidence may be saved in both.
func TestAFigureAndABoardOfAnotherWorkspaceAreNotFound(t *testing.T) {
	s, _ := newStore(t)
	f := fig(t, s, "one", 10)
	b, err := s.CreateBoard(workspace.Local, "local-owner", "board")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddItem(workspace.Local, b.ID, f.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Figure(otherWS, f.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("figure from workspace 2: %v", err)
	}
	if _, err := s.FigureByRevision(otherWS, f.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("figure by revision from workspace 2: %v", err)
	}
	if fs, _ := s.Figures(otherWS); len(fs) != 0 {
		t.Fatalf("figures listed in workspace 2: %v", fs)
	}
	if _, err := s.Board(otherWS, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("board from workspace 2: %v", err)
	}
	if bs, _ := s.Boards(otherWS); len(bs) != 0 {
		t.Fatalf("boards listed in workspace 2: %v", bs)
	}
	if err := s.AddItem(otherWS, b.ID, f.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("adding to a foreign board: %v", err)
	}
	if err := s.SetText(otherWS, f.ID, "taken", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("editing a foreign figure: %v", err)
	}
	if err := s.RemoveItem(otherWS, b.ID, 0, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("editing a foreign board: %v", err)
	}
	if _, _, err := s.Publish(otherWS, "someone", b.ID, "x", time.Hour); !errors.Is(err, ErrNotFound) {
		t.Fatalf("publishing a foreign board: %v", err)
	}

	_, tok, err := s.Publish(workspace.Local, "local-owner", b.ID, "x", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if ss, _ := s.Snapshots(otherWS, b.ID); len(ss) != 0 {
		t.Fatalf("snapshots listed in workspace 2: %v", ss)
	}
	sn, _ := s.Snapshots(workspace.Local, b.ID)
	if err := s.Revoke(otherWS, sn[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking a foreign snapshot: %v", err)
	}
	if _, err := s.Review(tok); err != nil {
		t.Fatalf("the review link (a capability) is not tied to a workspace: %v", err)
	}

	// the same evidence saved in workspace 2 is its own figure
	g, err := s.CreateFigure(otherWS, "someone", NewFigure{DatasetID: 7, Title: "one", Caption: "c", Provenance: json.RawMessage(`{"dataset":"a.fits","channel":3}`)}, pngBytes(t, 8, 6, 10))
	if err != nil || g.ID == f.ID {
		t.Fatalf("same evidence in workspace 2: %+v %v", g, err)
	}
}
