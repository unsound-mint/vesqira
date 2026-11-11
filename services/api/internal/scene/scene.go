// Package scene validates the scene/1 description of what a viewer pane draws (ADR 0042, section 3): a base plane and
// its display settings, overlays in drawing order, and the part of the image in view. The wire shape is described once in
// packages/contracts/bodies.json (Scene and the schemas it refers to); this package is the server's strict reading of
// it. Parse refuses unknown keys, unknown kinds, missing required values, non-finite or out-of-range numbers and lists
// over their bounds, and returns a value whose JSON is re-marshalled by the server, never the client's bytes.
package scene

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"unicode"
	"unicode/utf8"
)

const (
	Version     = "scene/1"
	MaxBytes    = 64 << 10 // 256 saved regions at about 100 bytes each fit with room to spare
	MaxOverlays = 16
	MaxSaved    = 256
	maxCoord    = 1_000_000
	maxZoom     = 16
)

// ErrInvalid wraps every refusal; the message says which part is wrong, so the API can return it as is.
var ErrInvalid = errors.New("invalid scene")

// Kinds are every overlay kind scene/1 specifies. Drawn are the ones this version renders; a figure claiming any other
// would describe layers its picture does not have, so Parse refuses them until the slice that draws them.
var (
	Kinds = []string{"orientation", "regions", "contours", "catalog", "beam", "grid"}
	Drawn = map[string]bool{"orientation": true, "regions": true, "contours": true, "catalog": true, "beam": true, "grid": true}
)

type Scene struct {
	Schema   string    `json:"schema"`
	Base     Base      `json:"base"`
	Overlays []Overlay `json:"overlays"`
	View     View      `json:"view"`
	Link     *Link     `json:"link,omitempty"`
	// Render records how the base raster of a saved figure was drawn, so "draw again" can reproduce it (ADR 0042,
	// section 5). Absent from a live pane's scene and from figures saved before it existed, which draw again from their PNG only.
	Render *Render `json:"render,omitempty"`
}

// Link is the link group a pane was in (ADR 0042, section 4): how its panes followed each other when the scene was made.
type Link struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Basis    string     `json:"basis"`
	Facets   LinkFacets `json:"facets"`
	Datasets []int64    `json:"datasets"`
}

type LinkFacets struct {
	Cursor  bool `json:"cursor"`
	Channel bool `json:"channel"`
	View    bool `json:"view"`
}

// Render is the part of a figure's picture that the scene does not say: the size of the canvas the plane was drawn on (a
// whole multiple of the plane when zoomed in, the display size when zoomed out), the two no-value colours the theme gave
// (the hatching, never a colour of the scale), and the width the figure was composed at.
type Render struct {
	Canvas      RenderCanvas `json:"canvas"`
	Nodata      [2][3]int    `json:"nodata"`
	FigureWidth int          `json:"figureWidth"`
}

type RenderCanvas struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

const (
	maxRenderSide   = 65536
	maxRenderPixels = 1 << 27
)

func parseRender(raw []byte) (*Render, error) {
	var in struct {
		Canvas *struct {
			Width  *int `json:"width"`
			Height *int `json:"height"`
		} `json:"canvas"`
		Nodata      [][]*int `json:"nodata"`
		FigureWidth *int     `json:"figureWidth"`
	}
	if err := strict(raw, &in); err != nil {
		return nil, bad("render: %v", err)
	}
	c := in.Canvas
	if c == nil || c.Width == nil || c.Height == nil || *c.Width < 1 || *c.Height < 1 || *c.Width > maxRenderSide || *c.Height > maxRenderSide || *c.Width**c.Height > maxRenderPixels {
		return nil, bad("render needs a canvas size of at least 1 pixel a side and at most %d pixels", maxRenderPixels)
	}
	if in.FigureWidth == nil || *in.FigureWidth < 200 || *in.FigureWidth > 4096 {
		return nil, bad("render needs the figure width, 200 to 4096 pixels")
	}
	r := &Render{Canvas: RenderCanvas{Width: *c.Width, Height: *c.Height}, FigureWidth: *in.FigureWidth}
	if len(in.Nodata) != 2 {
		return nil, bad("render needs the two no-value colours")
	}
	for i, col := range in.Nodata {
		if len(col) != 3 {
			return nil, bad("a no-value colour is three numbers from 0 to 255")
		}
		for j, v := range col {
			if v == nil || *v < 0 || *v > 255 {
				return nil, bad("a no-value colour is three numbers from 0 to 255")
			}
			r.Nodata[i][j] = *v
		}
	}
	return r, nil
}

type Base struct {
	DatasetID int64     `json:"datasetId"`
	Channel   int       `json:"channel"`
	Spectral  *Spectral `json:"spectral,omitempty"`
	Display   Display   `json:"display"`
}

