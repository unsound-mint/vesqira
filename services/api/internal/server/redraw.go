package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	idty "github.com/unsound-mint/vesqira/services/api/internal/identity"
	"github.com/unsound-mint/vesqira/services/api/internal/scene"
)

// "Draw again" (ADR 0042, section 5). The browser re-renders a figure's stored description with the viewer's renderer. Before it
// does, this route compares what the figure recorded about each file it was drawn from (figure-identity/1, ADR 0053) with the
// files as they are now, so a changed file is announced and never swapped in silently. It reads and changes nothing.

// Statuses of one source, strongest claim first.
const (
	srcUnchanged     = "unchanged"       // the full SHA-256 equals the recorded one: the same bytes
	srcProbable      = "probable"        // nothing differs, but what was recorded cannot prove the bytes are the same
	srcChanged       = "changed"         // the registered file is not what the figure was drawn from; it can still be read
	srcChangedOnDisk = "changed_on_disk" // the file on disk is not even what is registered; it cannot be read until registered again
	srcMissing       = "missing"         // no longer registered, or no readable file
)

// The wording the owner reads, kept in one place so the route, the web client and the ADR say the same things.
const (
	promptChanged  = "Drawing from changed data makes a different figure; the original PNG is kept. Draw it again anyway?"
	promptProbable = "Some data cannot be proven unchanged. Drawing from different bytes makes a different figure; the original PNG is kept. Draw it again anyway?"
	promptBlocked  = "This figure cannot be drawn again until every file it was drawn from can be read. The original PNG is kept."
	promptNone     = "This figure keeps no description of how it was drawn, so it is shown as the PNG that was saved."
	promptNoRender = "This figure's description does not record the resolution its picture was drawn at, so it is shown as the PNG that was saved. A drawing at another resolution would not be the same picture."
)

type redrawSource struct {
	Role      string   `json:"role"` // source | overlay
	DatasetID int64    `json:"datasetId"`
	Name      string   `json:"name"`
	Status    string   `json:"status"`
	Differs   []string `json:"differs"`
	Readable  bool     `json:"readable"`
	Text      string   `json:"text"`
}

type redrawResult struct {
	FigureID int64          `json:"figureId"`
	Kind     string         `json:"kind"`  // scene | channel-grid | none
	State    string         `json:"state"` // ready | ask | blocked | none
	Prompt   string         `json:"prompt,omitempty"`
	Sources  []redrawSource `json:"sources"`
}

// phrase turns what differs into words: "time differs", "size and fingerprint differ".
func phrase(d []string) string {
	switch len(d) {
	case 0:
		return "nothing recorded differs"
	case 1:
		return d[0] + " differs"
	}
	return strings.Join(d[:len(d)-1], ", ") + " and " + d[len(d)-1] + " differ"
}

func evidenceOf(size int64, sampled, full *identityValue) idty.Evidence {
	e := idty.Evidence{Size: size}
	if sampled != nil {
		e.Sampled = sampled.Value
	}
	if full != nil {
		e.Full = full.Value
	}
	return e
}

// classifySource compares what a figure recorded about a file with the registered file as it is now (src nil: it recorded
// nothing). It never claims more than the evidence: only equal full hashes give "unchanged"; equal size, time and sampled
// fingerprint give "probable", because the sampled fingerprint has a blind spot (ADR 0053).
func classifySource(src *figureDatasetIdentity, d catalog.Dataset) (status string, differs []string, text string) {
	if src == nil {
		return srcProbable, []string{}, fmt.Sprintf("%s: probably unchanged, not verified (no identity was recorded for this file when the figure was drawn).", d.Name)
	}
	diff := figureDiffers(*src, d)
	cur := catalog.Location{}
	if p, ok := primaryOf(d.Locations); ok {
		cur = p
	}
	var curSampled, curFull *identityValue
	if cur.Sampled != nil {
		curSampled = &identityValue{Value: cur.Sampled.Value}
	}
	if cur.Full != nil {
		curFull = &identityValue{Value: cur.Full.Value}
	}
	rel := idty.Relate(evidenceOf(src.SizeBytes, src.Sampled, src.Full), evidenceOf(d.SizeBytes, curSampled, curFull))
	switch {
	case rel == idty.Verified && len(diff) == 0:
		return srcUnchanged, diff, fmt.Sprintf("%s: unchanged (its SHA-256 equals the one recorded when the figure was drawn).", d.Name)
	case rel == idty.Verified:
		return srcUnchanged, diff, fmt.Sprintf("%s: unchanged (its SHA-256 equals the recorded one; only its modification time differs).", d.Name)
	case rel == idty.Different || len(diff) > 0:
		if len(diff) == 0 {
			diff = []string{"fingerprint"}
		}
		return srcChanged, diff, fmt.Sprintf("%s: changed since the figure was drawn (%s).", d.Name, phrase(diff))
	}
	why := "size and time match, but no fingerprint was recorded to compare"
	if src.Sampled != nil && curSampled != nil {
		why = "size and time match and the sampled fingerprint is equal, which does not prove the whole file is the same"
	}
	return srcProbable, diff, fmt.Sprintf("%s: probably unchanged, not verified (%s).", d.Name, why)
}

