package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFactsOfEachKind(t *testing.T) {
	t.Parallel()
	cube := factsOf(KindFITS, json.RawMessage(`{"naxis":3,"axes":[{"length":64},{"length":32},{"length":10}],"bunit":"K","bunit_status":"declared"}`))
	if cube.Naxis == nil || *cube.Naxis != 3 || *cube.Channels != 10 || *cube.Width != 64 || *cube.Height != 32 || cube.Bunit == nil || *cube.Bunit != "K" || !cube.BunitStated {
		t.Errorf("cube: %+v", cube)
	}
	// a plane has one channel, and a header with no unit states none: nothing is invented for either
	plane := factsOf(KindFITS, json.RawMessage(`{"naxis":2,"axes":[{"length":8},{"length":9}],"bunit":null,"bunit_status":"unresolved"}`))
	if *plane.Channels != 1 || plane.Bunit != nil || plane.BunitStated {
		t.Errorf("plane: %+v", plane)
	}
	// a degenerate third axis (length 1) is a plane too
	if f := factsOf(KindFITS, json.RawMessage(`{"naxis":3,"axes":[{"length":8},{"length":9},{"length":1}]}`)); *f.Channels != 1 {
		t.Errorf("degenerate axis: %+v", f)
	}
	// no axes: no size is claimed
	if f := factsOf(KindFITS, json.RawMessage(`{"ok":true,"axes":[]}`)); f.Width != nil || f.Height != nil {
		t.Errorf("no axes: %+v", f)
	}
	cat := factsOf(KindCatalog, json.RawMessage(`{"kind":"catalog","format":"tsv","rows":4,"columns":[{"name":"RA","dtype":"float","unit":"deg","scalar":true},{"name":"name","dtype":"string","scalar":true},{"name":"n","dtype":"int","scalar":true},{"name":"v","dtype":"float","scalar":false}]}`))
	if len(cat.NumericColumns) != 2 || cat.NumericColumns[0].Name != "RA" || cat.NumericColumns[0].Unit == nil || *cat.NumericColumns[0].Unit != "deg" ||
		cat.NumericColumns[1].Name != "n" || cat.NumericColumns[1].Unit != nil || cat.Naxis != nil || cat.Channels != nil || cat.Rows == nil || *cat.Rows != 4 {
		t.Errorf("catalog: %+v", cat)
	}
	if f := factsOf(KindHealpix, json.RawMessage(`{"kind":"healpix","nside":64}`)); f.Naxis != nil || f.BunitStated || f.NumericColumns == nil || len(f.NumericColumns) != 0 || f.Nside == nil || *f.Nside != 64 {
		t.Errorf("healpix: %+v", f)
	}
}

func TestSummaryLeavesOutTheHeavyFields(t *testing.T) {
	t.Parallel()
	d := Dataset{ID: 1, Name: "a.fits", Header: json.RawMessage(`{"axes":[]}`), Locations: []Location{{}}, LocationCount: 1, OrientationHeader: &VGTHeader{}}
	b, err := json.Marshal(d.Summary())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"header"`, `"locations"`, `"orientationHeader"`} {
		if strings.Contains(string(b), field) {
			t.Errorf("the summary carries %s: %s", field, b)
		}
	}
}