type Spectral struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

type Display struct {
	Mapping    string    `json:"mapping"`
	Colormap   string    `json:"colormap"`
	Stretch    string    `json:"stretch"`
	Range      string    `json:"range"`
	Limits     []float64 `json:"limits,omitempty"`
	LimitsUnit string    `json:"limitsUnit,omitempty"`
	ScaleGroup string    `json:"scaleGroup,omitempty"`
}

type View struct {
	Zoom   float64 `json:"zoom"`
	Centre *Point  `json:"centre,omitempty"`
}

type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// MaxLinePoints and MaxLineWidth mirror internal/regions: a saved line is copied into the scene as it was saved.
const (
	MaxLinePoints = 64
	MaxLineWidth  = 100000.0
)

// Region is a pixel rectangle (inclusive bounds), circle or, for a saved region only, a line (vertices, width across the
// path, its unit), 0-based with y from the bottom, as the region tools use. A line has no area: it is never the working region.
type Region struct {
	Shape string `json:"shape"`
	X0    *int   `json:"x0,omitempty"`
	Y0    *int   `json:"y0,omitempty"`
	X1    *int   `json:"x1,omitempty"`
	Y1    *int   `json:"y1,omitempty"`
	CX    *int   `json:"cx,omitempty"`
	CY    *int   `json:"cy,omitempty"`
	R     *int   `json:"r,omitempty"`
	// line
	Points    [][]int  `json:"points,omitempty"`
	Width     *float64 `json:"width,omitempty"`
	WidthUnit string   `json:"widthUnit,omitempty"`
}

type SavedRegion struct {
	ID       int64  `json:"id"`
	Shape    Region `json:"shape"`
	Selected bool   `json:"selected,omitempty"`
}

type Sampling struct {
	Mode  string   `json:"mode"`
	GapPx *float64 `json:"gapPx,omitempty"`
	Step  *int     `json:"step,omitempty"`
}

// Levels are the contour levels of a contours overlay: an explicit list, a linear or geometric generator, or multiples of
// a robust noise estimate of the contoured channel (sigma is recorded by a figure).
type Levels struct {
	Mode      string    `json:"mode"`
	Values    []float64 `json:"values,omitempty"`
	Start     *float64  `json:"start,omitempty"`
	Step      *float64  `json:"step,omitempty"`
	Factor    *float64  `json:"factor,omitempty"`
	Count     *int      `json:"count,omitempty"`
	Multiples []float64 `json:"multiples,omitempty"`
	Sigma     *float64  `json:"sigma,omitempty"`
}

// Style is how a drawn overlay is stroked; every field is optional.
type Style struct {
	Color string   `json:"color,omitempty"`
	Width *float64 `json:"width,omitempty"`
	Dash  string   `json:"dash,omitempty"`
}

// Overlay is one layer. Common fields first; the rest belong to one kind and are only set for it.
type Overlay struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	DatasetID int64  `json:"datasetId"`
	Visible   bool   `json:"visible"`
	Style
	// orientation
	Sampling      *Sampling `json:"sampling,omitempty"`
	Length        string    `json:"length,omitempty"`
	Opacity       string    `json:"opacity,omitempty"`
	ReliabilityID *int64    `json:"reliabilityId,omitempty"`
	Threshold     *float64  `json:"threshold,omitempty"`
	// regions
	Active *Region       `json:"active,omitempty"`
	Saved  []SavedRegion `json:"saved"`
	// contours
	Channel *int    `json:"channel,omitempty"`
	Levels  *Levels `json:"levels,omitempty"`
	Unit    *string `json:"unit,omitempty"`
	// beam
	Corner string `json:"corner,omitempty"`
	// grid
	Frame string `json:"frame,omitempty"`
	// catalog: the position columns and the angle's meaning come from the catalog's stored declaration, never from the scene
	Vectors      bool           `json:"vectors,omitempty"`
	LengthColumn string         `json:"lengthColumn,omitempty"`
	LengthScale  *float64       `json:"lengthScale,omitempty"`
	ColorColumn  string         `json:"colorColumn,omitempty"`
	Filter       *CatalogFilter `json:"filter,omitempty"`
}

// CatalogFilter keeps the rows of a catalog whose Column (divided by SigmaColumn when given) is at least Min. A row whose value or
// uncertainty is missing, unreadable or (for an uncertainty) not positive does not pass; the overlay counts such rows.
type CatalogFilter struct {
	Column      string   `json:"column"`
	SigmaColumn string   `json:"sigmaColumn,omitempty"`
	Min         *float64 `json:"min"`
}

