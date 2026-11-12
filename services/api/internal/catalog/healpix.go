package catalog

import "encoding/json"

// KindHealpix is the Kind of a HEALPix map (ADR 0067): a binary table of Stokes values and their covariances, one row per pixel of
// the whole sky, such as a Planck HFI SkyMap. It has no pixel grid and no WCS; it is only sampled onto another dataset's grid.
const KindHealpix = "healpix"

// HealpixColumn is one column the worker found and uses, with its TUNIT as written.
type HealpixColumn struct {
	Name string  `json:"name"`
	Unit *string `json:"unit"`
}

// HealpixHeader is what the worker read from a HEALPix map's table header when it was registered: the Header of a healpix dataset.
// The worker refuses to describe a map that does not state POLCCONV and COORDSYS, so a registered one always has both.
type HealpixHeader struct {
	Kind                  string          `json:"kind"`
	Nside                 int             `json:"nside"`
	Npix                  int64           `json:"npix"`
	Ordering              string          `json:"ordering"` // RING or NESTED
	Coordsys              string          `json:"coordsys"` // as the file writes it (G, GALACTIC, ...)
	Frame                 string          `json:"frame"`    // galactic, celestial or ecliptic
	Polcconv              string          `json:"polcconv"` // COSMO or IAU
	Columns               []HealpixColumn `json:"columns"`
	StokesUnit            *string         `json:"stokes_unit"`
	CovarianceUnit        *string         `json:"covariance_unit"`
	HasQUCov              bool            `json:"has_qu_cov"`
	HasI                  bool            `json:"has_i"`
	BadData               *float64        `json:"bad_data"`
	PixelResolutionArcmin float64         `json:"pixel_resolution_arcmin"`
	Extname               *string         `json:"extname"`
	Telescope             *string         `json:"telescope"`
	Frequency             *string         `json:"frequency"`
	ProvenanceNote        string          `json:"provenance_note"`
}

// ParseHealpixHeader reads the header of a healpix dataset; ok is false for any other kind.
func ParseHealpixHeader(raw json.RawMessage) (h HealpixHeader, ok bool) {
	if json.Unmarshal(raw, &h) != nil || h.Kind != KindHealpix {
		return HealpixHeader{}, false
	}
	return h, true
}

// HasPixelGrid is true for an image or cube. A point catalog (rows of sources) and a HEALPix map (pixels of the whole sky, no WCS)
// have none, so every operation that reads planes, pixels or a WCS refuses them.
func (d Dataset) HasPixelGrid() bool { return d.Kind != KindCatalog && d.Kind != KindHealpix }

// NoGridReason is a sentence saying why a dataset without a pixel grid cannot be used where one is needed, or "" for an image.
func (d Dataset) NoGridReason() string {
	switch d.Kind {
	case KindCatalog:
		return "this is a point catalog: it has no pixel grid, so this operation does not apply to it"
	case KindHealpix:
		return "this is a HEALPix map: it has no pixel grid or WCS of its own, so this operation does not apply to it; sample it onto a dataset's grid first"
	}
	return ""
}
