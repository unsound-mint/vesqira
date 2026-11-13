package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/lifecycle"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Cleanup of old family maps (ADR 0070). A family comparison registers one dataset per non-reference member and one for the spread; they
// are derived resources and the person may want them gone. Listing and removing go through the same catalog delete as any dataset (ADR 0041),
// with a stricter rule: only a map nothing else points at is removed. There is no automatic policy: a result may be wanted next month,
// and what points at it (a figure, a board, a report, a published review) is not something a timer can judge.

// familyMapLoose are the impact groups of a dataset that do not hold a family map back: its place in the exploration history, other
// locations of the file, its offline copy and its own provenance record all go with it, and none of them is work the person did on it.
var familyMapLoose = []string{"history", "locations", "offline", "record"}

type familyMapView struct {
	DatasetID   int64     `json:"datasetId"`
	Name        string    `json:"name"`
	Role        string    `json:"role"` // spread or difference
	Member      int       `json:"member"`
	Run         string    `json:"run"` // one comparison's maps share it
	ReferenceID int64     `json:"referenceId"`
	HostID      int64     `json:"hostId"`
	SizeBytes   int64     `json:"sizeBytes"`
	CreatedAt   time.Time `json:"createdAt"`
	// Holds say what keeps the map: a figure, board, report or published review that uses it, or something made from it. Empty when it can go.
	Holds []string `json:"holds"`
}

var familyCleanMu sync.Mutex // one removal at a time: what holds a map is read, then the map is deleted against that reading

// familyMapOf reads a family map's record, nil when the dataset is not a family comparison result of this workspace.
func (s *Server) familyMapOf(ws workspace.ID, rec derived.Record) (familyMapView, bool) {
	d, err := s.Store.Get(ws, rec.DatasetID)
	if err != nil {
		return familyMapView{}, false
	}
	var p struct {
		Role        string `json:"role"`
		Member      int    `json:"member"`
		Run         string `json:"run"`
		ReferenceID int64  `json:"referenceId"`
	}
	_ = json.Unmarshal(rec.Params, &p)
	run := p.Run
	if run == "" { // a result made before runs were named: its maps share the moment they were made and their reference
		run = fmt.Sprintf("%d-%d", p.ReferenceID, rec.CreatedAt.Unix())
	}
	v := familyMapView{DatasetID: d.ID, Name: d.Name, Role: p.Role, Member: p.Member, Run: run, ReferenceID: p.ReferenceID, HostID: d.HostID, SizeBytes: d.SizeBytes, CreatedAt: rec.CreatedAt, Holds: []string{}}
	if o := s.owned(d); o != nil {
		v.SizeBytes = o.SizeBytes
	}
	return v, true
}

// familyMapHolds is what keeps a family map, in plain words; also whether the map is gone already.
func (s *Server) familyMapHolds(ws workspace.ID, id int64) (impactView, []string, int, string) {
	im, code, msg := s.impactOf(ws, lifecycle.KindDataset, id)
	if code != 0 {
		return im, nil, code, msg
	}
	holds := []string{}
	for _, g := range im.Groups {
		if !slices.Contains(familyMapLoose, g.Key) {
			holds = append(holds, g.Text)
		}
	}
	return im, holds, 0, ""
}

// listFamilyMaps lists the comparison and spread maps family runs made in this workspace, newest first, each with what holds it.
func (s *Server) listFamilyMaps(w http.ResponseWriter, r *http.Request) {
	ws := wsOf(r)
	recs, err := s.Derived.ByOp(ws, "family_agreement")
	if err != nil {
		log.Printf("derived: %v", err)
		fail(w, 500, "internal error")
		return
	}
	out := []familyMapView{}
	for _, rec := range recs {
		v, ok := s.familyMapOf(ws, rec)
		if !ok {
			continue
		}
		_, holds, code, _ := s.familyMapHolds(ws, v.DatasetID)
		if code != 0 {
			continue
		}
		v.Holds = holds
		out = append(out, v)
		if len(out) >= 1000 {
			break
		}
	}
	writeJSON(w, 200, map[string]any{"maps": out})
}

