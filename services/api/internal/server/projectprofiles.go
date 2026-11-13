package server

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Tool profiles a project folder brings (ADR 0068, slice 6). They apply to that project's folder only: a run whose folder is inside a
// project is read with the program's profiles plus the ones that project's .vesqira/profiles/ holds, and nothing else sees them. They
// are read when a project is scanned, previewed or applied, and again when a run is imported by path from inside the project, so an
// edit to a profile file is seen at the next of those and never in the middle of a request. Projects on a host are not read: the
// profile files of a remote folder are not fetched.
type projectProfiles struct {
	mu     sync.RWMutex
	layers map[int64]*projectLayer
}

type projectLayer struct {
	ws     workspace.ID
	root   string // canonical project folder
	reg    *runs.Registry
	status []discover.ProfileStatus
}

// loadProjectProfiles reads the project's profile files through fsys (the project's own folder), replaces the project's layer and
// returns the registry for scanning and what each file gave. The project's layer is dropped when the folder holds no profile file.
func (s *Server) loadProjectProfiles(ws workspace.ID, p projects.Project, fsys discover.FS) (*runs.Registry, []discover.ProfileStatus) {
	base := s.profiles()
	if p.HostID != 0 {
		return base, nil
	}
	files := discover.ReadProfileFiles(fsys)
	s.projProfiles.mu.Lock()
	defer s.projProfiles.mu.Unlock()
	if len(files) == 0 {
		delete(s.projProfiles.layers, p.ID)
		return base, nil
	}
	reg, status := discover.ProjectRegistry(base, files)
	if s.projProfiles.layers == nil {
		s.projProfiles.layers = map[int64]*projectLayer{}
	}
	s.projProfiles.layers[p.ID] = &projectLayer{ws: ws, root: p.Path, reg: reg, status: status}
	return reg, status
}

// profilesAt is the registry for something at a path on this computer (a run folder, a dataset): the program's profiles, plus the
// profiles of the project whose folder holds the path (the innermost one when projects nest).
func (s *Server) profilesAt(ws workspace.ID, path string) *runs.Registry {
	if path == "" || s.Projects == nil {
		return s.profiles()
	}
	path = filepath.Clean(path)
	s.projProfiles.mu.RLock()
	var best *projectLayer
	for _, l := range s.projProfiles.layers {
		if l.ws == ws && inside(l.root, path) && (best == nil || len(l.root) > len(best.root)) {
			best = l
		}
	}
	s.projProfiles.mu.RUnlock()
	if best != nil {
		return best.reg
	}
	return s.profiles()
}

// refreshProfilesAt loads the profiles of the project that holds path when none are cached for it, or always when fresh is set
// (an import by path from inside a project must see the file as it is now).
func (s *Server) refreshProfilesAt(ws workspace.ID, path string, fresh bool) *runs.Registry {
	if s.Projects == nil || path == "" {
		return s.profiles()
	}
	path = filepath.Clean(path)
	if !fresh {
		if reg := s.profilesAt(ws, path); reg != s.profiles() {
			return reg
		}
	}
	list, err := s.Projects.List(ws, 500)
	if err != nil {
		return s.profiles()
	}
	var best *projects.Project
	for i := range list {
		if p := list[i]; p.HostID == 0 && inside(p.Path, path) && (best == nil || len(p.Path) > len(best.Path)) {
			best = &list[i]
		}
	}
	if best == nil {
		return s.profiles()
	}
	reg, _ := s.loadProjectProfiles(ws, *best, discover.LocalFS{Root: best.Path})
	return reg
}

func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
