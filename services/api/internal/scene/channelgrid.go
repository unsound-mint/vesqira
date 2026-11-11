package scene

import (
	"encoding/json"
	"math"
)

// ChannelGridVersion is the schema of a channel-map grid (ADR 0049). It is a separate versioned description from scene/1: a
// scene is what one pane draws (one base channel, overlays in base pixels), a grid is many panels of one dataset that share a
// colour scale, so putting it in scene/1 would make every reader of a scene handle a base with no single channel. A figure
// stores one or the other in the same column, told apart by the schema key.
const ChannelGridVersion = "channel-grid/1"

// The caps a grid is held to. The worker enforces the same ones when it plans the panels; they are repeated here so a stored
// description cannot claim a grid the app would refuse to draw.
const (
	MaxGridPanels      = 64
	MaxGridPanelPixels = 1 << 20 // one panel as sent, after the block-mean preview
	MaxGridPixels      = 1 << 24 // every panel together (64 MiB of float32)
	MaxGridChannels    = 1 << 24
	MaxGridAverage     = 256
	MaxGridColumns     = 8
	maxPreviewStep     = 64
)

type ChannelGrid struct {
	Schema    string `json:"schema"`
	DatasetID int64  `json:"datasetId"`
	First     int    `json:"first"`
	Last      int    `json:"last"`
	Stride    int    `json:"stride"`
	// Average is the number of channels each panel is the mean of (1: a single channel).
	Average int `json:"average"`
	Columns int `json:"columns"`
	Panels  int `json:"panels"`
	// Spectral is the unit and, for a velocity from a frequency or wavelength axis, the convention the labels were made in
	// (ADR 0040); absent means the file's own axis unit.
	Spectral *GridSpectral `json:"spectral,omitempty"`
	Display  GridDisplay   `json:"display"`
	// Preview records the block-mean step the panels were drawn at (1 = the full plane) and their size as drawn.
	Preview GridPreview `json:"preview"`
}

type GridSpectral struct {
	Unit       string `json:"unit"`
	Convention string `json:"convention,omitempty"`
}

// GridDisplay is the one scale every panel shares, as numbers. Source says where Lo and Hi came from ("percentile" of the
// sampled panels, or "custom"), because limits recomputed per panel would not be a shared scale.
type GridDisplay struct {
	Colormap   string     `json:"colormap"`
	Stretch    string     `json:"stretch"`
	Limits     [2]float64 `json:"limits"`
	LimitsUnit string     `json:"limitsUnit,omitempty"`
	Source     string     `json:"source"`
}

type GridPreview struct {
	Step        int `json:"step"`
	PanelWidth  int `json:"panelWidth"`
	PanelHeight int `json:"panelHeight"`
}

// GridPanels is the number of panels the range, stride and average give: panels start every stride channels from first.
func GridPanels(first, last, stride int) int {
	if stride < 1 || last < first {
		return 0
	}
	return (last-first)/stride + 1
}

