package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
)

// The desktop's own file chooser. The server runs it on the machine the researcher sits at (the API listens on
// loopback), so the chooser shows that desktop's bookmarks and its network mounts. Only a path comes back.

var (
	errNoChooser = errors.New("no file chooser on this desktop")
	errCancelled = errors.New("cancelled")
	errTimedOut  = errors.New("timed out")
)

const chooserTimeout = 5 * time.Minute

// Chooser opens a file chooser and returns the chosen absolute path. mode is "folder" or "file".
// It returns errNoChooser when none is installed and errCancelled when the person closes it.
type Chooser func(ctx context.Context, mode, title string, ext []string) (string, error)

var extRe = regexp.MustCompile(`^\.[A-Za-z0-9.]{1,15}$`)

// systemChooser tries the xdg-desktop-portal chooser first (the only one that works inside a sandbox), then zenity
// (GTK, shows Nautilus bookmarks and GVFS mounts), then kdialog.
func systemChooser(ctx context.Context, mode, title string, ext []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, chooserTimeout)
	defer cancel()
	p, err := chain(ctx, mode, title, ext, portalChooserFrom(connectPortal, readableHere), commandChooser)
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
		return "", errTimedOut
	}
	return p, err
}

// commandChooser runs zenity or kdialog, whichever is installed.
func commandChooser(ctx context.Context, mode, title string, ext []string) (string, error) {
	var cmd *exec.Cmd
	if z, err := exec.LookPath("zenity"); err == nil {
		args := []string{"--file-selection", "--title=" + title}
		if mode == "folder" {
			args = append(args, "--directory")
		} else if len(ext) > 0 {
			args = append(args, "--file-filter=Files | "+globs(ext))
		}
		cmd = exec.CommandContext(ctx, z, args...)
	} else if k, err := exec.LookPath("kdialog"); err == nil {
		var args []string
		if mode == "folder" {
			args = []string{"--getexistingdirectory", ".", "--title", title}
		} else {
			filter := ""
			if len(ext) > 0 {
				filter = globs(ext)
			}
			args = []string{"--getopenfilename", ".", filter, "--title", title}
		}
		cmd = exec.CommandContext(ctx, k, args...)
	} else {
		return "", errNoChooser
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 { // both tools exit 1 when the dialog is closed without a choice
		return "", errCancelled
	}
	if err != nil {
		return "", err
	}
	p := strings.TrimRight(out.String(), "\r\n")
	if p == "" {
		return "", errCancelled
	}
	return p, nil
}

// globs turns [".fits", ".toml"] into "*.fits *.toml" (both tools take shell-style patterns).
func globs(ext []string) string {
	g := make([]string, len(ext))
	for i, e := range ext {
		g[i] = "*" + e
	}
	return strings.Join(g, " ")
}

type gvfsInfo struct {
	Host string `json:"host"`
	User string `json:"user,omitempty"`
	Port string `json:"port,omitempty"`
}

var gvfsMount = regexp.MustCompile(`^/run/user/[0-9]+/gvfs/(?:sftp|ssh):([^/]*)(/.*)?$`)

// parseGVFS reads a path under a GVFS ssh mount, /run/user/<uid>/gvfs/sftp:host=H[,user=U][,port=P]/rest, as the file
// manager makes when it connects to a server. The mount folder's name is URL-escaped; the names below it are not.
func parseGVFS(p string) (info gvfsInfo, rest string, ok bool) {
	m := gvfsMount.FindStringSubmatch(filepath.Clean(p))
	if m == nil {
		return info, "", false
	}
	for _, kv := range strings.Split(m[1], ",") {
		k, v, found := strings.Cut(kv, "=")
		if !found {
			continue
		}
		v, err := url.PathUnescape(v)
		if err != nil {
			return gvfsInfo{}, "", false
		}
		switch k {
		case "host":
			info.Host = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]") // gvfs may write an IPv6 literal as [::1]
		case "user":
			info.User = v
		case "port":
			info.Port = v
		}
	}
	if info.Host == "" {
		return gvfsInfo{}, "", false
	}
	rest = m[2]
	if rest == "" {
		rest = "/"
	}
	return info, rest, true
}

