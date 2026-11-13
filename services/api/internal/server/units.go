package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// declareUnit records, or withdraws, the physical unit a person declares for a dataset whose header has no BUNIT. The
// declaration is an overlay: the file and its header facts are never changed, and everywhere the unit is shown it is
// labelled as declared by the person. A file that states its own BUNIT is refused (409): that would be a correction of what
// the file says. POST, so the JSON content-type and cross-site guards apply.
func (s *Server) declareUnit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in struct {
		Unit  string `json:"unit"`
		Clear bool   `json:"clear"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, "invalid request body")
		return
	}
	d, code, msg := s.dataset(wsOf(r), id)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	if d.Kind == catalog.KindHealpix {
		fail(w, 409, "a HEALPix map has no brightness: its columns carry their own units")
		return
	}
	if d.Kind == catalog.KindCatalog {
		fail(w, 409, "a point catalog has no brightness: its columns carry their own units")
		return
	}
	if !in.Clear {
		if bad, err := s.noBrightness(wsOf(r)); err == nil && bad[id] {
			fail(w, 409, catalog.ErrNotBrightness.Error())
			return
		}
	}
	switch {
	case in.Clear:
		if err := s.Store.ClearUnitDeclaration(id); err != nil {
			log.Printf("unit: %v", err)
			fail(w, 500, "internal error")
			return
		}
		s.Audit.RecordCtx(r.Context(), "unit declaration withdrawn", "dataset", id, d.Name)
	default:
		switch err := s.Store.SetUnitDeclaration(d, in.Unit, "user"); {
		case errors.Is(err, catalog.ErrInvalidUnit):
			fail(w, 400, err.Error())
			return
		case errors.Is(err, catalog.ErrUnitInFile), errors.Is(err, catalog.ErrNotBrightness):
			fail(w, 409, err.Error())
			return
		case err != nil:
			log.Printf("unit: %v", err)
			fail(w, 500, "internal error")
			return
		}
		s.Audit.RecordCtx(r.Context(), "unit declared", "dataset", id, d.Name+": "+in.Unit)
	}
	d, code, msg = s.dataset(wsOf(r), id)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	writeJSON(w, 200, d)
}

// declaredUnitOf is the unit sent to the worker with a request for d, or "" when there is none to send. A declaration made
// about different bytes (the file changed since) is not applied: the person is shown that it is stale and confirms it again,
// as with a stale orientation declaration. The worker uses the unit only when the file has no BUNIT of its own.
func declaredUnitOf(d catalog.Dataset) string {
	if d.UnitDeclaration == nil || d.UnitDeclaration.Stale {
		return ""
	}
	return d.UnitDeclaration.Unit
}

// noBrightness is the datasets that cannot be given a brightness unit: the store's (orientation maps, the reliability maps declared for
// them, point catalogs) and the orientation and reliability outputs of a run, by their generic roles (ADR 0068, slice 3). The run record says what those files are, and an orientation map
// whose frame, quantity or unit nobody has declared yet (ADR 0068, slice 2: none is assumed) is still an angle map, not a brightness one.
func (s *Server) noBrightness(ws workspace.ID) (map[int64]bool, error) {
	out, err := s.Store.NoBrightnessIDs(ws)
	if err != nil || s.Runs == nil {
		return out, err
	}
	roles, err := s.Runs.OutputRoles(ws)
	if err != nil {
		return out, err
	}
	for id, role := range roles {
		if role == runs.RoleOrientation || role == runs.RoleReliability {
			out[id] = true
		}
	}
	return out, nil
}
