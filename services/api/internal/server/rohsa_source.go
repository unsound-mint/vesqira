package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// rohsaSource is where one ROHSA fit lives: a folder under a source root on this machine, or a folder on a host. The import
// reads the fit's small records and registers its FITS files through it and never touches the .dat itself, so one importer
// serves both (ADR 0046). Everything a source reads is bounded and inside the roots that machine enforces.
type rohsaSource interface {
	HostID() int64 // 0 on this machine
	Dat() string   // the .dat's real path on its machine
	Key() string   // the run's key: the .dat's path, host-qualified for a host
	// Near reads a small text record beside the .dat; nil when it is absent, unreadable or too large.
	Near(ctx context.Context, name string) []byte
	// Register registers a FITS file given relative to the .dat's folder. found is false when it is not there or not
	// available (outside the roots, a link out): such a file is skipped, never invented.
	Register(ctx context.Context, rel string) (d catalog.Dataset, found bool, code int, msg string)
	// Fits lists the *.fits names in a sub-folder (relative to the .dat's folder), sorted; nil when there is none.
	Fits(ctx context.Context, rel string) []string
	// Failed says the machine stopped answering (or the request was cancelled) at some point of the reads above. A file that
	// is not there is an ordinary answer and a note; a silence is not, and the import must not save a run built from it.
	Failed() (code int, msg string)
}

type localROHSA struct {
	s   *Server
	dat string
}

