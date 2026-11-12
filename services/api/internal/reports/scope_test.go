package reports

import (
	"errors"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

const otherWS workspace.ID = 2

// ADR 0050 slice 3: titles are unique per workspace, a report of another workspace is not found, and what a report's seen
// revision is depends on the account that asks.
func TestReportsAreScopedByWorkspaceAndSeenIsPerAccount(t *testing.T) {
	s := newStore(t)
	a, err := s.Create(workspace.Local, "local-owner", "Notes", "first", AuthorOwner)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(otherWS, "someone", "Notes", "second", AuthorOwner)
	if err != nil {
		t.Fatalf("the same title in another workspace is allowed: %v", err)
	}
	if _, err := s.Create(workspace.Local, "local-owner", "notes", "again", AuthorOwner); !errors.Is(err, ErrConflict) {
		t.Fatalf("a title is still unique inside one workspace: %v", err)
	}

	if _, err := s.Get(otherWS, "someone", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a foreign report must look missing: %v", err)
	}
	if _, err := s.ByTitle(otherWS, "someone", "Notes"); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.List(otherWS, "someone"); len(l) != 1 || l[0].ID != b.ID {
		t.Fatalf("list of workspace 2: %+v", l)
	}
	if err := s.Delete(otherWS, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a foreign report: %v", err)
	}
	if err := s.MarkSeen(otherWS, "someone", a.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("marking a foreign report seen: %v", err)
	}

	// an agent saves; the author has not seen it, the second account has never seen anything
	body := "agent edit"
	if _, err := s.Update(workspace.Local, "local-owner", a.ID, Update{Body: &body, Base: a.Revision, Author: AuthorAgent}); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.List(workspace.Local, "local-owner"); !l[0].AgentUnseen {
		t.Fatal("the agent's save is new to the owner")
	}
	if err := s.MarkSeen(workspace.Local, "local-owner", a.ID, 2); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.List(workspace.Local, "local-owner"); l[0].AgentUnseen {
		t.Fatal("seen by the owner")
	}
	if l, _ := s.List(workspace.Local, "member-2"); !l[0].AgentUnseen {
		t.Fatal("another account has not seen it: the mark is per account")
	}
	if got, _ := s.Get(workspace.Local, "member-2", a.ID); got.SeenRevision != 0 {
		t.Fatalf("member-2 seen revision %d", got.SeenRevision)
	}
	if got, _ := s.Get(workspace.Local, "local-owner", a.ID); got.SeenRevision != 2 {
		t.Fatalf("owner seen revision %d", got.SeenRevision)
	}
}
