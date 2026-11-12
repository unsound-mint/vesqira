package server

import (
	"context"
	"path"
	"strconv"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
)

// maskByRule makes a typed mask (ADR 0055) from the file a mask-role rule selected, which is registered as a dataset like any
// image, for the dataset the rule's target template names. It goes through makeMask, the code the mask route uses: the same checks
// (same grid, the type, the meaning of nonzero), the same worker, a mask file of its own. The mask is `declared` (the type and the
// meaning of nonzero are the researcher's, written into a rule they saved) and its author is "rule:<name>", so a reviewer and the
// researcher can tell it from a mask made by hand. Nothing is guessed: a missing target, another grid or a refused type leaves no
// mask and says why.
func (s *Server) maskByRule(ctx context.Context, p projects.Project, res *applyResult, maskDS catalog.Dataset, rule discover.Rule) (notes []string) {
	later := func(why string) []string { // the mask can be made once the problem is fixed: report it as failed, so a later apply tries again
		res.retry = true
		return []string{why}
	}
	if !s.masksOn() {
		return later("typed masks are not configured on this server")
	}
	if !maskDS.HasPixelGrid() {
		return []string{"this file is a point catalog or a HEALPix map, not a pixel mask"}
	}
	rel, err := rule.MaskTargetPath(res.Path)
	if err != nil {
		return []string{"no mask was made: " + err.Error()}
	}
	facts, err := s.Store.FactsByPath(wsCtx(ctx), p.HostID) // read now: the target may have been registered earlier in this same apply
	if err != nil {
		return later("the catalog could not be read")
	}
	fa, ok := facts[absFor(p, rel)]
	if !ok {
		return later("no mask was made: the dataset this mask belongs to, " + rel + ", is not registered yet; register it, then apply again")
	}
	target, err := s.Store.Get(wsCtx(ctx), fa.ID)
	if err != nil {
		return later("the dataset " + rel + " could not be read")
	}
	if !target.HasPixelGrid() {
		return []string{"no mask was made: " + rel + " is a point catalog or a HEALPix map, which has no pixel grid to flag"}
	}
	name := cleanText(path.Base(res.Path), 80)
	by := rule.DeclaredBy()
	if have, err := s.Masks.ForDataset(wsCtx(ctx), target.ID); err == nil {
		for _, m := range have {
			if m.Author == by && m.Name == name {
				return []string{"already imported as a mask of " + rel + " by this rule; delete that mask to import the file again"}
			}
		}
	}
	v, code, msg := s.makeMask(ctx, target, maskReq{Method: "import", Type: rule.MaskType, Name: name, ImportDatasetID: maskDS.ID, Nonzero: rule.MaskNonzero, Meaning: rule.MaskMeaning}, by)
	if code != 0 {
		if code == 499 || code >= 500 {
			return later("the mask could not be made (" + msg + ")")
		}
		return []string{"no mask was made: " + msg} // a refusal of the data (another grid, a bad file): trying again would say the same
	}
	res.Declared = append(res.Declared, "mask of "+rel+", type "+rule.MaskType+", "+strconv.FormatInt(v.Flagged, 10)+" pixels flagged (declared by your rule "+rule.Name+")")
	return nil
}
