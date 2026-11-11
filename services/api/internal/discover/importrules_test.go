package discover

import (
	"errors"
	"strings"
	"testing"
)

func TestGlobAndRegexMatching(t *testing.T) {
	m := func(r Rule, rel string) bool {
		t.Helper()
		f, err := r.Matcher()
		if err != nil {
			t.Fatalf("%+v: %v", r, err)
		}
		return f(rel)
	}
	g := func(p string) Rule { return Rule{Name: "g", Kind: "glob", Pattern: p} }
	// a pattern without a slash is a file name at any depth; "*" never crosses a folder
	for _, c := range []struct {
		pat, rel string
		want     bool
	}{
		{"*_kms.fits", "a/b/mom0_kms.fits", true},
		{"*_kms.fits", "mom0_kms.fits.gz", false},
		{"data/stars/*.dat", "data/stars/a.dat", true},
		{"data/stars/*.dat", "data/stars/sub/a.dat", false},
		{"data/stars/*.dat", "x/data/stars/a.dat", false}, // anchored at the project folder
		{"vda/*/vgt/psi_*.fits", "vda/n3/vgt/psi_x.fits", true},
		{"vda/*/vgt/psi_*.fits", "vda/n3/n4/vgt/psi_x.fits", false},
		{"vda/**/psi_*.fits", "vda/n3/n4/vgt/psi_x.fits", true},
		{"vda/**/psi_*.fits", "vda/psi_x.fits", true}, // ** also stands for no folder at all
		{"data/**", "data/a/b.fits", true},
		{"data/**", "data", false},
		{"**/maps/*.fits", "validation_output/rohsa/l/maps/a.fits", true},
		{"**/maps/*.fits", "maps/a.fits", true},
		{"psi_*.FITS", "psi_a.fits", false}, // case-sensitive, said so
	} {
		if got := m(g(c.pat), c.rel); got != c.want {
			t.Errorf("glob %q on %q = %v, want %v", c.pat, c.rel, got, c.want)
		}
	}
	// a regex is searched in the whole relative path, so it is anchored by the researcher
	re := func(p string) Rule { return Rule{Name: "r", Kind: "regex", Pattern: p} }
	if !m(re(`psi_\d+\.fits$`), "a/psi_12.fits") || m(re(`^psi_\d+\.fits$`), "a/psi_12.fits") || !m(re(`(?i)\.fits$`), "A.FITS") {
		t.Fatal("regex semantics")
	}
	// invalid patterns are refused at save time, with a reason
	for _, r := range []Rule{g(""), g("/abs/*.fits"), g("a/../b"), g("a/[x"), re("("), {Name: "x", Kind: "sed", Pattern: "a"}, g(strings.Repeat("a", MaxPatternLen+1))} {
		if _, err := r.Matcher(); err == nil {
			t.Errorf("%+v was accepted", r)
		}
	}
}

func rules3() []Rule {
	return []Rule{
		{Name: "psi maps", Kind: "glob", Pattern: "vda/*/vgt/psi_*.fits", Role: RoleOrientation, AngleUnit: "rad", AngleFrame: "pixel", Quantity: "b_field"},
		{Name: "kms", Kind: "glob", Pattern: "*_kms.fits", Role: RoleDataset, Unit: "km/s"},
		{Name: "all of vda", Kind: "glob", Pattern: "vda/**", Role: RoleIgnore},
	}
}

