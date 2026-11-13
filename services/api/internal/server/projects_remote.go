package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

const walkPage = 2000

// remoteReader reads the start of a file in a project on a host through the host's worker. The project folder is one of
// the host's roots, so the worker refuses anything outside it, whatever the path says.
func (s *Server) remoteReader(ctx context.Context, h hosts.Host, root string) func(rel string, max int64) ([]byte, bool, error) {
	return func(rel string, max int64) ([]byte, bool, error) {
		rep, err := s.Remote.Do(ctx, h, map[string]any{"op": "readtext", "path": path.Join(root, rel), "max": max})
		if err != nil {
			return nil, false, err
		}
		var t struct {
			Text      string `json:"text"`
			Truncated bool   `json:"truncated"`
		}
		if json.Unmarshal(rep.Head, &t) != nil {
			return nil, false, errors.New("the host answered unexpectedly")
		}
		return []byte(t.Text), t.Truncated, nil
	}
}

// walkRemote lists a project folder on a host through the worker's walk, page by page, into memory. The caller's
// discover rules go with every request, so the host does not even stat what must never be read. The walk stops at the
// entry or time budget and says so.
func (s *Server) walkRemote(ctx context.Context, h hosts.Host, root string, globs []string, fsys *discover.MemFS, maxEntries int, maxDur time.Duration, progress func(int)) (truncated bool, why string, err error) {
	if len(globs) > 60 {
		globs = globs[:60]
	}
	safe := []string{} // never null: the worker refuses a null list, which made a remote project with no skip globs unscannable
	for _, g := range globs {
		if len(g) <= 200 {
			safe = append(safe, g)
		}
	}
	start, after, n := time.Now(), "", 0
	for {
		if err := ctx.Err(); err != nil {
			return false, "", err
		}
		if n >= maxEntries {
			return true, "stopped after the limit of entries was reached", nil
		}
		if time.Since(start) > maxDur {
			return true, "stopped after the time limit for walking was reached", nil
		}
		rep, err := s.Remote.Do(ctx, h, map[string]any{"op": "walk", "path": root, "after": after, "limit": walkPage,
			"skip_names": discover.SkipNames, "never_files": discover.NeverFiles, "never_dirs": discover.NeverDirs,
			"skip_globs": safe, "leaf_dir_suffixes": discover.LeafDirSuffixes})
		if err != nil {
			return false, "", err
		}
		var page struct {
			Entries []struct {
				P       string `json:"p"`
				Dir     bool   `json:"dir"`
				Leaf    bool   `json:"leaf"`
				Link    string `json:"link"`
				Never   bool   `json:"never"`
				Size    int64  `json:"size"`
				MtimeNS int64  `json:"mtime_ns"`
			} `json:"entries"`
			Next *string `json:"next"`
		}
		if json.Unmarshal(rep.Head, &page) != nil {
			return false, "", errors.New("the host answered unexpectedly")
		}
		for _, e := range page.Entries {
			en := discover.Entry{Dir: e.Dir, Size: e.Size, MtimeNS: e.MtimeNS}
			if e.Link != "" {
				en.Dir, en.Link = false, true
				en.LinkOutside, en.LinkDangling = e.Link == "outside", e.Link == "dangling"
			}
			fsys.Add(e.P, en)
		}
		n += len(page.Entries)
		if progress != nil {
			progress(n)
		}
		if page.Next == nil {
			return false, "", nil
		}
		after = *page.Next
	}
}

// remoteCanon asks a host where a folder really is, whether it is a folder, and what the account's home is.
func (s *Server) remoteCanon(ctx context.Context, h hosts.Host, p string) (real string, isDir bool, home string, err error) {
	rep, err := s.Remote.Do(ctx, h, map[string]any{"op": "canon", "path": p})
	if err != nil {
		return "", false, "", err
	}
	var c struct {
		Real  string `json:"real"`
		IsDir bool   `json:"is_dir"`
		Home  string `json:"home"`
	}
	if json.Unmarshal(rep.Head, &c) != nil || c.Real == "" {
		return "", false, "", errors.New("the host answered unexpectedly")
	}
	return c.Real, c.IsDir, c.Home, nil
}

// hostErrStatus maps a worker failure to the answer the API gives for it.
func hostErrStatus(err error) (int, string) {
	var re *worker.RequestError
	switch {
	case errors.As(err, &re):
		return 422, "the host refused: " + re.Message
	case errors.Is(err, context.Canceled):
		return 499, "canceled"
	case errors.Is(err, worker.ErrReplyTooLarge):
		return 502, "the host's answer was larger than Vesqira accepts; the connection itself works"
	default:
		return 502, "the host is not reachable (is the VPN up?)"
	}
}

func hostPath(root, rel string) string {
	if rel == "" {
		return root
	}
	return fmt.Sprintf("%s/%s", root, rel)
}
