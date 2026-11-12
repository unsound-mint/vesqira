package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// analysisAuthor labels the interpretation of a map an analysis produced: a proposal made by the operation (it keeps what
// its input declared), shown as such until the person confirms it, never as theirs and never as the file's.
const analysisAuthor = "vesqira-analysis"

// axialRun is what an analysis of a map declared as an axial orientation map hands to the worker (ADR 0042): the unit, the
// frame when declared, and the reliability map. Align and convolve resample the doubled angle instead of the angle.
type axialRun struct {
	args    []string // worker options, for a run on this computer
	unit    string
	frame   string // "" when not declared
	weights string // the reliability map's path on the machine the analysis runs on, or ""
	interp  catalog.Interpretation
}

// axialAnalysis decides what an analysis may do with the input. It returns nil for a scalar map or an operation that does
// not combine angles. A moment or slab of an axial map is refused: averaging angles arithmetically is not valid. remote is
// true when the analysis will run on the input's host, where the reliability map is read by its path there.
func (s *Server) axialAnalysis(ctx context.Context, op string, d catalog.Dataset, remote bool) (*axialRun, int, string) {
	in := d.Interpretation
	if in == nil || in.Kind != "axial_orientation" {
		return nil, 0, ""
	}
	switch op {
	case "moment", "slab":
		return nil, 422, "this map is declared as an axial orientation map: a " + op + " would average its angles, which is not valid (0 and 180 degrees are the same orientation). Align or convolve it instead, or declare it as a scalar map first"
	case "align", "convolve":
	default:
		return nil, 0, ""
	}
	if in.Stale {
		return nil, 409, "the file changed since you declared it as an orientation map; declare it again to confirm the unit and frame still apply"
	}
	if op == "align" && in.Frame == nil {
		return nil, 422, "the angle's frame (pixel or sky) is not declared for this map, so it cannot be aligned onto another grid: declare the frame first (the file does not say it)"
	}
	a := &axialRun{unit: in.Unit, interp: *in}
	if in.Frame != nil {
		a.frame = *in.Frame
	}
	a.args = []string{"--axial-unit", in.Unit}
	if a.frame != "" {
		a.args = append(a.args, "--axial-frame", a.frame)
	}
	if in.SkyFrame != nil {
		a.args = append(a.args, "--axial-sky-frame", *in.SkyFrame) // stated in another celestial frame than the grid's: the worker carries it first
	}
	if in.ReliabilityID != nil {
		p, code, msg := s.reliabilityPath(ctx, d, remote)
		if code != 0 {
			return nil, code, msg
		}
		a.weights = p
		a.args = append(a.args, "--weights", a.weights)
	}
	return a, 0, ""
}

// acceptSourceBeam validates the resolution a convolution is asked to start from when the file declares no beam (ADR 0018, amendment) and,
// for the VGT approximation, replaces whatever numbers the request carried with the ones the file's own keywords give, so the basis the
// result records ("vgt_approximate": declared by the file, approximate) is true of the values used. Returns a status and sentence on refusal.
func acceptSourceBeam(op string, d catalog.Dataset, opts map[string]string) (int, string) {
	basis := opts["source-beam-basis"]
	_, hasMaj := opts["source-bmaj"]
	_, hasMin := opts["source-bmin"]
	_, hasPA := opts["source-bpa"]
	if basis == "" && !hasMaj && !hasMin && !hasPA {
		return 0, ""
	}
	if op != "convolve" {
		return 422, "a source resolution applies to a convolution only"
	}
	switch basis {
	case "user":
		if !hasMaj || !hasMin {
			return 422, "enter the source resolution as major and minor FWHM in arcseconds"
		}
	case "vgt_approximate":
		var res struct {
			Status string `json:"status"`
			Gauss  *struct {
				Bmaj float64 `json:"bmaj"`
				Bmin float64 `json:"bmin"`
				Bpa  float64 `json:"bpa"`
			} `json:"gaussian_arcsec"`
		}
		if d.VGT == nil || json.Unmarshal(d.VGT.Resolution, &res) != nil || res.Status != "approximate" || res.Gauss == nil {
			return 422, "the file's header does not establish an approximate resolution (effective resolution: not established), so there is nothing to accept; enter the resolution yourself"
		}
		opts["source-bmaj"], opts["source-bmin"], opts["source-bpa"] = fmt.Sprint(res.Gauss.Bmaj), fmt.Sprint(res.Gauss.Bmin), fmt.Sprint(res.Gauss.Bpa)
	default:
		return 422, "say whether the source resolution is the VGT approximation or one you entered (sourceBeamBasis)"
	}
	return 0, ""
}

