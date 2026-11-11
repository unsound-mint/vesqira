package history

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func open(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const sc = `{"schema":"scene/1"}`

func TestStepsKeepTheirParentAndBranchesAreNeverDeleted(t *testing.T) {
	s := open(t)
	a, err := s.Add(1, nil, 7, "opened", "Opened cube", []byte(sc))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Add(1, &a.ID, 7, "channel", "Channel 10", []byte(sc))
	// continuing from a: a second child of a, b stays
	c, err := s.Add(1, &a.ID, 7, "continued", "Continued from Opened cube", []byte(sc))
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.List(1)
	if err != nil || len(list) != 3 {
		t.Fatalf("%v %v", list, err)
	}
	if list[1].ID != b.ID || list[2].ID != c.ID || *list[1].ParentID != a.ID || *list[2].ParentID != a.ID || list[0].ParentID != nil {
		t.Errorf("tree: %+v", list)
	}
	got, err := s.Get(c.ID)
	if err != nil || string(got.Scene) != sc {
		t.Errorf("get: %+v %v", got, err)
	}
	if _, err := s.Get(999); err != ErrNotFound {
		t.Errorf("unknown step: %v", err)
	}
}

func TestAStepCannotPointIntoAnotherProjectsHistory(t *testing.T) {
	s := open(t)
	a, _ := s.Add(1, nil, 7, "opened", "Opened", []byte(sc))
	if _, err := s.Add(2, &a.ID, 7, "channel", "x", []byte(sc)); err == nil || !strings.HasPrefix(err.Error(), "invalid") {
		t.Errorf("foreign parent: %v", err)
	}
	if list, _ := s.List(2); len(list) != 0 {
		t.Errorf("project 2: %v", list)
	}
}

func TestOldestStepsArePrunedPerProjectAndChildrenSurvive(t *testing.T) {
	s := open(t)
	first, _ := s.Add(1, nil, 7, "opened", "first", []byte(sc))
	other, _ := s.Add(2, nil, 7, "opened", "other project", []byte(sc))
	prev := first.ID
	for i := 0; i < Keep+9; i++ {
		st, err := s.Add(1, &prev, 7, "channel", "step", []byte(sc))
		if err != nil {
			t.Fatal(err)
		}
		prev = st.ID
	}
	list, _ := s.List(1)
	if len(list) != Keep {
		t.Fatalf("kept %d, want %d", len(list), Keep)
	}
	if _, err := s.Get(first.ID); err != ErrNotFound {
		t.Errorf("the oldest step should be pruned: %v", err)
	}
	if list[0].ParentID == nil {
		t.Errorf("the oldest kept step keeps its parent id (a pruned step), the browser treats it as the start of a branch")
	}
	if _, err := s.Get(other.ID); err != nil {
		t.Errorf("another project's step was pruned: %v", err)
	}
}

func TestStepsAreChecked(t *testing.T) {
	s := open(t)
	for name, f := range map[string]func() error{
		"kind":   func() error { _, e := s.Add(1, nil, 7, "bogus", "x", []byte(sc)); return e },
		"label":  func() error { _, e := s.Add(1, nil, 7, "opened", " \n ", []byte(sc)); return e },
		"empty":  func() error { _, e := s.Add(1, nil, 7, "opened", "x", nil); return e },
		"large":  func() error { _, e := s.Add(1, nil, 7, "opened", "x", make([]byte, MaxScene+1)); return e },
		"parent": func() error { p := int64(42); _, e := s.Add(1, &p, 7, "opened", "x", []byte(sc)); return e },
	} {
		if err := f(); err == nil || !strings.HasPrefix(err.Error(), "invalid") {
			t.Errorf("%s: %v", name, err)
		}
	}
	st, _ := s.Add(1, nil, 7, "opened", "a\nlabel   with\tspace", []byte(sc))
	if st.Label != "a label with space" {
		t.Errorf("label %q", st.Label)
	}
}
