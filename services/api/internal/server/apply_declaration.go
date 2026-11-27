package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/compat"
	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// "Apply this declaration to the other runs" (ADR 0010, amendment of 2026-11-14). A researcher who has declared one run's orientation
// map (unit, frame, what the angle is) can copy that declaration to the same output of the other runs read with the same tool
// profile, in the same project or folder. The copy is the user's own declaration, labelled as applied from the source dataset, and
// it is written only where nothing says otherwise: never over an existing interpretation, never over what the file's own header
// states, and only after the target's values are checked against the unit where that can be done without reading the plane.

const (
	// ScopeProject and ScopeFolder are the two sets of runs an apply may reach.
	scopeProject = "project"
	scopeFolder  = "folder"

	outcomeDeclare  = "declare" // preview only: what an apply would do
	outcomeDeclared = "declared"
	outcomeSkipped  = "skipped"
	outcomeRefused  = "refused"

	// maxApplyTargets bounds one apply: a project of many hundreds of runs fits, an unbounded list does not.
	maxApplyTargets = 5000
	// radianEpsilon is how far past 2 pi a value may lie before a declaration in radians is refused: rounding, not a different unit.
	radianEpsilon = 1e-3
	// applyReads is how many value-range reads run at once (a remote host answers each over its own link).
	applyReads = 4
)

var applyIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type applyReliability struct {
	DatasetID int64  `json:"datasetId"`
	Name      string `json:"name"`
}

// applyRow is one candidate target: in a preview what an apply would do with it (outcome "declare", "skipped" or "refused"), in a result what
// it did ("declared", "skipped" or "refused"). Reason says why for a skipped or refused one; Note qualifies a declared one (no reliability map).
type applyRow struct {
	DatasetID   int64             `json:"datasetId"`
	Name        string            `json:"name"`
	RunName     string            `json:"runName"`
	Outcome     string            `json:"outcome"`
	Reason      string            `json:"reason,omitempty"`
	Note        string            `json:"note,omitempty"`
	Reliability *applyReliability `json:"reliability"`
}

type applyScope struct {
	Scope string `json:"scope"`
	Label string `json:"label"`
	Runs  int    `json:"runs"`
}

type applySourceInfo struct {
	DatasetID int64   `json:"datasetId"`
	Name      string  `json:"name"`
	RunName   string  `json:"runName"`
	Profile   string  `json:"profile"`
	Unit      string  `json:"unit"`
	Frame     *string `json:"frame"`
	Quantity  *string `json:"quantity"`
}

type applyPreview struct {
	Eligible bool             `json:"eligible"`
	Reason   string           `json:"reason,omitempty"`
	Source   *applySourceInfo `json:"source"`
	Scopes   []applyScope     `json:"scopes"`
	Scope    string           `json:"scope"`
	Rows     []applyRow       `json:"rows"`
	Counts   map[string]int   `json:"counts"`
}

// applyBasis is a source dataset resolved to what an apply copies from it, and the sets of runs it may reach.
type applyBasis struct {
	src    catalog.Dataset
	run    runs.RoleOutput
	scopes []applyScopeSet
}

// applyScopeSet is one reachable set of runs: those on host whose folder lies inside root.
type applyScopeSet struct {
	applyScope
	host int64
	root string
}

// runKey splits a run's stored folder key ("h3:/path" for host 3, a plain path for this computer).
func runKey(key string) (host int64, p string) {
	if strings.HasPrefix(key, "h") {
		if i := strings.IndexByte(key, ':'); i > 1 {
			if n, err := strconv.ParseInt(key[1:i], 10, 64); err == nil {
				return n, key[i+1:]
			}
		}
	}
	return 0, key
}

// insideOn is inside for a folder on a host, whose paths are slash-separated whatever this computer is.
func insideOn(host int64, root, p string) bool {
	if host == 0 {
		return inside(root, filepath.Clean(p))
	}
	root, p = path.Clean(root), path.Clean(p)
	return p == root || root == "/" || strings.HasPrefix(p, root+"/")
}

func folderLabel(host int64, dir string) string {
	l := "…/" + path.Base(filepath.ToSlash(dir))
	if host != 0 {
		l = "host " + strconv.FormatInt(host, 10) + ", " + l
	}
	return l
}

