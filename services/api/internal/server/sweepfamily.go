package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sweeps"
)

// A sweep's outputs as a family (ADR 0070, ADR 0048): the jobs of a sweep share a tool and an input and differ in the grid's parameters,
// and each finished job became a run whose outputs are registered datasets. The rule that maps a job to its map is the run's output role
// (orientation, reliability, ...): the person picks one role and every job contributes its single output of that role. Nothing is guessed
// from file names, and a job whose output cannot be compared with the first usable one is listed with the reason and left out.

// sweepFamilyMember is one job that contributes a map.
type sweepFamilyMember struct {
	JobID     int64  `json:"jobId"`
	Label     string `json:"label"` // the parameter values that differ across the sweep: "N = 12, λ = 10"
	DatasetID int64  `json:"datasetId"`
	Name      string `json:"name"`
}

type sweepFamilyLeft struct {
	JobID  int64  `json:"jobId"`
	Label  string `json:"label"`
	Reason string `json:"reason"`
}

// sweepFamilyLabel names a job by the sweep parameters that take more than one value; a sweep of one fixed value is named by all of them.
func sweepFamilyLabel(params []sweeps.Param, values map[string]string) string {
	var varying []sweeps.Param
	for _, p := range params {
		if len(slices.Compact(slices.Sorted(slices.Values(p.Values)))) > 1 {
			varying = append(varying, p)
		}
	}
	if len(varying) == 0 {
		varying = params
	}
	return sweeps.Label(varying, values)
}

// sweepFamily answers with the maps of a sweep's finished jobs that can be compared with one another. role selects the run output
// (default: the only role the jobs have in common, else the roles are listed and none is chosen).
func (s *Server) sweepFamily(w http.ResponseWriter, r *http.Request) {
	sw, ok := s.sweepFrom(w, r)
	if !ok {
		return
	}
	if s.Runs == nil {
		fail(w, 404, "runs are not available")
		return
	}
	ws := wsOf(r)
	ms, err := s.Sweeps.Members(sw.ID)
	if err != nil {
		log.Printf("sweeps: %v", err)
		fail(w, 500, "internal error")
		return
	}
	type job struct {
		id      int64
		label   string
		outputs []runs.Output
		reason  string // why the job has no output to look at
	}
	var js []job
	roleJobs := map[string]int{}
	for _, m := range ms {
		j, err := s.Jobs.Get(m.JobID)
		if err != nil {
			continue
		}
		e := job{id: j.ID, label: sweepFamilyLabel(sw.Params, m.Values)}
		switch {
		case j.RunID == nil:
			e.reason = "no result yet (the job is " + j.State + ")"
		default:
			run, err := s.Runs.Get(ws, *j.RunID)
			if errors.Is(err, runs.ErrNotFound) {
				e.reason = "its run is no longer in the catalog"
			} else if err != nil {
				log.Printf("sweeps: %v", err)
				fail(w, 500, "internal error")
				return
			} else {
				seen := map[string]bool{}
				for _, o := range run.Outputs {
					if o.DatasetID != 0 {
						e.outputs = append(e.outputs, o)
						if !seen[o.Role] {
							seen[o.Role] = true
							roleJobs[o.Role]++
						}
					}
				}
				if len(e.outputs) == 0 {
					e.reason = "its run has no registered output maps"
				}
			}
		}
		js = append(js, e)
	}
	type roleOut struct {
		Role string `json:"role"`
		Jobs int    `json:"jobs"`
	}
	roles := []roleOut{}
	for _, role := range slices.Sorted(func(yield func(string) bool) {
		for k := range roleJobs {
			if !yield(k) {
				return
			}
		}
	}) {
		roles = append(roles, roleOut{role, roleJobs[role]})
	}
	sort.SliceStable(roles, func(i, j int) bool { return roles[i].Jobs > roles[j].Jobs })
	role := r.URL.Query().Get("role")
	out := map[string]any{"sweepId": sw.ID, "name": sw.Name, "roles": roles, "role": role, "members": []sweepFamilyMember{}, "excluded": []sweepFamilyLeft{}, "referenceId": 0, "note": ""}
	if role == "" && len(roles) == 1 {
		role = roles[0].Role
		out["role"] = role
	}
	switch {
	case len(roles) == 0:
		out["note"] = "no job of this sweep has registered output maps yet"
		writeJSON(w, 200, out)
		return
	case role == "":
		out["note"] = "the jobs have several kinds of output: choose which one to compare"
		writeJSON(w, 200, out)
		return
	case roleJobs[role] == 0:
		fail(w, 422, "no job of this sweep has an output with that role")
		return
	}
	members, left := []sweepFamilyMember{}, []sweepFamilyLeft{}
	var ref *catalog.Dataset
	for _, e := range js {
		why := e.reason
		var d catalog.Dataset
		if why == "" {
			var picks []runs.Output
			for _, o := range e.outputs {
				if o.Role == role {
					picks = append(picks, o)
				}
			}
			switch {
			case len(picks) == 0:
				why = "its run has no output with the role " + role
			case len(picks) > 1:
				why = fmt.Sprintf("its run has %d outputs with the role %s, so none is chosen for it", len(picks), role)
			default:
				var code int
				var msg string
				if d, code, msg = s.imageDataset(ws, picks[0].DatasetID); code != 0 {
					why = picks[0].Name + ": " + msg
				} else if d.Archived {
					why = d.Name + " is archived"
				}
			}
		}
		if why == "" {
			why = sweepFamilyFits(role, ref, d)
		}
		if why == "" && len(members) >= maxFamily {
			why = fmt.Sprintf("the family view compares at most %d maps; these are the first %d in the sweep's order", maxFamily, maxFamily)
		}
		if why != "" {
			left = append(left, sweepFamilyLeft{e.id, e.label, why})
			continue
		}
		if ref == nil {
			dd := d
			ref = &dd
		}
		members = append(members, sweepFamilyMember{JobID: e.id, Label: e.label, DatasetID: d.ID, Name: d.Name})
	}
	out["members"], out["excluded"] = members, left
	if ref != nil {
		out["referenceId"] = ref.ID
	}
	switch {
	case len(members) < 2:
		out["note"] = "fewer than two jobs have maps that can be compared"
	case len(left) > 0:
		out["note"] = "the jobs listed as left out are not compared: nothing is resampled, converted or assumed to make them fit"
	}
	writeJSON(w, 200, out)
}

