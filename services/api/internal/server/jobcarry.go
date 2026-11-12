package server

import (
	"context"
	"encoding/json"
	"path"
	"path/filepath"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
)

// Masks carried through a cutout, slab, align or convolve job on a host (ADR 0064 addendum). The job's script passes --carry-masks and
// --missing-out to analysis.py with files the API chose when it drafted the job: fixed names directly in the job's own output folder, never
// text from the browser (jobs.RenderAnalysis refuses any other). When the result is registered the API checks that each expected file is on
// the host, with the expected name, in the folder of result.fits, and only then records the masks (recordCarried, as for a subset). A mask
// that was not written is never recorded. Nothing is deleted on a host.

// jobCarryPlan is what the draft keeps in the job's params for registration: the source masks chosen to carry (with the fingerprint they
// had when the script was drafted), where each carried copy goes, the masks left behind, and the no-value mask's file.
type jobCarryPlan struct {
	Carried []jobCarried `json:"carried"`
	Missing string       `json:"missing,omitempty"`
	Left    []int64      `json:"left"`
	Extra   string       `json:"extra,omitempty"`
}

type jobCarried struct {
	ID  int64  `json:"id"`
	SHA string `json:"sha"`
	Out string `json:"out"`
}

// intoFolder moves the plan's output files into dir (the job's output folder), keeping their generated names.
func (p *carryPlan) intoFolder(dir string) {
	if p == nil {
		return
	}
	for i := range p.carried {
		p.carried[i].out = path.Join(dir, filepath.Base(p.carried[i].out))
	}
	if p.missingOut != "" {
		p.missingOut = path.Join(dir, filepath.Base(p.missingOut))
	}
}

func (p *carryPlan) missingFile() string {
	if p == nil {
		return ""
	}
	return p.missingOut
}

// jobPlan is the plan as a job's params keep it, or nil when the job carries nothing and recomputes nothing.
func (p *carryPlan) jobPlan(extra string) *jobCarryPlan {
	if p == nil {
		return nil
	}
	cp := &jobCarryPlan{Carried: []jobCarried{}, Missing: p.missingOut, Left: []int64{}, Extra: extra}
	for _, c := range p.carried {
		cp.Carried = append(cp.Carried, jobCarried{ID: c.mask.ID, SHA: c.mask.SHA256, Out: c.out})
	}
	for _, m := range p.left {
		cp.Left = append(cp.Left, m.ID)
	}
	return cp
}

// registerJobCarry records the masks a finished job was asked to carry, as masks of the registered result nd. It returns what the result's
// derived record keeps (`maskCarry`) and, when the masks could not be recorded, a sentence for the person with the way out (the result
// itself stays registered). A non-zero code is only for the host being unreachable, where nothing was decided: registering again repeats it.
func (s *Server) registerJobCarry(ctx context.Context, h hosts.Host, j jobs.Job, p analysisJobParams, summary json.RawMessage, nd catalog.Dataset) (map[string]any, string, int, string) {
	cp := p.CarryPlan
	skip := func(why string) (map[string]any, string, int, string) {
		return nil, "The result is registered, but no mask was recorded for it: " + why + ". Make the masks again on the result.", 0, ""
	}
	if len(p.Sources) == 0 || s.Masks == nil {
		return skip("the job's record of what to carry is incomplete")
	}
	src, err := s.Store.Get(p.Sources[0])
	if err != nil {
		return skip("the file the job read is no longer in the catalog")
	}
	folder := path.Dir(nd.Path)
	// each expected file must be present on the host under the name asked for, in the result's own folder
	var wants []string
	for _, c := range cp.Carried {
		wants = append(wants, c.Out)
	}
	if cp.Missing != "" {
		wants = append(wants, cp.Missing)
	}
	real := map[string]string{}
	for _, w := range wants {
		kind := "carried"
		if w == cp.Missing {
			kind = "missing"
		}
		if !jobs.CarryOutOK(w, path.Clean(j.OutputDir), kind) {
			return skip("the job's record of its mask files is not valid")
		}
		r, _, _, code, msg := s.resolveRemote(ctx, h, w)
		switch {
		case code == 502:
			return nil, "", code, msg
		case code != 0:
			return skip("the mask file " + path.Base(w) + " is not in the job's output folder on the host")
		case !hostFileNamed(r, path.Base(w)) || path.Dir(r) != folder:
			return skip("the mask file " + path.Base(w) + " is not where the job was asked to write it")
		}
		real[w] = r
	}
	// registering again must not record the same files twice: they are already masks of this result
	if existing, err := s.Masks.ForDataset(nd.ID); err == nil {
		for _, m := range existing {
			for _, r := range real {
				if m.HostID == h.ID && m.Path == r {
					return s.recordedCarry(nd), "", 0, ""
				}
			}
		}
	}
	plan := &carryPlan{op: p.Op, hostID: h.ID}
	for _, c := range cp.Carried {
		m, code, msg := s.usableMask(c.ID, src)
		switch {
		case code == 502:
			return nil, "", code, msg
		case code != 0:
			return skip(msg)
		case m.SHA256 != c.SHA:
			return skip("the mask \"" + m.Name + "\" changed after the job was drafted, so the job may have carried other flags than it records")
		}
		plan.carried = append(plan.carried, carryOut{mask: m, out: real[c.Out]})
	}
	if cp.Missing != "" {
		plan.missingOut = real[cp.Missing]
	}
	for _, id := range cp.Left {
		if m, err := s.Masks.Get(id); err == nil {
			plan.left = append(plan.left, m)
		}
	}
	carried, code, msg := s.recordCarried(plan, summary, src, nd, cp.Extra)
	if code != 0 {
		return skip(msg)
	}
	return carried, "", 0, ""
}

// recordedCarry is the `maskCarry` a result's derived record already keeps, for a second registration of the same job.
func (s *Server) recordedCarry(nd catalog.Dataset) map[string]any {
	rec, err := s.Derived.Get(nd.ID)
	if err != nil {
		return nil
	}
	var pm struct {
		MaskCarry map[string]any `json:"maskCarry"`
	}
	_ = json.Unmarshal(rec.Params, &pm)
	return pm.MaskCarry
}