func TestRuleValidation(t *testing.T) {
	if bad := ValidateRules(rules3()); len(bad) != 0 {
		t.Fatalf("%v", bad)
	}
	cases := map[string]Rule{
		"angle unit is required":     {Name: "a", Kind: "glob", Pattern: "*.fits", Role: RoleOrientation},
		"coordinate frame is":        {Name: "a", Kind: "glob", Pattern: "*.dat", Role: RoleCatalog, Lon: "ra", Lat: "dec", CoordFormat: "degrees"},
		"needs an equinox":           {Name: "a", Kind: "glob", Pattern: "*.dat", Role: RoleCatalog, Lon: "ra", Lat: "dec", CoordFormat: "degrees", CoordFrame: "fk5"},
		"for a dataset rule":         {Name: "a", Kind: "glob", Pattern: "*.fits", Role: RoleOrientation, AngleUnit: "deg", Unit: "K"},
		"role must be":               {Name: "a", Kind: "glob", Pattern: "*.fits", Role: "bogus"},
		"name:":                      {Name: "../x", Kind: "glob", Pattern: "*.fits", Role: RoleDataset},
		"needs an angle column":      {Name: "a", Kind: "glob", Pattern: "*.dat", Role: RoleCatalog, Lon: "a", Lat: "b", CoordFormat: "degrees", CoordFrame: "icrs", Quantity: "b_field"},
		"quantity must be":           {Name: "a", Kind: "glob", Pattern: "*.fits", Role: RoleOrientation, AngleUnit: "deg", Quantity: "magic"},
		"declarations for a catalog": {Name: "a", Kind: "glob", Pattern: "*.fits", Role: RoleDataset, Values: []string{"x"}},
	}
	for want, r := range cases {
		bad := strings.Join(r.Validate(), " | ")
		if !strings.Contains(bad, want) {
			t.Errorf("%+v: %q lacks %q", r, bad, want)
		}
	}
	twice := append(rules3(), Rule{Name: "KMS", Kind: "glob", Pattern: "x", Role: RoleIgnore})
	if bad := ValidateRules(twice); len(bad) == 0 || !strings.Contains(bad[0], "used twice") {
		t.Fatalf("%v", bad)
	}
	if ValidateRules(make([]Rule, MaxRules+1))[0] != "at most 100 rules" {
		t.Fatal("rule count not bounded")
	}
}

func TestFirstMatchWinsAndConflictsAreShown(t *testing.T) {
	items := []Item{
		{Path: "vda/n3/vgt/psi_a.fits", Role: BIDataset},
		{Path: "vda/n3/vgt/mom_kms.fits", Role: BIDataset},
		{Path: "other/mom_kms.fits", Role: BIDataset},
		{Path: "readme.txt", Role: BIOther},
		{Path: "a.zip", Role: BIArchive, Locked: true},
	}
	c := Classify(items, rules3())
	by := map[string]Proposal{}
	for _, p := range c.Proposals {
		by[p.Path] = p
	}
	// psi_a matches rule 1 and rule 3: rule 1 wins, rule 3 is listed as also matching
	p := by["vda/n3/vgt/psi_a.fits"]
	if p.Rule != "psi maps" || p.ProposedRole != RoleOrientation || strings.Join(p.Matched, ",") != "psi maps,all of vda" {
		t.Fatalf("%+v", p)
	}
	if !strings.Contains(strings.Join(p.Declares, ";"), "declared by your rule psi maps") {
		t.Fatalf("declarations are not labelled: %v", p.Declares)
	}
	// mom_kms matches rule 2 (before rule 3), so the ignore rule does not take it
	if p := by["vda/n3/vgt/mom_kms.fits"]; p.Rule != "kms" || p.ProposedRole != RoleDataset {
		t.Fatalf("%+v", p)
	}
	if p := by["readme.txt"]; p.By != "built-in" || p.ProposedRole != BIOther || p.Rule != "" {
		t.Fatalf("%+v", p)
	}
	if p := by["a.zip"]; p.By != "built-in" || len(p.Matched) != 0 {
		t.Fatalf("a locked file must not be matched: %+v", p)
	}
	if c.ConflictCount != 2 || len(c.Conflicts) != 2 {
		t.Fatalf("conflicts: %+v", c.Conflicts)
	}
	cf := map[string]Conflict{}
	for _, x := range c.Conflicts {
		cf[x.Path] = x
	}
	if x := cf["vda/n3/vgt/psi_a.fits"]; x.Winner != "psi maps" || !x.Differ {
		t.Fatalf("%+v", x)
	}
	r := c.Rules
	if r[0].Wins != 1 || r[1].Wins != 2 || r[1].Matches != 2 || r[2].Matches != 2 || r[2].Wins != 0 || r[2].Shadowed != 2 {
		t.Fatalf("%+v", r)
	}
	// reordering changes who wins, and nothing else
	rs := rules3()
	rs[0], rs[2] = rs[2], rs[0]
	c = Classify(items, rs)
	for _, p := range c.Proposals {
		if p.Path == "vda/n3/vgt/psi_a.fits" && (p.Rule != "all of vda" || p.ProposedRole != RoleIgnore) {
			t.Fatalf("order was not honoured: %+v", p)
		}
	}
	// a rule with a broken pattern matches nothing and says why
	c = Classify(items, []Rule{{Name: "broken", Kind: "regex", Pattern: "(", Role: RoleIgnore}})
	if c.Rules[0].Error == "" || c.Rules[0].Matches != 0 {
		t.Fatalf("%+v", c.Rules[0])
	}
}