// MarshalJSON writes only the fields of the overlay's kind; a regions overlay always has its saved list, even empty.
// Every kind Parse accepts has its own case, so nothing it read is dropped when the server stores its reading.
func (o Overlay) MarshalJSON() ([]byte, error) {
	type common struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		DatasetID int64  `json:"datasetId"`
		Visible   bool   `json:"visible"`
	}
	c := common{o.ID, o.Kind, o.DatasetID, o.Visible}
	switch o.Kind {
	case "orientation":
		return json.Marshal(struct {
			common
			Sampling      *Sampling `json:"sampling"`
			Length        string    `json:"length"`
			Opacity       string    `json:"opacity"`
			ReliabilityID *int64    `json:"reliabilityId,omitempty"`
			Threshold     *float64  `json:"threshold,omitempty"`
			Style
		}{c, o.Sampling, o.Length, o.Opacity, o.ReliabilityID, o.Threshold, o.Style})
	case "contours":
		return json.Marshal(struct {
			common
			Channel *int    `json:"channel"`
			Levels  *Levels `json:"levels"`
			Unit    *string `json:"unit,omitempty"`
			Style
		}{c, o.Channel, o.Levels, o.Unit, o.Style})
	case "catalog":
		return json.Marshal(struct {
			common
			Vectors      bool           `json:"vectors,omitempty"`
			LengthColumn string         `json:"lengthColumn,omitempty"`
			LengthScale  *float64       `json:"lengthScale,omitempty"`
			ColorColumn  string         `json:"colorColumn,omitempty"`
			Filter       *CatalogFilter `json:"filter,omitempty"`
			Style
		}{c, o.Vectors, o.LengthColumn, o.LengthScale, o.ColorColumn, o.Filter, o.Style})
	case "beam":
		return json.Marshal(struct {
			common
			Corner string `json:"corner,omitempty"`
			Style
		}{c, o.Corner, o.Style})
	case "grid":
		return json.Marshal(struct {
			common
			Frame string `json:"frame"`
			Style
		}{c, o.Frame, o.Style})
	case "regions":
		saved := o.Saved
		if saved == nil {
			saved = []SavedRegion{}
		}
		return json.Marshal(struct {
			common
			Active *Region       `json:"active,omitempty"`
			Saved  []SavedRegion `json:"saved"`
		}{c, o.Active, saved})
	}
	return nil, fmt.Errorf("overlay kind %q has no stored form", o.Kind)
}

var (
	idText    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	unitText  = regexp.MustCompile(`^[A-Za-z0-9/*^. -]{0,16}$`)
	plainText = regexp.MustCompile(`^[^\x00-\x1f<>"&\\]{0,64}$`)
	// a catalog column name as the table gives it (names may hold brackets and percent signs), shown in the interface
	columnText = regexp.MustCompile(`^[^\x00-\x1f<>"&\\]{1,64}$`)
)

func bad(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func isOneOf(v string, ok ...string) bool {
	for _, o := range ok {
		if v == o {
			return true
		}
	}
	return false
}

func strict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data")
	}
	return nil
}

// Parse reads and validates a scene. Required values are decoded through pointers, so a missing or null one is refused
// rather than read as zero.
func Parse(raw []byte) (Scene, error) {
	if len(raw) > MaxBytes {
		return Scene{}, bad("larger than %d bytes", MaxBytes)
	}
	var in struct {
		Schema   *string           `json:"schema"`
		Base     *json.RawMessage  `json:"base"`
		Overlays []json.RawMessage `json:"overlays"`
		View     *json.RawMessage  `json:"view"`
		Link     json.RawMessage   `json:"link"`
		Render   *json.RawMessage  `json:"render"`
	}
	if err := strict(raw, &in); err != nil {
		return Scene{}, bad("not a scene object: %v", err)
	}
	if in.Schema == nil || *in.Schema != Version {
		return Scene{}, bad("schema must be %q", Version)
	}
	if in.Base == nil || in.View == nil || in.Overlays == nil {
		return Scene{}, bad("base, overlays and view are required")
	}
	var s Scene
	s.Schema = Version
	var err error
	if s.Base, err = parseBase(*in.Base); err != nil {
		return Scene{}, err
	}
	if s.View, err = parseView(*in.View); err != nil {
		return Scene{}, err
	}
	if in.Link != nil { // present: a null is refused too (json.RawMessage keeps the literal)
		l, err := parseLink(in.Link)
		if err != nil {
			return Scene{}, err
		}
		if !slices.Contains(l.Datasets, s.Base.DatasetID) {
			return Scene{}, bad("link: the group's datasets must include the scene's own")
		}
		s.Link = &l
	}
	if in.Render != nil {
		if s.Render, err = parseRender(*in.Render); err != nil {
			return Scene{}, err
		}
	}
	if len(in.Overlays) > MaxOverlays {
		return Scene{}, bad("at most %d overlays", MaxOverlays)
	}
	seen := map[string]bool{}
	s.Overlays = make([]Overlay, 0, len(in.Overlays))
	for i, r := range in.Overlays {
		o, err := parseOverlay(r)
		if err != nil {
			return Scene{}, fmt.Errorf("overlay %d: %w", i+1, err)
		}
		if seen[o.ID] {
			return Scene{}, bad("overlay id %q is used twice", o.ID)
		}
		seen[o.ID] = true
		s.Overlays = append(s.Overlays, o)
	}
	return s, nil
}

