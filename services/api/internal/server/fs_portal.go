package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// The xdg-desktop-portal file chooser (org.freedesktop.portal.FileChooser), the first choice of the desktop chooser
// (ADR 0031). It is the only chooser that works inside a sandbox, and on a desktop that has a portal it is the chooser
// the person already knows. The D-Bus conversation is behind portalClient so the reply parsing and the fallback order
// are tested without a session bus.

const (
	portalName     = "org.freedesktop.portal.Desktop"
	portalPath     = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalChooser  = "org.freedesktop.portal.FileChooser"
	portalRequest  = "org.freedesktop.portal.Request"
	portalDocs     = "org.freedesktop.portal.Documents"
	portalDocsPath = dbus.ObjectPath("/org/freedesktop/portal/documents")
)

// errPortalUnavailable means no portal answered before a dialog was shown, so the next chooser may be tried. Anything
// that fails after the dialog was shown is not this error: a second dialog must not open behind the first.
var errPortalUnavailable = errors.New("no desktop portal")

// portalClient is the small slice of D-Bus the chooser needs.
type portalClient interface {
	// Open shows the chooser and returns the Response of the request: the code (0 chosen, 1 cancelled, 2 other) and results.
	Open(ctx context.Context, title string, options map[string]dbus.Variant) (code uint32, results map[string]dbus.Variant, err error)
	// HostPath asks the Documents portal for the real path behind a document id; ok is false when it does not say.
	HostPath(ctx context.Context, docID string) (path string, ok bool)
}

// portalChooserFrom builds a chooser from a portal. newPortal is called per request and fails when there is no
// session bus or no portal service.
func portalChooserFrom(newPortal func(ctx context.Context) (portalClient, func(), error), readable func(string) bool) func(ctx context.Context, mode, title string, ext []string) (string, error) {
	return func(ctx context.Context, mode, title string, ext []string) (string, error) {
		p, closeFn, err := newPortal(ctx)
		if err != nil {
			return "", errPortalUnavailable
		}
		defer closeFn()
		code, results, err := p.Open(ctx, title, portalOptions(mode, ext))
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if errors.Is(err, errPortalUnavailable) {
				slog.Info("portal file chooser not used", "reason", err) // so a marshalling mistake does not hide behind the fallback
			}
			return "", err // only an error wrapping errPortalUnavailable (refused before any dialog) lets the next chooser run
		}
		path, err := parsePortalResponse(code, results)
		if err != nil {
			return "", err
		}
		return resolveDocPath(ctx, p, path, readable), nil
	}
}

// portalOptions are the a{sv} options of OpenFile. directory needs FileChooser version 3.
func portalOptions(mode string, ext []string) map[string]dbus.Variant {
	o := map[string]dbus.Variant{
		"modal":    dbus.MakeVariant(true),
		"multiple": dbus.MakeVariant(false),
	}
	if mode == "folder" {
		o["directory"] = dbus.MakeVariant(true)
	} else if len(ext) > 0 {
		type pat struct {
			Kind uint32 // 0: a glob pattern
			Glob string
		}
		type filter struct {
			Name     string
			Patterns []pat
		}
		f := filter{Name: "Files"}
		for _, e := range ext {
			f.Patterns = append(f.Patterns, pat{0, "*" + e})
		}
		o["filters"] = dbus.MakeVariant([]filter{f})
	}
	return o
}

// parsePortalResponse reads the Response signal of a FileChooser request: code 1 is the person closing the dialog.
func parsePortalResponse(code uint32, results map[string]dbus.Variant) (string, error) {
	switch code {
	case 0:
	case 1:
		return "", errCancelled
	default:
		return "", fmt.Errorf("the portal chooser ended with code %d", code)
	}
	v, ok := results["uris"]
	if !ok {
		return "", errors.New("the portal chooser returned no uris")
	}
	uris, ok := v.Value().([]string)
	if !ok || len(uris) == 0 {
		return "", errCancelled // an empty choice is no choice
	}
	return fileURIPath(uris[0])
}

// fileURIPath turns a file:// URI into the absolute path it names. Only local file URIs are accepted.
func fileURIPath(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || (u.Host != "" && !strings.EqualFold(u.Host, "localhost")) {
		return "", errors.New("the portal chooser returned something that is not a local file")
	}
	if !filepath.IsAbs(u.Path) || strings.ContainsRune(u.Path, 0) {
		return "", errors.New("the portal chooser returned an unusable path")
	}
	return filepath.Clean(u.Path), nil
}

var docPortalPath = regexp.MustCompile(`^/run/user/[0-9]+/doc/([0-9A-Za-z]+)(/.*)?$`)

// isDocPortalPath reports whether p is inside the Documents portal's FUSE mount, where a sandboxed app is handed files.
func isDocPortalPath(p string) bool { return docPortalPath.MatchString(filepath.Clean(p)) }

// resolveDocPath maps a document-portal path back to the real path when, and only when, the Documents portal says
// what it is and this process can read it (an unsandboxed app, or a sandbox granted that folder). Otherwise the
// document-portal path is returned unchanged: it is what this process can read, and the caller says so. The path
// is never reconstructed by guessing (ADR 0052).
func resolveDocPath(ctx context.Context, p portalClient, path string, readable func(string) bool) string {
	m := docPortalPath.FindStringSubmatch(path)
	if m == nil {
		return path
	}
	host, ok := p.HostPath(ctx, m[1])
	if !ok || !filepath.IsAbs(host) || strings.ContainsRune(host, 0) {
		return path
	}
	// /doc/<id>/<name>[/more]: the document is <name>, and the host path names it, so only what follows <name> is appended.
	if _, tail, found := strings.Cut(strings.TrimPrefix(m[2], "/"), "/"); found && tail != "" {
		host = filepath.Join(host, tail)
	}
	host = filepath.Clean(host)
	if !readable(host) {
		return path
	}
	return host
}

