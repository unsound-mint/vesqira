package server

import (
	"context"
	"encoding/json"
	"log"
	"path"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
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
	if in.ReliabilityID != nil {
		rel, code, msg := s.imageDataset(*in.ReliabilityID)
		if code != 0 {
			return nil, 409, "the declared reliability dataset: " + msg
		}
		if rel.HostID != d.HostID {
			return nil, 409, "the reliability map is on another host: both maps must be on the same machine to be used together"
		}
		if rel.Interpretation != nil && rel.Interpretation.Kind == "axial_orientation" {
			return nil, 409, "the declared reliability dataset is itself declared as angles"
		}
		if why, err := compat.Grids(d.Header, rel.Header); err != nil || len(why) > 0 {
			return nil, 409, "the reliability map no longer shares this map's grid: " + strings.Join(why, "; ")
		}
		if remote {
			a.weights = rel.Path
		} else {
			real, _, status, m := s.authorize(ctx, rel)
			if status != 0 {
				return nil, status, "reliability map: " + m
			}
			a.weights = real
		}
		a.args = append(a.args, "--weights", a.weights)
	}
	return a, 0, ""
}

// finishAxialResult runs after an analysis of an axial map succeeded and its result is registered. When the worker wrote a
// companion reliability map it is registered as a derived dataset too, and the result is given the input's unit, frame
// and quantity as an interpretation proposed by the operation, with that map as its reliability. register brings a file in
// (from this computer or from a host). A failure to register the companion is logged and does not discard the result.
func (s *Server) finishAxialResult(res analysisOutcome, nd catalog.Dataset, a *axialRun, relPath string, register func(string) (catalog.Dataset, int, string), op string, sources []int64, params json.RawMessage) catalog.Dataset {
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
	if got, err := s.Store.Get(nd.ID); err == nil {
		return got
	}
	return nd
}

// analysisOutcome is the worker's reply that the server keeps.
type analysisOutcome struct {
	Summary  json.RawMessage
	Software json.RawMessage
}

// axialParams is what an analysis job's record keeps of the declaration it ran under, so the result can be registered with it.
type axialParams struct {
	Unit     string  `json:"unit"`
	Frame    *string `json:"frame"`
	Quantity *string `json:"quantity"`
}

func remoteRelPath(outDir string) string { return path.Join(outDir, "result.reliability.fits") }
