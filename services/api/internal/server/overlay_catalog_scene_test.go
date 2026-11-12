package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/scene"
)

// A figure that draws a catalog is held to the catalog the scene names: a point catalog with a fresh declaration, columns that
// exist, and a declared angle meaning for vectors. Saving a figure does not run the worker, so the server checks it here.
func TestACatalogFigureSceneIsHeldToTheCatalogDeclaration(t *testing.T) {
	t.Parallel()
	quantity := "starlight_polarization"
	header, _ := json.Marshal(catalog.CatalogHeader{Kind: catalog.KindCatalog, Format: "csv", Rows: 3, Columns: []catalog.CatalogColumn{
		{Name: "ra", Dtype: "float", Scalar: true}, {Name: "dec", Dtype: "float", Scalar: true}, {Name: "pa", Dtype: "float", Scalar: true},
		{Name: "p", Dtype: "float", Scalar: true}, {Name: "arr", Dtype: "other", Scalar: false}}})
	img := catalog.Dataset{ID: 1, Name: "base.fits", Kind: catalog.KindFITS, Header: json.RawMessage(`{"axes":[{"length":8},{"length":8}]}`)}
	decl := catalog.CatalogDeclaration{Lon: "ra", Lat: "dec", Format: "degrees", Frame: "icrs", Angle: &catalog.CatalogAngle{Column: "pa", Unit: "deg", Frame: "sky", Quantity: &quantity}}
	stars := catalog.Dataset{ID: 2, Name: "stars.csv", Kind: catalog.KindCatalog, Header: header, CatalogDeclaration: &decl}
	ch := 0
	vp := ViewParams{Kind: "plane", Channel: &ch, Colormap: "gray", Stretch: "linear", Range: "minmax"}
	check := func(overlay string, ds catalog.Dataset) error {
		raw := `{"schema":"scene/1","base":{"datasetId":1,"channel":0,"display":{"mapping":"scalar","colormap":"gray","stretch":"linear","range":"minmax"}},"overlays":[` + overlay + `],"view":{"zoom":1}}`
		sc, err := scene.Parse([]byte(raw))
		if err != nil {
			t.Fatalf("%s: %v", overlay, err)
		}
		_, err = checkScene(sc, img, vp, func(id int64) (catalog.Dataset, error) {
			if id == ds.ID {
				return ds, nil
			}
			return catalog.Dataset{}, catalog.ErrNotFound
		})
		return err
	}
	ok := `{"id":"s","kind":"catalog","datasetId":2,"visible":true,"vectors":true,"lengthColumn":"p","filter":{"column":"p","min":2}}`
	if err := check(ok, stars); err != nil {
		t.Fatalf("a declared catalog with vectors: %v", err)
	}
	stale := stars
	d1 := decl
	d1.Stale = true
	stale.CatalogDeclaration = &d1
	undeclared := stars
	undeclared.CatalogDeclaration = nil
	noQuantity := stars
	d2 := decl
	d2.Angle = &catalog.CatalogAngle{Column: "pa", Unit: "deg", Frame: "sky"}
	noQuantity.CatalogDeclaration = &d2
	asImage := catalog.Dataset{ID: 2, Kind: catalog.KindFITS, Header: img.Header}
	for name, c := range map[string]struct {
		overlay string
		ds      catalog.Dataset
		want    string
	}{
		"a declaration the file outgrew":    {ok, stale, "changed since you declared"},
		"no declaration":                    {ok, undeclared, "no columns are declared"},
		"vectors with no declared quantity": {ok, noQuantity, "does not declare what its angle column"},
		"a column the table does not have":  {`{"id":"s","kind":"catalog","datasetId":2,"visible":true,"colorColumn":"nope"}`, stars, "colour column"},
		"a column that holds arrays":        {`{"id":"s","kind":"catalog","datasetId":2,"visible":true,"filter":{"column":"arr","min":1}}`, stars, "filter column"},
		"a catalog overlay of an image":     {`{"id":"s","kind":"catalog","datasetId":2,"visible":true}`, asImage, "needs a point catalog"},
		"contours of a catalog":             {`{"id":"c","kind":"contours","datasetId":2,"visible":true,"channel":0,"levels":{"mode":"list","values":[1]}}`, stars, "only drawn by a catalog overlay"},
	} {
		err := check(c.overlay, c.ds)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}
