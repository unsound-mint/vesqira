package server

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/godbus/dbus/v5"
)

func uris(u ...string) map[string]dbus.Variant {
	return map[string]dbus.Variant{"uris": dbus.MakeVariant(u)}
}

func TestParsePortalResponse(t *testing.T) {
	for _, c := range []struct {
		name string
		code uint32
		res  map[string]dbus.Variant
		want string
		err  error // matched with errors.Is
		bad  bool  // any other error
	}{
		{"file", 0, uris("file:///home/me/cube.fits"), "/home/me/cube.fits", nil, false},
		{"escapes", 0, uris("file:///home/me/my%20data/a%23b.fits"), "/home/me/my data/a#b.fits", nil, false},
		{"folder with slash", 0, uris("file:///data/runs/"), "/data/runs", nil, false},
		{"localhost host", 0, uris("file://localhost/data"), "/data", nil, false},
		{"first of several", 0, uris("file:///a", "file:///b"), "/a", nil, false},
		{"doc portal path is passed on", 0, uris("file:///run/user/1000/doc/ab12cd/cube.fits"), "/run/user/1000/doc/ab12cd/cube.fits", nil, false},
		{"dot segments cleaned", 0, uris("file:///data/a/../b"), "/data/b", nil, false},
		{"cancelled", 1, map[string]dbus.Variant{}, "", errCancelled, false},
		{"empty list is a cancel", 0, uris(), "", errCancelled, false},
		{"other end", 2, map[string]dbus.Variant{}, "", nil, true},
		{"no uris", 0, map[string]dbus.Variant{}, "", nil, true},
		{"remote host", 0, uris("file://evil.example/etc/passwd"), "", nil, true},
		{"not a file uri", 0, uris("https://example.org/x"), "", nil, true},
		{"relative", 0, uris("file:relative/x"), "", nil, true},
		{"NUL", 0, uris("file:///a%00b"), "", nil, true},
	} {
		got, err := parsePortalResponse(c.code, c.res)
		switch {
		case c.err != nil:
			if !errors.Is(err, c.err) {
				t.Errorf("%s: err %v, want %v", c.name, err, c.err)
			}
		case c.bad:
			if err == nil || errors.Is(err, errCancelled) {
				t.Errorf("%s: want a failure, got %q %v", c.name, got, err)
			}
		default:
			if err != nil || got != c.want {
				t.Errorf("%s: got %q %v, want %q", c.name, got, err, c.want)
			}
		}
	}
}

type fakePortal struct {
	code    uint32
	res     map[string]dbus.Variant
	openErr error
	hosts   map[string]string
	opts    map[string]dbus.Variant
	asked   []string
}

func (f *fakePortal) Open(_ context.Context, _ string, o map[string]dbus.Variant) (uint32, map[string]dbus.Variant, error) {
	f.opts = o
	return f.code, f.res, f.openErr
}

func (f *fakePortal) HostPath(_ context.Context, id string) (string, bool) {
	f.asked = append(f.asked, id)
	p, ok := f.hosts[id]
	return p, ok
}

func fakeNew(f *fakePortal, err error) func(context.Context) (portalClient, func(), error) {
	return func(context.Context) (portalClient, func(), error) { return f, func() {}, err }
}

func TestDocumentPortalPaths(t *testing.T) {
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	for _, c := range []struct {
		name     string
		in       string
		hosts    map[string]string
		readable func(string) bool
		want     string
	}{
		{"mapped back", "/run/user/1000/doc/ab12/cube.fits", map[string]string{"ab12": "/data/cubes/cube.fits"}, yes, "/data/cubes/cube.fits"},
		{"inside a granted folder", "/run/user/1000/doc/ab12/cubes/lmc/a.fits", map[string]string{"ab12": "/data/cubes"}, yes, "/data/cubes/lmc/a.fits"},
		{"host path not readable here (sandbox): stays the doc path", "/run/user/1000/doc/ab12/cube.fits", map[string]string{"ab12": "/data/cubes/cube.fits"}, no, "/run/user/1000/doc/ab12/cube.fits"},
		{"portal does not say", "/run/user/1000/doc/ab12/cube.fits", nil, yes, "/run/user/1000/doc/ab12/cube.fits"},
		{"relative host path is not trusted", "/run/user/1000/doc/ab12/x", map[string]string{"ab12": "rel/x"}, yes, "/run/user/1000/doc/ab12/x"},
		{"ordinary path is not asked about", "/home/me/x.fits", map[string]string{}, yes, "/home/me/x.fits"},
	} {
		f := &fakePortal{hosts: c.hosts}
		if got := resolveDocPath(context.Background(), f, c.in, c.readable); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
		if c.name == "ordinary path is not asked about" && len(f.asked) != 0 {
			t.Errorf("asked the Documents portal about an ordinary path")
		}
	}
	if !isDocPortalPath("/run/user/1000/doc/ab12/x") || isDocPortalPath("/run/user/1000/gvfs/sftp:host=h/x") || isDocPortalPath("/home/me") {
		t.Error("isDocPortalPath")
	}
}

