package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	idty "github.com/unsound-mint/vesqira/services/api/internal/identity"
)

// "A newer result is available" (ADR 0053). Nothing here swaps a dataset or redraws a figure: it only says that a newer file
// exists, or that the file behind a dataset or a figure is no longer what was registered, and what differs.

// Newer is one such notice. Reasons: newer_file (a run's output folder gained a later file of the same role, such as the
// psi_<stem>_1.fits a re-run writes instead of overwriting), changed_in_place (the registered file's size or time differs and
// its bytes may differ) and figure_data_changed (the dataset behind a figure is not the bytes the figure was drawn from).
type Newer struct {
	Reason string `json:"reason"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	// DatasetID is the dataset the notice is about when it is a dataset that exists (a figure's source, or an already registered
	// newer file); 0 when the newer file is not registered.
	DatasetID  int64     `json:"datasetId"`
	SizeBytes  int64     `json:"sizeBytes"`
	ModifiedAt time.Time `json:"modifiedAt"`
	// Differs lists what differs from what was registered or drawn: size, time and fingerprint. "fingerprint" is said only
	// when both values were computed and they differ; a missing value is never read as a match or a difference.
	Differs []string `json:"differs"`
}

var runSuffix = regexp.MustCompile(`^(.*)_(\d+)$`)

// splitRunSuffix reads the "_<n>" a re-run appends: "psi_x_2" is ("psi_x", 2), "psi_x" is ("psi_x", 0).
func splitRunSuffix(stem string) (string, int) {
	if m := runSuffix.FindStringSubmatch(stem); m != nil {
		if n, err := strconv.Atoi(m[2]); err == nil {
			return m[1], n
		}
	}
	return stem, 0
}

// newerSibling finds, among the entries of a run's output folder, the file named like name with the highest "_<n>" greater
// than name's own (same extension, a regular file).
func newerSibling(name string, entries []fs.DirEntry) (string, bool) {
	ext := filepath.Ext(name)
	base, own := splitRunSuffix(strings.TrimSuffix(name, ext))
	best, bestN := "", own
	for _, e := range entries {
		if !e.Type().IsRegular() || filepath.Ext(e.Name()) != ext {
			continue
		}
		b, n := splitRunSuffix(strings.TrimSuffix(e.Name(), ext))
		if b == base && n > bestN {
			best, bestN = e.Name(), n
		}
	}
	return best, best != ""
}

// sampledOf is the sampled fingerprint of a file as it is now ("" when it cannot be read). It reads about 1 MiB and is only asked
// for files that already differ from what was registered, so nothing is remembered between requests: a remembered value
// could outlive the bytes it was computed from.
func (s *Server) sampledOf(real string) string {
	v, err := idty.Sampled(real)
	if err != nil {
		return ""
	}
	return v
}

// differs says what differs between two states of a file.
func differs(sizeA int64, modA time.Time, fpA string, sizeB int64, modB time.Time, fpB string) []string {
	out := []string{}
	if sizeA != sizeB {
		out = append(out, "size")
	}
	if modA.UnixNano() != modB.UnixNano() {
		out = append(out, "time")
	}
	if fpA != "" && fpB != "" && fpA != fpB {
		out = append(out, "fingerprint")
	}
	return out
}

func storedSampled(d catalog.Dataset) string {
	if p, ok := primaryOf(d.Locations); ok && p.Sampled != nil {
		return p.Sampled.Value
	}
	return ""
}

// newerResults is GET /api/datasets/newer: notices for datasets and figures. Only files on this computer are looked at
// (looking on a host would wait on the network, as availability does not).
func (s *Server) newerResults(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Store.List(wsOf(r))
	if err != nil {
		log.Printf("newer: %v", err)
		fail(w, 500, "internal error")
		return
	}
	roles := map[int64]string{}
	if s.Runs != nil {
		if roles, err = s.Runs.OutputRoles(wsOf(r)); err != nil {
			log.Printf("newer: %v", err)
			fail(w, 500, "internal error")
			return
		}
	}
	byID, byPath := map[int64]catalog.Dataset{}, map[string]catalog.Dataset{}
	for _, d := range ds {
		byID[d.ID] = d
		if d.HostID == 0 {
			byPath[d.Path] = d
		}
	}
	dsNotes := map[int64][]Newer{}
	dirs := map[string][]fs.DirEntry{}
	for _, d := range ds {
		if d.Archived || d.HostID != 0 {
			continue
		}
		real, err := s.roots(wsOf(r)).Resolve(d.Path)
		if err != nil {
			continue
		}
		info, err := statFile(real)
		if err != nil {
			continue // gone: availability reports that
		}
		if info.Size() != d.SizeBytes || info.ModTime().UnixNano() != d.ModifiedAt.UnixNano() {
			dsNotes[d.ID] = append(dsNotes[d.ID], Newer{Reason: "changed_in_place", Name: d.Name, Path: d.Path, DatasetID: d.ID, SizeBytes: info.Size(), ModifiedAt: info.ModTime().UTC(),
				Differs: differs(d.SizeBytes, d.ModifiedAt, storedSampled(d), info.Size(), info.ModTime(), s.sampledOf(real))})
		}
		if _, isOutput := roles[d.ID]; !isOutput {
			continue
		}
		dir := filepath.Dir(real)
		entries, seen := dirs[dir]
		if !seen {
			entries, _ = os.ReadDir(dir)
			dirs[dir] = entries
		}
		name, found := newerSibling(filepath.Base(real), entries)
		if !found {
			continue
		}
		creal, err := s.roots(wsOf(r)).Resolve(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		cinfo, err := statFile(creal)
		if err != nil {
			continue
		}
		n := Newer{Reason: "newer_file", Name: name, Path: creal, SizeBytes: cinfo.Size(), ModifiedAt: cinfo.ModTime().UTC()}
		if reg, ok := byPath[creal]; ok {
			n.DatasetID = reg.ID
		}
		n.Differs = differs(d.SizeBytes, d.ModifiedAt, storedSampled(d), cinfo.Size(), cinfo.ModTime(), s.sampledOf(creal))
		dsNotes[d.ID] = append(dsNotes[d.ID], n)
	}
	figNotes := map[int64][]Newer{}
	if s.Boards != nil {
		figs, err := s.Boards.Figures(wsOf(r))
		if err != nil {
			log.Printf("newer: %v", err)
			fail(w, 500, "internal error")
			return
		}
		for _, f := range figs {
			var id figureIdentity
			if f.Archived || len(f.Identity) == 0 || json.Unmarshal(f.Identity, &id) != nil {
				continue
			}
			for _, src := range id.Datasets {
				d, ok := byID[src.DatasetID]
				if !ok || src.Role != "source" {
					continue
				}
				if diff := figureDiffers(src, d); len(diff) > 0 {
					figNotes[f.ID] = append(figNotes[f.ID], Newer{Reason: "figure_data_changed", Name: d.Name, Path: "", DatasetID: d.ID, SizeBytes: d.SizeBytes, ModifiedAt: d.ModifiedAt, Differs: diff})
				}
				if more := dsNotes[d.ID]; len(more) > 0 {
					figNotes[f.ID] = append(figNotes[f.ID], more...)
				}
			}
		}
	}
	keyed := func(m map[int64][]Newer) map[string][]Newer {
		out := map[string][]Newer{}
		for id, v := range m {
			out[strconv.FormatInt(id, 10)] = v
		}
		return out
	}
	writeJSON(w, 200, map[string]any{"datasets": keyed(dsNotes), "figures": keyed(figNotes), "note": "remote files are not checked here"})
}

// ---- what a figure records about the bytes it was drawn from ----

type identityValue struct {
	Value     string    `json:"value"`
	Algorithm string    `json:"algorithm,omitempty"`
	Covers    string    `json:"covers,omitempty"`
	At        time.Time `json:"at"`
	By        string    `json:"by"`
}

type figureDatasetIdentity struct {
	Role       string         `json:"role"` // source | overlay | reliability
	DatasetID  int64          `json:"datasetId"`
	Name       string         `json:"name"`
	SizeBytes  int64          `json:"sizeBytes"`
	ModifiedAt time.Time      `json:"modifiedAt"`
	Sampled    *identityValue `json:"sampledFingerprint"` // null: not computed
	Full       *identityValue `json:"fullSha256"`         // null: not computed
	// Declarations are the declared unit, orientation interpretation and catalog declaration in force when the figure was
	// saved (ADR 0042, section 5). Absent on figures saved before they were recorded.
	Declarations *figureDeclarations `json:"declarations,omitempty"`
}

// figureIdentity is the owner-side record of each dataset's identity when a figure is saved (figures.identity). "Draw again"
// (a later slice) compares with it; today it only feeds the notices above. Reviewer-safe provenance is unchanged.
type figureIdentity struct {
	Schema   string                  `json:"schema"`
	Datasets []figureDatasetIdentity `json:"datasets"`
	// RedrawnFrom is set on a figure made by "draw again": the figure it redraws. The original is never changed; this is the
	// only link between them (ADR 0042, section 5).
	RedrawnFrom *redrawnFrom `json:"redrawnFrom,omitempty"`
}

type redrawnFrom struct {
	FigureID int64  `json:"figureId"`
	Revision string `json:"revision"`
}

func figureDiffers(src figureDatasetIdentity, d catalog.Dataset) []string {
	cur := catalog.Location{}
	if p, ok := primaryOf(d.Locations); ok {
		cur = p
	}
	fpA, fpB := "", ""
	switch {
	case src.Full != nil && cur.Full != nil:
		fpA, fpB = src.Full.Value, cur.Full.Value
	case src.Sampled != nil && cur.Sampled != nil:
		fpA, fpB = src.Sampled.Value, cur.Sampled.Value
	}
	return differs(src.SizeBytes, src.ModifiedAt, fpA, d.SizeBytes, d.ModifiedAt, fpB)
}

func (s *Server) buildFigureIdentity(ctx context.Context, source catalog.Dataset, overlays, reliability []catalog.Dataset, from *redrawnFrom) json.RawMessage {
	doc := figureIdentity{Schema: "figure-identity/1", RedrawnFrom: from}
	add := func(role string, d catalog.Dataset) {
		locs, err := s.Store.Locations(wsCtx(ctx), d.ID)
		if err != nil {
			return
		}
		p, _ := primaryOf(locs)
		e := figureDatasetIdentity{Role: role, DatasetID: d.ID, Name: d.Name, SizeBytes: d.SizeBytes, ModifiedAt: d.ModifiedAt.UTC(), Declarations: declarationsOf(d)}
		if p.Sampled != nil {
			e.Sampled = &identityValue{Value: p.Sampled.Value, Algorithm: idty.SampledAlgorithm, Covers: idty.Covers, At: p.Sampled.At, By: p.Sampled.By}
		}
		if p.Full != nil {
			e.Full = &identityValue{Value: p.Full.Value, At: p.Full.At, By: p.Full.By}
		}
		doc.Datasets = append(doc.Datasets, e)
	}
	add("source", source)
	for _, o := range overlays {
		add("overlay", o)
	}
	for _, r := range reliability {
		add("reliability", r)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	return b
}