// classifyUnreadable says why a source cannot be read now. onDisk is what the file on this computer looks like now (name of what
// differs from the figure's record), when it could be looked at.
func classifyUnreadable(name string, status int, onDisk []string) (string, []string, string) {
	if status == http.StatusConflict {
		return srcChangedOnDisk, onDisk, fmt.Sprintf("%s: the file on disk changed after it was registered (%s); register it again, then draw again.", name, phrase(onDisk))
	}
	why := "not available"
	if status == http.StatusNotFound {
		why = "the file was not found"
	}
	return srcMissing, []string{}, fmt.Sprintf("%s: %s.", name, why)
}

// figureSources lists the datasets a stored description draws from, the base first, then each overlay's dataset once.
func figureSources(f boards.Figure) (kind string, ids []int64, sc *scene.Scene, ok bool, why string) {
	if len(f.Scene) == 0 {
		return "none", nil, nil, false, promptNone
	}
	var head struct {
		Schema string `json:"schema"`
	}
	if json.Unmarshal(f.Scene, &head) != nil {
		return "none", nil, nil, false, promptNone
	}
	switch head.Schema {
	case scene.Version:
		parsed, err := scene.Parse(f.Scene)
		if err != nil {
			return "none", nil, nil, false, promptNone
		}
		sc = &parsed
		if sc.Render == nil {
			return "scene", nil, sc, false, promptNoRender
		}
		ids = []int64{sc.Base.DatasetID}
		seen := map[int64]bool{sc.Base.DatasetID: true}
		for _, o := range sc.Overlays {
			if !seen[o.DatasetID] {
				seen[o.DatasetID] = true
				ids = append(ids, o.DatasetID)
			}
		}
		return "scene", ids, sc, true, ""
	case scene.ChannelGridVersion:
		g, err := scene.ParseChannelGrid(f.Scene)
		if err != nil {
			return "none", nil, nil, false, promptNone
		}
		if g.Render == nil {
			return "channel-grid", nil, nil, false, promptNoRender
		}
		return "channel-grid", []int64{g.DatasetID}, nil, true, ""
	}
	return "none", nil, nil, false, promptNone
}

// sceneReliabilityIDs lists the reliability maps a scene's visible orientation overlays read, in scene order: the one the
// overlay names (it sets the line opacity) or, when it names none but sets the length or a threshold from reliability, the one
// its dataset declares now. lookup finds a dataset of the scene.
func sceneReliabilityIDs(sc *scene.Scene, lookup func(int64) (catalog.Dataset, bool)) []int64 {
	var out []int64
	seen := map[int64]bool{}
	for _, o := range sc.Overlays {
		if o.Kind != "orientation" || !o.Visible {
			continue
		}
		id := o.ReliabilityID
		if id == nil && (o.Length == "reliability" || o.Threshold != nil) {
			if d, ok := lookup(o.DatasetID); ok && d.Interpretation != nil {
				id = d.Interpretation.ReliabilityID
			}
		}
		if id != nil && !seen[*id] {
			seen[*id] = true
			out = append(out, *id)
		}
	}
	return out
}

// reliabilityMaps are the reliability maps a figure's scene reads that are not already recorded as its source or an overlay's
// dataset: a map that sets line opacity, length or the threshold decides which lines exist and how they look, so a figure
// records its identity too (role "reliability").
func (s *Server) reliabilityMaps(sc *scene.Scene, base catalog.Dataset, others []catalog.Dataset) []catalog.Dataset {
	if sc == nil {
		return nil
	}
	known := map[int64]catalog.Dataset{base.ID: base}
	for _, o := range others {
		known[o.ID] = o
	}
	var out []catalog.Dataset
	for _, id := range sceneReliabilityIDs(sc, func(id int64) (catalog.Dataset, bool) { d, ok := known[id]; return d, ok }) {
		if _, dup := known[id]; dup {
			continue
		}
		if d, err := s.Store.Get(id); err == nil {
			out = append(out, d)
		}
	}
	return out
}

// onDiskDiffers says what differs between the figure's record of a file on this computer and the file now (size, time and, when
// both exist, the sampled fingerprint). Files on a host are not looked at.
func (s *Server) onDiskDiffers(d catalog.Dataset, src *figureDatasetIdentity) []string {
	if d.HostID != 0 {
		return []string{}
	}
	real, err := s.roots().Resolve(d.Path)
	if err != nil {
		return []string{}
	}
	info, err := statFile(real)
	if err != nil {
		return []string{}
	}
	if src == nil {
		return differs(d.SizeBytes, d.ModifiedAt, "", info.Size(), info.ModTime(), "")
	}
	fp := ""
	if src.Sampled != nil {
		fp = s.sampledOf(real)
	}
	sf := ""
	if src.Sampled != nil {
		sf = src.Sampled.Value
	}
	return differs(src.SizeBytes, src.ModifiedAt, sf, info.Size(), info.ModTime(), fp)
}