// applyBasisOf resolves the source dataset. When the declaration cannot be applied it returns the reason (an answer, not an error).
func (s *Server) applyBasisOf(ws workspace.ID, id int64) (b applyBasis, reason string, code int, msg string) {
	src, code, msg := s.dataset(ws, id)
	if code != 0 {
		return b, "", code, msg
	}
	b.src = src
	in := src.Interpretation
	switch {
	case in == nil || in.Kind != "axial_orientation":
		return b, "this map is not declared as an axial orientation map yet", 0, ""
	case in.DeclaredBy != "user":
		return b, "only a declaration you made yourself is applied to other runs; this one was made by " + in.DeclaredBy + ", and each run's own header or import rule stands for it", 0, ""
	case in.AppliedFrom != nil:
		return b, "this declaration was itself copied from dataset " + strconv.FormatInt(*in.AppliedFrom, 10) + "; apply it from that one, so every copy names the declaration it came from", 0, ""
	case in.Stale:
		return b, "the file changed since you declared it; declare it again before applying it to other runs", 0, ""
	case s.Runs == nil:
		return b, "runs are not configured", 0, ""
	}
	run, role, found, err := s.Runs.OutputOf(ws, id)
	if err != nil {
		log.Printf("apply: %v", err)
		return b, "", 500, "internal error"
	}
	switch {
	case !found:
		return b, "this map is not an output of an imported run, so there are no other runs to apply it to", 0, ""
	case role != runs.RoleOrientation:
		return b, "this is the " + role + " output of its run; only an orientation output is applied to the other runs", 0, ""
	}
	b.run = run
	host, dir := runKey(run.Dir)
	if s.Projects != nil {
		list, err := s.Projects.List(ws, 500)
		if err != nil {
			log.Printf("apply: %v", err)
			return b, "", 500, "internal error"
		}
		var best *projects.Project
		for i := range list {
			if p := list[i]; p.HostID == host && insideOn(host, p.Path, dir) && (best == nil || len(p.Path) > len(best.Path)) {
				best = &list[i]
			}
		}
		if best != nil {
			b.scopes = append(b.scopes, applyScopeSet{applyScope: applyScope{Scope: scopeProject, Label: best.Name}, host: host, root: best.Path})
		}
	}
	parent := path.Dir(filepath.ToSlash(dir))
	if host == 0 {
		parent = filepath.Dir(dir)
	}
	b.scopes = append(b.scopes, applyScopeSet{applyScope: applyScope{Scope: scopeFolder, Label: folderLabel(host, parent)}, host: host, root: parent})
	return b, "", 0, ""
}

// candidates are the orientation outputs of the runs read with the source's profile, in the scope, apart from the source itself.
func (s *Server) applyCandidates(ws workspace.ID, b applyBasis, set applyScopeSet) ([]runs.RoleOutput, error) {
	all, err := s.Runs.WithRole(ws, b.run.Profile, runs.RoleOrientation)
	if err != nil {
		return nil, err
	}
	var out []runs.RoleOutput
	for _, o := range all {
		if o.DatasetID == b.src.ID {
			continue
		}
		if h, p := runKey(o.Dir); h == set.host && insideOn(set.host, set.root, p) {
			out = append(out, o)
		}
	}
	return out, nil
}

// headerStatesConvention is whether the file's own header carries a PSIFRAME or PSIANGLE card (whatever its value) or has a profile place
// any of the frame, what the angle is or its unit: then the file has spoken, and a copied declaration does not replace it. A header that
// was stored before profiles read it is judged by its cards alone.
func headerStatesConvention(h *catalog.VGTHeader) bool {
	if h == nil {
		return false
	}
	return h.Orientation != nil || h.PsiFrame != nil || h.PsiAngle != nil || (h.Profile != nil && len(h.Unresolved) < 3)
}

// existingReason says who already declared a target, for the skipped list.
func existingReason(in *catalog.Interpretation) string {
	switch name, byRule := discover.RuleName(in.DeclaredBy); {
	case in.DeclaredBy == "user" && in.AppliedFrom != nil:
		return "already declared by you (copied from dataset " + strconv.FormatInt(*in.AppliedFrom, 10) + ")"
	case in.DeclaredBy == "user":
		return "already declared by you"
	case catalog.IsFileHeaderAuthor(in.DeclaredBy):
		return "already declared by the file's own header"
	case byRule:
		return "already declared by your import rule " + name
	default:
		return "already has an interpretation proposed by " + in.DeclaredBy + "; it is not replaced"
	}
}