// axialStatedDifferently says, in a sentence, why two axial maps cannot simply be subtracted pixel by pixel: their angles are not stated
// against the same direction. The difference subtracts the stored numbers, so a pixel-grid angle minus a sky angle, or an angle stated
// in the Galactic frame minus one stated in the grid's own, would be wrong by an angle that varies across the map. Relative orientation
// carries both through the sky and is the comparison that is right for them. "" when the statements agree.
func axialStatedDifferently(a, b *catalog.Interpretation) string {
	if a.Frame == nil || b.Frame == nil {
		return "" // an undeclared frame is the person's own business here: a difference of two maps on one grid was always allowed for them
	}
	fa, fb := *a.Frame, *b.Frame
	own := func(in *catalog.Interpretation) string {
		if in.SkyFrame != nil {
			return "east of north in the " + catalog.SkyFrameName(*in.SkyFrame) + " frame"
		}
		if *in.Frame == "sky" {
			return "east of north in the grid's own frame"
		}
		return "counter-clockwise from +axis 1 on the pixel grid"
	}
	if fa != fb || deref(a.SkyFrame) != deref(b.SkyFrame) {
		return "the two maps state their angles differently (" + own(a) + " against " + own(b) + "), so subtracting them pixel by pixel would be wrong by an angle that changes across the map. " +
			"Use the relative orientation comparison, which carries both into the sky, or declare both the same way if they really are"
	}
	return ""
}

// reliabilityPath is the path of the reliability map declared for an axial map d, checked: still a scalar map on d's grid and on
// the same machine. remote is true when the path is read on d's host (its path there), otherwise it is authorised here.
func (s *Server) reliabilityPath(ctx context.Context, d catalog.Dataset, remote bool) (string, int, string) {
	rel, code, msg := s.imageDataset(wsCtx(ctx), *d.Interpretation.ReliabilityID)
	if code != 0 {
		return "", 409, "the declared reliability dataset: " + msg
	}
	if rel.HostID != d.HostID {
		return "", 409, "the reliability map is on another host: both maps must be on the same machine to be used together"
	}
	if rel.Interpretation != nil && rel.Interpretation.Kind == "axial_orientation" {
		return "", 409, "the declared reliability dataset is itself declared as angles"
	}
	if why, err := compat.Grids(effectiveHeader(d), effectiveHeader(rel)); err != nil || len(why) > 0 {
		return "", 409, "the reliability map no longer shares this map's grid: " + strings.Join(why, "; ")
	}
	if remote {
		return rel.Path, 0, ""
	}
	real, _, status, m := s.authorize(ctx, rel)
	if status != 0 {
		return "", status, "reliability map: " + m
	}
	return real, 0, ""
}

// finishAxialResult runs after an analysis of an axial map succeeded and its result is registered. When the worker wrote a
// companion reliability map it is registered as a derived dataset too, and the result is given the input's unit, frame
// and quantity as an interpretation proposed by the operation, with that map as its reliability. register brings a file in
// (from this computer or from a host). A failure to register the companion is logged and does not discard the result.
func (s *Server) finishAxialResult(ws workspace.ID, res analysisOutcome, nd catalog.Dataset, a *axialRun, relPath string, register func(string) (catalog.Dataset, int, string), op string, sources []int64, params json.RawMessage) catalog.Dataset {
	var sum struct {
		Axial           bool `json:"axial"`
		ReliabilityFile bool `json:"reliability_file"`
	}
	_ = json.Unmarshal(res.Summary, &sum)
	if !sum.Axial || a == nil {
		return nd
	}
	out := catalog.Interpretation{Kind: "axial_orientation", Unit: a.interp.Unit, Frame: a.interp.Frame, Quantity: a.interp.Quantity, DeclaredBy: analysisAuthor}
	if sum.ReliabilityFile {
		rd, code, msg := register(relPath)
		if code != 0 {
			log.Printf("analysis: companion reliability map: %d %s", code, msg)
		} else {
			note, _ := json.Marshal(map[string]any{"policy": "The companion reliability map of " + nd.Name + ": the resultant length of the resampled doubled angles (|(Q, U)| / w, between 0 and 1), or, for a nearest-neighbour result, the declared reliability picked with the same sample. See the orientation map's own record for the method.", "companionOf": nd.ID})
			if err := s.Derived.Save(derived.Record{DatasetID: rd.ID, Op: op, Params: params, Sources: sources, Summary: note, Software: res.Software}); err != nil {
				log.Printf("derived: %v", err)
			}
			out.ReliabilityID = &rd.ID
		}
	}
	if err := s.Store.SetInterpretation(nd, out); err != nil {
		log.Printf("analysis: interpretation of the result: %v", err)
		return nd
	}
	if got, err := s.Store.Get(ws, nd.ID); err == nil {
		return got
	}
	return nd
}

// analysisOutcome is the worker's reply that the server keeps.
type analysisOutcome struct {
	Summary  json.RawMessage
	Software json.RawMessage
	// Corrections is the id-free record of the researcher's corrections the program applied to its inputs (ADR 0061), nil for
	// none: what a derived record keeps, so the result says which correction state it was made with.
	Corrections []map[string]any
}

// axialParams is what an analysis job's record keeps of the declaration it ran under, so the result can be registered with it.
type axialParams struct {
	Unit     string  `json:"unit"`
	Frame    *string `json:"frame"`
	Quantity *string `json:"quantity"`
	// SkyFrame is the celestial frame the input's angles were stated in when not its grid's; the result is in its grid's own frame.
	SkyFrame *string `json:"skyFrame,omitempty"`
}

func remoteRelPath(outDir string) string { return path.Join(outDir, "result.reliability.fits") }
