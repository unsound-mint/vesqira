package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

// The plain folder listing behind the desktop file tree: names only, inside the readable roots. It is not the project
// tree (GET /api/projects/{id}/tree), which groups what discovery found; this one shows the folder as it is on disk.

// neverListed are folders that hold credentials, and the folder where an unfinished transfer waits; they are neither shown in the tree nor listed.
var neverListed = map[string]bool{".ssh": true, ".gnupg": true, hosts.PartialDir: true} // the last is where an unfinished transfer waits (ADR 0051)

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
	// Target is what a link says it points to, as written (readlink); Points says where that lands once resolved: "inside" a
	// readable root (the link is usable, and still shown as a link), "outside" them (it is not usable), or "broken". Both are empty
	// for anything that is not a link, and Points is empty for the links past linkBudget.
	Target string `json:"target,omitempty"`
	Points string `json:"points,omitempty"`
}

// linkBudget bounds how many links of one folder are resolved to say where they point; the rest keep their Target only.
const linkBudget = 500

const maxTreeEntries = 5000

// linkPoints says where the link `name` in the folder `dir` lands: "inside" when its fully resolved target is a readable root or
// below one, "outside" when it is anywhere else, "broken" when it resolves to nothing. It only resolves and compares names; no
// content is read and nothing is entered. The label is for display: using the target still goes through Roots.Resolve and OpenFile.
func linkPoints(roots sourceroot.Roots, dir, name string) string {
	real, err := filepath.EvalSymlinks(filepath.Join(dir, name))
	switch {
	case err != nil:
		return "broken"
	case roots.ResolveDirOK(real) && !holdsCredentials(real):
		return "inside"
	default:
		return "outside"
	}
}

// fileTree lists one folder inside the readable roots: folders first, then files, by name. It lists names only.
func (s *Server) fileTree(w http.ResponseWriter, r *http.Request) {
	dir, err := s.roots().ResolveDir(r.URL.Query().Get("path"))
	if err != nil || holdsCredentials(dir) {
		writeJSON(w, 404, map[string]string{"error": "that folder is not inside a folder Vesqira may read"})
		return
	}
	// The folder is opened once, without following a link, and everything below is read through that descriptor: a folder swapped
	// for a link after the check above is refused, and an entry that is a link is read (readlink) and never entered (ADR 0050 section 8).
	f, err := sourceroot.OpenDir(dir)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "that folder is not inside a folder Vesqira may read"})
		return
	}
	defer f.Close()
	des, err := f.ReadDir(-1)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": "could not read that folder"})
		return
	}
	fd := int(f.Fd())
	roots := s.roots()
	links := 0
	out := make([]dirEntry, 0, len(des))
	for _, d := range des {
		if neverListed[d.Name()] {
			continue
		}
		e := dirEntry{Name: d.Name(), Kind: "file"}
		switch {
		case d.Type()&os.ModeSymlink != 0:
			e.Kind = "link"
			buf := make([]byte, 4096)
			if n, err := unix.Readlinkat(fd, d.Name(), buf); err == nil && n < len(buf) {
				e.Target = string(buf[:n])
			}
			if links++; links <= linkBudget {
				e.Points = linkPoints(roots, dir, d.Name())
			}
		case d.IsDir():
			e.Kind = "dir"
		default:
			var st unix.Stat_t
			if unix.Fstatat(fd, d.Name(), &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
				e.Size = st.Size
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