// planApplyTarget decides, from the catalog alone, what an apply does with one target. The interpretation it would write is returned
// with a "declare" outcome.
func (s *Server) planApplyTarget(ws workspace.ID, b applyBasis, t runs.RoleOutput) (applyRow, catalog.Interpretation) {
	row := applyRow{DatasetID: t.DatasetID, RunName: t.RunName}
	d, err := s.Store.Get(ws, t.DatasetID)
	if err != nil {
		row.Outcome, row.Reason = outcomeRefused, "this dataset is no longer registered"
		if !errors.Is(err, catalog.ErrNotFound) {
			log.Printf("apply: %v", err)
			row.Reason = "this dataset could not be read from the catalog"
		}
		return row, catalog.Interpretation{}
	}
	row.Name = d.Name
	skip := func(why string) (applyRow, catalog.Interpretation) {
		row.Outcome, row.Reason = outcomeSkipped, why
		return row, catalog.Interpretation{}
	}
	refuse := func(why string) (applyRow, catalog.Interpretation) {
		row.Outcome, row.Reason = outcomeRefused, why
		return row, catalog.Interpretation{}
	}
	switch {
	case d.Archived:
		return skip("archived")
	case d.Interpretation != nil:
		return skip(existingReason(d.Interpretation))
	case headerStatesConvention(d.OrientationHeader):
		return skip("the file's own header states the convention, and a file's own statement wins over a copied declaration")
	case !d.HasPixelGrid():
		return refuse("not an image with a pixel grid")
	case nonDegenerateAxes(d.Header) != 2:
		return refuse("not a single-plane map (two axes longer than 1)")
	}
	in := catalog.Interpretation{Kind: "axial_orientation", Unit: b.src.Interpretation.Unit, Frame: b.src.Interpretation.Frame, Quantity: b.src.Interpretation.Quantity}
	switch len(t.Reliability) {
	case 0:
		row.Note = "its run has no reliability output: declared without a reliability map"
	case 1:
		rd, err := s.Store.Get(ws, t.Reliability[0])
		switch {
		case err != nil || !rd.HasPixelGrid():
			row.Note = "its run's reliability output is not available in the catalog: declared without a reliability map"
		case rd.HostID != d.HostID:
			row.Note = "its run's reliability map is on another host: declared without a reliability map"
		case rd.Interpretation != nil && rd.Interpretation.Kind == "axial_orientation":
			row.Note = "its run's reliability output is itself declared as angles: declared without a reliability map"
		default:
			why, gerr := compat.Grids(effectiveHeader(d), effectiveHeader(rd))
			if gerr != nil || len(why) > 0 {
				row.Note = "its run's reliability map is not known to share this map's grid"
				if len(why) > 0 {
					row.Note += " (" + why[0] + ")"
				}
				row.Note += ": declared without a reliability map"
			} else {
				in.ReliabilityID = &rd.ID
				row.Reliability = &applyReliability{DatasetID: rd.ID, Name: rd.Name}
			}
		}
	default:
		row.Note = "its run lists several reliability outputs, and none is picked for it: declared without a reliability map"
	}
	row.Outcome = outcomeDeclare
	return row, in
}

func (s *Server) applyCounts(rows []applyRow) map[string]int {
	c := map[string]int{outcomeDeclare: 0, outcomeDeclared: 0, outcomeSkipped: 0, outcomeRefused: 0}
	for _, r := range rows {
		c[r.Outcome]++
	}
	return c
}

// applyPreviewRoute lists what applying the dataset's declaration to the other runs would do, one row per candidate, from the catalog
// alone: no file is read (the values are checked when the apply is confirmed).
func (s *Server) applyPreviewRoute(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	ws := wsOf(r)
	b, reason, code, msg := s.applyBasisOf(ws, id)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	out := applyPreview{Eligible: reason == "", Reason: reason, Scopes: []applyScope{}, Rows: []applyRow{}, Counts: map[string]int{}}
	if !out.Eligible {
		writeJSON(w, 200, out)
		return
	}
	in := b.src.Interpretation
	out.Source = &applySourceInfo{DatasetID: b.src.ID, Name: b.src.Name, RunName: b.run.RunName, Profile: b.run.Profile, Unit: in.Unit, Frame: in.Frame, Quantity: in.Quantity}
	want := r.URL.Query().Get("scope")
	if want == "" {
		want = b.scopes[0].Scope
	}
	var chosen *applyScopeSet
	for i := range b.scopes {
		set := &b.scopes[i]
		cands, err := s.applyCandidates(ws, b, *set)
		if err != nil {
			log.Printf("apply: %v", err)
			fail(w, 500, "internal error")
			return
		}
		set.Runs = len(cands)
		out.Scopes = append(out.Scopes, set.applyScope)
		if set.Scope == want {
			chosen = set
			for _, t := range cands {
				row, _ := s.planApplyTarget(ws, b, t)
				out.Rows = append(out.Rows, row)
			}
		}
	}
	if chosen == nil {
		fail(w, 400, "scope must be project or folder, and project only when the run is inside an opened project")
		return
	}
	out.Scope = chosen.Scope
	out.Counts = s.applyCounts(out.Rows)
	writeJSON(w, 200, out)
}