// ParseChannelGrid reads and validates a channel-grid/1 description. Required values are read through pointers, so a missing
// or null one is refused rather than read as zero.
func ParseChannelGrid(raw []byte) (ChannelGrid, error) {
	if len(raw) > 8<<10 {
		return ChannelGrid{}, bad("a channel grid is larger than 8 KiB")
	}
	var in struct {
		Schema    *string `json:"schema"`
		DatasetID *int64  `json:"datasetId"`
		First     *int    `json:"first"`
		Last      *int    `json:"last"`
		Stride    *int    `json:"stride"`
		Average   *int    `json:"average"`
		Columns   *int    `json:"columns"`
		Panels    *int    `json:"panels"`
		Spectral  *struct {
			Unit       *string `json:"unit"`
			Convention string  `json:"convention"`
		} `json:"spectral"`
		Display *struct {
			Colormap   *string    `json:"colormap"`
			Stretch    *string    `json:"stretch"`
			Limits     []*float64 `json:"limits"`
			LimitsUnit string     `json:"limitsUnit"`
			Source     *string    `json:"source"`
		} `json:"display"`
		Preview *struct {
			Step        *int `json:"step"`
			PanelWidth  *int `json:"panelWidth"`
			PanelHeight *int `json:"panelHeight"`
		} `json:"preview"`
	}
	if err := strict(raw, &in); err != nil {
		return ChannelGrid{}, bad("not a channel grid object: %v", err)
	}
	if in.Schema == nil || *in.Schema != ChannelGridVersion {
		return ChannelGrid{}, bad("schema must be %q", ChannelGridVersion)
	}
	if in.DatasetID == nil || *in.DatasetID < 1 || in.First == nil || in.Last == nil || in.Stride == nil || in.Average == nil ||
		in.Columns == nil || in.Panels == nil || in.Display == nil || in.Preview == nil {
		return ChannelGrid{}, bad("datasetId, first, last, stride, average, columns, panels, display and preview are required")
	}
	g := ChannelGrid{Schema: ChannelGridVersion, DatasetID: *in.DatasetID, First: *in.First, Last: *in.Last, Stride: *in.Stride,
		Average: *in.Average, Columns: *in.Columns, Panels: *in.Panels}
	if g.First < 0 || g.Last < g.First || g.Last >= MaxGridChannels {
		return ChannelGrid{}, bad("the channel range is first <= last, from 0")
	}
	if g.Stride < 1 || g.Stride > MaxGridChannels || g.Average < 1 || g.Average > MaxGridAverage || g.Average > g.Stride {
		return ChannelGrid{}, bad("stride is 1 or more; average is 1 to %d and not more than the stride (panels would share channels)", MaxGridAverage)
	}
	n := GridPanels(g.First, g.Last, g.Stride)
	if n > MaxGridPanels {
		return ChannelGrid{}, bad("%d panels are more than the %d a grid holds", n, MaxGridPanels)
	}
	if g.Panels != n {
		return ChannelGrid{}, bad("panels is %d but the range, stride and average give %d", g.Panels, n)
	}
	if g.Columns < 1 || g.Columns > MaxGridColumns {
		return ChannelGrid{}, bad("columns is 1 to %d", MaxGridColumns)
	}
	if in.Spectral != nil {
		if in.Spectral.Unit == nil || !unitText.MatchString(*in.Spectral.Unit) || *in.Spectral.Unit == "" {
			return ChannelGrid{}, bad("the spectral unit is a short unit text")
		}
		if in.Spectral.Convention != "" && !isOneOf(in.Spectral.Convention, "radio", "optical", "relativistic") {
			return ChannelGrid{}, bad("the velocity convention is radio, optical or relativistic")
		}
		g.Spectral = &GridSpectral{Unit: *in.Spectral.Unit, Convention: in.Spectral.Convention}
	}
	d := in.Display
	if d.Colormap == nil || !isOneOf(*d.Colormap, "viridis", "inferno", "gray") || d.Stretch == nil || !isOneOf(*d.Stretch, "linear", "sqrt", "asinh") ||
		d.Source == nil || !plainText.MatchString(*d.Source) || *d.Source == "" || len(d.Limits) != 2 || d.Limits[0] == nil || d.Limits[1] == nil {
		return ChannelGrid{}, bad("display needs a colormap (viridis, inferno, gray), a stretch, two shared limits and where they came from")
	}
	lo, hi := *d.Limits[0], *d.Limits[1]
	if !finite(lo) || !finite(hi) || !(lo < hi) || math.Abs(lo) > 1e30 || math.Abs(hi) > 1e30 {
		return ChannelGrid{}, bad("the shared limits are two finite numbers, the lower below the upper")
	}
	if !unitText.MatchString(d.LimitsUnit) {
		return ChannelGrid{}, bad("the limits unit is a short unit text")
	}
	g.Display = GridDisplay{Colormap: *d.Colormap, Stretch: *d.Stretch, Limits: [2]float64{lo, hi}, LimitsUnit: d.LimitsUnit, Source: *d.Source}
	p := in.Preview
	if p.Step == nil || p.PanelWidth == nil || p.PanelHeight == nil || *p.Step < 1 || *p.Step > maxPreviewStep ||
		*p.PanelWidth < 1 || *p.PanelHeight < 1 || *p.PanelWidth > MaxGridPanelPixels || *p.PanelHeight > MaxGridPanelPixels {
		return ChannelGrid{}, bad("preview needs a block-mean step (1 to %d) and the panel size as drawn", maxPreviewStep)
	}
	if px := *p.PanelWidth * *p.PanelHeight; px > MaxGridPanelPixels || px*g.Panels > MaxGridPixels {
		return ChannelGrid{}, bad("the panels hold %d pixels each and %d together: more than the %d and %d a grid may transfer", px, px*g.Panels, MaxGridPanelPixels, MaxGridPixels)
	}
	g.Preview = GridPreview{Step: *p.Step, PanelWidth: *p.PanelWidth, PanelHeight: *p.PanelHeight}
	return g, nil
}

// Marshal writes what a figure stores: the server's reading of the grid, never the client's bytes.
func (g ChannelGrid) Marshal() ([]byte, error) { return json.Marshal(g) }
