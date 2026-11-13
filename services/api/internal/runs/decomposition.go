package runs

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// The decomposition record of a tool profile (ADR 0068, slice 4): where a tool wrote its per-pixel Gaussian components and what each
// field of the record means. The reading itself is the worker's (services/scientific-worker/decomp.py); a profile only declares. A
// field the profile does not state stays unresolved and the viewer says so: nothing here supplies a unit, an index base or a width
// convention by default. The same declaration is validated again by the worker, which is the one that reads the data.

// Declaration kinds of a component source.
const (
	SourceTable    = "table"     // a text table, one row per (pixel, component), such as ROHSA's .dat
	SourceFitsMaps = "fits_maps" // FITS maps per parameter: one cube with a component axis, or one 2D map per component
)

// Decomposition is the profile's section for a tool that fits per-pixel Gaussian components. Components, Crop and the map file
// templates are read by the API at import; the rest is sent to the worker as the declaration.
type Decomposition struct {
	Source    DecompSource `json:"source"`
	Amplitude struct {
		Unit string `json:"unit,omitempty"` // "grid" is the brightness unit of the crop; any other text is a unit the profile declares
	} `json:"amplitude"`
	Centre struct {
		Unit  string `json:"unit,omitempty"`  // "channel" or a velocity unit
		Base  *int   `json:"base,omitempty"`  // first channel number, 0 or 1; only for channels
		Frame string `json:"frame,omitempty"` // spectral frame of a velocity centre ("grid": the crop's own axis)
	} `json:"centre"`
	Width struct {
		Kind string `json:"kind,omitempty"` // sigma, fwhm or dispersion
		Unit string `json:"unit,omitempty"`
	} `json:"width"`
	Order string `json:"order,omitempty"`
	// Components is the dotted key of the run record that holds the number of components per pixel; a layout with one map per
	// component counts its files instead. Crop is the key that holds the path of the cube the fit was made on (its file name is
	// looked for beside the record).
	Components string `json:"components,omitempty"`
	Crop       string `json:"crop,omitempty"`
}

type DecompSource struct {
	Kind string `json:"kind"`
	// table
	Columns   *DecompColumns `json:"columns,omitempty"`
	Ncols     int            `json:"ncols,omitempty"`
	PixelBase *int           `json:"pixelBase,omitempty"`
	// fits_maps: per parameter (amplitude, centre, width), where its map is. ComponentAxis is the FITS axis (1 to 3) that counts
	// the components of a cube; absent when each component has its own file.
	Maps          map[string]DecompMap `json:"maps,omitempty"`
	ComponentAxis *int                 `json:"componentAxis,omitempty"`
}

type DecompColumns struct {
	Row       int `json:"row"`
	Col       int `json:"col"`
	Amplitude int `json:"amplitude"`
	Centre    int `json:"centre"`
	Width     int `json:"width"`
}

