package server

import (
	"context"
	"errors"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
)

// Text files on a host in the editor (ADR 0029, host files). Same routes as the local editor, selected by hostId. The
// host's own scripts do the path checks that need its filesystem (hosts.ReadText, hosts.ReplaceText); this file adds the
// policy that is the same as for local files: inside the host's declared roots, read-only data roles, credential
// folders, text kinds, 2 MiB, and the audit.

// hostTextPolicy is every check that needs no round trip. status 0 is acceptance.
func (s *Server) hostTextPolicy(h hosts.Host, p string) (int, string) {
	if strings.ContainsAny(p, "'!") {
		return 400, "file names containing ' or ! cannot be edited on a host"
	}
	if strings.ContainsAny(p, "\x00\n\r\\") || len(p) > 2000 {
		return 400, "the path contains a NUL, newline, carriage return or backslash, or is too long, which the editor does not send to a host"
	}
	if !path.IsAbs(p) || path.Clean(p) != p || !h.InRoots(p) || holdsCredentials(filepath.FromSlash(p)) {
		return 404, "that file is not inside a folder Vesqira may read on the host"
	}
	if role, ro := s.hostReadOnlyRole(h, p); ro {
		return 403, "that file is " + role + " source data (data/" + role + "), which Vesqira treats as read-only; the editor does not open it"
	}
	return checkTextName(p)
}

// hostReadOnlyRole is readOnlyRole for a path on a host: the path below each declared root of the host and below each
// project folder opened on it (discover.RoleOf, the scanner's own rule).
func (s *Server) hostReadOnlyRole(h hosts.Host, p string) (string, bool) {
	check := func(base string) (string, bool) {
		base = strings.TrimRight(base, "/")
		if !strings.HasPrefix(p, base+"/") {
			return "", false
		}
		role := discover.RoleOf(strings.TrimPrefix(p, base+"/"))
		return role, discover.RoleIsReadOnly(role)
	}
	for _, r := range h.Roots {
		if role, ro := check(r); ro {
			return role, true
		}
	}
	if s.Projects != nil {
		if list, err := s.Projects.List(1000); err == nil {
			for _, pr := range list {
				if pr.HostID == h.ID {
					if role, ro := check(pr.Path); ro {
						return role, true
					}
				}
			}
		}
	}
	return "", false
}

// hostTextStatus says what a refusal or failure of the host's script means for the researcher.
func hostTextStatus(err error) (int, string) {
	var wr *hosts.WriteRefused
	if errors.As(err, &wr) {
		switch wr.Reason {
		case "absent":
			return 404, "that file does not exist on the host"
		case "link", "notdir", "escape", "notfile", "root", "unreadable":
			return 404, "that file is not a plain file inside a folder Vesqira may read on the host (a symbolic link on the way, a folder, or no permission)"
		case "toolarge":
			return 413, "that file is larger than the editor's 2 MiB limit"
		case "mktemp":
			return 403, "the folder is not writable on the host, so nothing was written"
		case "readonly":
			return 403, "the file is read-only on the host, so it is left as it is"
		case "truncated":
			return 502, "the content did not arrive intact (the connection was cut?); nothing was written"
		}
		return 502, wr.Error()
	}
	if errors.Is(err, hosts.ErrEditPath) {
		return 404, "that file is not inside a folder Vesqira may read on the host"
	}
	return 502, err.Error()
}

func (s *Server) hostFor(w http.ResponseWriter, id int64) (hosts.Host, bool) {
	if s.HostDB == nil || s.Remote == nil {
		fail(w, 501, "remote hosts are not configured")
		return hosts.Host{}, false
	}
	h, err := s.HostDB.Get(id)
	if err != nil {
		fail(w, 404, "no such host")
		return hosts.Host{}, false
	}
	return h, true
}

func asHostTextFile(h hosts.Host, p string, f hosts.EditFile) textFile {
	return textFile{Path: p, Name: path.Base(p), Content: string(f.Content), HostID: h.ID, Host: h.Name,
		textVersion: textVersion{Size: f.Size, Mtime: f.Mtime, Sha256: f.Sha256}}
}

func (s *Server) getHostText(w http.ResponseWriter, r *http.Request, rawID string) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		fail(w, 400, "bad host id")
		return
	}
	h, ok := s.hostFor(w, id)
	if !ok {
		return
	}
	p := r.URL.Query().Get("path")
	if st, m := s.hostTextPolicy(h, p); st != 0 {
		fail(w, st, m)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()
	f, err := s.Remote.ReadText(ctx, h, p)
	if err != nil {
		st, m := hostTextStatus(err)
		fail(w, st, m)
		return
	}
	if st, m := checkTextBytes(f.Content); st != 0 {
		fail(w, st, m)
		return
	}
	// hash first, host and path last (the detail is cut at 300 characters); never the content.
	s.Audit.Record("file opened for editing on host", "host", h.ID, "sha256 "+f.Sha256+" "+h.Name+":"+p)
	writeJSON(w, 200, asHostTextFile(h, p, f))
}

func (s *Server) putHostText(w http.ResponseWriter, r *http.Request, in textSave) {
	h, ok := s.hostFor(w, in.HostID)
	if !ok {
		return
	}
	if in.Base.Sha256 == "" {
		fail(w, 400, "the sha256 of the opened file is required")
		return
	}
	if st, m := s.hostTextPolicy(h, in.Path); st != 0 {
		fail(w, st, m)
		return
	}
	mu := s.hostEditLock(h.ID)
	mu.Lock()
	defer mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()
	saved, current, err := s.Remote.ReplaceText(ctx, h, in.Path, []byte(in.Content), in.Base.Sha256)
	if err != nil {
		st, m := hostTextStatus(err)
		fail(w, st, m)
		return
	}
	if current != nil {
		if st, m := checkTextBytes(current.Content); st != 0 { // it is not text any more: nothing to compare
			fail(w, st, m)
			return
		}
		writeJSON(w, 409, textConflict{Error: "the file changed on the host since you opened it; nothing was written", Current: asHostTextFile(h, in.Path, *current)})
		return
	}
	s.Audit.Record("file saved on host", "host", h.ID, "sha256 "+in.Base.Sha256+" -> "+saved.Sha256+" "+h.Name+":"+in.Path)
	writeJSON(w, 200, asHostTextFile(h, in.Path, saved))
}

// hostEditLock is the lock for one host: saves to different hosts do not wait for each other.
func (s *Server) hostEditLock(id int64) *sync.Mutex {
	s.hostEditMu.Lock()
	defer s.hostEditMu.Unlock()
	if s.hostEditLocks == nil {
		s.hostEditLocks = map[int64]*sync.Mutex{}
	}
	if s.hostEditLocks[id] == nil {
		s.hostEditLocks[id] = &sync.Mutex{}
	}
	return s.hostEditLocks[id]
}
