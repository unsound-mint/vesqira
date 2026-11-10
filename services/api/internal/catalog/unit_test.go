package catalog

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openForUnits(t *testing.T) *Store {
	t.Helper()
	s, err := Open("file:" + filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func register(t *testing.T, s *Store, name, header string, size int64) Dataset {
	t.Helper()
	d, err := s.Upsert(Dataset{Path: "/data/" + name, Name: name, SizeBytes: size, ModifiedAt: time.Unix(1700000000, 0), Header: json.RawMessage(header)})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

const noBunit = `{"bunit":null,"bunit_status":"unresolved"}`
const withBunit = `{"bunit":"K","bunit_status":"declared"}`

// a header with a unit but no status (or the status but no unit) still states one
const bunitWithoutStatus = `{"bunit":"K"}`
const statusWithoutBunit = `{"bunit_status":"declared"}`

func TestADeclaredUnitIsAnOverlayThatNeverTouchesTheHeaderFacts(t *testing.T) {
	s := openForUnits(t)
	d := register(t, s, "a.fits", noBunit, 100)
	if err := s.SetUnitDeclaration(d, "Jy/beam", "user"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(d.ID)
	if err != nil || got.UnitDeclaration == nil || got.UnitDeclaration.Unit != "Jy/beam" || got.UnitDeclaration.DeclaredBy != "user" || got.UnitDeclaration.Stale {
		t.Fatalf("%+v %v", got.UnitDeclaration, err)
	}
	if string(got.Header) != noBunit {
		t.Fatalf("the header facts the file gave were changed: %s", got.Header)
	}
	list, _ := s.List()
	if len(list) != 1 || list[0].UnitDeclaration == nil || list[0].UnitDeclaration.Unit != "Jy/beam" {
		t.Fatalf("list: %+v", list)
	}
	if fresh, _ := s.FreshUnits(); fresh[d.ID] != "Jy/beam" {
		t.Fatalf("FreshUnits misses it: %v", fresh)
	}
	// withdrawing returns to what the file said: nothing
	if err := s.ClearUnitDeclaration(d.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(d.ID); got.UnitDeclaration != nil {
		t.Fatal("still declared after withdrawing")
	}
}

func TestAFileThatStatesItsUnitCannotBeGivenAnother(t *testing.T) {
	s := openForUnits(t)
	d := register(t, s, "k.fits", withBunit, 100)
	if err := s.SetUnitDeclaration(d, "Jy/beam", "user"); !errors.Is(err, ErrUnitInFile) {
		t.Fatalf("a declaration over the file's own BUNIT must be refused: %v", err)
	}
	if got, _ := s.Get(d.ID); got.UnitDeclaration != nil {
		t.Fatal("it was stored anyway")
	}
}

func TestAFileThatStatesItsUnitInEitherFieldCannotBeGivenAnother(t *testing.T) {
	s := openForUnits(t)
	for name, h := range map[string]string{"unit only": bunitWithoutStatus, "status only": statusWithoutBunit} {
		d := register(t, s, name+".fits", h, 100)
		if err := s.SetUnitDeclaration(d, "Jy/beam", "user"); !errors.Is(err, ErrUnitInFile) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// the worker writes null, not a missing field, for a file without BUNIT
	if err := s.SetUnitDeclaration(register(t, s, "none.fits", noBunit, 100), "Jy/beam", "user"); err != nil {
		t.Fatal(err)
	}
}

func TestAUnitIsKeptAsTypedAndOnlyPrintableText(t *testing.T) {
	for in, ok := range map[string]bool{
		"Jy/beam": true, "K km/s": true, "10^-20 erg/s/cm2/Hz": true, "µJy": true, "ADU": true, "counts/s": true,
		"W m-2 Hz-1 sr-1": true, "(km/s)^2": true, "e-/s": true, "% of peak": true, "MJy/sr": true,
		"m²": true, "W m-2 µm-1": true, "°": true, "Jy·km/s": true,
		"<b>K</b>": false, "K&amp;": false, `K"`: false, "K'": false, `K\n`: false, "K`m`": false, "K;rm": false, "K=1": false,
		"": false, " K": false, "K ": false, "K  km/s": false, "K\nkm": false, "K\x00": false, "\tK": false,
		strings.Repeat("a", 40): true, strings.Repeat("a", 41): false, strings.Repeat("µ", 40): true,
	} {
		if got := ValidUnit(in); got != ok {
			t.Errorf("ValidUnit(%q) = %v, want %v", in, got, ok)
		}
	}
	s := openForUnits(t)
	d := register(t, s, "a.fits", noBunit, 100)
	if err := s.SetUnitDeclaration(d, "K\nkm", "user"); !errors.Is(err, ErrInvalidUnit) {
		t.Fatalf("%v", err)
	}
	if err := s.SetUnitDeclaration(d, "Jy/beam", "user"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUnitDeclaration(d, "mJy/beam", "user"); err != nil { // declaring again replaces it
		t.Fatal(err)
	}
	if got, _ := s.Get(d.ID); got.UnitDeclaration.Unit != "mJy/beam" {
		t.Fatalf("%+v", got.UnitDeclaration)
	}
}

// A declaration describes the bytes that were registered when it was made; if the file changes it says so.
func TestADeclarationMadeAboutDifferentBytesIsStale(t *testing.T) {
	s := openForUnits(t)
	d := register(t, s, "a.fits", noBunit, 100)
	if err := s.SetUnitDeclaration(d, "Jy/beam", "user"); err != nil {
		t.Fatal(err)
	}
	register(t, s, "a.fits", noBunit, 250) // the file was rewritten and registered again
	got, _ := s.Get(d.ID)
	if got.UnitDeclaration == nil || !got.UnitDeclaration.Stale {
		t.Fatalf("%+v", got.UnitDeclaration)
	}
	// a stale declaration describes other bytes: it settles nothing, and declaring again makes it current
	if fresh, _ := s.FreshUnits(); len(fresh) != 0 {
		t.Fatalf("a stale declaration counted as current: %v", fresh)
	}
	if err := s.SetUnitDeclaration(got, "Jy/beam", "user"); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := s.FreshUnits(); fresh[d.ID] != "Jy/beam" {
		t.Fatalf("%v", fresh)
	}
}

func TestDeletingADatasetTakesItsDeclarationWithIt(t *testing.T) {
	s := openForUnits(t)
	d := register(t, s, "a.fits", noBunit, 100)
	if err := s.SetUnitDeclaration(d, "Jy/beam", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DELETE FROM datasets WHERE id=?`, d.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB().QueryRow(`SELECT COUNT(*) FROM dataset_units`).Scan(&n)
	if n != 0 {
		t.Fatalf("an orphan declaration was left: %d", n)
	}
}

// Orientation maps and the reliability maps declared for them have no brightness: a brightness unit would be a wrong claim.
func TestOrientationAndReliabilityMapsCannotBeGivenABrightnessUnit(t *testing.T) {
	s := openForUnits(t)
	psi := register(t, s, "psi.fits", noBunit, 100)
	rel := register(t, s, "rel.fits", noBunit, 100)
	plain := register(t, s, "map.fits", noBunit, 100)
	relID := rel.ID
	if err := s.SetInterpretation(psi, Interpretation{Kind: "axial_orientation", Unit: "deg", ReliabilityID: &relID}); err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string]Dataset{"the orientation map": psi, "its reliability map": rel} {
		if err := s.SetUnitDeclaration(d, "Jy/beam", "user"); !errors.Is(err, ErrNotBrightness) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := s.SetUnitDeclaration(plain, "Jy/beam", "user"); err != nil {
		t.Fatal(err)
	}
	if no, _ := s.NoBrightnessIDs(); !no[psi.ID] || !no[rel.ID] || no[plain.ID] {
		t.Fatalf("%v", no)
	}
	// a proposal by a run import counts the same: nobody has confirmed it, and it is still not a brightness
	other := register(t, s, "psi2.fits", noBunit, 100)
	if err := s.SetInterpretation(other, Interpretation{Kind: "axial_orientation", Unit: "rad", DeclaredBy: "vgt-run-adapter"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUnitDeclaration(other, "K", "user"); !errors.Is(err, ErrNotBrightness) {
		t.Fatalf("%v", err)
	}
}

func TestDeclaringForSeveralIsAllOrNoneAndSkipsWhatCannotHaveOne(t *testing.T) {
	s := openForUnits(t)
	a, b := register(t, s, "a.fits", noBunit, 100), register(t, s, "b.fits", noBunit, 100)
	k := register(t, s, "k.fits", withBunit, 100)
	applied, err := s.SetUnitDeclarations([]Dataset{a, k, b}, "Jy/beam", "user")
	if err != nil || len(applied) != 2 || applied[0].ID != a.ID || applied[1].ID != b.ID {
		t.Fatalf("a file that states its unit is skipped, not an error: %v %v", applied, err)
	}
	if got, _ := s.Get(k.ID); got.UnitDeclaration != nil {
		t.Fatal("the file with its own unit was declared")
	}
	// an invalid unit applies to none of them
	c := register(t, s, "c.fits", noBunit, 100)
	if _, err := s.SetUnitDeclarations([]Dataset{c}, "K\nkm", "user"); !errors.Is(err, ErrInvalidUnit) {
		t.Fatalf("%v", err)
	}
	if got, _ := s.Get(c.ID); got.UnitDeclaration != nil {
		t.Fatal("an invalid unit was stored")
	}
}

// A file that says its unit now (rewritten with a BUNIT) must not keep a declaration made while it had none: it could not be
// shown or withdrawn, and would come back if the BUNIT went away again.
func TestAFileThatGainsABunitLosesItsDeclaration(t *testing.T) {
	s := openForUnits(t)
	d := register(t, s, "a.fits", noBunit, 100)
	if err := s.SetUnitDeclaration(d, "Jy/beam", "user"); err != nil {
		t.Fatal(err)
	}
	register(t, s, "a.fits", withBunit, 120)
	if got, _ := s.Get(d.ID); got.UnitDeclaration != nil {
		t.Fatalf("a declaration outlived the file stating its own unit: %+v", got.UnitDeclaration)
	}
	register(t, s, "a.fits", noBunit, 130) // the BUNIT disappears again
	if got, _ := s.Get(d.ID); got.UnitDeclaration != nil {
		t.Fatal("the old declaration came back")
	}
}