// matchHost finds the registered host a GVFS mount points at. A host stores an ssh destination (a host name or an
// alias from the user's ssh config, optionally user@). It matches when the destination, or the HostName its alias
// stands for in the ssh config, equals the mount's host, and the user agrees wherever both sides state one. The port
// is not compared: ssh decides it from the user's own configuration. The first match wins; one that names the same
// user is preferred over one that states none.
func matchHost(list []hosts.Host, alias []hosts.SSHHost, g gvfsInfo) (hosts.Host, bool) {
	var best hosts.Host
	found, exact := false, false
	for _, h := range list {
		user, name := "", h.Dest
		if i := strings.LastIndex(h.Dest, "@"); i >= 0 { // ssh splits at the last @: a user name may itself hold one
			user, name = h.Dest[:i], h.Dest[i+1:]
		}
		names := []string{name}
		for _, a := range alias {
			if a.Alias == name {
				if a.HostName != "" {
					names = append(names, a.HostName)
				}
				if user == "" {
					user = a.User
				}
			}
		}
		hit := false
		for _, n := range names {
			if strings.EqualFold(n, g.Host) {
				hit = true
			}
		}
		if !hit || (g.User != "" && user != "" && user != g.User) {
			continue
		}
		sameUser := g.User != "" && user == g.User
		if !found || (sameUser && !exact) {
			best, found, exact = h, true, sameUser
		}
	}
	return best, found
}

// choose runs the desktop chooser. One at a time: a second request while a dialog is open is refused (409).
func (s *Server) choose(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode  string   `json:"mode"`
		Title string   `json:"title"`
		Ext   []string `json:"ext"`
	}
	if !body(w, r, 4<<10, &in, true) {
		return
	}
	if in.Mode != "folder" && in.Mode != "file" {
		fail(w, 400, "mode must be folder or file")
		return
	}
	title := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, in.Title)
	if len(title) > 120 {
		title = title[:120]
	}
	if title == "" {
		title = "Choose"
	}
	if len(in.Ext) > 8 {
		fail(w, 400, "bad ext")
		return
	}
	for _, e := range in.Ext {
		if !extRe.MatchString(e) {
			fail(w, 400, "bad ext")
			return
		}
	}
	pick := s.Chooser
	if pick == nil {
		pick = systemChooser
	}
	if !s.choosing.CompareAndSwap(false, true) {
		fail(w, 409, "a file chooser is already open")
		return
	}
	defer s.choosing.Store(false)
	p, err := pick(r.Context(), in.Mode, title, in.Ext)
	switch {
	case errors.Is(err, errNoChooser):
		fail(w, 501, "no file chooser on this desktop")
		return
	case errors.Is(err, errCancelled):
		w.WriteHeader(204)
		return
	case errors.Is(err, errTimedOut):
		fail(w, 408, "the file chooser was left open too long")
		return
	case err != nil:
		fail(w, 502, "the file chooser failed")
		return
	}
	if !filepath.IsAbs(p) || strings.ContainsRune(p, 0) {
		fail(w, 502, "the file chooser returned an unusable path")
		return
	}
	p = filepath.Clean(p)
	out := struct {
		Path       string    `json:"path"`
		HostID     *int64    `json:"hostId"`
		RemotePath *string   `json:"remotePath"`
		GVFS       *gvfsInfo `json:"gvfs"`
		// DocumentPortal: the path is inside the Documents portal's mount (/run/user/<uid>/doc), the only place a
		// sandboxed app can read what it was handed. It is not the file's real location, and the page says so.
		DocumentPortal bool `json:"documentPortal,omitempty"`
	}{Path: p, DocumentPortal: isDocPortalPath(p)}
	out.HostID, out.RemotePath, out.GVFS = s.gvfsHost(p)
	writeJSON(w, 200, out)
}

// gvfsHost says where a path inside a GVFS ssh mount belongs: the registered host and the path on it, or, when no host
// matches, the mount's host, user and port so the page can offer to add it. All three are nil for any other path.
// Only the mount folder's name is read; nothing is read through the mount.
func (s *Server) gvfsHost(p string) (hostID *int64, remote *string, g *gvfsInfo) {
	info, rest, ok := parseGVFS(p)
	if !ok {
		return nil, nil, nil
	}
	if s.HostDB != nil {
		if list, err := s.HostDB.List(); err == nil {
			var alias []hosts.SSHHost
			if s.SSHConfig != "" {
				alias, _ = hosts.LoadSSHConfig(s.SSHConfig)
			}
			if h, ok := matchHost(list, alias, info); ok {
				return &h.ID, &rest, nil
			}
		}
	}
	return nil, nil, &info
}