func parseBase(raw []byte) (Base, error) {
	var in struct {
		DatasetID *int64 `json:"datasetId"`
		Channel   *int   `json:"channel"`
		Spectral  *struct {
			Value *float64 `json:"value"`
			Unit  *string  `json:"unit"`
		} `json:"spectral"`
		Display *struct {
			Mapping    *string    `json:"mapping"`
			Colormap   *string    `json:"colormap"`
			Stretch    *string    `json:"stretch"`
			Range      *string    `json:"range"`
			Limits     []*float64 `json:"limits"` // pointers: a null limit is refused, not read as 0
			LimitsUnit string     `json:"limitsUnit"`
			ScaleGroup string     `json:"scaleGroup"`
		} `json:"display"`
	}
	if err := strict(raw, &in); err != nil {
		return Base{}, bad("base: %v", err)
	}
	if in.DatasetID == nil || *in.DatasetID < 1 || in.Channel == nil || *in.Channel < 0 || *in.Channel > maxCoord {
		return Base{}, bad("base needs a dataset id and a channel of at least 0")
	}
	b := Base{DatasetID: *in.DatasetID, Channel: *in.Channel}
	if sp := in.Spectral; sp != nil {
		if sp.Value == nil || !finite(*sp.Value) || sp.Unit == nil || !unitText.MatchString(*sp.Unit) {
			return Base{}, bad("the spectral value needs a finite value and a short plain unit")
		}
		b.Spectral = &Spectral{Value: *sp.Value, Unit: *sp.Unit}
	}
	d := in.Display
	if d == nil || d.Mapping == nil || d.Colormap == nil || d.Stretch == nil || d.Range == nil {
		return Base{}, bad("display needs mapping, colormap, stretch and range")
	}
	if !isOneOf(*d.Mapping, "scalar", "axial") || !isOneOf(*d.Colormap, "viridis", "inferno", "gray", "twilight") || !isOneOf(*d.Stretch, "linear", "sqrt", "asinh") || !isOneOf(*d.Range, "percentile", "minmax", "custom") {
		return Base{}, bad("display has a value it does not know")
	}
	var limits []float64
	if d.Limits != nil {
		if len(d.Limits) != 2 || d.Limits[0] == nil || d.Limits[1] == nil || !finite(*d.Limits[0]) || !finite(*d.Limits[1]) || *d.Limits[0] > *d.Limits[1] {
			return Base{}, bad("limits are two finite numbers, the lower first")
		}
		limits = []float64{*d.Limits[0], *d.Limits[1]}
	}
	if *d.Range == "custom" && *d.Mapping == "scalar" && limits == nil {
		return Base{}, bad("a custom range needs its limits")
	}
	if !plainText.MatchString(d.LimitsUnit) || (d.ScaleGroup != "" && !idText.MatchString(d.ScaleGroup)) {
		return Base{}, bad("the limits unit or scale group is not plain short text")
	}
	b.Display = Display{Mapping: *d.Mapping, Colormap: *d.Colormap, Stretch: *d.Stretch, Range: *d.Range, Limits: limits, LimitsUnit: d.LimitsUnit, ScaleGroup: d.ScaleGroup}
	return b, nil
}

var linkID = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// MaxLinkName is the longest group name, in characters.
const MaxLinkName = 60

// parseLink reads the link group strictly: a name of printable text (no control characters), the basis, every facet stated,
// and two to eight distinct datasets.
func parseLink(raw []byte) (Link, error) {
	var in struct {
		ID     *string `json:"id"`
		Name   *string `json:"name"`
		Basis  *string `json:"basis"`
		Facets *struct {
			Cursor  *bool `json:"cursor"`
			Channel *bool `json:"channel"`
			View    *bool `json:"view"`
		} `json:"facets"`
		Datasets []*int64 `json:"datasets"`
	}
	if err := strict(raw, &in); err != nil {
		return Link{}, bad("link: %v", err)
	}
	if in.ID == nil || !linkID.MatchString(*in.ID) {
		return Link{}, bad("link: id is 1 to 32 characters of a-z, 0-9 and -")
	}
	if in.Name == nil || *in.Name == "" || utf8.RuneCountInString(*in.Name) > MaxLinkName || !utf8.ValidString(*in.Name) {
		return Link{}, bad("link: name is 1 to %d characters", MaxLinkName)
	}
	for _, r := range *in.Name {
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			return Link{}, bad("link: name must be printable text without line breaks")
		}
	}
	if in.Basis == nil || !isOneOf(*in.Basis, "same-grid", "wcs") {
		return Link{}, bad("link: basis must be same-grid or wcs")
	}
	f := in.Facets
	if f == nil || f.Cursor == nil || f.Channel == nil || f.View == nil {
		return Link{}, bad("link: facets must state cursor, channel and view")
	}
	if len(in.Datasets) < 2 || len(in.Datasets) > 8 {
		return Link{}, bad("link: two to eight datasets")
	}
	ds := make([]int64, 0, len(in.Datasets))
	for _, d := range in.Datasets {
		if d == nil || *d < 1 || slices.Contains(ds, *d) {
			return Link{}, bad("link: datasets are distinct positive ids")
		}
		ds = append(ds, *d)
	}
	return Link{ID: *in.ID, Name: *in.Name, Basis: *in.Basis, Facets: LinkFacets{*f.Cursor, *f.Channel, *f.View}, Datasets: ds}, nil
}