type familyMapRemoval struct {
	DatasetID int64  `json:"datasetId"`
	Name      string `json:"name"`
	Removed   bool   `json:"removed"`
	Reason    string `json:"reason,omitempty"`
	// Bytes says what became of the file: removed (written by Vesqira on this computer), kept (on a host, where Vesqira deletes nothing), or none.
	Bytes string `json:"bytes"`
}

// removeFamilyMaps removes the chosen family maps that nothing holds, from the catalog and, for files Vesqira wrote on this computer, from disk.
// A map held by a figure, board, report or published review, or one that something was made from, is refused with the reason; the maps that
// can go still go. The sources of the family are never touched: only datasets with a family comparison record are accepted.
func (s *Server) removeFamilyMaps(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DatasetIDs []int64 `json:"datasetIds"`
	}
	if !body(w, r, 16<<10, &in, true) {
		return
	}
	if n := len(in.DatasetIDs); n < 1 || n > 500 {
		fail(w, 422, "choose between 1 and 500 maps")
		return
	}
	ws := wsOf(r)
	familyCleanMu.Lock()
	defer familyCleanMu.Unlock()
	results := make([]familyMapRemoval, 0, len(in.DatasetIDs))
	removed := 0
	for i, id := range in.DatasetIDs {
		if slices.Index(in.DatasetIDs, id) != i {
			continue
		}
		res := familyMapRemoval{DatasetID: id, Bytes: "none"}
		rec, err := s.Derived.Get(id)
		if err != nil || rec.Op != "family_agreement" {
			res.Reason = "this is not a map made by a family comparison, so it is not removed here"
			results = append(results, res)
			continue
		}
		v, ok := s.familyMapOf(ws, rec)
		if !ok {
			res.Reason = "this map is not in the catalog"
			results = append(results, res)
			continue
		}
		res.Name = v.Name
		im, holds, code, msg := s.familyMapHolds(ws, id)
		switch {
		case code != 0:
			res.Reason = msg
		case len(holds) > 0:
			res.Reason = "kept: " + strings.Join(holds, " ")
		default:
			res.Removed, res.Bytes, res.Reason = s.deleteFamilyMap(r, ws, v, im)
		}
		if res.Removed {
			removed++
		}
		results = append(results, res)
	}
	if removed == 0 {
		writeJSON(w, 409, map[string]any{"error": results[0].Reason, "results": results})
		return
	}
	writeJSON(w, 200, map[string]any{"results": results})
}

// deleteFamilyMap deletes one family map against the impact just read, and removes its bytes when Vesqira wrote them on this computer.
func (s *Server) deleteFamilyMap(r *http.Request, ws workspace.ID, v familyMapView, im impactView) (removed bool, bytes, reason string) {
	id := v.DatasetID
	_, err := s.lc().Delete(ws, lifecycle.KindDataset, id, im.Digest)
	var changed *lifecycle.ChangedError
	var blocked *lifecycle.BlockedError
	switch {
	case errors.As(err, &changed), errors.As(err, &blocked):
		return false, "none", "kept: what depends on it changed while it was being removed; look again"
	case errors.Is(err, lifecycle.ErrNotFound):
		return false, "none", "this map is not in the catalog"
	case err != nil:
		log.Printf("family maps: delete %d: %v", id, err)
		return false, "none", "internal error"
	}
	bytes = "none"
	detail := fmt.Sprintf("deleted the family map %q from the catalog; its sources were not touched", im.Name)
	if im.OwnedBytes != nil {
		bytes = "removed"
		if err := os.Remove(im.OwnedBytes.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("family maps: removing %s: %v", im.OwnedBytes.Name, err)
			bytes = "failed"
			detail += "; its file could not be removed"
		} else {
			detail += "; its file was removed"
		}
	} else if v.HostID != 0 {
		bytes = "kept"
		detail += "; the file on the host was left in place (Vesqira deletes nothing there)"
	}
	s.Offline.RemoveFiles(id)
	s.Audit.RecordCtx(r.Context(), "deleted", lifecycle.KindDataset, id, detail)
	return true, bytes, ""
}