func TestPortalChooserWithFake(t *testing.T) {
	f := &fakePortal{res: uris("file:///run/user/1000/doc/ab/run"), hosts: map[string]string{"ab": "/shared/run"}}
	ch := portalChooserFrom(fakeNew(f, nil), func(string) bool { return true })
	got, err := ch(context.Background(), "folder", "Open", nil)
	if err != nil || got != "/shared/run" {
		t.Fatalf("got %q %v", got, err)
	}
	if v, ok := f.opts["directory"]; !ok || v.Value() != true {
		t.Errorf("folder mode must ask for a directory: %v", f.opts)
	}
	f = &fakePortal{res: uris("file:///x.fits")}
	_, _ = portalChooserFrom(fakeNew(f, nil), readableHere)(context.Background(), "file", "Open", []string{".fits", ".fit"})
	if _, ok := f.opts["directory"]; ok {
		t.Error("file mode must not ask for a directory")
	}
	if _, ok := f.opts["filters"]; !ok {
		t.Error("extensions become a filter")
	}
	f = &fakePortal{code: 1}
	if _, err := portalChooserFrom(fakeNew(f, nil), readableHere)(context.Background(), "file", "Open", nil); !errors.Is(err, errCancelled) {
		t.Errorf("cancel: %v", err)
	}
}

func TestChooserFallbackOrder(t *testing.T) {
	var order []string
	mk := func(name string, p string, err error) func(context.Context, string, string, []string) (string, error) {
		return func(context.Context, string, string, []string) (string, error) {
			order = append(order, name)
			return p, err
		}
	}
	ctx := context.Background()
	run := func(cs ...func(context.Context, string, string, []string) (string, error)) (string, error) {
		order = nil
		return chain(ctx, "file", "t", nil, cs...)
	}
	// the portal answers: nothing else is tried
	if p, err := run(mk("portal", "/a", nil), mk("zenity", "/b", nil)); p != "/a" || err != nil || !reflect.DeepEqual(order, []string{"portal"}) {
		t.Errorf("portal first: %q %v %v", p, err, order)
	}
	// the portal is absent: the command chooser runs
	if p, err := run(mk("portal", "", errPortalUnavailable), mk("zenity", "/b", nil)); p != "/b" || err != nil || !reflect.DeepEqual(order, []string{"portal", "zenity"}) {
		t.Errorf("fallback: %q %v %v", p, err, order)
	}
	// a refused call is wrapped and still falls back
	if p, _ := run(mk("portal", "", errors.Join(errPortalUnavailable, errors.New("no backend"))), mk("zenity", "/b", nil)); p != "/b" {
		t.Errorf("wrapped unavailable: %q", p)
	}
	// a cancel in the portal must not open a second dialog
	if _, err := run(mk("portal", "", errCancelled), mk("zenity", "/b", nil)); !errors.Is(err, errCancelled) || !reflect.DeepEqual(order, []string{"portal"}) {
		t.Errorf("cancel stops: %v %v", err, order)
	}
	// a failure after the dialog stops too
	boom := errors.New("boom")
	if _, err := run(mk("portal", "", boom), mk("zenity", "/b", nil)); !errors.Is(err, boom) || len(order) != 1 {
		t.Errorf("failure stops: %v %v", err, order)
	}
	// nothing at all
	if _, err := run(mk("portal", "", errPortalUnavailable), mk("zenity", "", errNoChooser)); !errors.Is(err, errNoChooser) {
		t.Errorf("none: %v", err)
	}
	// the portal factory failing means unavailable, not a failure
	if _, err := portalChooserFrom(fakeNew(nil, errors.New("no bus")), readableHere)(ctx, "file", "t", nil); !errors.Is(err, errPortalUnavailable) {
		t.Errorf("no bus: %v", err)
	}
	// a call refused before any dialog falls back; a context that ended does not
	f := &fakePortal{openErr: errors.New("UnknownMethod")}
	if _, err := portalChooserFrom(fakeNew(f, nil), readableHere)(ctx, "file", "t", nil); !errors.Is(err, errPortalUnavailable) {
		t.Errorf("refused call: %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := portalChooserFrom(fakeNew(f, nil), readableHere)(cctx, "file", "t", nil); errors.Is(err, errPortalUnavailable) {
		t.Errorf("context ended must not fall back: %v", err)
	}
}
