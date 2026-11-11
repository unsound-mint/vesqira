package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/lifecycle"
)

// Archive, restore and delete for datasets, runs, figures and boards (ADR 0041). Deleting an entry forgets it in the
// catalog; it never removes a source file. Bytes Vesqira wrote itself (a managed copy, an analysis result) are removed
// only when the request says so separately.

// archivedMode reads ?archived=: "" (default) hides archived entries, "all" shows both, "only" shows just the archived.
func archivedMode(r *http.Request) string {
	switch m := r.URL.Query().Get("archived"); m {
	case "all", "only":
		return m
	}
	return ""
}

func keepArchived(mode string, archived bool) bool {
	return mode == "all" || (mode == "only") == archived
}

// ownedBytes says when a dataset's file is one Vesqira wrote under its own data directory.
type ownedBytes struct {
	Kind      string `json:"kind"` // "managed copy" | "analysis result"
	Name      string `json:"name"`
	SizeBytes int64  `json:"sizeBytes"`
	path      string
}

// owned reports the file of d when it lies inside the managed-copy or analysis-result folder; never for a file on a
// host, and never for a file merely registered from elsewhere.
func (s *Server) owned(d catalog.Dataset) *ownedBytes {
	if d.HostID != 0 || d.Path == "" {
		return nil
	}
	real, err := filepath.EvalSymlinks(d.Path)
	if err != nil {
		return nil
	}
	dirs := []struct{ kind, dir string }{{"managed copy", s.ManagedDir}}
	if s.Analysis != nil {
		dirs = append(dirs, struct{ kind, dir string }{"analysis result", s.Analysis.DerivedDir})
	}
	for _, c := range dirs {
		if c.dir == "" {
			continue
		}
		root, err := filepath.EvalSymlinks(c.dir)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(real, root+string(filepath.Separator)) {
			continue
		}
		info, err := os.Lstat(real)
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		return &ownedBytes{Kind: c.kind, Name: filepath.Base(real), SizeBytes: info.Size(), path: real}
	}
	return nil
}

func (s *Server) lc() *lifecycle.Service { return lifecycle.New(s.Store.DB()) }

type impactView struct {
	lifecycle.Impact
	// FileNote is said to the person about the entry's files; it is true of every entry kind that has one.
	FileNote string `json:"fileNote"`
	// OwnedBytes is set when the dataset's file is one Vesqira wrote: the delete then needs an explicit choice.
	OwnedBytes *ownedBytes `json:"ownedBytes"`
}

func (s *Server) impactOf(kind string, id int64) (impactView, int, string) {
	im, err := s.lc().Impact(kind, id)
	if errors.Is(err, lifecycle.ErrNotFound) {
		return impactView{}, 404, kind + " not found"
	}
	if err != nil {
		log.Printf("impact: %v", err)
		return impactView{}, 500, "internal error"
	}
	v := impactView{Impact: im}
	switch kind {
	case lifecycle.KindDataset:
		v.FileNote = "The file on disk is not deleted."
		if d, err := s.Store.Get(id); err == nil {
			v.OwnedBytes = s.owned(d)
			if v.OwnedBytes != nil {
				v.FileNote = "The original this was made from is not touched."
			}
		}
	case lifecycle.KindRun:
		v.FileNote = "The run folder on disk is not deleted."
	}
	return v, 0, ""
}

func (s *Server) lifecycleImpact(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathInt(w, r, "id")
		if !ok {
			return
		}
		v, code, msg := s.impactOf(kind, id)
		if code != 0 {
			fail(w, code, msg)
			return
		}
		writeJSON(w, 200, v)
	}
}

func (s *Server) lifecycleArchive(kind string, archive bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathInt(w, r, "id")
		if !ok {
			return
		}
		var changed bool
		var err error
		if archive {
			changed, err = s.lc().Archive(kind, id)
		} else {
			changed, err = s.lc().Restore(kind, id)
		}
		if errors.Is(err, lifecycle.ErrNotFound) {
			fail(w, 404, kind+" not found")
			return
		}
		if err != nil {
			log.Printf("archive: %v", err)
			fail(w, 500, "internal error")
			return
		}
		v, _, _ := s.impactOf(kind, id)
		if changed {
			action := "restored"
			if archive {
				action = "archived"
			}
			s.Audit.Record(action, kind, id, fmt.Sprintf("%s %q", action, v.Name))
		}
		writeJSON(w, 200, map[string]any{"kind": kind, "id": id, "archived": archive, "name": v.Name})
	}
}

