package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/review"
)

func getRaw(t *testing.T, u string, hdr map[string]string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func complete(t *testing.T, ts *httptest.Server, q string) (int, completion, []byte) {
	t.Helper()
	code, b := getRaw(t, ts.URL+"/api/fs/complete?"+q, nil)
	var c completion
	_ = json.Unmarshal(b, &c)
	return code, c, b
}

func names(c completion) []string {
	out := []string{}
	for _, e := range c.Entries {
		out = append(out, e.Name)
	}
	return out
}

// Completion may list a folder outside the source roots (choosing a folder to open needs it), but it answers names,
// whether each is a folder and a path, and nothing else.
func TestCompletionListsOutsideTheRootsAndAnswersNamesOnly(t *testing.T) {
	ts, _, sibling := newServer(t, &fakeWorker{})
	os.Mkdir(filepath.Join(sibling, "runs"), 0o755)
	os.WriteFile(filepath.Join(sibling, "secret.fits"), []byte("private contents"), 0o600)
	os.WriteFile(filepath.Join(sibling, ".hidden"), nil, 0o644)
	code, c, raw := complete(t, ts, "prefix="+url.QueryEscape(sibling+"/"))
	if code != 200 || c.Base != sibling {
		t.Fatalf("%d %+v", code, c)
	}
	if got := names(c); !reflect.DeepEqual(got, []string{"runs", "secret.fits"}) {
		t.Fatalf("folders first, dot entry hidden: %v", got)
	}
	if strings.Contains(string(raw), "private contents") || strings.Contains(string(raw), "size") {
		t.Fatalf("contents or sizes leaked: %s", raw)
	}
	var generic struct {
		Entries []map[string]any `json:"entries"`
	}
	json.Unmarshal(raw, &generic)
	for _, e := range generic.Entries {
		if len(e) != 3 || e["name"] == nil || e["path"] == nil || e["dir"] == nil {
			t.Fatalf("entry fields: %v", e)
		}
	}
	// reading contents stays limited to the roots, as before
	if code := post(t, ts, filepath.Join(sibling, "secret.fits"), nil).StatusCode; code != 403 {
		t.Errorf("register outside the roots: %d", code)
	}
}

func TestCompletionFiltersAndSorts(t *testing.T) {
	ts, root, _ := newServer(t, &fakeWorker{})
	for _, f := range []string{"b.fits", "a.FITS", "c.txt", ".dot.fits", "Alpha.fits"} {
		os.WriteFile(filepath.Join(root, f), nil, 0o644)
	}
	os.Mkdir(filepath.Join(root, "zdir"), 0o755)
	os.Symlink(filepath.Join(root, "zdir"), filepath.Join(root, "link-to-dir"))
	q := func(extra string) []string {
		t.Helper()
		code, c, _ := complete(t, ts, "prefix="+url.QueryEscape(root+"/")+extra)
		if code != 200 {
			t.Fatal(code)
		}
		return names(c)
	}
	if got := q("&kind=file&ext=.fits"); !reflect.DeepEqual(got, []string{"link-to-dir", "zdir", "Alpha.fits", "a.FITS", "b.fits"}) {
		t.Errorf("file+ext (folders stay for navigation, extension is case-insensitive): %v", got)
	}
	if got := q("&kind=dir"); !reflect.DeepEqual(got, []string{"link-to-dir", "zdir"}) {
		t.Errorf("dir: %v", got)
	}
	_, c, _ := complete(t, ts, "prefix="+url.QueryEscape(root+"/.d"))
	if got := names(c); !reflect.DeepEqual(got, []string{".dot.fits"}) {
		t.Errorf("a typed dot shows dot entries: %v", got)
	}
	_, c, _ = complete(t, ts, "prefix="+url.QueryEscape(root+"/al"))
	if got := names(c); !reflect.DeepEqual(got, []string{"Alpha.fits"}) {
		t.Errorf("case-insensitive fallback: %v", got)
	}
	_, c, _ = complete(t, ts, "prefix="+url.QueryEscape(root+"/a"))
	if got := names(c); !reflect.DeepEqual(got, []string{"a.FITS"}) {
		t.Errorf("case-sensitive match wins over the fallback: %v", got)
	}
	_, c, _ = complete(t, ts, "prefix="+url.QueryEscape(root+"/nope/"))
	if c.Entries == nil || len(c.Entries) != 0 {
		t.Errorf("a missing folder is an empty list: %+v", c)
	}
}

func TestCompletionIsLimitedAndSaysSo(t *testing.T) {
	ts, root, _ := newServer(t, &fakeWorker{})
	for i := 0; i < 70; i++ {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("f%02d.txt", i)), nil, 0o644)
	}
	_, c, _ := complete(t, ts, "prefix="+url.QueryEscape(root+"/"))
	if len(c.Entries) != 50 || !c.Truncated || c.Entries[0].Name != "f00.txt" {
		t.Fatalf("%d entries, truncated=%v, first %v", len(c.Entries), c.Truncated, names(c)[:1])
	}
}

