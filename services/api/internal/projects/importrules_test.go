package projects

import (
	"reflect"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/discover"
)

func TestRulesKeepTheirOrderAndAreReplacedWhole(t *testing.T) {
	s := newStore(t)
	a := discover.Rule{Name: "a", Kind: "glob", Pattern: "*.fits", Role: discover.RoleDataset, Unit: "K"}
	b := discover.Rule{Name: "b", Kind: "regex", Pattern: `^x/`, Role: discover.RoleCatalog, Values: []string{"P", "sP"}, Lon: "ra", Lat: "dec", CoordFormat: "degrees", CoordFrame: "icrs"}
	if got, err := s.Rules(1); err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	if err := s.SetRules(1, []discover.Rule{a, b}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRules(2, []discover.Rule{b}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Rules(1)
	if !reflect.DeepEqual(got, []discover.Rule{a, b}) {
		t.Fatalf("%+v", got)
	}
	if err := s.SetRules(1, []discover.Rule{b, a}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Rules(1)
	if !reflect.DeepEqual(got, []discover.Rule{b, a}) {
		t.Fatalf("order lost: %+v", got)
	}
	if other, _ := s.Rules(2); len(other) != 1 {
		t.Fatalf("another project's rules were touched: %+v", other)
	}
	if err := s.SetRules(1, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Rules(1); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestAppliedRecordsReplacePerFile(t *testing.T) {
	s := newStore(t)
	if err := s.SaveApplied(1, []Applied{{Path: "a.fits", Rule: "r", Size: 1, MtimeNS: 2, Outcome: "failed", Detail: "no"}, {Path: "b.fits", Rule: "r", Size: 3, MtimeNS: 4, Outcome: "registered"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveApplied(1, []Applied{{Path: "a.fits", Rule: "r", Size: 1, MtimeNS: 2, Outcome: "registered"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.AppliedFiles(1)
	if err != nil || len(got) != 2 || got["a.fits"].Outcome != "registered" || got["b.fits"].Size != 3 {
		t.Fatalf("%+v %v", got, err)
	}
	if other, _ := s.AppliedFiles(2); len(other) != 0 {
		t.Fatal("records of another project")
	}
}