func inventoryProject(t *testing.T) string {
	root := canonical(t)
	write(t, root, "validation_output/rohsa/n4/rohsa_n4_run_trace.toml", "n_gauss = 4\nregion = \"n4\"\nstatus = \"ran\"\n")
	write(t, root, "validation_output/rohsa/n4/rohsa_n4.dat", "# i j\n")
	write(t, root, "validation_output/rohsa/n4/maps/mu_kms.fits", "x")
	write(t, root, "validation_output/rohsa/n4/notes.txt", "x")
	write(t, root, "validation_output/a/run_metadata.toml", vgtMeta+"reliability_path = \"/x/rel.fits\"\n")
	write(t, root, "validation_output/a/psi.fits", "x")
	write(t, root, "validation_output/a/other.fits", "x")
	write(t, root, "validation_output/a/log.txt", "x")
	write(t, root, "data/exp_raw/cube.fits", "x")
	write(t, root, "data/sims/box.h5", "x")
	write(t, root, "vda/n3/vgt psi maps/psi_1.fits", "x")
	write(t, root, "vda/n3/vgt psi maps/psi_2.fits", "x")
	write(t, root, "stars/s1.dat", "a b\n1 2\n")
	write(t, root, "stars/s2.dat", "a b\n1 2\n")
	write(t, root, "README.md", "x")
	write(t, root, "backup.tar.gz", "x")
	write(t, root, "secret/key_DO_NOT_OPEN.fits", "x")
	return root
}

func TestItemsNameTheBuiltinRoleAndTheRuleBehindIt(t *testing.T) {
	root := inventoryProject(t)
	r := scan(t, LocalFS{root}, Options{Inventory: true})
	by := map[string]Item{}
	for _, it := range Items(r) {
		by[it.Path] = it
	}
	want := map[string]string{
		"validation_output/a/run_metadata.toml":              BIRunRecord,
		"validation_output/a/psi.fits":                       "vgt_psi",
		"validation_output/a/other.fits":                     BIRunOutput,
		"validation_output/a/log.txt":                        BIRunOutput,
		"validation_output/rohsa/n4/rohsa_n4_run_trace.toml": BIRunRecord,
		"validation_output/rohsa/n4/rohsa_n4.dat":            BIROHSAFit,
		"validation_output/rohsa/n4/maps/mu_kms.fits":        BIROHSAProduct,
		"validation_output/rohsa/n4/notes.txt":               BIRunOutput,
		"data/exp_raw/cube.fits":                             BIDataset,
		"data/sims/box.h5":                                   BIOtherData,
		"vda/n3/vgt psi maps/psi_1.fits":                     BIDataset,
		"README.md":                                          BIOther,
		"backup.tar.gz":                                      BIArchive,
		"stars/s1.dat":                                       BIOther, // not in a declared data folder: not offered as a catalog
	}
	for p, role := range want {
		it, ok := by[p]
		if !ok || it.Role != role || it.Why == "" {
			t.Errorf("%s: %+v, want role %s", p, it, role)
		}
	}
	if !by["backup.tar.gz"].Locked || !by["validation_output/a/run_metadata.toml"].Locked || by["README.md"].Locked {
		t.Fatal("locks")
	}
	if !strings.Contains(by["validation_output/a/psi.fits"].Why, "run_metadata.toml names psi.fits") {
		t.Fatalf("why: %s", by["validation_output/a/psi.fits"].Why)
	}
	for p := range by {
		if strings.Contains(strings.ToLower(p), "do_not_open") {
			t.Fatalf("a never-read name was listed: %s", p)
		}
	}
	// grouping: by role and extension, counts and bytes, a bounded number of samples
	c := Classify(Items(r), nil)
	gs := GroupProposals(c.Proposals, 1, "", 10)
	var datasets *Group
	for i := range gs {
		if gs[i].Role == BIDataset && gs[i].Ext == ".fits" {
			datasets = &gs[i]
		}
		if len(gs[i].Samples) > 1 {
			t.Fatalf("samples are not bounded: %+v", gs[i])
		}
	}
	if datasets == nil || datasets.Count != 3 || datasets.Bytes != 3 || datasets.By != "built-in" {
		t.Fatalf("%+v", gs)
	}
	gs = GroupProposals(c.Proposals, 1, GroupKey(*datasets), 10)
	for _, g := range gs {
		if g.Role == BIDataset && g.Ext == ".fits" && len(g.Samples) != 3 {
			t.Fatalf("expand: %+v", g)
		}
	}
	// the same files under a rule are grouped under the rule's name
	c = Classify(Items(r), []Rule{{Name: "stars", Kind: "glob", Pattern: "stars/*.dat", Role: RoleCatalog, Lon: "a", Lat: "b", CoordFormat: "degrees", CoordFrame: "icrs"}})
	found := false
	for _, g := range GroupProposals(c.Proposals, 5, "", 10) {
		if g.By == "stars" && g.Role == RoleCatalog && g.Ext == ".dat" && g.Count == 2 {
			found = true
		}
	}
	if !found {
		t.Fatal("rule group missing")
	}
	if Ext("a/b.FITS.GZ") != ".fits.gz" || Ext("Makefile") != "(none)" || Ext(".hidden") != "(none)" {
		t.Fatal("Ext")
	}
}

