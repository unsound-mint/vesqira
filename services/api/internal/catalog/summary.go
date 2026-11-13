package catalog

import (
	"encoding/json"
	"time"
)

// Facts are the few header facts the explorer, the pickers, the overlay form and the filters read for every dataset at once. They are
// read from the stored header when a dataset is loaded, so the list can leave the header out (a header runs to a few kilobytes; a
// project holds thousands of datasets). A fact a kind does not have is null, false or empty, never a default that looks like data.
type Facts struct {
	// Naxis is the number of image axes the header declares (an image or cube only).
	Naxis *int `json:"naxis"`
	// Channels is the length of the third axis, 1 for a plane (an image or cube only).
	Channels *int `json:"channels"`
	// Width and Height are the lengths of the first two axes, as declared; null when the header has fewer than two axes.
	Width  *int `json:"width"`
	Height *int `json:"height"`
	// Ctypes are the CTYPE cards of the first two axes as declared, "" where an axis declares none: what tells a sky grid from a pixel grid.
	Ctypes []string `json:"ctypes"`
	// Bunit is the BUNIT card as the file states it; null when it has none.
	Bunit *string `json:"bunit"`
	// BunitStated is true when the file itself states a unit: then nothing can be declared for it.
	BunitStated bool `json:"bunitStated"`
	// Rows is a catalog's row count; null for anything else.
	Rows *int64 `json:"rows"`
	// Nside is a HEALPix map's resolution parameter; null for anything else.
	Nside *int `json:"nside"`
	// NumericColumns are a catalog's scalar numeric columns with the unit the file gives, in the file's order (a catalog only; otherwise empty).
	NumericColumns []FactColumn `json:"numericColumns"`
}

// FactColumn is a catalog column as the overlay form offers it: its name and the unit the file states for it (null when it states none).
type FactColumn struct {
	Name string  `json:"name"`
	Unit *string `json:"unit"`
}

func factsOf(kind string, raw json.RawMessage) Facts {
	f := Facts{NumericColumns: []FactColumn{}, Ctypes: []string{}}
	switch kind {
	case KindCatalog:
		if h, ok := ParseCatalogHeader(raw); ok {
			f.Rows = &h.Rows
			for _, c := range h.Columns {
				if c.Scalar && (c.Dtype == "float" || c.Dtype == "int") {
					f.NumericColumns = append(f.NumericColumns, FactColumn{Name: c.Name, Unit: c.Unit})
				}
			}
		}
	case KindHealpix:
		var h struct {
			Nside *int `json:"nside"`
		}
		if json.Unmarshal(raw, &h) == nil {
			f.Nside = h.Nside
		}
	case KindFITS:
		var h struct {
			Naxis *int `json:"naxis"`
			Axes  []struct {
				Length int     `json:"length"`
				Ctype  *string `json:"ctype"`
			} `json:"axes"`
			Bunit      *string `json:"bunit"`
			BunitState string  `json:"bunit_status"`
		}
		if json.Unmarshal(raw, &h) != nil {
			return f
		}
		f.Naxis, f.Bunit, f.BunitStated = h.Naxis, h.Bunit, h.BunitState == "declared" || h.Bunit != nil
		n := 1
		if len(h.Axes) > 2 && h.Axes[2].Length > 1 {
			n = h.Axes[2].Length
		}
		f.Channels = &n
		for i := 0; i < len(h.Axes) && i < 2; i++ {
			c := ""
			if h.Axes[i].Ctype != nil {
				c = *h.Axes[i].Ctype
			}
			f.Ctypes = append(f.Ctypes, c)
		}
		if len(h.Axes) >= 2 {
			w, ht := h.Axes[0].Length, h.Axes[1].Length
			f.Width, f.Height = &w, &ht
		}
	}
	return f
}

// Summary is a dataset as the list shows it: everything the explorer, pickers, boards and the first paint of the inspector read,
// and none of what is heavy (the header, the file-header reading, the locations), which GET /api/datasets/{id} sends. Dataset.Summary
// builds it; the two never disagree on a field they share.
type Summary struct {
	ID              int64            `json:"id"`
	Path            string           `json:"path"`
	Name            string           `json:"name"`
	SizeBytes       int64            `json:"sizeBytes"`
	ModifiedAt      time.Time        `json:"modifiedAt"`
	AddedAt         time.Time        `json:"addedAt"`
	HostID          int64            `json:"hostId"`
	Nature          string           `json:"nature"`
	Interpretation  *Interpretation  `json:"interpretation"`
	UnitDeclaration *UnitDeclaration `json:"unitDeclaration"`
	Kind            string           `json:"kind"`
	// CatalogDeclaration is what the person declared about a catalog's columns (small); null for anything else.
	CatalogDeclaration *CatalogDeclaration `json:"catalogDeclaration"`
	Archived           bool                `json:"archived"`
	Corrections        *Corrections        `json:"corrections"`
	Facts              Facts               `json:"facts"`
	// LocationCount is how many files the dataset's bytes are in, the registered one included (the locations themselves are in the detail).
	LocationCount int `json:"locationCount"`
}

// Summary is the part of d the list sends.
func (d Dataset) Summary() Summary {
	return Summary{ID: d.ID, Path: d.Path, Name: d.Name, SizeBytes: d.SizeBytes, ModifiedAt: d.ModifiedAt, AddedAt: d.AddedAt, HostID: d.HostID,
		Nature: d.Nature, Interpretation: d.Interpretation, UnitDeclaration: d.UnitDeclaration, Kind: d.Kind, CatalogDeclaration: d.CatalogDeclaration,
		Archived: d.Archived, Corrections: d.Corrections, Facts: d.Facts, LocationCount: d.LocationCount}
}