func TestCompletionExpandsHomeAndRefusesBadPrefixes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.Mkdir(filepath.Join(home, "data"), 0o755)
	ts, _, _ := newServer(t, &fakeWorker{})
	_, c, _ := complete(t, ts, "prefix="+url.QueryEscape("~/da"))
	if c.Base != home || !reflect.DeepEqual(names(c), []string{"data"}) || c.Entries[0].Path != filepath.Join(home, "data") {
		t.Errorf("~ expands to the home folder: %+v", c)
	}
	_, c, _ = complete(t, ts, "prefix=~")
	if c.Base != home {
		t.Errorf("bare ~: %+v", c)
	}
	for name, q := range map[string]string{
		"relative":      "prefix=data/x",
		"empty":         "prefix=",
		"other user":    "prefix=" + url.QueryEscape("~root/x"),
		"NUL":           "prefix=" + url.QueryEscape("/tmp/a\x00b"),
		"bad kind":      "prefix=/tmp/&kind=all",
		"bad extension": "prefix=/tmp/&ext=fits",
		"too long":      "prefix=/" + strings.Repeat("a", 5000),
	} {
		if code, _, _ := complete(t, ts, q); code != 400 {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	// ".." is resolved on the text and the listing is still one folder
	_, c, _ = complete(t, ts, "prefix="+url.QueryEscape(filepath.Join(home, "data", "..")+"/"))
	if c.Base != home {
		t.Errorf("clean: %+v", c)
	}
}

func TestFSRoutesGuardsAndReviewListener(t *testing.T) {
	ts, root, _ := newServer(t, &fakeWorker{})
	if code, _ := getRaw(t, ts.URL+"/api/fs/complete?prefix="+url.QueryEscape(root+"/"), map[string]string{"Sec-Fetch-Site": "cross-site"}); code != 403 {
		t.Errorf("cross-site completion: %d", code)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/fs/complete?prefix=/", nil)
	req.Host = "evil.example"
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Errorf("foreign Host: %d", r.StatusCode)
	}
	req, _ = http.NewRequest("POST", ts.URL+"/api/fs/choose", strings.NewReader(`{"mode":"folder"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Errorf("cross-site choose: %d", r.StatusCode)
	}
	req, _ = http.NewRequest("POST", ts.URL+"/api/fs/choose", strings.NewReader(`{"mode":"folder"}`))
	req.Header.Set("Content-Type", "text/plain")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 415 {
		t.Errorf("text/plain choose: %d", r.StatusCode)
	}

	// the reviewer's listener has no /api/fs at all
	base := t.TempDir()
	cat, err := catalog.Open("file:" + filepath.Join(base, "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	bs, _ := boards.Open(cat.DB(), base)
	rv := httptest.NewServer((&review.Handler{Store: bs}).Handler())
	defer rv.Close()
	for _, u := range []string{"/api/fs/complete?prefix=/", "/api/hosts/1/complete?prefix=/"} {
		if code, _ := getRaw(t, rv.URL+u, nil); code != 404 {
			t.Errorf("review listener %s: %d, want 404", u, code)
		}
	}
	rq, _ := http.NewRequest("POST", rv.URL+"/api/fs/choose", strings.NewReader(`{"mode":"folder"}`))
	rq.Header.Set("Content-Type", "application/json")
	if r, _ := http.DefaultClient.Do(rq); r.StatusCode != 404 && r.StatusCode != 405 {
		t.Errorf("review listener choose: %d", r.StatusCode)
	}
}

func TestParseGVFS(t *testing.T) {
	for _, c := range []struct {
		in   string
		want gvfsInfo
		rest string
		ok   bool
	}{
		{"/run/user/1000/gvfs/sftp:host=login.hpc.example/home/me/vgt", gvfsInfo{Host: "login.hpc.example"}, "/home/me/vgt", true},
		{"/run/user/1000/gvfs/sftp:host=login.hpc.example,user=me/home/me", gvfsInfo{Host: "login.hpc.example", User: "me"}, "/home/me", true},
		{"/run/user/1000/gvfs/sftp:host=h,user=me,port=2222/data/x.fits", gvfsInfo{Host: "h", User: "me", Port: "2222"}, "/data/x.fits", true},
		{"/run/user/1000/gvfs/sftp:host=h,user=me%40corp.example/d", gvfsInfo{Host: "h", User: "me@corp.example"}, "/d", true},
		{"/run/user/1000/gvfs/sftp:host=h/dir with %20 and space/f", gvfsInfo{Host: "h"}, "/dir with %20 and space/f", true}, // names below the mount are literal
		{"/run/user/1000/gvfs/sftp:host=fe80%3A%3A1,user=me/d", gvfsInfo{Host: "fe80::1", User: "me"}, "/d", true},
		{"/run/user/1000/gvfs/sftp:host=[2001:db8::7],port=22/d", gvfsInfo{Host: "2001:db8::7", Port: "22"}, "/d", true},
		{"/run/user/1000/gvfs/sftp:host=h,user=j%C3%BCrgen/d", gvfsInfo{Host: "h", User: "jürgen"}, "/d", true},
		{"/run/user/1000/gvfs/sftp:host=h,user=/d", gvfsInfo{Host: "h"}, "/d", true}, // an empty user is no user
		{"/run/user/1000/gvfs/sftp:host=h,user=%zz/d", gvfsInfo{}, "", false},        // bad escape
		{"/run/user/1000/gvfs/ssh:host=h", gvfsInfo{Host: "h"}, "/", true},
		{"/run/user/1000/gvfs/sftp:host=h/../../secret", gvfsInfo{}, "", false}, // cleaned: leaves the mount
		{"/run/user/1000/gvfs/smb-share:server=s,share=x/y", gvfsInfo{}, "", false},
		{"/home/me/data", gvfsInfo{}, "", false},
		{"/run/user/1000/gvfs/sftp:user=me/x", gvfsInfo{}, "", false}, // no host
	} {
		g, rest, ok := parseGVFS(c.in)
		if ok != c.ok || g != c.want || rest != c.rest {
			t.Errorf("%q: got %+v %q %v, want %+v %q %v", c.in, g, rest, ok, c.want, c.rest, c.ok)
		}
	}
}

func TestMatchHost(t *testing.T) {
	list := []hosts.Host{
		{ID: 1, Dest: "cluster"},
		{ID: 2, Dest: "other.example"},
		{ID: 3, Dest: "me@login.hpc.example"},
		{ID: 4, Dest: "login.hpc.example"},
	}
	alias := []hosts.SSHHost{{Alias: "cluster", HostName: "login.hpc.example", User: "alice"}}
	for _, c := range []struct {
		g    gvfsInfo
		want int64
	}{
		{gvfsInfo{Host: "other.example"}, 2},
		{gvfsInfo{Host: "login.hpc.example", User: "me"}, 3}, // same user wins over an alias for another user
		{gvfsInfo{Host: "login.hpc.example", User: "alice"}, 1},
		{gvfsInfo{Host: "LOGIN.hpc.example", User: "alice"}, 1},
		{gvfsInfo{Host: "unknown.example"}, 0},
		{gvfsInfo{Host: "other.example", User: "bob"}, 2}, // the host states no user: the ssh configuration decides
	} {
		h, ok := matchHost(list, alias, c.g)
		if (c.want == 0) == ok || h.ID != c.want {
			t.Errorf("%+v: got %d %v, want %d", c.g, h.ID, ok, c.want)
		}
	}
}

func chooseServer(t *testing.T, ch Chooser, hs []hosts.Host) *httptest.Server {
	t.Helper()
	s := &Server{Chooser: ch}
	if len(hs) > 0 {
		cat, err := catalog.Open("file:" + filepath.Join(t.TempDir(), "c.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cat.Close() })
		db, _ := hosts.Open(cat.DB())
		for _, h := range hs {
			h.Python, h.Roots = "python3", []string{"/data"}
			if _, err := db.Create(h); err != nil {
				t.Fatal(err)
			}
		}
		s.HostDB = db
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestChooseReturnsThePathAndMapsAGVFSMountToAHost(t *testing.T) {
	var gotMode, gotTitle string
	var gotExt []string
	next := "/home/me/data"
	ts := chooseServer(t, func(_ context.Context, mode, title string, ext []string) (string, error) {
		gotMode, gotTitle, gotExt = mode, title, ext
		return next, nil
	}, []hosts.Host{{Name: "Cluster", Dest: "login.hpc.example"}})

	r, b := postJSON(t, ts.URL+"/api/fs/choose", map[string]any{"mode": "file", "title": "Pick\ncube", "ext": []string{".fits"}})
	if r.StatusCode != 200 || gotMode != "file" || gotTitle != "Pickcube" || !reflect.DeepEqual(gotExt, []string{".fits"}) {
		t.Fatalf("%d %s %q %q %v", r.StatusCode, b, gotMode, gotTitle, gotExt)
	}
	if string(b) != `{"path":"/home/me/data","hostId":null,"remotePath":null,"gvfs":null}`+"\n" {
		t.Errorf("local: %s", b)
	}

	next = "/run/user/1000/gvfs/sftp:host=login.hpc.example,user=me/shared/vgt"
	_, b = postJSON(t, ts.URL+"/api/fs/choose", map[string]any{"mode": "folder"})
	var out struct {
		Path       string
		HostID     *int64
		RemotePath *string
		GVFS       *gvfsInfo
	}
	json.Unmarshal(b, &out)
	if out.HostID == nil || *out.HostID != 1 || out.RemotePath == nil || *out.RemotePath != "/shared/vgt" || out.GVFS != nil {
		t.Errorf("registered host: %s", b)
	}

	next = "/run/user/1000/gvfs/sftp:host=new.example,user=me,port=2222/x"
	_, b = postJSON(t, ts.URL+"/api/fs/choose", map[string]any{"mode": "folder"})
	out.HostID, out.RemotePath, out.GVFS = nil, nil, nil
	json.Unmarshal(b, &out)
	if out.HostID != nil || out.RemotePath != nil || out.GVFS == nil || *out.GVFS != (gvfsInfo{Host: "new.example", User: "me", Port: "2222"}) {
		t.Errorf("unknown host offers to add it: %s", b)
	}
}

func TestChooseOutcomes(t *testing.T) {
	var err error
	ts := chooseServer(t, func(context.Context, string, string, []string) (string, error) { return "/x", err }, nil)
	for _, c := range []struct {
		err  error
		want int
	}{{errNoChooser, 501}, {errCancelled, 204}, {errTimedOut, 408}, {io.ErrUnexpectedEOF, 502}} {
		err = c.err
		if r, _ := postJSON(t, ts.URL+"/api/fs/choose", map[string]any{"mode": "folder"}); r.StatusCode != c.want {
			t.Errorf("%v: %d, want %d", c.err, r.StatusCode, c.want)
		}
	}
	err = nil
	for name, in := range map[string]map[string]any{
		"mode":      {"mode": "both"},
		"ext":       {"mode": "file", "ext": []string{"fits"}},
		"ext shell": {"mode": "file", "ext": []string{".f;rm"}},
		"unknown":   {"mode": "file", "other": 1},
	} {
		if r, _ := postJSON(t, ts.URL+"/api/fs/choose", in); r.StatusCode != 400 {
			t.Errorf("%s: %d, want 400", name, r.StatusCode)
		}
	}
}

func TestOnlyOneChooserAtATime(t *testing.T) {
	open, release := make(chan struct{}), make(chan struct{})
	ts := chooseServer(t, func(context.Context, string, string, []string) (string, error) {
		close(open)
		<-release
		return "/x", nil
	}, nil)
	done := make(chan int)
	go func() {
		r, _ := postJSON(t, ts.URL+"/api/fs/choose", map[string]any{"mode": "folder"})
		done <- r.StatusCode
	}()
	<-open
	if r, _ := postJSON(t, ts.URL+"/api/fs/choose", map[string]any{"mode": "folder"}); r.StatusCode != 409 {
		t.Errorf("second chooser: %d, want 409", r.StatusCode)
	}
	close(release)
	if c := <-done; c != 200 {
		t.Errorf("first: %d", c)
	}
}