func parseView(raw []byte) (View, error) {
	var in struct {
		Zoom   *float64 `json:"zoom"`
		Centre *struct {
			X *float64 `json:"x"`
			Y *float64 `json:"y"`
		} `json:"centre"`
	}
	if err := strict(raw, &in); err != nil {
		return View{}, bad("view: %v", err)
	}
	if in.Zoom == nil || !finite(*in.Zoom) || *in.Zoom < 1 || *in.Zoom > maxZoom {
		return View{}, bad("zoom must be between 1 and %d", maxZoom)
	}
	v := View{Zoom: *in.Zoom}
	if c := in.Centre; c != nil {
		if c.X == nil || c.Y == nil || !finite(*c.X) || !finite(*c.Y) || *c.X < 0 || *c.Y < 0 || *c.X > maxCoord || *c.Y > maxCoord {
			return View{}, bad("the view centre is two image coordinates of at least 0")
		}
		v.Centre = &Point{X: *c.X, Y: *c.Y}
	}
	return v, nil
}

// maxLevel bounds every level, multiple and threshold: far beyond any physical brightness, and safe to print.
const maxLevel = 1e30

// styleIn is the optional stroke style as read (pointers, so a null is refused rather than read as absent).
type styleIn struct {
	Color *string  `json:"color"`
	Width *float64 `json:"width"`
	Dash  *string  `json:"dash"`
}

func (s styleIn) read() (Style, error) {
	var out Style
	if s.Color != nil {
		if !isOneOf(*s.Color, "white", "cyan", "amber", "magenta", "lime") {
			return Style{}, bad("an overlay colour is white, cyan, amber, magenta or lime")
		}
		out.Color = *s.Color
	}
	if s.Width != nil {
		if !finite(*s.Width) || *s.Width < 0.5 || *s.Width > 4 {
			return Style{}, bad("an overlay width is between 0.5 and 4")
		}
		out.Width = s.Width
	}
	if s.Dash != nil {
		if !isOneOf(*s.Dash, "solid", "dashed") {
			return Style{}, bad("an overlay dash is solid or dashed")
		}
		out.Dash = *s.Dash
	}
	return out, nil
}

// increasing reports whether vs are finite, within maxLevel and strictly increasing.
func increasing(vs []*float64) bool {
	for i, v := range vs {
		if v == nil || !finite(*v) || math.Abs(*v) > maxLevel || (i > 0 && *v <= *vs[i-1]) {
			return false
		}
	}
	return true
}

func deref(vs []*float64) []float64 {
	out := make([]float64, len(vs))
	for i, v := range vs {
		out[i] = *v
	}
	return out
}

