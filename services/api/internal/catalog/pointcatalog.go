package catalog

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// KindCatalog and KindFITS are the values of Dataset.Kind. A point catalog is a table of sources (ADR 0044); a FITS dataset is
// an image or cube. Everything registered before catalogs existed is FITS.
const (
	KindFITS    = "fits"
	KindCatalog = "catalog"
)

// CatalogAngle declares an angle column of a catalog. A catalog has no pixel grid, so the frame is always "sky": a position
// angle east of north, in the catalog's own celestial frame (the same sense as the "sky" frame of an orientation map).
type CatalogAngle struct {
	Column string `json:"column"`
	Unit   string `json:"unit"`  // "deg" or "rad", required
	Frame  string `json:"frame"` // "sky"
	// Quantity is what the angle describes, in the vocabulary of an orientation map's declaration. Nil means not declared.
	Quantity    *string `json:"quantity"`
	SigmaColumn *string `json:"sigmaColumn"`
}

// CatalogDeclaration is what a person says about a catalog's columns. The file declares none of it (a plain table has no
// frame, no units, no meaning); a proposal from the column names is only shown, never stored until it is confirmed. It is an
// overlay: the file and the columns the worker read are never changed.
type CatalogDeclaration struct {
	Lon    string `json:"lon"`
	Lat    string `json:"lat"`
	Format string `json:"format"` // degrees, hours_degrees, sexagesimal_hours_degrees, sexagesimal_degrees
	Frame  string `json:"frame"`  // icrs, fk5, fk4, galactic
	// Equinox is required for fk5 (J...) and fk4 (B...), and refused for the others.
	Equinox *string       `json:"equinox"`
	Angle   *CatalogAngle `json:"angle"`
	// Values are further columns to show next to a source (at most MaxCatalogValues).
	Values     []string  `json:"values"`
	DeclaredBy string    `json:"declaredBy"` // "user"
	DeclaredAt time.Time `json:"declaredAt"`
	// Tier and MappingVersion say how far the declaration is established (ADR 0055).
	Tier           string `json:"tier"`
	MappingVersion string `json:"mappingVersion"`
	// Stale is true when the file's registered size or modification time differs from what it was at the declaration.
	Stale bool `json:"stale"`
}

// MaxCatalogValues is the most value columns one declaration may list.
const MaxCatalogValues = 8

// CatalogColumn is one column as the worker read it when the file was registered.
type CatalogColumn struct {
	Name   string  `json:"name"`
	Dtype  string  `json:"dtype"` // float, int, bool, string, other
	Unit   *string `json:"unit"`
	Scalar bool    `json:"scalar"`
}

// CatalogHeader is what the worker read from a catalog when it was registered: the Header of a catalog dataset.
type CatalogHeader struct {
	Kind    string          `json:"kind"`
	Format  string          `json:"format"`
	Rows    int64           `json:"rows"`
	Columns []CatalogColumn `json:"columns"`
}

// ParseCatalogHeader reads the header of a catalog dataset; ok is false for a FITS dataset.
func ParseCatalogHeader(raw json.RawMessage) (h CatalogHeader, ok bool) {
	if json.Unmarshal(raw, &h) != nil || h.Kind != KindCatalog {
		return CatalogHeader{}, false
	}
	return h, true
}

// kindOf says what a registered header describes.
func kindOf(raw json.RawMessage) string {
	var k struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(raw, &k) == nil && k.Kind == KindCatalog {
		return KindCatalog
	}
	return KindFITS
}

var (
	catalogFormats  = map[string]bool{"degrees": true, "hours_degrees": true, "sexagesimal_hours_degrees": true, "sexagesimal_degrees": true}
	catalogFrames   = map[string]bool{"icrs": true, "fk5": true, "fk4": true, "galactic": true}
	angleQuantities = map[string]bool{"b_field": true, "gradient": true, "dust_polarization": true, "starlight_polarization": true, "unspecified": true}
	equinoxRE       = regexp.MustCompile(`^[JB][0-9]{4}(\.[0-9]{1,3})?$`)
)

// ErrNotACatalog is returned when a catalog operation is asked of a FITS dataset.
var ErrNotACatalog = errors.New("this dataset is not a point catalog")

