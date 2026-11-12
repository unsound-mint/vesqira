package catalog

import (
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Every interpretation carries a tier (ADR 0055): the researcher's word is `declared`; a rule's proposal is `inferred`, named by who
// wrote it, with its mapping version. Nothing the store writes is `verified`: that is for a check the app ran.
func TestEveryInterpretationIsWrittenWithATier(t *testing.T) {
	s := openForUnits(t)
	d := register(t, s, "psi.fits", noBunit, 100)
	for _, c := range []struct{ by, tier, mapping string }{
		{"user", TierDeclared, ""},
		{"", TierDeclared, ""},
		{"vgt-run-adapter", TierInferred, "1"},
		{"vesqira-analysis", TierInferred, "1"},
		{"some-future-rule", TierInferred, "1"},
		{"rule:psi maps", TierDeclared, ""},     // the researcher's own import rule: declared, the author keeps the rule's name
		{"profile:VGT.jl@1", TierDeclared, "1"}, // the file's own statement read by a profile: declared by the file, never verified
		{"vgt-file-header", TierDeclared, "1"},  // the same reading before profiles were versioned
	} {
		if err := s.SetInterpretation(d, Interpretation{Kind: "axial_orientation", Unit: "deg", DeclaredBy: c.by}); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Get(workspace.Local, d.ID)
		if got.Interpretation.Tier != c.tier || got.Interpretation.MappingVersion != c.mapping {
			t.Errorf("declared by %q: %q %q, want %q %q", c.by, got.Interpretation.Tier, got.Interpretation.MappingVersion, c.tier, c.mapping)
		}
	}
	// the proposals list says the same
	_ = s.SetInterpretation(d, Interpretation{Kind: "axial_orientation", Unit: "rad", DeclaredBy: "vgt-run-adapter"})
	p, _ := s.ProposedInterpretations(workspace.Local)
	if p[d.ID].Tier != TierInferred || p[d.ID].MappingVersion != "1" {
		t.Errorf("proposed: %+v", p[d.ID])
	}
	// a unit declared by a person is declared; one a rule proposed is inferred, and the person confirming it makes it theirs
	u := register(t, s, "map.fits", noBunit, 100)
	if err := s.SetUnitDeclaration(u, "K", "rohsa-run-adapter"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(workspace.Local, u.ID)
	if got.UnitDeclaration.Tier != TierInferred || got.UnitDeclaration.MappingVersion != "1" {
		t.Errorf("rule-proposed unit: %+v", got.UnitDeclaration)
	}
	if err := s.SetUnitDeclaration(u, "K", "user"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(workspace.Local, u.ID)
	if got.UnitDeclaration.Tier != TierDeclared || got.UnitDeclaration.MappingVersion != "" {
		t.Errorf("confirmed unit: %+v", got.UnitDeclaration)
	}
}
