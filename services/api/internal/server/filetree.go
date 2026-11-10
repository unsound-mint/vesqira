package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The plain folder listing behind the desktop file tree: names only, inside the readable roots. It is not the project
// tree (GET /api/projects/{id}/tree), which groups what discovery found; this one shows the folder as it is on disk.

// neverListed are folders that hold credentials; they are neither shown in the tree nor listed.
var neverListed = map[string]bool{".ssh": true, ".gnupg": true}

func holdsCredentials(real string) bool {
	for _, part := range strings.Split(real, string(filepath.Separator)) {
		if neverListed[part] {
			return true
		}
	}
	return false
}

type dirEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // dir, file, link (a symlink, never followed from here)
	Size int64  `json:"size,omitempty"`
}

const maxTreeEntries = 5000

// fileTree lists one folder inside the readable roots: folders first, then files, by name. It lists names only.
func (s *Server) fileTree(w http.ResponseWriter, r *http.Request) {
	dir, err := s.roots().ResolveDir(r.URL.Query().Get("path"))
	if err != nil || holdsCredentials(dir) {
		writeJSON(w, 404, map[string]string{"error": "that folder is not inside a folder Vesqira may read"})
		return
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": "could not read that folder"})
		return
	}
	out := make([]dirEntry, 0, len(des))
	for _, d := range des {
		if neverListed[d.Name()] {
			continue
		}
		e := dirEntry{Name: d.Name(), Kind: "file"}
		switch {
		case d.Type()&os.ModeSymlink != 0:
			e.Kind = "link"
		case d.IsDir():
			e.Kind = "dir"
		default:
			if fi, err := d.Info(); err == nil {
				e.Size = fi.Size()
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Kind == "dir") != (out[j].Kind == "dir") {
			return out[i].Kind == "dir"
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	truncated := len(out) > maxTreeEntries
	if truncated {
		out = out[:maxTreeEntries]
	}
	writeJSON(w, 200, map[string]any{"path": dir, "entries": out, "truncated": truncated})
}
