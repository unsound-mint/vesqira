package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/review"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// fakeSSH stands in for the ssh client: it drops the ssh options and the destination, runs the remote command on this
// machine with $HOME redirected to a scratch "remote home", and can pretend the host is unreachable.
func fakeSSH(t *testing.T, remoteHome string) string {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
[ -e %q ] && exit 255
while [ $# -gt 0 ]; do case "$1" in -o) shift 2;; -T) shift;; *) break;; esac; done
shift
cmd=$(printf '%%s' "$*" | sed 's#\$HOME#%s#g')
exec sh -c "$cmd"
`, filepath.Join(remoteHome, "DOWN"), remoteHome)
	p := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// The second laptop or the cluster login node: cubes stay on the host, planes come over the worker's channel, and the
// host's own worker enforces the roots. Run against a fake ssh and the real worker.
func TestRemoteHostEndToEndWithFakeSSH(t *testing.T) {
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	base := t.TempDir()
	remoteHome := filepath.Join(base, "remote-home")
	os.MkdirAll(remoteHome, 0o755)
	remoteRoot := filepath.Join(base, "remote_data")
	outside := filepath.Join(base, "private_elsewhere")
	for _, d := range []string{remoteRoot, outside, filepath.Join(base, "localroot")} {
		os.MkdirAll(d, 0o755)
	}
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), remoteRoot)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cube, _ := os.ReadFile(filepath.Join(remoteRoot, "hi_cube.fits"))
	os.WriteFile(filepath.Join(outside, "secret.fits"), cube, 0o644)
	os.Symlink(filepath.Join(outside, "secret.fits"), filepath.Join(remoteRoot, "escape.fits"))
	localRoot := filepath.Join(base, "localroot")
	os.WriteFile(filepath.Join(localRoot, "hi_cube.fits"), cube, 0o644)

	files := map[string][]byte{}
	for _, f := range []string{"plane_worker.py", "fits_header.py"} {
		b, err := os.ReadFile(filepath.Join(repo, "services/scientific-worker", f))
		if err != nil {
			t.Fatal(err)
		}
		files[f] = b
	}
	mgr := hosts.NewManager(fakeSSH(t, remoteHome), files)
	mgr.ControlDir = t.TempDir()
	t.Cleanup(mgr.Close)
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	hs, _ := hosts.Open(cat.DB())
	bs, _ := boards.Open(cat.DB(), base)
	rs, _ := sourceroot.New([]string{localRoot})
	local := &worker.Persistent{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(local.Close)
	hdr := headerWorker(t, "python3", repo)
	ts := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: hdr, Planes: local, Cache: NewPlaneCache(8 << 20), Boards: bs, HostDB: hs, Remote: mgr}).Handler())
	defer ts.Close()
	rv := httptest.NewServer((&review.Handler{Store: bs}).Handler())
	defer rv.Close()
	api := func(p string) string { return ts.URL + p }

	// hosts are validated: nothing that could become an ssh option, no relative or root-wide roots
	for name, in := range map[string]map[string]any{
		"option injection": {"name": "x", "dest": "-oProxyCommand=evil", "roots": []string{remoteRoot}},
		"spaces":           {"name": "x", "dest": "a b", "roots": []string{remoteRoot}},
		"relative root":    {"name": "x", "dest": "ok", "roots": []string{"data"}},
		"filesystem root":  {"name": "x", "dest": "ok", "roots": []string{"/"}},
		"no roots":         {"name": "x", "dest": "ok", "roots": []string{}},
		"odd python":       {"name": "x", "dest": "ok", "python": "python3; rm -rf /", "roots": []string{remoteRoot}},
	} {
		if r, _ := postJSON(t, api("/api/hosts"), in); r.StatusCode != 422 {
			t.Errorf("%s: %d, want 422", name, r.StatusCode)
		}
	}
	// the remote command runs with a minimal PATH, so name the interpreter that has astropy (as a user would for a host)
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	r, hb := postJSON(t, api("/api/hosts"), map[string]any{"name": "Second laptop", "dest": "laptop2", "python": py, "roots": []string{remoteRoot}})
	if r.StatusCode != 201 {
		t.Fatalf("create host: %d %s", r.StatusCode, hb)
	}
	var h hosts.Host
	json.Unmarshal(hb, &h)
	hp := func(p string) string { return api("/api/hosts/" + itoa(int(h.ID)) + p) }

	// check: deploys the worker by content hash and reports that the roots are enforced there
	r, cb := postJSON(t, hp("/check"), map[string]any{})
	if r.StatusCode != 200 || !strings.Contains(string(cb), `"enforces_roots": true`) {
		t.Fatalf("check: %d %s", r.StatusCode, cb)
	}
	if _, err := os.Stat(filepath.Join(remoteHome, ".cache", "vesqira", mgr.Hash(), "plane_worker.py")); err != nil {
		t.Errorf("worker not deployed under its hash: %v", err)
	}

	// browse and register a remote cube
	_, lb := get(t, hp("/list"))
	if !strings.Contains(string(lb), `"hi_cube.fits"`) || strings.Contains(string(lb), "escape.fits") {
		t.Errorf("listing must show the cube and hide the link that leaves the root: %s", lb)
	}
	for _, p := range []string{filepath.Join(outside, "secret.fits"), filepath.Join(remoteRoot, "escape.fits"), filepath.Join(remoteRoot, "..", "private_elsewhere", "secret.fits"), "/etc/passwd"} {
		if r, _ := postJSON(t, hp("/register"), map[string]any{"path": p}); r.StatusCode != 403 {
			t.Errorf("registering %s: %d, want 403", p, r.StatusCode)
		}
	}
	r, db := postJSON(t, hp("/register"), map[string]any{"path": filepath.Join(remoteRoot, "hi_cube.fits")})
	if r.StatusCode != 201 {
		t.Fatalf("register remote: %d %s", r.StatusCode, db)
	}
	// completion on the host names entries even outside the host's roots, and only names
	rc, cb := get(t, hp("/complete?kind=file&ext=.fits&prefix="+url.QueryEscape(outside+"/sec")))
	var comp completion
	json.Unmarshal(cb, &comp)
	if rc.StatusCode != 200 || len(comp.Entries) != 1 || comp.Entries[0] != (completeEntry{Name: "secret.fits", Path: filepath.Join(outside, "secret.fits")}) {
		t.Errorf("host completion: %d %s", rc.StatusCode, cb)
	}
	if rc, _ := get(t, hp("/complete?prefix=relative")); rc.StatusCode != 400 {
		t.Errorf("host completion with a relative prefix: %d", rc.StatusCode)
	}
	var remote catalog.Dataset
	json.Unmarshal(db, &remote)
	var localDS catalog.Dataset
	json.NewDecoder(post(t, ts, filepath.Join(localRoot, "hi_cube.fits"), nil).Body).Decode(&localDS)
	if remote.HostID != h.ID || localDS.HostID != 0 || remote.ID == localDS.ID {
		t.Fatalf("a remote file and a local file with the same name are different datasets: %+v %+v", remote, localDS)
	}

	// the same cube, read locally and through the host, gives the same pixels and the same coordinates
	plane := func(id int64) []byte {
		resp, body := get(t, api("/api/datasets/"+itoa(int(id))+"/plane?channel=40"))
		if resp.StatusCode != 200 {
			t.Fatalf("plane %d: %d %s", id, resp.StatusCode, body)
		}
		n := int(binary.LittleEndian.Uint32(body[:4]))
		return body[4+n:]
	}
	if !bytes.Equal(plane(remote.ID), plane(localDS.ID)) {
		t.Error("remote and local planes differ")
	}
	_, c1 := get(t, api("/api/datasets/"+itoa(int(remote.ID))+"/coord?x=20&y=9&channel=40"))
	_, c2 := get(t, api("/api/datasets/"+itoa(int(localDS.ID))+"/coord?x=20&y=9&channel=40"))
	if string(c1) != string(c2) {
		t.Errorf("coordinates differ across hosts: %s vs %s", c1, c2)
	}
	if r, _ := get(t, api("/api/compat?a="+itoa(int(remote.ID))+"&b="+itoa(int(localDS.ID)))); r.StatusCode != 200 {
		t.Errorf("compat across hosts: %d", r.StatusCode)
	}

	// a figure of a remote cube reaches a reviewer without the host, its address or any remote path
	_, fb := postJSON(t, api("/api/figures"), map[string]any{"datasetId": remote.ID, "title": "Remote peak", "view": map[string]any{"kind": "plane", "channel": 40}, "previewPng": testPNG(t)})
	var f boards.Figure
	json.Unmarshal(fb, &f)
	_, bb := postJSON(t, api("/api/boards"), map[string]any{"title": "Hosts"})
	var b boards.Board
	json.Unmarshal(bb, &b)
	postJSON(t, api("/api/boards/"+itoa(int(b.ID))+"/items"), map[string]any{"figureId": f.ID})
	_, pub := postJSON(t, api("/api/boards/"+itoa(int(b.ID))+"/publish"), map[string]any{"hours": 1})
	var p struct{ Token string }
	json.Unmarshal(pub, &p)
	_, rbody := get(t, rv.URL+"/review-api/"+p.Token)
	for _, leak := range []string{"Second laptop", "laptop2", remoteRoot, "remote_data", base, "hostId"} {
		if strings.Contains(string(rbody), leak) {
			t.Errorf("reviewer response leaks %q: %s", leak, rbody)
		}
	}

	// a changed file is refused until registered again
	os.WriteFile(filepath.Join(remoteRoot, "hi_cube.fits"), append(cube, 0), 0o644)
	if r, _ := get(t, api("/api/datasets/"+itoa(int(remote.ID))+"/plane?channel=1")); r.StatusCode != 409 {
		t.Errorf("changed remote file: %d, want 409", r.StatusCode)
	}

	// the host going away is a clear 502, not a hang or a leak, and it comes back when the host does
	os.WriteFile(filepath.Join(remoteHome, "DOWN"), nil, 0o644)
	mgr.Forget(h.ID)
	r, db = get(t, api("/api/datasets/"+itoa(int(remote.ID))+"/view"))
	if r.StatusCode != 502 || strings.Contains(string(db), "laptop2") {
		t.Errorf("unreachable host: %d %s", r.StatusCode, db)
	}
	if r, _ := get(t, api("/api/datasets/"+itoa(int(localDS.ID))+"/view")); r.StatusCode != 200 {
		t.Errorf("local files must keep working while a host is down: %d", r.StatusCode)
	}
}

// Choosing source folders on a host that is not saved yet: names of folders only, from the home directory, no credentials.
func TestBrowseHostFoldersBeforeTheHostIsSaved(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "remote-home")
	for _, d := range []string{"cubes/run1", ".ssh", "docs", ".cache"} {
		os.MkdirAll(filepath.Join(home, d), 0o755)
	}
	os.WriteFile(filepath.Join(home, "notes.txt"), []byte("x"), 0o644)
	mgr := hosts.NewManager(fakeSSH(t, home), map[string][]byte{})
	mgr.ControlDir = t.TempDir()
	srv := &Server{Remote: mgr, HostDB: &hosts.Store{}}
	get := func(q string) (int, map[string]any) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/hosts/browse?"+q, nil)
		r.Host = "127.0.0.1:8787"
		srv.Handler().ServeHTTP(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, out := get("dest=cluster")
	if code != 200 {
		t.Fatalf("home: %d %v", code, out)
	}
	var names []string
	for _, e := range out["entries"].([]any) {
		names = append(names, e.(map[string]any)["name"].(string))
	}
	if got := strings.Join(names, ","); got != ".cache,cubes,docs" { // no .ssh, no files
		t.Fatalf("entries = %s", got)
	}
	if code, out = get("dest=cluster&path=" + filepath.Join(home, "cubes")); code != 200 || out["entries"].([]any)[0].(map[string]any)["name"] != "run1" {
		t.Fatalf("sub folder: %d %v", code, out)
	}
	if code, out = get("dest=cluster&path=" + filepath.Join(home, "docs")); code != 200 || out["entries"] == nil { // an empty folder is [], never null
		t.Fatalf("empty folder: %d %v", code, out)
	}
	for _, q := range []string{"dest=-oProxyCommand=x", "dest=a;b", "dest=cluster&path=relative", "dest=cluster&path=/a/../b"} {
		if code, _ := get(q); code != 400 {
			t.Errorf("%s: %d, want 400", q, code)
		}
	}
}