// chain runs the choosers in order. Only "this chooser is not available" moves on; a cancel, a timeout or a failure
// after a dialog was shown ends the request.
func chain(ctx context.Context, mode, title string, ext []string, choosers ...func(context.Context, string, string, []string) (string, error)) (string, error) {
	for _, c := range choosers {
		p, err := c(ctx, mode, title, ext)
		if errors.Is(err, errPortalUnavailable) || errors.Is(err, errNoChooser) {
			continue
		}
		return p, err
	}
	return "", errNoChooser
}

// dbusPortal is the real client over the session bus.
type dbusPortal struct {
	conn *dbus.Conn
}

func readableHere(p string) bool { _, err := os.Stat(p); return err == nil }

// connectPortal opens a private session-bus connection and checks that the portal is there or can be started.
func connectPortal(ctx context.Context) (portalClient, func(), error) {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" && !readableHere(fmt.Sprintf("/run/user/%d/bus", os.Getuid())) {
		return nil, nil, errPortalUnavailable
	}
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, nil, errPortalUnavailable
	}
	var owned bool
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, portalName).Store(&owned); err != nil || !owned {
		var act []string
		_ = conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.ListActivatableNames", 0).Store(&act)
		found := false
		for _, n := range act {
			found = found || n == portalName
		}
		if !found {
			conn.Close()
			return nil, nil, errPortalUnavailable
		}
	}
	return &dbusPortal{conn}, func() { conn.Close() }, nil
}

func (d *dbusPortal) Open(ctx context.Context, title string, options map[string]dbus.Variant) (uint32, map[string]dbus.Variant, error) {
	var tok [8]byte
	if _, err := rand.Read(tok[:]); err != nil {
		return 0, nil, err
	}
	token := "vesqira" + hex.EncodeToString(tok[:])
	options["handle_token"] = dbus.MakeVariant(token)
	sender := strings.ReplaceAll(strings.TrimPrefix(d.conn.Names()[0], ":"), ".", "_")
	expected := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + token)

	// The Response can arrive as soon as the call returns, so listen before calling.
	ch := make(chan *dbus.Signal, 8)
	d.conn.Signal(ch)
	defer d.conn.RemoveSignal(ch)
	// Everything up to the call returning is "before any dialog": the next chooser may run. After it, errors are final.
	if _, folder := options["directory"]; folder {
		// FileChooser before version 3 ignores `directory` and would open a file dialog for a folder request.
		v, err := d.conn.Object(portalName, portalPath).GetProperty(portalChooser + ".version")
		if ver, ok := v.Value().(uint32); err != nil || !ok || ver < 3 {
			return 0, nil, fmt.Errorf("%w: FileChooser version %v cannot choose folders", errPortalUnavailable, v.Value())
		}
	}
	// Only the portal's own reply counts: another client on the session bus could emit a Response on the request path.
	var owner string
	if err := d.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, portalName).Store(&owner); err != nil {
		return 0, nil, fmt.Errorf("%w: %v", errPortalUnavailable, err)
	}
	if err := d.conn.AddMatchSignalContext(ctx, dbus.WithMatchSender(portalName), dbus.WithMatchInterface(portalRequest), dbus.WithMatchMember("Response")); err != nil {
		return 0, nil, fmt.Errorf("%w: %v", errPortalUnavailable, err)
	}
	var handle dbus.ObjectPath
	// A parent window needs the shell's window handle ("x11:<xid>" or "wayland:<handle>"); a browser tab has none, and the
	// desktop window uses its own native dialog (ADR 0037), so the dialog is opened without a parent.
	err := d.conn.Object(portalName, portalPath).CallWithContext(ctx, portalChooser+".OpenFile", 0, "", title, options).Store(&handle)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", errPortalUnavailable, err)
	}
	for {
		select {
		case <-ctx.Done():
			_ = d.conn.Object(portalName, handle).Call(portalRequest+".Close", dbus.FlagNoReplyExpected)
			return 0, nil, ctx.Err()
		case sig, open := <-ch:
			if !open {
				return 0, nil, errors.New("the session bus connection closed while the chooser was open")
			}
			if sig.Sender != owner || sig.Name != portalRequest+".Response" || (sig.Path != handle && sig.Path != expected) || len(sig.Body) != 2 {
				continue
			}
			code, ok1 := sig.Body[0].(uint32)
			res, ok2 := sig.Body[1].(map[string]dbus.Variant)
			if !ok1 || !ok2 {
				return 0, nil, errors.New("malformed portal response")
			}
			return code, res, nil
		}
	}
}

func (d *dbusPortal) HostPath(ctx context.Context, docID string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var out map[string][]byte
	if err := d.conn.Object(portalDocs, portalDocsPath).CallWithContext(ctx, portalDocs+".GetHostPaths", 0, []string{docID}).Store(&out); err != nil {
		return "", false // the portal predates GetHostPaths, or the id is unknown
	}
	b, ok := out[docID]
	if !ok {
		return "", false
	}
	return strings.TrimRight(string(b), "\x00"), true
}