// DecompMap says where a parameter's map is: HDU is a number or a name (0 when absent), and the file is an exact name (a cube with
// a component axis) or a pattern with one group that captures the component number, counted from 1 (a map per component).
type DecompMap struct {
	HDU     any    `json:"hdu,omitempty"`
	File    string `json:"file,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	re      *regexp.Regexp
}

// DecompParams are the three parameters every component has, in the order the worker lists them.
var DecompParams = []string{"amplitude", "centre", "width"}

func (d *Decomposition) compile() error {
	s := &d.Source
	switch s.Kind {
	case SourceTable:
		if s.Columns == nil || s.Ncols < 5 || s.Ncols > 32 {
			return errors.New("a table source needs its five columns and a column count from 5 to 32")
		}
		if len(s.Maps) > 0 || s.ComponentAxis != nil {
			return errors.New("a table source has no maps")
		}
		c := s.Columns
		seen := map[int]bool{}
		for _, v := range []int{c.Row, c.Col, c.Amplitude, c.Centre, c.Width} {
			if v < 0 || v >= s.Ncols || seen[v] {
				return errors.New("the table's columns must be different numbers below the column count")
			}
			seen[v] = true
		}
		if s.PixelBase != nil && *s.PixelBase != 0 && *s.PixelBase != 1 {
			return errors.New("pixelBase must be 0 or 1")
		}
	case SourceFitsMaps:
		if s.Columns != nil || s.Ncols != 0 || s.PixelBase != nil {
			return errors.New("a FITS maps source has no table columns")
		}
		if len(s.Maps) != len(DecompParams) {
			return errors.New("a FITS maps source says where the amplitude, centre and width maps are")
		}
		if s.ComponentAxis != nil && (*s.ComponentAxis < 1 || *s.ComponentAxis > 3) {
			return errors.New("componentAxis must be the FITS axis 1, 2 or 3")
		}
		for _, p := range DecompParams {
			m, ok := s.Maps[p]
			if !ok {
				return fmt.Errorf("the %s map is not declared", p)
			}
			if (m.File == "") == (m.Pattern == "") {
				return fmt.Errorf("the %s map names a file or a pattern, not both and not neither", p)
			}
			if s.ComponentAxis != nil && m.Pattern != "" {
				return fmt.Errorf("the %s map is a pattern, but a component axis says it is one cube", p)
			}
			if s.ComponentAxis == nil && m.Pattern == "" {
				return fmt.Errorf("the %s map is one file, but no component axis says which axis counts the components", p)
			}
			if strings.ContainsAny(m.File, `/\`) {
				return fmt.Errorf("the %s map's file is a name in the run's folder", p)
			}
			if m.Pattern != "" {
				re, err := regexp.Compile(m.Pattern)
				if err != nil {
					return err
				}
				if re.NumSubexp() != 1 {
					return fmt.Errorf("the %s pattern needs exactly one group, the component number", p)
				}
				m.re = re
				s.Maps[p] = m
			}
			switch h := m.HDU.(type) {
			case nil:
			case float64:
				if h < 0 || h != float64(int(h)) || h > 999 {
					return fmt.Errorf("the %s map's hdu is a number from 0 or a name", p)
				}
			case string:
				if h == "" || len(h) > 68 {
					return fmt.Errorf("the %s map's hdu is a number from 0 or a name", p)
				}
			default:
				return fmt.Errorf("the %s map's hdu is a number from 0 or a name", p)
			}
		}
	default:
		return errors.New("a decomposition source is a table or fits_maps")
	}
	if b := d.Centre.Base; b != nil && *b != 0 && *b != 1 {
		return errors.New("centre.base must be 0 or 1")
	}
	switch d.Width.Kind {
	case "", "sigma", "fwhm", "dispersion":
	default:
		return errors.New("width.kind must be sigma, fwhm or dispersion")
	}
	return nil
}

// Declaration is what the worker is sent: the fields it reads, none of the import-side ones (file templates, record keys).
func (d *Decomposition) Declaration() map[string]any {
	s := map[string]any{"kind": d.Source.Kind}
	switch d.Source.Kind {
	case SourceTable:
		c := d.Source.Columns
		s["columns"] = map[string]any{"row": c.Row, "col": c.Col, "amplitude": c.Amplitude, "centre": c.Centre, "width": c.Width}
		s["ncols"] = d.Source.Ncols
		if d.Source.PixelBase != nil {
			s["pixelBase"] = *d.Source.PixelBase
		}
	case SourceFitsMaps:
		maps := map[string]any{}
		for p, m := range d.Source.Maps {
			e := map[string]any{}
			if m.HDU != nil {
				e["hdu"] = m.HDU
			}
			maps[p] = e
		}
		s["maps"] = maps
		if d.Source.ComponentAxis != nil {
			s["componentAxis"] = *d.Source.ComponentAxis
		}
	}
	out := map[string]any{"source": s}
	put := func(key string, vals map[string]any) {
		m := map[string]any{}
		for k, v := range vals {
			switch x := v.(type) {
			case string:
				if x != "" {
					m[k] = x
				}
			case *int:
				if x != nil {
					m[k] = *x
				}
			}
		}
		out[key] = m
	}
	put("amplitude", map[string]any{"unit": d.Amplitude.Unit})
	put("centre", map[string]any{"unit": d.Centre.Unit, "base": d.Centre.Base, "frame": d.Centre.Frame})
	put("width", map[string]any{"kind": d.Width.Kind, "unit": d.Width.Unit})
	if d.Order != "" {
		out["order"] = d.Order
	}
	return out
}

// DecompositionOf is the decomposition a profile declares, following the profile its components are read as (ROHSA-LMC reads
// its components as ROHSA's); nil when neither declares one.
func (r *Registry) DecompositionOf(p *Profile) *Decomposition {
	for i := 0; p != nil && i < 4; i++ {
		if p.Decomposition != nil {
			return p.Decomposition
		}
		if p.Components == "" {
			return nil
		}
		p = r.ByName(p.Components)
	}
	return nil
}

// MapFiles picks the files of each parameter's maps out of the names in a run's folder: the exact file of a cube, or the numbered
// files of a map-per-component layout, which must be numbered 1 to n without a gap and the same count for every parameter.
func (d *Decomposition) MapFiles(names []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, p := range DecompParams {
		m := d.Source.Maps[p]
		if m.File != "" {
			found := false
			for _, n := range names {
				found = found || n == m.File
			}
			if !found {
				return nil, fmt.Errorf("the %s map %s is not in the run's folder", p, m.File)
			}
			out[p] = []string{m.File}
			continue
		}
		type numbered struct {
			n    int
			name string
		}
		var hit []numbered
		for _, n := range names {
			if g := m.re.FindStringSubmatch(n); g != nil {
				k, err := strconv.Atoi(g[1])
				if err != nil || k < 1 || k > 64 {
					return nil, fmt.Errorf("%s: the component number must be from 1 to 64", n)
				}
				hit = append(hit, numbered{k, n})
			}
		}
		if len(hit) == 0 {
			return nil, fmt.Errorf("no file of the run's folder is a %s map (pattern %s)", p, m.Pattern)
		}
		sort.Slice(hit, func(i, j int) bool { return hit[i].n < hit[j].n })
		for i, h := range hit {
			if h.n != i+1 {
				return nil, fmt.Errorf("the %s maps are not numbered 1 to %d without a gap (%s)", p, len(hit), h.name)
			}
			out[p] = append(out[p], h.name)
		}
	}
	if d.Source.ComponentAxis == nil {
		n := len(out[DecompParams[0]])
		for _, p := range DecompParams[1:] {
			if len(out[p]) != n {
				return nil, fmt.Errorf("the %s maps are %d files and the amplitude maps %d: every component needs all three", p, len(out[p]), n)
			}
		}
	}
	return out, nil
}

// RecordString is the string at a dotted key of a TOML run record, "" when it is absent or not text; RecordInt the whole number.
func RecordString(data []byte, key string) string {
	v, _ := recordValue(data, key).(string)
	return v
}

func RecordInt(data []byte, key string) int {
	switch v := recordValue(data, key).(type) {
	case int64:
		return int(v)
	}
	return 0
}

func recordValue(data []byte, key string) any {
	var m map[string]any
	if key == "" {
		return nil
	}
	if _, err := toml.Decode(string(data), &m); err != nil {
		return nil
	}
	var cur any = m
	for _, k := range strings.Split(key, ".") {
		t, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = t[k]
	}
	return cur
}

// DecompRecord is what a decomposition run keeps of how its components are read: the profile that declared it (so the declaration
// is looked up again, never copied into the row) and, for FITS maps, the file names in the run's folder.
type DecompRecord struct {
	Profile string              `json:"profile"`
	Files   map[string][]string `json:"files,omitempty"`
}