func (l localROHSA) Failed() (int, string) { return 0, "" }
func (l localROHSA) HostID() int64         { return 0 }
func (l localROHSA) Dat() string           { return l.dat }
func (l localROHSA) Key() string           { return l.dat }
func (l localROHSA) Near(ctx context.Context, name string) []byte {
	return l.s.readNear(wsCtx(ctx), filepath.Dir(l.dat), name)
}
func (l localROHSA) Register(ctx context.Context, rel string) (catalog.Dataset, bool, int, string) {
	real, err := l.s.roots(wsCtx(ctx)).Resolve(filepath.Join(filepath.Dir(l.dat), filepath.FromSlash(rel)))
	if err != nil {
		return catalog.Dataset{}, false, 0, ""
	}
	d, code, msg := l.s.registerPath(ctx, real)
	return d, true, code, msg
}
func (l localROHSA) Fits(ctx context.Context, rel string) []string {
	pdir, err := l.s.roots(wsCtx(ctx)).ResolveDir(filepath.Join(filepath.Dir(l.dat), filepath.FromSlash(rel)))
	if err != nil {
		return nil
	}
	ents, err := os.ReadDir(pdir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".fits") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// remoteROHSA is a fit on a host. Its records are read and its FITS files registered by the host's worker, which enforces
// the host's roots on the resolved path, exactly as for any other remote file.
type remoteROHSA struct {
	s    *Server
	h    hosts.Host
	dat  string
	down *hostSilence // shared by the copies of this value; nil means the caller does not ask
}

// hostSilence remembers that the host did not answer during an import (ssh dropped, the deadline passed, the caller left).
type hostSilence struct {
	mu   sync.Mutex
	code int
	msg  string
}

func (h *hostSilence) note(code int, msg string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.code == 0 {
		h.code, h.msg = code, msg
	}
	h.mu.Unlock()
}

func (r remoteROHSA) Failed() (int, string) {
	if r.down == nil {
		return 0, ""
	}
	r.down.mu.Lock()
	defer r.down.mu.Unlock()
	return r.down.code, r.down.msg
}

func newRemoteROHSA(s *Server, h hosts.Host, dat string) remoteROHSA {
	return remoteROHSA{s: s, h: h, dat: dat, down: &hostSilence{}}
}

func (r remoteROHSA) HostID() int64 { return r.h.ID }
func (r remoteROHSA) Dat() string   { return r.dat }
func (r remoteROHSA) Key() string   { return hostKey(r.h.ID, r.dat) }
func (r remoteROHSA) Near(ctx context.Context, name string) []byte {
	text, code, msg := r.s.remoteText(ctx, r.h, path.Join(path.Dir(r.dat), name))
	if code == 502 || ctx.Err() != nil {
		r.down.note(502, msg)
	}
	if code != 0 || len(text) > runs.MaxMetadataBytes {
		return nil
	}
	return []byte(text)
}
func (r remoteROHSA) Register(ctx context.Context, rel string) (catalog.Dataset, bool, int, string) {
	d, code, msg := r.s.registerRemoteFile(ctx, r.h, path.Join(path.Dir(r.dat), rel))
	if code == 502 || ctx.Err() != nil {
		r.down.note(502, msg)
	}
	if code == 403 {
		return catalog.Dataset{}, false, 0, "" // not available there: skipped
	}
	return d, true, code, msg
}
func (r remoteROHSA) Fits(ctx context.Context, rel string) []string {
	_, files, _, err := r.s.remoteList(ctx, r.h, path.Join(path.Dir(r.dat), rel))
	if silent(err) || ctx.Err() != nil {
		r.down.note(502, "the host is not reachable (is the VPN up?)")
	}
	var names []string
	for _, f := range files {
		if strings.HasSuffix(f, ".fits") {
			names = append(names, f)
		}
	}
	sort.Strings(names)
	return names
}

// silent says whether an error from the host's worker means the host did not answer, as opposed to the worker refusing the
// request (a folder that is absent, outside the roots or not a folder: the same refusal for all of them).
func silent(err error) bool {
	var re *worker.RequestError
	return err != nil && !errors.As(err, &re)
}

// remoteList lists one folder on a host through its worker: the folder's real path, its file names and its sub-folder names.
// A folder that is absent, outside the roots or not a folder is one answer (empty real, nil error); the host not answering is
// an error.
func (s *Server) remoteList(ctx context.Context, h hosts.Host, dir string) (real string, files []string, dirs []string, err error) {
	if !h.InRoots(dir) {
		return "", nil, nil, nil
	}
	rep, err := s.Remote.Do(ctx, h, map[string]any{"op": "list", "path": dir})
	if silent(err) {
		return "", nil, nil, err
	}
	if err != nil {
		return "", nil, nil, nil
	}
	var l struct {
		Path    string `json:"path"`
		Entries []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"entries"`
	}
	if json.Unmarshal(rep.Head, &l) != nil {
		return "", nil, nil, nil
	}
	for _, e := range l.Entries {
		switch e.Kind {
		case "file":
			files = append(files, e.Name)
		case "dir":
			dirs = append(dirs, e.Name)
		}
	}
	return l.Path, files, dirs, nil
}

// Where a job's fit may be below its output folder: the pipeline writes <output>/<region>/rohsa_<label>.dat, and the search
// goes a little deeper than that rather than guess the layout; it stops going down at a folder that holds a fit.
const (
	rohsaSearchDepth = 3
	rohsaSearchDirs  = 40
)

// findRemoteROHSA looks for rohsa_*.dat files at and below dir on a host, in breadth, within the bounds above. It reports
// each as the real path on the host. It never lists more than rohsaSearchDirs folders, so a huge tree costs a bounded number
// of calls; `more` is true when the bound stopped it before the tree was exhausted.
func (s *Server) findRemoteROHSA(ctx context.Context, h hosts.Host, dir string) (dats []string, more bool, err error) {
	type item struct {
		dir   string
		depth int
	}
	queue := []item{{dir, 0}}
	listed := 0
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		if listed >= rohsaSearchDirs {
			return dats, true, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		listed++
		real, files, subs, err := s.remoteList(ctx, h, it.dir)
		if err != nil {
			return nil, false, err // the host stopped answering: not "no fit here"
		}
		if real == "" {
			if it.depth == 0 {
				return nil, false, errors.New("not available")
			}
			continue
		}
		found := false
		sort.Strings(files)
		for _, f := range files {
			if rohsaDat.MatchString(f) {
				dats, found = append(dats, path.Join(real, f)), true
			}
		}
		if found || it.depth >= rohsaSearchDepth {
			continue
		}
		sort.Strings(subs)
		for _, d := range subs {
			if !strings.HasPrefix(d, ".") {
				queue = append(queue, item{path.Join(real, d), it.depth + 1})
			}
		}
	}
	return dats, false, nil
}

// rohsaTargetRemote decides which .dat an import path on a host names: the .dat itself, or the one fit found in the folder
// (or just below it). Several fits are refused with their names, none with the reason; nothing is guessed.
func (s *Server) rohsaTargetRemote(ctx context.Context, h hosts.Host, p string) (dat string, code int, msg string) {
	if !h.InRoots(p) {
		return "", 403, "not available"
	}
	if strings.HasSuffix(strings.ToLower(p), ".dat") {
		rep, err := s.Remote.Do(ctx, h, map[string]any{"op": "resolve", "path": p})
		var re *worker.RequestError
		switch {
		case errors.As(err, &re):
			return "", 403, "not available"
		case err != nil:
			return "", 502, "the host is not reachable (is the VPN up?)"
		}
		var rs struct {
			Real string `json:"real"`
		}
		if json.Unmarshal(rep.Head, &rs) != nil || !h.InRoots(rs.Real) {
			return "", 403, "not available"
		}
		if !rohsaDat.MatchString(path.Base(rs.Real)) {
			return "", 422, "a ROHSA fit is a file named rohsa_<label>.dat"
		}
		return rs.Real, 0, ""
	}
	dats, more, err := s.findRemoteROHSA(ctx, h, p)
	switch {
	case err != nil && errors.Is(err, context.Canceled):
		return "", 499, "canceled"
	case err != nil && silent(err):
		return "", 502, "the host is not reachable (is the VPN up?)"
	case err != nil:
		return "", 403, "not available"
	}
	switch len(dats) {
	case 0:
		why := "no ROHSA fit (a rohsa_*.dat) was found in that folder or the folders just below it"
		if more {
			why += " (the search stopped after " + fmt.Sprint(rohsaSearchDirs) + " folders; give the .dat)"
		}
		return "", 422, why
	case 1:
		return dats[0], 0, ""
	}
	shown := make([]string, 0, 6)
	for i, d := range dats {
		if i == 5 {
			shown = append(shown, fmt.Sprintf("and %d more", len(dats)-5))
			break
		}
		shown = append(shown, strings.TrimPrefix(d, strings.TrimRight(p, "/")+"/"))
	}
	return "", 422, "this folder holds several ROHSA fits (" + strings.Join(shown, ", ") + "); give the .dat to import"
}