// ValidateCatalogDeclaration lists everything wrong with in against the columns of catalog dataset d; an empty list means it can
// be stored. It checks the declaration is coherent, not that the values will all be readable (the worker reports those).
func ValidateCatalogDeclaration(d Dataset, in CatalogDeclaration) ([]string, error) {
	h, ok := ParseCatalogHeader(d.Header)
	if !ok {
		return nil, ErrNotACatalog
	}
	cols := map[string]CatalogColumn{}
	for _, c := range h.Columns {
		cols[c.Name] = c
	}
	var bad []string
	need := func(what, name string) {
		c, ok := cols[name]
		switch {
		case name == "":
			bad = append(bad, what+": choose a column")
		case !ok:
			bad = append(bad, fmt.Sprintf("%s: the table has no column named %q", what, name))
		case !c.Scalar:
			bad = append(bad, fmt.Sprintf("%s: column %q holds arrays, not one value per row", what, name))
		}
	}
	need("longitude", in.Lon)
	need("latitude", in.Lat)
	if in.Lon != "" && in.Lon == in.Lat {
		bad = append(bad, "longitude and latitude must be different columns")
	}
	if !catalogFormats[in.Format] {
		bad = append(bad, "format: say how the positions are written (degrees, hours and degrees, or sexagesimal)")
	}
	if !catalogFrames[in.Frame] {
		bad = append(bad, "frame: choose the celestial frame (icrs, fk5, fk4 or galactic); Vesqira does not assume it")
	} else {
		eq := ""
		if in.Equinox != nil {
			eq = *in.Equinox
		}
		switch in.Frame {
		case "fk5", "fk4":
			prefix := map[string]string{"fk5": "J", "fk4": "B"}[in.Frame]
			if !equinoxRE.MatchString(eq) || eq[:1] != prefix {
				bad = append(bad, fmt.Sprintf("equinox: %s needs an equinox such as %s%s", in.Frame, prefix, map[string]string{"J": "2000", "B": "1950"}[prefix]))
			}
		default:
			if eq != "" {
				bad = append(bad, fmt.Sprintf("equinox: %s has no equinox", in.Frame))
			}
		}
		if in.Frame == "galactic" && (in.Format == "hours_degrees" || in.Format == "sexagesimal_hours_degrees") {
			bad = append(bad, "format: Galactic longitude is not written in hours")
		}
	}
	if a := in.Angle; a != nil {
		need("angle", a.Column)
		if a.Unit != "deg" && a.Unit != "rad" {
			bad = append(bad, "angle: choose the unit, deg or rad; Vesqira does not guess it from the values")
		}
		if a.Frame != "sky" {
			bad = append(bad, "angle: a catalog has no pixel grid; its angle is a position angle on the sky (east of north)")
		}
		if a.Quantity != nil && !angleQuantities[*a.Quantity] {
			bad = append(bad, "angle: quantity must be b_field, gradient, dust_polarization, starlight_polarization, unspecified or not declared")
		}
		if a.SigmaColumn != nil {
			need("uncertainty", *a.SigmaColumn)
		}
		if a.Column == in.Lon || a.Column == in.Lat || (a.SigmaColumn != nil && (*a.SigmaColumn == a.Column || *a.SigmaColumn == in.Lon || *a.SigmaColumn == in.Lat)) {
			bad = append(bad, "angle: the angle and its uncertainty must be columns of their own")
		}
	}
	if len(in.Values) > MaxCatalogValues {
		bad = append(bad, fmt.Sprintf("values: at most %d columns", MaxCatalogValues))
	}
	seen := map[string]bool{}
	for _, v := range in.Values {
		need("values", v)
		if seen[v] {
			bad = append(bad, fmt.Sprintf("values: column %q is listed twice", v))
		}
		seen[v] = true
	}
	return bad, nil
}

// catalogDeclaration reads the stored declaration of dataset id, with its staleness against the registered file.
func (s *Store) catalogDeclaration(id int64, d Dataset) (*CatalogDeclaration, error) {
	var raw string
	var by, tier, mapping string
	var at, size, mod int64
	err := s.db.QueryRow(`SELECT declaration,declared_by,declared_at,size_bytes,modified_ns,tier,mapping_version FROM catalog_declarations WHERE dataset_id=?`, id).Scan(&raw, &by, &at, &size, &mod, &tier, &mapping)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c CatalogDeclaration
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, fmt.Errorf("stored catalog declaration of dataset %d: %w", id, err)
	}
	c.DeclaredBy, c.DeclaredAt, c.Tier, c.MappingVersion = by, time.Unix(at, 0).UTC(), tier, mapping
	c.Stale = size != d.SizeBytes || mod != d.ModifiedAt.UnixNano()
	return &c, nil
}

// SetCatalogDeclaration stores a declaration the caller has validated (ValidateCatalogDeclaration). The registered size and
// modification time are kept with it, so a later change of the file shows the declaration as stale.
func (s *Store) SetCatalogDeclaration(d Dataset, in CatalogDeclaration, by string) error {
	if by == "" {
		by = "user"
	}
	if in.Values == nil {
		in.Values = []string{}
	}
	in.DeclaredBy, in.DeclaredAt, in.Stale, in.Tier, in.MappingVersion = "", time.Time{}, false, "", "" // kept in their own columns
	tier, mapping := TierOf(by)
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO catalog_declarations(dataset_id,declaration,declared_by,declared_at,size_bytes,modified_ns,tier,mapping_version) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(dataset_id) DO UPDATE SET declaration=excluded.declaration,declared_by=excluded.declared_by,declared_at=excluded.declared_at,
			size_bytes=excluded.size_bytes,modified_ns=excluded.modified_ns,tier=excluded.tier,mapping_version=excluded.mapping_version`,
		d.ID, string(b), by, time.Now().Unix(), d.SizeBytes, d.ModifiedAt.UnixNano(), tier, mapping)
	return err
}

// ClearCatalogDeclaration withdraws a catalog's declaration.
func (s *Store) ClearCatalogDeclaration(id int64) error {
	_, err := s.db.Exec(`DELETE FROM catalog_declarations WHERE dataset_id=?`, id)
	return err
}
