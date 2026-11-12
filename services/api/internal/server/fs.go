package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// Path completion: the names in one folder, so that a path field can finish what the researcher typed. It answers
// names and whether each is a folder, never contents, sizes or dates, and never anything outside the one folder asked
// for. It is deliberately not limited to the source roots: choosing a folder to open (a project, a run, a repository
// on a host) needs to see folders that are not readable yet. Reading what is inside a file stays limited to the roots.
// See docs/adr/0031-path-completion-and-file-chooser.md.

const (
	completeMaxEntries = 50
	completeMaxRead    = 2000
	completeBudget     = 200 * time.Millisecond
	completeMaxPrefix  = 4096
)

type completion struct {
	Base      string          `json:"base"`
	Entries   []completeEntry `json:"entries"`
	Truncated bool            `json:"truncated"`
	// GVFS: the folder is inside a file-manager mount of a host that is not registered, so the page can offer to add it.
	// Taken from the mount folder's name only; nothing is read through the mount.
	GVFS *gvfsInfo `json:"gvfs,omitempty"`
}

type completeEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
	// Link and Target mark a symbolic link and say what it points to as written, so the chooser shows it as a link. A completion
	// is deliberately not limited to the source roots (ADR 0031), so this does not say whether the target is readable; opening it
	// as a project or registering a file in it still goes through the roots.
	Link   bool   `json:"link,omitempty"`
	Target string `json:"target,omitempty"`
}

type completeQuery struct {
	prefix string
	kind   string
	exts   []string
}

// parseCompleteQuery reads and checks the query shared by the local and the host route. The prefix may start with
// "~/" (the home folder of whoever lists it) or "/".
func parseCompleteQuery(r *http.Request) (completeQuery, string) {
	q := r.URL.Query()
	c := completeQuery{prefix: q.Get("prefix"), kind: q.Get("kind")}
	if c.kind == "" {
		c.kind = "any"
	}
	if c.kind != "dir" && c.kind != "file" && c.kind != "any" {
		return c, "bad kind"
	}
	if strings.ContainsRune(c.prefix, 0) || len(c.prefix) > completeMaxPrefix {
		return c, "bad prefix"
	}
	if c.prefix != "~" && !strings.HasPrefix(c.prefix, "~/") && !strings.HasPrefix(c.prefix, "/") {
		return c, "the path must be absolute or start with ~/"
	}
	if raw := q.Get("ext"); raw != "" {
		for _, e := range strings.Split(raw, ",") {
			e = strings.ToLower(strings.TrimSpace(e))
			if len(e) < 2 || len(e) > 16 || e[0] != '.' || strings.ContainsAny(e, "/\x00") {
				return c, "bad ext"
			}
			c.exts = append(c.exts, e)
		}
	}
	return c, ""
}

func (s *Server) completeLocal(w http.ResponseWriter, r *http.Request) {
	c, bad := parseCompleteQuery(r)
	if bad != "" {
		fail(w, 400, bad)
		return
	}
	p := c.prefix
	if s.multiUser() { // the group server completes only inside the workspace's data roots (ADR 0050 section 3)
		s.completeInRoots(w, r, p, c)
		return
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || !filepath.IsAbs(home) {
			fail(w, 400, "no home folder")
			return
		}
		rest := strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/")
		p = filepath.Join(home, rest)
		if rest == "" || strings.HasSuffix(c.prefix, "/") {
			p += "/"
		}
	}
	dir, last := splitPrefix(p)
	out, err := listNames(dir, last, c)
	if err != nil {
		// A folder that is not there (a half-typed path) or not readable is an empty answer, not an error: the
		// field keeps working, and the answer does not say which of the two it was.
		out = completion{Base: dir, Entries: []completeEntry{}}
	}
	if _, _, g := s.gvfsHost(out.Base); g != nil {
		out.GVFS = g
	}
	writeJSON(w, 200, out)
}

// completeInRoots is path completion on the group server: no home folder (the server account's is nobody's business), and a
// folder is listed only when its real path lies inside one of the workspace's data roots; anything else is the same empty
// answer as a folder that does not exist, so the answer does not reveal what is outside. A link listed inside a root is only
// labelled, never entered, as in local mode.
func (s *Server) completeInRoots(w http.ResponseWriter, r *http.Request, p string, c completeQuery) {
	dir, last := splitPrefix(p)
	empty := completion{Base: dir, Entries: []completeEntry{}}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || !s.roots(wsOf(r)).ResolveDirOK(real) {
		writeJSON(w, 200, empty)
		return
	}
	out, err := listNames(real, last, c) // the checked real folder, not the typed name a link could be swapped under
	if err != nil {
		out = empty
	}
	writeJSON(w, 200, out)
}

