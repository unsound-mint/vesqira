package catalog

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

func catalogDataset(t *testing.T, s *Store, cols ...CatalogColumn) Dataset {
	t.Helper()
	hdr, _ := json.Marshal(CatalogHeader{Kind: KindCatalog, Format: "tsv", Rows: 3, Columns: cols})
	d, err := s.Upsert(workspace.Local, "local-owner", Dataset{Path: "/data/stars.dat", Name: "stars.dat", SizeBytes: 100, ModifiedAt: time.Unix(1000, 0), Header: hdr})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

var starCols = []CatalogColumn{
	{Name: "Star", Dtype: "string", Scalar: true}, {Name: "RA(J2000)", Dtype: "string", Scalar: true}, {Name: "DEC(J2000)", Dtype: "string", Scalar: true},
	{Name: "PA(deg)", Dtype: "float", Scalar: true}, {Name: "sPA(deg)", Dtype: "float", Scalar: true}, {Name: "vec", Dtype: "float", Scalar: false},
}

func sp(s string) *string { return &s }

func goodDecl() CatalogDeclaration {
	return CatalogDeclaration{Lon: "RA(J2000)", Lat: "DEC(J2000)", Format: "sexagesimal_hours_degrees", Frame: "fk5", Equinox: sp("J2000"),
		Angle: &CatalogAngle{Column: "PA(deg)", Unit: "deg", Frame: "sky", Quantity: sp("starlight_polarization"), SigmaColumn: sp("sPA(deg)")}, Values: []string{"Star"}}
}

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestACatalogHeaderMakesAKindAndAFitsHeaderDoesNot(t *testing.T) {
	s := open(t)
	d := catalogDataset(t, s, starCols...)
	if d.Kind != KindCatalog {
		t.Fatalf("kind %q", d.Kind)
	}
	f, err := s.Upsert(workspace.Local, "local-owner", Dataset{Path: "/data/a.fits", Name: "a.fits", SizeBytes: 1, ModifiedAt: time.Unix(1, 0), Header: json.RawMessage(`{"naxis":2}`)})
	if err != nil || f.Kind != KindFITS || f.CatalogDeclaration != nil {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestValidationNamesEveryProblemAndNothingIsAssumed(t *testing.T) {
	s := open(t)
	d := catalogDataset(t, s, starCols...)
	if bad, err := ValidateCatalogDeclaration(d, goodDecl()); err != nil || len(bad) != 0 {
		t.Fatalf("a good declaration was refused: %v %v", bad, err)
	}
	mod := func(f func(c *CatalogDeclaration)) CatalogDeclaration {
		c := goodDecl()
		f(&c)
		return c
	}
	for want, c := range map[string]CatalogDeclaration{
		"choose the celestial frame":     mod(func(c *CatalogDeclaration) { c.Frame = "" }),
		"needs an equinox":               mod(func(c *CatalogDeclaration) { c.Equinox = nil }),
		"fk5 needs an equinox such as J": mod(func(c *CatalogDeclaration) { c.Equinox = sp("B1950") }),
		"icrs has no equinox":            mod(func(c *CatalogDeclaration) { c.Frame, c.Format = "icrs", "degrees" }),
		"not written in hours":           mod(func(c *CatalogDeclaration) { c.Frame, c.Equinox = "galactic", nil }),
		"no column named":                mod(func(c *CatalogDeclaration) { c.Lon = "RA" }),
		"holds arrays":                   mod(func(c *CatalogDeclaration) { c.Values = []string{"vec"} }),
		"different columns":              mod(func(c *CatalogDeclaration) { c.Lat = c.Lon }),
		"say how the positions":          mod(func(c *CatalogDeclaration) { c.Format = "" }),
		"choose the unit":                mod(func(c *CatalogDeclaration) { c.Angle.Unit = "" }),
		"no pixel grid":                  mod(func(c *CatalogDeclaration) { c.Angle.Frame = "pixel" }),
		"quantity must be":               mod(func(c *CatalogDeclaration) { c.Angle.Quantity = sp("vibes") }),
		"columns of their own":           mod(func(c *CatalogDeclaration) { c.Angle.SigmaColumn = sp("PA(deg)") }),
		"listed twice":                   mod(func(c *CatalogDeclaration) { c.Values = []string{"Star", "Star"} }),
		"at most 8":                      mod(func(c *CatalogDeclaration) { c.Values = make([]string, 9) }),
	} {
		bad, err := ValidateCatalogDeclaration(d, c)
		if err != nil || !strings.Contains(strings.Join(bad, "|"), want) {
			t.Errorf("want a reason containing %q, got %v (%v)", want, bad, err)
		}
	}
	// a quantity that is not declared is allowed (the file never says); it stays null
	c := goodDecl()
	c.Angle.Quantity = nil
	if bad, _ := ValidateCatalogDeclaration(d, c); len(bad) != 0 {
		t.Errorf("undeclared quantity refused: %v", bad)
	}
	fits, _ := s.Upsert(workspace.Local, "local-owner", Dataset{Path: "/data/a.fits", Name: "a.fits", SizeBytes: 1, ModifiedAt: time.Unix(1, 0), Header: json.RawMessage(`{"naxis":2}`)})
	if _, err := ValidateCatalogDeclaration(fits, goodDecl()); err != ErrNotACatalog {
		t.Errorf("a FITS dataset took a catalog declaration: %v", err)
	}
}

func TestADeclarationIsStaleWhenTheFileChangesAndFollowsTheDatasetOutOfExistence(t *testing.T) {
	s := open(t)
	d := catalogDataset(t, s, starCols...)
	if err := s.SetCatalogDeclaration(d, goodDecl(), "user"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(workspace.Local, d.ID)
	c := got.CatalogDeclaration
	if c == nil || c.Stale || c.DeclaredBy != "user" || c.Lon != "RA(J2000)" || c.Angle == nil || *c.Angle.Quantity != "starlight_polarization" || c.DeclaredAt.IsZero() {
		t.Fatalf("%+v", c)
	}
	// the same path registered again after the file changed
	hdr, _ := json.Marshal(CatalogHeader{Kind: KindCatalog, Format: "tsv", Rows: 4, Columns: starCols})
	if _, err := s.Upsert(workspace.Local, "local-owner", Dataset{Path: "/data/stars.dat", Name: "stars.dat", SizeBytes: 140, ModifiedAt: time.Unix(2000, 0), Header: hdr}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(workspace.Local, d.ID)
	if got.CatalogDeclaration == nil || !got.CatalogDeclaration.Stale {
		t.Fatalf("a declaration about other bytes must be stale: %+v", got.CatalogDeclaration)
	}
	all, _ := s.List(workspace.Local)
	if all[0].CatalogDeclaration == nil || !all[0].CatalogDeclaration.Stale {
		t.Fatalf("the list must say so too: %+v", all[0].CatalogDeclaration)
	}
	// confirming it again makes it fresh; withdrawing removes it
	if err := s.SetCatalogDeclaration(got, goodDecl(), "user"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Get(workspace.Local, d.ID); got.CatalogDeclaration.Stale {
		t.Fatal("confirmed declaration is still stale")
	}
	if err := s.ClearCatalogDeclaration(d.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Get(workspace.Local, d.ID); got.CatalogDeclaration != nil {
		t.Fatal("withdrawn declaration is still there")
	}
	if err := s.SetCatalogDeclaration(got, goodDecl(), "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM datasets WHERE id=?`, d.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM catalog_declarations`).Scan(&n)
	if n != 0 {
		t.Fatal("a declaration outlived its dataset")
	}
}

func TestACatalogIsNeverGivenABrightnessUnit(t *testing.T) {
	s := open(t)
	d := catalogDataset(t, s, starCols...)
	applied, err := s.SetUnitDeclarations([]Dataset{d}, "Jy", "user")
	if err != nil || len(applied) != 0 {
		t.Fatalf("applied %v, %v", applied, err)
	}
	ids, err := s.NoBrightnessIDs(workspace.Local)
	if err != nil || !ids[d.ID] {
		t.Fatalf("%v %v", ids, err)
	}
}