func (s *Server) lifecycleDelete(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathInt(w, r, "id")
		if !ok {
			return
		}
		var in struct {
			ImpactDigest string `json:"impactDigest"`
			OwnedBytes   string `json:"ownedBytes"` // "keep" | "remove"; required for a file Vesqira wrote
		}
		if !body(w, r, 4<<10, &in, true) {
			return
		}
		if in.ImpactDigest == "" {
			fail(w, 400, "send the impactDigest of the preview you looked at")
			return
		}
		pre, code, msg := s.impactOf(kind, id)
		if code != 0 {
			fail(w, code, msg)
			return
		}
		if pre.OwnedBytes != nil && in.OwnedBytes != "keep" && in.OwnedBytes != "remove" {
			fail(w, 400, fmt.Sprintf("this dataset is a %s that Vesqira wrote: say separately whether its bytes are removed (ownedBytes: \"remove\") or kept (\"keep\")", pre.OwnedBytes.Kind))
			return
		}
		if pre.OwnedBytes == nil && in.OwnedBytes == "remove" {
			fail(w, 400, "only a file Vesqira wrote itself can be removed; a registered file is never deleted")
			return
		}
		res, err := s.lc().Delete(kind, id, in.ImpactDigest)
		var changed *lifecycle.ChangedError
		var blocked *lifecycle.BlockedError
		switch {
		case errors.As(err, &changed):
			v, _, _ := s.impactOf(kind, id)
			writeJSON(w, 409, map[string]any{"error": "What depends on this changed since you looked. Review the new impact and confirm again.", "impact": v})
			return
		case errors.As(err, &blocked):
			v, _, _ := s.impactOf(kind, id)
			writeJSON(w, 409, map[string]any{"error": blocked.Error(), "impact": v})
			return
		case errors.Is(err, lifecycle.ErrNotFound):
			fail(w, 404, kind+" not found")
			return
		case err != nil:
			log.Printf("delete %s %d: %v", kind, id, err)
			fail(w, 500, "internal error")
			return
		}
		detail := fmt.Sprintf("deleted %q from the catalog; no file was deleted", res.Impact.Name)
		bytesState := "none"
		if pre.OwnedBytes != nil {
			bytesState = "kept"
			if in.OwnedBytes == "remove" {
				if err := os.Remove(pre.OwnedBytes.path); err != nil {
					log.Printf("delete %s %d: removing %s: %v", kind, id, pre.OwnedBytes.Name, err)
					bytesState = "failed"
					detail = fmt.Sprintf("deleted %q from the catalog; its %s bytes could not be removed", res.Impact.Name, pre.OwnedBytes.Kind)
				} else {
					bytesState = "removed"
					detail = fmt.Sprintf("deleted %q from the catalog and removed its %s bytes (%d bytes); the original was not touched", res.Impact.Name, pre.OwnedBytes.Kind, pre.OwnedBytes.SizeBytes)
				}
			} else {
				detail = fmt.Sprintf("deleted %q from the catalog; its %s bytes were kept on disk", res.Impact.Name, pre.OwnedBytes.Kind)
			}
		}
		if kind == lifecycle.KindDataset {
			s.Offline.RemoveFiles(id) // the rows went with the dataset; this is the offline copy's files (ADR 0059)
		}
		if res.OrphanPreview != "" && s.Boards != nil {
			if err := s.Boards.RemovePreview(res.OrphanPreview); err != nil {
				log.Printf("delete figure %d: removing preview: %v", id, err)
			}
		}
		s.Audit.Record("deleted", kind, id, detail)
		writeJSON(w, 200, map[string]any{"deleted": true, "kind": kind, "id": id, "ownedBytes": bytesState})
	}
}