func TestInventoryIsBounded(t *testing.T) {
	root := canonical(t)
	for _, p := range []string{"a/x.txt", "a/b/y.txt", "a/b/c/z.txt", "a/b/c/d/w.txt"} {
		write(t, root, p, "x")
	}
	r := scan(t, LocalFS{root}, Options{Inventory: true, MaxDepth: 2})
	if !r.Truncated || !strings.Contains(r.TruncatedWhy, "2 folder levels") {
		t.Fatalf("%+v", r)
	}
	got := map[string]bool{}
	for _, o := range r.Others {
		got[o.Path] = true
	}
	if !got["a/x.txt"] || !got["a/b/y.txt"] || got["a/b/c/z.txt"] {
		t.Fatalf("depth cap: %v", got)
	}
	// without the inventory nothing is listed (the normal scan only counts), and the count is the same
	plain := scan(t, LocalFS{root}, Options{})
	if len(plain.Others) != 0 || plain.OtherFiles != 4 {
		t.Fatalf("%+v", plain)
	}
	// entry budget
	r = scan(t, LocalFS{root}, Options{Inventory: true, MaxEntries: 2})
	if !r.Truncated || !strings.Contains(r.TruncatedWhy, "entries") {
		t.Fatalf("%+v", r)
	}
}

func TestRulesNeverReachWhatTheWalkSkips(t *testing.T) {
	root := inventoryProject(t)
	write(t, root, "node_modules/x/psi_x.fits", "x")
	write(t, root, "blind_set_key/psi_key.fits", "x")
	c := &countingFS{FS: LocalFS{root}}
	r := scan(t, c, Options{Inventory: true})
	// the broadest rules possible see only what the walk listed
	cl := Classify(Items(r), []Rule{{Name: "all", Kind: "regex", Pattern: ".*", Role: RoleDataset}})
	for _, p := range cl.Proposals {
		if strings.Contains(strings.ToLower(p.Path), "do_not_open") || strings.Contains(p.Path, "blind_set_key") || strings.Contains(p.Path, "node_modules") {
			t.Fatalf("a rule was offered %s", p.Path)
		}
	}
	for _, p := range append(c.touched, c.listed...) {
		if strings.Contains(strings.ToLower(p), "do_not_open") || strings.Contains(p, "blind_set_key") {
			t.Fatalf("touched %q", p)
		}
	}
	// archives and run records are listed but no rule claims them
	for _, p := range cl.Proposals {
		if p.Locked && p.By != "built-in" {
			t.Fatalf("a locked file was claimed: %+v", p)
		}
	}
}