func parseLevels(raw json.RawMessage) (*Levels, error) {
	var in struct {
		Mode      *string    `json:"mode"`
		Values    []*float64 `json:"values"`
		Start     *float64   `json:"start"`
		Step      *float64   `json:"step"`
		Factor    *float64   `json:"factor"`
		Count     *int       `json:"count"`
		Multiples []*float64 `json:"multiples"`
		Sigma     *float64   `json:"sigma"`
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, bad("contours need their levels")
	}
	if err := strict(raw, &in); err != nil || in.Mode == nil {
		return nil, bad("contour levels: not a levels object")
	}
	num := func(p *float64) bool { return p != nil && finite(*p) && math.Abs(*p) <= maxLevel }
	count := func() bool { return in.Count != nil && *in.Count >= 1 && *in.Count <= 64 }
	only := func(ok ...bool) bool { // every field the mode does not use is absent
		for _, b := range ok {
			if !b {
				return false
			}
		}
		return true
	}
	l := &Levels{Mode: *in.Mode}
	switch *in.Mode {
	case "list":
		if len(in.Values) < 1 || len(in.Values) > 64 || !increasing(in.Values) || !only(in.Start == nil, in.Step == nil, in.Factor == nil, in.Count == nil, in.Multiples == nil, in.Sigma == nil) {
			return nil, bad("a level list is 1 to 64 finite, strictly increasing numbers")
		}
		l.Values = deref(in.Values)
	case "linear":
		if !num(in.Start) || !num(in.Step) || *in.Step == 0 || !count() || !only(in.Values == nil, in.Factor == nil, in.Multiples == nil, in.Sigma == nil) {
			return nil, bad("linear levels need a start, a step that is not zero and a count of 1 to 64")
		}
		l.Start, l.Step, l.Count = in.Start, in.Step, in.Count
	case "geometric":
		if !num(in.Start) || *in.Start == 0 || !num(in.Factor) || *in.Factor <= 1 || !count() || !only(in.Values == nil, in.Step == nil, in.Multiples == nil, in.Sigma == nil) {
			return nil, bad("geometric levels need a start that is not zero, a factor above 1 and a count of 1 to 64")
		}
		l.Start, l.Factor, l.Count = in.Start, in.Factor, in.Count
	case "sigma":
		if len(in.Multiples) < 1 || len(in.Multiples) > 64 || !increasing(in.Multiples) || !only(in.Values == nil, in.Start == nil, in.Step == nil, in.Factor == nil, in.Count == nil) {
			return nil, bad("sigma levels are 1 to 64 finite, strictly increasing multiples")
		}
		for _, m := range in.Multiples {
			if *m == 0 {
				return nil, bad("a sigma multiple of zero is not a contour of the noise")
			}
		}
		l.Multiples = deref(in.Multiples)
		if in.Sigma != nil {
			if !num(in.Sigma) || *in.Sigma <= 0 {
				return nil, bad("a recorded sigma is a positive finite number")
			}
			l.Sigma = in.Sigma
		}
	default:
		return nil, bad("contour levels are list, linear, geometric or sigma")
	}
	return l, nil
}

// ParseOverlay reads one overlay as strictly as Parse does inside a scene (the overlay geometry route takes one alone).
func ParseOverlay(raw []byte) (Overlay, error) {
	if len(raw) > MaxBytes {
		return Overlay{}, bad("larger than %d bytes", MaxBytes)
	}
	return parseOverlay(raw)
}