// checkRedraw builds the comparison for one figure.
func (s *Server) checkRedraw(ctx context.Context, f boards.Figure) redrawResult {
	res := redrawResult{FigureID: f.ID, Sources: []redrawSource{}}
	kind, ids, sc, ok, why := figureSources(f)
	res.Kind = kind
	if !ok {
		res.State, res.Prompt = "none", why
		return res
	}
	roles := map[int64]string{}
	for i, id := range ids {
		roles[id] = "overlay"
		if i == 0 {
			roles[id] = "source"
		}
	}
	if sc != nil { // a reliability map the scene reads is a file the picture depends on too
		lookup := func(id int64) (catalog.Dataset, bool) { d, err := s.Store.Get(id); return d, err == nil }
		for _, id := range sceneReliabilityIDs(sc, lookup) {
			if _, dup := roles[id]; !dup {
				ids = append(ids, id)
				roles[id] = "reliability"
			}
		}
	}
	var doc figureIdentity
	if len(f.Identity) > 0 {
		_ = json.Unmarshal(f.Identity, &doc)
	}
	recorded := func(id int64) *figureDatasetIdentity {
		for i := range doc.Datasets {
			if doc.Datasets[i].DatasetID == id {
				return &doc.Datasets[i]
			}
		}
		return nil
	}
	blocked, anyChanged, anyProbable := false, false, false
	for _, id := range ids {
		role := roles[id]
		src := recorded(id)
		out := redrawSource{Role: role, DatasetID: id, Differs: []string{}}
		d, err := s.Store.Get(id)
		if err != nil {
			out.Name = "dataset no longer registered"
			if src != nil {
				out.Name = src.Name
			}
			out.Status, out.Text = srcMissing, fmt.Sprintf("%s: no longer registered.", out.Name)
			blocked = true
			res.Sources = append(res.Sources, out)
			continue
		}
		out.Name = d.Name
		eff, loc, _, _, status, _ := s.chooseRead(ctx, d, false)
		_ = eff
		if status == http.StatusForbidden && d.HostID == 0 {
			if _, err := statFile(d.Path); errors.Is(err, fs.ErrNotExist) {
				status = http.StatusNotFound // the roots check cannot resolve a path that is gone; say that, not "not available"
			}
		}
		if status != 0 {
			out.Status, out.Differs, out.Text = classifyUnreadable(d.Name, status, s.onDiskDiffers(d, src))
			blocked = true
			res.Sources = append(res.Sources, out)
			continue
		}
		out.Readable = true
		out.Status, out.Differs, out.Text = classifySource(src, d)
		if loc.HostID != 0 && out.Status == srcUnchanged {
			// a file on a host is not looked at here: its record is compared, the file itself is checked when it is read
			out.Status = srcProbable
			out.Text = fmt.Sprintf("%s: probably unchanged, not verified (the file is on a host and was not looked at here).", d.Name)
		}
		if !loc.Primary && out.Status == srcUnchanged {
			out.Text = strings.TrimSuffix(out.Text, ".") + "; read from a verified copy at another location."
		}
		switch out.Status {
		case srcChanged:
			anyChanged = true
		case srcProbable:
			anyProbable = true
		}
		res.Sources = append(res.Sources, out)
	}
	switch {
	case blocked:
		res.State, res.Prompt = "blocked", promptBlocked
	case anyChanged:
		res.State, res.Prompt = "ask", promptChanged
	case anyProbable:
		res.State, res.Prompt = "ask", promptProbable
	default:
		res.State = "ready"
	}
	return res
}

// redrawCheck is GET /api/figures/{id}/redraw-check.
func (s *Server) redrawCheck(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	f, err := s.Boards.Figure(id)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	writeJSON(w, 200, s.checkRedraw(r.Context(), f))
}

// redrawLink resolves the figure a redraw is made from, and refuses one that cannot be what it says: it must exist, be a figure
// of the same dataset, and carry the same kind of description (a scene for a scene, a channel grid for a grid).
func (s *Server) redrawLink(from int64, datasetID int64, grid bool) (boards.Figure, error) {
	orig, err := s.Boards.Figure(from)
	if errors.Is(err, boards.ErrNotFound) {
		return orig, errors.New("the figure to draw again does not exist")
	}
	if err != nil {
		return orig, err
	}
	kind, _, _, _, _ := figureSources(orig)
	if orig.DatasetID != datasetID || (kind == "channel-grid") != grid || kind == "none" {
		return orig, errors.New("a figure can only be drawn again from a figure of the same dataset and the same kind")
	}
	return orig, nil
}