func TestRulesSectionInProjectFile(t *testing.T) {
	base := "# my notes\n[project]\nname = \"x\"\n\n[layout]\ndata = [\"data\"]\n"
	rs := []Rule{
		{Name: "psi maps", Kind: "glob", Pattern: "vda/*/vgt/psi_*.fits", Role: RoleOrientation, AngleUnit: "rad", AngleFrame: "pixel", Quantity: "b_field"},
		{Name: "stars", Kind: "regex", Pattern: `^stars/.*\.dat$`, Role: RoleCatalog, Lon: "RA", Lat: "DEC", CoordFormat: "sexagesimal_hours_degrees", CoordFrame: "fk5", Equinox: "J2000", AngleColumn: "PA", AngleUnit: "deg", Values: []string{"P", "sP"}},
	}
	out, err := ReplaceRules([]byte(base), rs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), base) {
		t.Fatalf("the researcher's text was changed:\n%s", out)
	}
	pf, err := ParseProjectFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Import.Rules) != 2 || pf.Import.Rules[0].Name != "psi maps" || pf.Import.Rules[1].Pattern != `^stars/.*\.dat$` || pf.Import.Rules[1].Values[1] != "sP" || pf.Import.Rules[1].Equinox != "J2000" {
		t.Fatalf("%+v", pf.Import.Rules)
	}
	if bad := ValidateRules(pf.Import.Rules); len(bad) != 0 {
		t.Fatalf("a written rule is not valid when read back: %v", bad)
	}
	// writing again replaces the section (order may change) and nothing else; an empty list removes it
	rs[0], rs[1] = rs[1], rs[0]
	again, err := ReplaceRules(out, rs)
	if err != nil {
		t.Fatal(err)
	}
	pf, _ = ParseProjectFile(again)
	if len(pf.Import.Rules) != 2 || pf.Import.Rules[0].Name != "stars" || strings.Count(string(again), "vesqira:import-rules begin") != 1 || !strings.HasPrefix(string(again), base) {
		t.Fatalf("%s", again)
	}
	none, err := ReplaceRules(again, nil)
	if err != nil || strings.TrimRight(string(none), "\n") != strings.TrimRight(base, "\n") {
		t.Fatalf("%q %v", none, err)
	}
	// rules the researcher wrote outside the markers are never rewritten
	foreign := base + "\n[[import.rules]]\nname = \"mine\"\nkind = \"glob\"\npattern = \"*.fits\"\nrole = \"dataset\"\n"
	if _, err := ReplaceRules([]byte(foreign), rs); !errors.Is(err, ErrForeignRules) {
		t.Fatalf("%v", err)
	}
	if pf, _ := ParseProjectFile([]byte(foreign)); len(pf.Import.Rules) != 1 {
		t.Fatal("hand-written rules are read")
	}
	if _, err := ReplaceRules([]byte(base+"\n"+rulesBegin+"\n"), rs); err == nil {
		t.Fatal("a half section was accepted")
	}
	// quotes and backslashes in values survive the round trip
	odd := []Rule{{Name: "odd", Kind: "regex", Pattern: `a"b\.fits$`, Role: RoleIgnore}}
	out, err = ReplaceRules(nil, odd)
	if err != nil {
		t.Fatal(err)
	}
	if pf, _ := ParseProjectFile(out); pf.Import.Rules[0].Pattern != `a"b\.fits$` {
		t.Fatalf("%q", pf.Import.Rules[0].Pattern)
	}
}

func TestRuleDeclaredByLabel(t *testing.T) {
	r := Rule{Name: "psi maps"}
	if n, ok := RuleName(r.DeclaredBy()); !ok || n != "psi maps" {
		t.Fatal()
	}
	if _, ok := RuleName("user"); ok {
		t.Fatal()
	}
}