// valueRange reads the extremes of a target's finite values in the declared unit from the worker, which answers with numbers only
// (no plane crosses the link from a host). It refuses to guess: a reply without them (an older worker) is an error.
func (s *Server) valueRange(ctx context.Context, d catalog.Dataset, unit string) (lo, hi *float64, why string) {
	_, _, real, k, status, msg := s.chooseRead(ctx, d, true)
	if status != 0 {
		return nil, nil, "the file could not be read to check its values: " + msg
	}
	svc := s.planesOn(k.host)
	if svc == nil {
		return nil, nil, "the scientific worker is not configured, so the values cannot be checked"
	}
	req := map[string]any{"op": "axial", "path": real, "unit": unit}
	if k.ino != 0 && k.host == 0 {
		req["expect"] = []int64{k.dev, k.ino}
	}
	rep, err := svc.Do(ctx, req)
	var re *worker.RequestError
	switch {
	case errors.As(err, &re):
		return nil, nil, "the values could not be read: " + re.Message
	case err != nil:
		return nil, nil, "the values could not be read (the worker or the host did not answer)"
	case sentHash(req) != k.corr:
		return nil, nil, "the corrections of this dataset changed while its values were being read; try again"
	}
	var head struct {
		Stats struct {
			Min *float64 `json:"value_min"`
			Max *float64 `json:"value_max"`
		} `json:"stats"`
	}
	if json.Unmarshal(rep.Head, &head) != nil || head.Stats.Min == nil || head.Stats.Max == nil {
		return nil, nil, "the worker did not report the range of the values (it may be an older version, or the map has no value), so the unit was not checked"
	}
	return head.Stats.Min, head.Stats.Max, ""
}

// rangeRefusal is the reason a declared unit cannot hold the values, or "". Radians: nothing may lie beyond 2 pi (+ a rounding margin).
// Degrees are not refused here, because degrees declared on radian values are a warning in the statistics (ADR 0010), not a certainty.
func rangeRefusal(unit string, lo, hi float64) string {
	if unit == "rad" && (hi > 2*math.Pi+radianEpsilon || lo < -2*math.Pi-radianEpsilon) {
		return fmt.Sprintf("its values run from %s to %s, beyond 2π, so they cannot be radians as declared", trim(lo), trim(hi))
	}
	return ""
}

func trim(v float64) string { return strconv.FormatFloat(v, 'g', 6, 64) }

