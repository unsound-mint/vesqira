package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

// roots is a snapshot of the folders that may be read right now.
func (s *Server) roots() sourceroot.Roots {
	s.rootsMu.RLock()
	defer s.rootsMu.RUnlock()
	return append(sourceroot.Roots(nil), s.Roots...)
}

// neverListed are folders that hold credentials; they are neither shown in the tree nor allowed as a project.
var neverListed = map[string]bool{".ssh": true, ".gnupg": true}

func holdsCredentials(real string) bool {
	for _, part := range strings.Split(real, string(filepath.Separator)) {
		if neverListed[part] {
			return true
		}
	}
	return false
}

// tooBroad refuses folders whose opening would make the whole home directory (or more) readable.
func tooBroad(real string) bool {
	if real == string(filepath.Separator) {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil {
		if h, err := filepath.EvalSymlinks(home); err == nil {
			if rel, err := filepath.Rel(real, h); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return true // real is the home directory or one of its parents
			}
		}
	}
	return false
}

// openProject makes a folder the owner chose readable, like opening a folder in an editor. Owner-only, like every route on
// this listener; it is never mounted on the review listener.
func (s *Server) openProject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil || !filepath.IsAbs(in.Path) {
		writeJSON(w, 400, map[string]string{"error": "give the absolute path of a folder"})
		return
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(in.Path))
	if fi, e := os.Stat(real); err != nil || e != nil || !fi.IsDir() {
		writeJSON(w, 400, map[string]string{"error": "that is not a folder on this computer"})
		return
	}
	if tooBroad(real) || holdsCredentials(real) {
		writeJSON(w, 400, map[string]string{"error": "open a project folder inside your home directory, not the home directory itself, the filesystem root or a credentials folder"})
		return
	}
	s.rootsMu.Lock()
	if !s.Roots.ResolveDirOK(real) {
		s.Roots = append(s.Roots, real)
		if s.RootsFile != "" {
			if f, err := os.OpenFile(s.RootsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				_, _ = f.WriteString(real + "\n")
				_ = f.Close()
			}
		}
	}
	s.rootsMu.Unlock()
	writeJSON(w, 200, map[string]string{"path": real, "name": filepath.Base(real)})
}

type treeEntry struct {
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
	out := make([]treeEntry, 0, len(des))
	for _, d := range des {
		if neverListed[d.Name()] {
			continue
		}
		e := treeEntry{Name: d.Name(), Kind: "file"}
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
