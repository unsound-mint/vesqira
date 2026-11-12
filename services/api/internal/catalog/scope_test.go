package catalog

import (
	"errors"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

const otherWS workspace.ID = 2

// ADR 0050 slice 3: the workspace is the access unit. A dataset of one workspace is not found, listed or reachable by a
// sibling workspace, and the same path may be registered in both.
func TestADatasetOfAnotherWorkspaceIsNotFoundAndThePathCanBeRegisteredInBoth(t *testing.T) {
	s := openStore(t)
	one := reg(t, s, 0, "/data/a.fits", 100, t0)

	if _, err := s.Get(otherWS, one.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a foreign id must look like a missing one, got %v", err)
	}
	if ds, err := s.List(otherWS); err != nil || len(ds) != 0 {
		t.Fatalf("list from workspace 2: %v %v", ds, err)
	}
	if _, err := s.Location(otherWS, one.ID, one.Locations[0].ID); !errors.Is(err, ErrNoLocation) {
		t.Fatalf("a foreign location must look missing, got %v", err)
	}
	if err := s.RemoveLocation(otherWS, one.ID, one.Locations[0].ID); !errors.Is(err, ErrNoLocation) {
		t.Fatalf("a foreign location cannot be removed, got %v", err)
	}

	two, err := s.Upsert(otherWS, "someone", Dataset{Path: "/data/a.fits", Name: "a.fits", SizeBytes: 100, ModifiedAt: t0, Header: []byte(`{}`)})
	if err != nil {
		t.Fatalf("the same path in another workspace is a different dataset: %v", err)
	}
	if two.ID == one.ID {
		t.Fatalf("one row for two workspaces: %d", two.ID)
	}
	if ds, _ := s.List(workspace.Local); len(ds) != 1 || ds[0].ID != one.ID {
		t.Fatalf("workspace 1 must still see only its own: %+v", ds)
	}
	if owner, found, _ := s.LocationOwner(otherWS, 0, "/data/a.fits"); !found || owner != two.ID {
		t.Fatalf("location owner is per workspace: %d %v", owner, found)
	}
}