// sweepFamilyFits is the reason d, an output of the given run role, cannot join a family whose reference is ref (nil for the first member, which
// only has to be usable itself): a mix of orientation and scalar maps, orientations stated differently or not declared, another grid, another brightness unit
// or another place. It is the same checks familyAgreement makes, as sentences for one job each.
func sweepFamilyFits(role string, ref *catalog.Dataset, d catalog.Dataset) string {
	axial := d.Interpretation != nil && d.Interpretation.Kind == "axial_orientation"
	if axial && d.Interpretation.Stale {
		return d.Name + " changed since it was declared as an orientation map; declare it again"
	}
	if !axial {
		if role == runs.RoleOrientation { // an orientation map nobody declared would be compared as a scalar map: a plain difference of angles
			return d.Name + " is an orientation map that has not been declared (unit and frame): declare it in the viewer first, so the comparison uses axial statistics; none is assumed"
		}
		if _, ok := unitOf(d); !ok {
			return d.Name + " has no brightness unit, in its header or declared by you; none is assumed"
		}
	}
	if ref == nil {
		return ""
	}
	if where := familyPlaces([]catalog.Dataset{*ref, d}); where != "" {
		return where
	}
	refAxial := ref.Interpretation != nil && ref.Interpretation.Kind == "axial_orientation"
	if axial != refAxial {
		return d.Name + " and " + ref.Name + " are not the same kind of map (orientation and scalar): a difference of those is not meaningful"
	}
	if axial {
		if why := axialStatedDifferently(ref.Interpretation, d.Interpretation); why != "" {
			return why
		}
	} else {
		ru, _ := unitOf(*ref)
		if du, _ := unitOf(d); du != ru {
			return fmt.Sprintf("%s is in %s and %s in %s: convert one first, nothing is converted here", d.Name, du, ref.Name, ru)
		}
	}
	if why, err := compat.Grids(effectiveHeader(*ref), effectiveHeader(d)); err != nil || len(why) > 0 {
		return d.Name + " and " + ref.Name + " are not known to share a grid" + reasonsSuffix(why) + "; nothing is resampled"
	}
	return ""
}

func reasonsSuffix(why []string) string {
	if len(why) == 0 {
		return ""
	}
	return " (" + strings.Join(why[:min(len(why), 2)], "; ") + ")"
}