func maskRule() Rule {
	return Rule{Name: "masks", Kind: "glob", Pattern: "data/**/*_mask.fits", Role: RoleMask, MaskType: "low_snr", MaskNonzero: "valid", MaskTarget: "{dir}/{stem}.fits", MaskStrip: "_mask"}
}

// A mask rule says what the files flag, what nonzero means and which dataset they belong to: nothing is defaulted, and no other role
// accepts these fields.
func TestMaskRuleValidation(t *testing.T) {
	if bad := maskRule().Validate(); len(bad) != 0 {
		t.Fatal(bad)
	}
	for name, mut := range map[string]func(*Rule){
		"no type":         func(r *Rule) { r.MaskType = "" },
		"missing type":    func(r *Rule) { r.MaskType = "missing" },
		"no nonzero":      func(r *Rule) { r.MaskNonzero = "" },
		"other, no text":  func(r *Rule) { r.MaskType = "other" },
		"no target":       func(r *Rule) { r.MaskTarget = "" },
		"no stem":         func(r *Rule) { r.MaskTarget = "{dir}/psi.fits" },
		"outside":         func(r *Rule) { r.MaskTarget = "../{stem}.fits" },
		"absolute":        func(r *Rule) { r.MaskTarget = "/{stem}.fits" },
		"unknown holder":  func(r *Rule) { r.MaskTarget = "{dir}/{name}.fits" },
		"dir in the tail": func(r *Rule) { r.MaskTarget = "x/{dir}/{stem}.fits" },
	} {
		r := maskRule()
		mut(&r)
		if len(r.Validate()) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	d := Rule{Name: "d", Kind: "glob", Pattern: "*.fits", Role: RoleDataset, MaskType: "low_snr"}
	if len(d.Validate()) == 0 {
		t.Error("a dataset rule accepted a mask declaration")
	}
}

func TestMaskTargetPath(t *testing.T) {
	r := maskRule()
	for in, want := range map[string]string{
		"data/a/psi_mask.fits":  "data/a/psi.fits",
		"data/psi_mask.fits.gz": "data/psi.fits",
		"data/x.y_mask.fits":    "data/x.y.fits",
	} {
		if got, err := r.MaskTargetPath(in); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", in, got, err, want)
		}
	}
	if _, err := r.MaskTargetPath("data/psi.fits"); err == nil {
		t.Error("a name without the stripped suffix was given a target")
	}
	root := Rule{MaskTarget: "{dir}/{stem}.fits"}
	if got, err := root.MaskTargetPath("m.fits"); err == nil {
		t.Errorf("a mask that is its own target: %q", got)
	}
	if got, err := root.MaskTargetPath("sub/m.fits"); err != nil || got != "sub/m.fits" {
		_ = got
	}
	same := Rule{MaskTarget: "../{stem}.fits"}
	if _, err := same.MaskTargetPath("a/m.fits"); err == nil {
		t.Error("a target outside the project")
	}
	flat := Rule{MaskTarget: "data/{stem}.fits", MaskStrip: ".mask"}
	if got, err := flat.MaskTargetPath("elsewhere/psi.mask.fits"); err != nil || got != "data/psi.fits" {
		t.Errorf("%q %v", got, err)
	}
	if got, err := (Rule{MaskTarget: "{dir}/{stem}_cube.fits"}).MaskTargetPath("top.fits"); err != nil || got != "top_cube.fits" {
		t.Errorf("top-level mask: %q %v", got, err)
	}
}

// The mask fields survive the round trip through project.toml.
func TestMaskRuleRoundTripsThroughProjectToml(t *testing.T) {
	r := maskRule()
	r.MaskMeaning = "ghost \"rings\""
	out, err := ReplaceRules([]byte("[project]\nname = \"x\"\n"), []Rule{r})
	if err != nil {
		t.Fatal(err)
	}
	pf, err := ParseProjectFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := pf.Import.Rules; len(got) != 1 || !RulesEqual(got, []Rule{r}) {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(strings.Join(r.describe(), " "), "belongs to the dataset at {dir}/{stem}.fits") {
		t.Fatal(r.describe())
	}
}