// splitPrefix splits an absolute path as typed into the folder to list and the last component typed so far.
// The folder is cleaned lexically (filepath.Clean), so ".." is resolved on the text, not through links.
func splitPrefix(p string) (dir, last string) {
	if strings.HasSuffix(p, "/") {
		return filepath.Clean(p), ""
	}
	d, l := filepath.Split(p)
	return filepath.Clean(d), l
}

// listNames lists one folder: no recursion, no reading of any file. Dot entries are hidden unless the typed component
// starts with a dot. The match is a case-sensitive prefix, falling back to case-insensitive when nothing matches.
func listNames(dir, last string, c completeQuery) (completion, error) {
	f, err := os.Open(dir)
	if err != nil {
		return completion{}, err
	}
	defer f.Close()
	deadline := time.Now().Add(completeBudget)
	out := completion{Base: dir, Entries: []completeEntry{}}
	var all []os.DirEntry
	for {
		batch, err := f.ReadDir(128)
		all = append(all, batch...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if len(all) == 0 {
				return completion{}, err
			}
			out.Truncated = true
			break
		}
		if len(all) >= completeMaxRead || time.Now().After(deadline) {
			out.Truncated = true
			break
		}
	}
	hidden := strings.HasPrefix(last, ".")
	match := func(fold bool) []os.DirEntry {
		var m []os.DirEntry
		for _, e := range all {
			n := e.Name()
			if !hidden && strings.HasPrefix(n, ".") {
				continue
			}
			if fold && strings.HasPrefix(strings.ToLower(n), strings.ToLower(last)) || !fold && strings.HasPrefix(n, last) {
				m = append(m, e)
			}
		}
		return m
	}
	m := match(false)
	if len(m) == 0 && last != "" {
		m = match(true)
	}
	for _, e := range m {
		isDir := e.IsDir()
		var link bool
		var target string
		if e.Type()&os.ModeSymlink != 0 {
			link = true
			target, _ = os.Readlink(filepath.Join(dir, e.Name())) // the link's own text: nothing is followed
			// A link says what it is only when asked: one stat of the entry itself, never a walk into it.
			if time.Now().After(deadline) {
				out.Truncated = true
			} else if st, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
				isDir = st.IsDir()
			}
		}
		if !isDir && (c.kind == "dir" || !hasExt(e.Name(), c.exts)) {
			continue
		}
		out.Entries = append(out.Entries, completeEntry{Name: e.Name(), Path: filepath.Join(dir, e.Name()), Dir: isDir, Link: link, Target: target})
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return a.Name < b.Name
	})
	if len(out.Entries) > completeMaxEntries {
		out.Entries = out.Entries[:completeMaxEntries]
		out.Truncated = true
	}
	return out, nil
}

func hasExt(name string, exts []string) bool {
	if len(exts) == 0 {
		return true
	}
	l := strings.ToLower(name)
	for _, e := range exts {
		if strings.HasSuffix(l, e) {
			return true
		}
	}
	return false
}

// completeRemote lists names in one folder on a host through its worker. Names only, outside the roots allowed, as
// for the local route; reading file contents on the host stays inside the host's roots.
func (s *Server) completeRemote(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hostFrom(w, r)
	if !ok {
		return
	}
	c, bad := parseCompleteQuery(r)
	if bad != "" {
		fail(w, 400, bad)
		return
	}
	rep, err := s.Remote.Do(r.Context(), h, map[string]any{"op": "complete", "prefix": c.prefix, "kind": c.kind, "ext": c.exts})
	var re *worker.RequestError
	switch {
	case errors.As(err, &re):
		fail(w, 400, "bad request")
		return
	case err != nil:
		fail(w, 502, "the host is not reachable (is the VPN up?)")
		return
	}
	var out completion
	if json.Unmarshal(rep.Head, &out) != nil || out.Entries == nil {
		fail(w, 502, "the host answered unexpectedly")
		return
	}
	writeJSON(w, 200, out) // re-encoded: only the three fields leave, whatever the host sent
}
