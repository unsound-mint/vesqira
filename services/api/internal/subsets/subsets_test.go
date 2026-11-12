package subsets

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/s.db?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func version(name string, def Definition) Version {
	return Version{Name: name, Reason: "because", Def: def, SourceSize: 100, SourceModified: time.Unix(5, 7), SourceIdentity: "sha256:ab", Author: "user"}
}

func TestADefinitionIsCheckedAndBecomesTheWorkersWindow(t *testing.T) {
	d, err := Definition{Box: &Box{X0: 1, X1: 9, Y0: 2, Y1: 8}, Channels: &Channels{C0: 3, C1: 4}}.Normalised()
	if err != nil || d.Step != 1 {
		t.Fatalf("%+v %v", d, err)
	}
	w := d.Window()
	if w["x0"] != 1 || w["y1"] != 8 || w["c0"] != 3 || w["step"] != nil {
		t.Fatalf("window: %v", w)
	}
	d.Step = 3
	d.Channels, d.World = nil, &World{Lo: -20, Hi: -10, Unit: "km/s", Frame: "LSRK"}
	got := d.Window()
	if got["step"] != 3 || got["world"].(map[string]any)["frame"] != "LSRK" || got["c0"] != nil {
		t.Fatalf("window: %v", got)
	}
	for name, bad := range map[string]Definition{
		"step":        {Step: 65},
		"order":       {Box: &Box{X0: 5, X1: 1, Y0: 0, Y1: 1}},
		"negative":    {Box: &Box{X0: -1, X1: 1, Y0: 0, Y1: 1}},
		"channels":    {Channels: &Channels{C0: 4, C1: 2}},
		"both":        {Channels: &Channels{C0: 0, C1: 1}, World: &World{Lo: 1, Hi: 2, Unit: "km/s"}},
		"unit":        {World: &World{Lo: 1, Hi: 2, Unit: "<script>"}},
		"frame":       {World: &World{Lo: 1, Hi: 2, Unit: "km/s", Frame: "a b"}},
		"region only": {Region: &RegionRef{ID: 1, Name: "x"}},
	} {
		if _, err := bad.Normalised(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestVersionsAreAppendedAndAStaleEditIsRefused(t *testing.T) {
	s := newStore(t)
	sub, err := s.Create(7, version("Core", Definition{Step: 1, Box: &Box{X0: 0, X1: 3, Y0: 0, Y1: 3}}))
	if err != nil || sub.Current.Version != 1 || sub.Current.Kind != "create" || sub.SourceID != 7 {
		t.Fatalf("%+v %v", sub, err)
	}
	if !sub.Current.SourceModified.Equal(time.Unix(5, 7)) || sub.Current.SourceIdentity != "sha256:ab" || sub.Current.SourceSize != 100 {
		t.Fatalf("the pinned identity was not kept: %+v", sub.Current)
	}
	next, err := s.AddVersion(sub.ID, 1, "edit", version("Core, wider", Definition{Step: 2, Box: &Box{X0: 0, X1: 9, Y0: 0, Y1: 9}}))
	if err != nil || next.Current.Version != 2 || next.Current.Name != "Core, wider" || next.Current.Def.Step != 2 {
		t.Fatalf("%+v %v", next, err)
	}
	if _, err := s.AddVersion(sub.ID, 1, "edit", version("late", Definition{Step: 1})); !errors.Is(err, ErrConflict) {
		t.Fatalf("an edit made from version 1 after version 2 exists: %v", err)
	}
	if _, err := s.AddVersion(99, 1, "edit", version("none", Definition{Step: 1})); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown subset: %v", err)
	}
	vs, err := s.Versions(sub.ID)
	if err != nil || len(vs) != 2 || vs[0].Name != "Core" || vs[1].Kind != "edit" {
		t.Fatalf("history: %+v %v", vs, err)
	}
	other, _ := s.Create(8, version("Other", Definition{Step: 1}))
	if all, _ := s.List(0); len(all) != 2 {
		t.Fatalf("all: %d", len(all))
	}
	if of7, _ := s.List(7); len(of7) != 1 || of7[0].Current.Version != 2 {
		t.Fatalf("of source 7: %+v", of7)
	}
	if err := s.Delete(sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(sub.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted subset: %v", err)
	}
	if _, err := s.Get(other.ID); err != nil {
		t.Fatalf("another subset went with it: %v", err)
	}
	if err := s.Delete(sub.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}