func parseOverlay(raw []byte) (Overlay, error) {
	var head struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return Overlay{}, bad("not an overlay object")
	}
	if !isOneOf(head.Kind, Kinds...) {
		return Overlay{}, bad("unknown overlay kind %q", head.Kind)
	}
	if !Drawn[head.Kind] {
		return Overlay{}, bad("%s overlays are not drawn in this version", head.Kind)
	}
	type common struct {
		ID        *string `json:"id"`
		Kind      string  `json:"kind"`
		DatasetID *int64  `json:"datasetId"`
		Visible   *bool   `json:"visible"`
	}
	check := func(c common) (Overlay, error) {
		if c.ID == nil || !idText.MatchString(*c.ID) || c.DatasetID == nil || *c.DatasetID < 1 || c.Visible == nil {
			return Overlay{}, bad("an overlay needs an id (letters, digits, _ and -, at most 32), a dataset id and visible")
		}
		return Overlay{ID: *c.ID, Kind: c.Kind, DatasetID: *c.DatasetID, Visible: *c.Visible}, nil
	}
	switch head.Kind {
	case "orientation":
		var in struct {
			common
			Sampling *struct {
				Mode  *string  `json:"mode"`
				GapPx *float64 `json:"gapPx"`
				Step  *int     `json:"step"`
			} `json:"sampling"`
			Length        *string  `json:"length"`
			Opacity       *string  `json:"opacity"`
			ReliabilityID *int64   `json:"reliabilityId"`
			Threshold     *float64 `json:"threshold"`
			styleIn
		}
		if err := strict(raw, &in); err != nil {
			return Overlay{}, bad("orientation: %v", err)
		}
		o, err := check(in.common)
		if err != nil {
			return o, err
		}
		if o.Style, err = in.styleIn.read(); err != nil {
			return Overlay{}, err
		}
		if in.Threshold != nil {
			if !finite(*in.Threshold) || math.Abs(*in.Threshold) > maxLevel {
				return Overlay{}, bad("the reliability threshold is a finite number")
			}
			o.Threshold = in.Threshold
		}
		sp := in.Sampling
		switch {
		case sp == nil || sp.Mode == nil:
			return Overlay{}, bad("orientation needs a sampling")
		case *sp.Mode == "auto" && sp.Step == nil && sp.GapPx != nil && finite(*sp.GapPx) && *sp.GapPx >= 4 && *sp.GapPx <= 256:
			o.Sampling = &Sampling{Mode: "auto", GapPx: sp.GapPx}
		case *sp.Mode == "step" && sp.GapPx == nil && sp.Step != nil && *sp.Step >= 1 && *sp.Step <= 4096:
			o.Sampling = &Sampling{Mode: "step", Step: sp.Step}
		default:
			return Overlay{}, bad("orientation sampling is auto with gapPx 4 to 256, or step with step 1 to 4096")
		}
		if in.Length == nil || !isOneOf(*in.Length, "fixed", "reliability") || in.Opacity == nil || !isOneOf(*in.Opacity, "reliability", "uniform") {
			return Overlay{}, bad("orientation length is fixed or reliability and opacity is reliability or uniform")
		}
		o.Length, o.Opacity = *in.Length, *in.Opacity
		if in.ReliabilityID != nil {
			if *in.ReliabilityID < 1 || o.Opacity != "reliability" {
				return Overlay{}, bad("a reliability map id is at least 1 and only goes with opacity reliability")
			}
			o.ReliabilityID = in.ReliabilityID
		}
		return o, nil
	case "contours":
		var in struct {
			common
			Channel *int            `json:"channel"`
			Levels  json.RawMessage `json:"levels"`
			Unit    *string         `json:"unit"`
			styleIn
		}
		if err := strict(raw, &in); err != nil {
			return Overlay{}, bad("contours: %v", err)
		}
		o, err := check(in.common)
		if err != nil {
			return o, err
		}
		if o.Style, err = in.styleIn.read(); err != nil {
			return Overlay{}, err
		}
		if in.Channel == nil || *in.Channel < 0 || *in.Channel > maxCoord {
			return Overlay{}, bad("contours need the channel they trace, at least 0")
		}
		if o.Levels, err = parseLevels(in.Levels); err != nil {
			return Overlay{}, err
		}
		if in.Unit != nil && !plainText.MatchString(*in.Unit) {
			return Overlay{}, bad("the contour unit is plain short text")
		}
		o.Channel, o.Unit = in.Channel, in.Unit
		return o, nil
	case "catalog":
		var in struct {
			common
			Vectors      *bool    `json:"vectors"`
			LengthColumn *string  `json:"lengthColumn"`
			LengthScale  *float64 `json:"lengthScale"`
			ColorColumn  *string  `json:"colorColumn"`
			Filter       *struct {
				Column      *string  `json:"column"`
				SigmaColumn *string  `json:"sigmaColumn"`
				Min         *float64 `json:"min"`
			} `json:"filter"`
			styleIn
		}
		if err := strict(raw, &in); err != nil {
			return Overlay{}, bad("catalog: %v", err)
		}
		o, err := check(in.common)
		if err != nil {
			return o, err
		}
		if o.Style, err = in.styleIn.read(); err != nil {
			return Overlay{}, err
		}
		column := func(what string, p *string) (string, error) {
			if p == nil {
				return "", nil
			}
			if !columnText.MatchString(*p) {
				return "", bad("the catalog's %s is a column name of 1 to 64 plain characters", what)
			}
			return *p, nil
		}
		o.Vectors = in.Vectors != nil && *in.Vectors
		if o.LengthColumn, err = column("length column", in.LengthColumn); err != nil {
			return Overlay{}, err
		}
		if o.ColorColumn, err = column("colour column", in.ColorColumn); err != nil {
			return Overlay{}, err
		}
		if o.LengthColumn != "" && !o.Vectors {
			return Overlay{}, bad("a catalog length column needs vectors to be on")
		}
		if in.LengthScale != nil {
			if o.LengthColumn == "" || !finite(*in.LengthScale) || *in.LengthScale <= 0 || *in.LengthScale > maxCoord {
				return Overlay{}, bad("a catalog length scale is a positive number of pixels per unit and goes with a length column")
			}
			o.LengthScale = in.LengthScale
		}
		if f := in.Filter; f != nil {
			if f.Column == nil || f.Min == nil || !finite(*f.Min) || math.Abs(*f.Min) > maxLevel {
				return Overlay{}, bad("a catalog filter needs a column and a finite minimum")
			}
			cf := &CatalogFilter{Min: f.Min}
			if cf.Column, err = column("filter column", f.Column); err != nil {
				return Overlay{}, err
			}
			if cf.SigmaColumn, err = column("filter uncertainty column", f.SigmaColumn); err != nil {
				return Overlay{}, err
			}
			o.Filter = cf
		}
		return o, nil
	case "beam":
		var in struct {
			common
			Corner *string `json:"corner"`
			styleIn
		}
		if err := strict(raw, &in); err != nil {
			return Overlay{}, bad("beam: %v", err)
		}
		o, err := check(in.common)
		if err != nil {
			return o, err
		}
		if o.Style, err = in.styleIn.read(); err != nil {
			return Overlay{}, err
		}
		if in.Corner != nil {
			if !isOneOf(*in.Corner, "bottom-left", "bottom-right", "top-left", "top-right") {
				return Overlay{}, bad("the beam corner is bottom-left, bottom-right, top-left or top-right")
			}
			o.Corner = *in.Corner
		}
		return o, nil
	case "grid":
		var in struct {
			common
			Frame *string `json:"frame"`
			styleIn
		}
		if err := strict(raw, &in); err != nil {
			return Overlay{}, bad("grid: %v", err)
		}
		o, err := check(in.common)
		if err != nil {
			return o, err
		}
		if o.Style, err = in.styleIn.read(); err != nil {
			return Overlay{}, err
		}
		if in.Frame == nil || !isOneOf(*in.Frame, "icrs", "fk5", "galactic") {
			return Overlay{}, bad("the grid frame is icrs, fk5 or galactic")
		}
		o.Frame = *in.Frame
		return o, nil
	case "regions":
		var in struct {
			common
			Active *json.RawMessage   `json:"active"`
			Saved  *[]json.RawMessage `json:"saved"`
		}
		if err := strict(raw, &in); err != nil {
			return Overlay{}, bad("regions: %v", err)
		}
		o, err := check(in.common)
		if err != nil {
			return o, err
		}
		if in.Active != nil && string(*in.Active) != "null" {
			r, err := parseRegion(*in.Active, false)
			if err != nil {
				return Overlay{}, err
			}
			o.Active = &r
		}
		if in.Saved == nil {
			return Overlay{}, bad("regions needs its saved list (it may be empty)")
		}
		if len(*in.Saved) > MaxSaved {
			return Overlay{}, bad("at most %d saved regions", MaxSaved)
		}
		o.Saved = make([]SavedRegion, 0, len(*in.Saved))
		ids := map[int64]bool{}
		for _, sr := range *in.Saved {
			var s struct {
				ID       *int64          `json:"id"`
				Shape    json.RawMessage `json:"shape"`
				Selected bool            `json:"selected"`
			}
			if err := strict(sr, &s); err != nil || s.ID == nil || *s.ID < 1 || ids[*s.ID] {
				return Overlay{}, bad("a saved region needs its own id and a shape")
			}
			ids[*s.ID] = true
			r, err := parseRegion(s.Shape, true)
			if err != nil {
				return Overlay{}, err
			}
			o.Saved = append(o.Saved, SavedRegion{ID: *s.ID, Shape: r, Selected: s.Selected})
		}
		return o, nil
	}
	return Overlay{}, bad("unknown overlay kind %q", head.Kind) // unreachable: every drawn kind is handled above
}