// applyRoute writes the source's declaration to the targets the client previewed, each re-validated here. It takes only ids from the
// scope the request names, so another workspace's dataset, another tool's run or a run outside the scope is refused by name.
func (s *Server) applyRoute(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SourceID  int64   `json:"sourceId"`
		Scope     string  `json:"scope"`
		TargetIDs []int64 `json:"targetIds"`
		Confirm   bool    `json:"confirm"`
	}
	if !body(w, r, 256<<10, &in, true) {
		return
	}
	if !in.Confirm {
		fail(w, 400, "declarations are written only when you confirm: send confirm: true")
		return
	}
	if len(in.TargetIDs) == 0 || len(in.TargetIDs) > maxApplyTargets {
		fail(w, 400, "send between 1 and "+strconv.Itoa(maxApplyTargets)+" target ids")
		return
	}
	ws := wsOf(r)
	b, reason, code, msg := s.applyBasisOf(ws, in.SourceID)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	if reason != "" {
		fail(w, 409, reason)
		return
	}
	var set *applyScopeSet
	for i := range b.scopes {
		if b.scopes[i].Scope == in.Scope {
			set = &b.scopes[i]
		}
	}
	if set == nil {
		fail(w, 400, "scope must be project or folder, and project only when the run is inside an opened project")
		return
	}
	cands, err := s.applyCandidates(ws, b, *set)
	if err != nil {
		log.Printf("apply: %v", err)
		fail(w, 500, "internal error")
		return
	}
	byDataset := map[int64]runs.RoleOutput{}
	for _, c := range cands {
		byDataset[c.DatasetID] = c
	}
	// phase 1, from the catalog: each target is accepted, skipped or refused; nothing is written yet
	type item struct {
		row  applyRow
		decl catalog.Interpretation
		d    catalog.Dataset
	}
	seen := map[int64]bool{}
	var items []*item
	for _, id := range in.TargetIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		t, ok := byDataset[id]
		if !ok { // not named by id: an unknown id, another workspace's dataset and the source itself are all the same answer
			items = append(items, &item{row: applyRow{DatasetID: id, Outcome: outcomeRefused, Reason: "not an orientation output of another run in this scope read with the same tool profile"}})
			continue
		}
		row, decl := s.planApplyTarget(ws, b, t)
		it := &item{row: row, decl: decl}
		if row.Outcome == outcomeDeclare {
			it.d, _ = s.Store.Get(ws, id)
		}
		items = append(items, it)
	}
	// phase 2: the value range of what would be declared in radians, a few reads at a time
	sem := make(chan struct{}, applyReads)
	var wg sync.WaitGroup
	for _, it := range items {
		if it.row.Outcome != outcomeDeclare || it.decl.Unit != "rad" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(it *item) {
			defer wg.Done()
			defer func() { <-sem }()
			lo, hi, why := s.valueRange(r.Context(), it.d, it.decl.Unit)
			if why == "" {
				why = rangeRefusal(it.decl.Unit, *lo, *hi)
			}
			if why != "" {
				it.row.Outcome, it.row.Reason, it.row.Note, it.row.Reliability = outcomeRefused, why, "", nil
			}
		}(it)
	}
	wg.Wait()
	if r.Context().Err() != nil {
		fail(w, 499, "the request was cancelled; nothing was declared")
		return
	}
	// phase 3: each accepted target is one conditional insert, so a declaration made since the preview is never overwritten
	var idb [16]byte
	if _, err := rand.Read(idb[:]); err != nil {
		fail(w, 500, "internal error")
		return
	}
	applyID := hex.EncodeToString(idb[:])
	by := actorID(r.Context())
	if by == "" {
		by = authz.LocalOwnerID
	}
	rows := make([]applyRow, 0, len(items))
	for _, it := range items {
		if it.row.Outcome == outcomeDeclare {
			ok, err := s.Store.ApplyInterpretation(it.d, it.decl, applyID, b.src.ID, by)
			switch {
			case err != nil:
				log.Printf("apply: %v", err)
				it.row.Outcome, it.row.Reason, it.row.Note, it.row.Reliability = outcomeRefused, "the declaration could not be saved", "", nil
			case !ok:
				it.row.Outcome, it.row.Reason, it.row.Note, it.row.Reliability = outcomeSkipped, "declared by someone since the preview; it is not replaced", "", nil
			default:
				it.row.Outcome = outcomeDeclared
			}
		}
		rows = append(rows, it.row)
	}
	counts := s.applyCounts(rows)
	s.Audit.RecordCtx(r.Context(), "orientation declaration applied to other runs", "dataset", b.src.ID,
		fmt.Sprintf("apply %s: %d declared, %d skipped, %d refused, scope %s", applyID, counts[outcomeDeclared], counts[outcomeSkipped], counts[outcomeRefused], in.Scope))
	writeJSON(w, 200, map[string]any{"applyId": applyID, "scope": in.Scope, "rows": rows, "counts": counts})
}

// undoApplyRoute removes exactly the declarations one apply wrote that nobody has edited or cleared since. It is repeatable: a second
// call removes nothing.
func (s *Server) undoApplyRoute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("applyId")
	if !applyIDPattern.MatchString(id) {
		fail(w, 400, "bad apply id")
		return
	}
	n, err := s.Store.UndoApply(wsOf(r), id)
	if err != nil {
		log.Printf("apply undo: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.Audit.RecordCtx(r.Context(), "orientation declarations of an apply removed", "dataset", 0, fmt.Sprintf("apply %s: %d removed", id, n))
	writeJSON(w, 200, map[string]any{"applyId": id, "removed": n})
}
