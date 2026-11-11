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
	Drawn = map[string]bool{"orientation": true, "regions": true}
)

type Scene struct {
	Schema   string    `json:"schema"`
	Base     Base      `json:"base"`
	Overlays []Overlay `json:"overlays"`
	View     View      `json:"view"`
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

// Region is a pixel rectangle (inclusive bounds) or circle, 0-based with y from the bottom, as the region tools use.
type Region struct {
	Shape string `json:"shape"`
	X0    *int   `json:"x0,omitempty"`
	Y0    *int   `json:"y0,omitempty"`
	X1    *int   `json:"x1,omitempty"`
	Y1    *int   `json:"y1,omitempty"`
	CX    *int   `json:"cx,omitempty"`
	CY    *int   `json:"cy,omitempty"`
	R     *int   `json:"r,omitempty"`
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

// Overlay is one layer. Common fields first; the rest belong to one kind and are only set for it.
type Overlay struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	DatasetID int64  `json:"datasetId"`
	Visible   bool   `json:"visible"`
	// orientation
	Sampling      *Sampling `json:"sampling,omitempty"`
	Length        string    `json:"length,omitempty"`
	Opacity       string    `json:"opacity,omitempty"`
	ReliabilityID *int64    `json:"reliabilityId,omitempty"`
	// regions
	Active *Region       `json:"active,omitempty"`
	Saved  []SavedRegion `json:"saved"`
}

// MarshalJSON writes only the fields of the overlay's kind; a regions overlay always has its saved list, even empty.
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
		}{c, o.Sampling, o.Length, o.Opacity, o.ReliabilityID})
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
	return json.Marshal(c)
}

var (
	idText    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	unitText  = regexp.MustCompile(`^[A-Za-z0-9/*^. -]{0,16}$`)
	plainText = regexp.MustCompile(`^[^\x00-\x1f<>"&\\]{0,64}$`)
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
			Length        *string `json:"length"`
			Opacity       *string `json:"opacity"`
			ReliabilityID *int64  `json:"reliabilityId"`
		}
		if err := strict(raw, &in); err != nil {
			return Overlay{}, bad("orientation: %v", err)
		}
		o, err := check(in.common)
		if err != nil {
			return o, err
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
		if in.Length == nil || *in.Length != "fixed" || in.Opacity == nil || !isOneOf(*in.Opacity, "reliability", "uniform") {
			return Overlay{}, bad("orientation length is fixed and opacity is reliability or uniform")
		}
		o.Length, o.Opacity = *in.Length, *in.Opacity
		if in.ReliabilityID != nil {
			if *in.ReliabilityID < 1 || o.Opacity != "reliability" {
				return Overlay{}, bad("a reliability map id is at least 1 and only goes with opacity reliability")
			}
			o.ReliabilityID = in.ReliabilityID
		}
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
			r, err := parseRegion(*in.Active)
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
			r, err := parseRegion(s.Shape)
			if err != nil {
				return Overlay{}, err
			}
			o.Saved = append(o.Saved, SavedRegion{ID: *s.ID, Shape: r, Selected: s.Selected})
		}
		return o, nil
	}
	return Overlay{}, bad("unknown overlay kind %q", head.Kind) // unreachable: every drawn kind is handled above
}

func parseRegion(raw []byte) (Region, error) {
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
	switch r.Shape {
	case "rect":
		if ok(r.X0, r.Y0, r.X1, r.Y1) && r.CX == nil && r.CY == nil && r.R == nil && *r.X0 <= *r.X1 && *r.Y0 <= *r.Y1 {
			return r, nil
		}
	case "circle":
		if ok(r.CX, r.CY, r.R) && r.X0 == nil && r.Y0 == nil && r.X1 == nil && r.Y1 == nil && *r.R >= 0 {
			return r, nil
		}
	}
	return Region{}, bad("a region is a rect (x0 <= x1, y0 <= y1) or a circle (cx, cy, r >= 0) in whole pixels")
}