func parseRegion(raw []byte, allowLine bool) (Region, error) {
	var r Region
	if err := strict(raw, &r); err != nil {
		return Region{}, bad("region: %v", err)
	}
	ok := func(ps ...*int) bool {
		for _, p := range ps {
			if p == nil || *p < -maxCoord || *p > maxCoord {
				return false
			}
		}
		return true
	}
	area := r.Points == nil && r.Width == nil && r.WidthUnit == ""
	switch r.Shape {
	case "line":
		if !allowLine {
			return Region{}, bad("a line has no area: it cannot be the working region (only a saved region may be a line)")
		}
		if r.X0 == nil && r.Y0 == nil && r.X1 == nil && r.Y1 == nil && r.CX == nil && r.CY == nil && r.R == nil &&
			len(r.Points) >= 2 && len(r.Points) <= MaxLinePoints && r.Width != nil && finite(*r.Width) && *r.Width > 0 && *r.Width <= MaxLineWidth &&
			(r.WidthUnit == "pixel" || r.WidthUnit == "arcsec") {
			for _, p := range r.Points {
				if len(p) != 2 || p[0] < -maxCoord || p[0] > maxCoord || p[1] < -maxCoord || p[1] > maxCoord {
					return Region{}, bad("each vertex of a line is a pair of whole pixel numbers")
				}
			}
			return r, nil
		}
		return Region{}, bad("a line has 2 to %d vertices, a width above 0 and its unit (pixel or arcsec)", MaxLinePoints)
	case "rect":
		if area && ok(r.X0, r.Y0, r.X1, r.Y1) && r.CX == nil && r.CY == nil && r.R == nil && *r.X0 <= *r.X1 && *r.Y0 <= *r.Y1 {
			return r, nil
		}
	case "circle":
		if area && ok(r.CX, r.CY, r.R) && r.X0 == nil && r.Y0 == nil && r.X1 == nil && r.Y1 == nil && *r.R >= 0 {
			return r, nil
		}
	}
	return Region{}, bad("a region is a rect (x0 <= x1, y0 <= y1) or a circle (cx, cy, r >= 0) in whole pixels (a saved region may also be a line)")
}
