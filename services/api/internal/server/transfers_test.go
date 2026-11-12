package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/notify"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/transfers"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// xferEnv is a queue with a "host" that is a folder on this machine, reached through a stand-in ssh that runs the remote
// command in place. Files in base switch its behaviour: DOWN (the connection is refused), DROP ("<folder> <bytes>": the
// next rsync server is cut once that folder holds that many bytes), CORRUPT (the host's sha256sum lies), NOSHA (the host has
// no hash tool).
type xferEnv struct {
	t             *testing.T
	srv           *Server
	api           func(string) string
	base          string
	local, remote string
	host          hosts.Host
	store         *transfers.Store
	notes         *notify.Store
	cat           *catalog.Store
	au            *audit.Store
	mgr           *hosts.Manager
}

func needRsyncBin(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync is not installed")
	}
}

func newXferEnv(t *testing.T) *xferEnv {
	t.Helper()
	needRsyncBin(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	local, remote := filepath.Join(base, "local"), filepath.Join(base, "remote")
	for _, d := range []string{local, remote, filepath.Join(base, "bin"), filepath.Join(base, "minbin")} {
		os.MkdirAll(d, 0o755)
	}
	for _, tool := range []string{"sh", "bash", "cat", "cut", "wc", "tr", "head", "sed", "dirname", "mkdir", "rm", "mv", "cp", "rsync", "id", "ls", "sleep"} {
		if p, err := exec.LookPath(tool); err == nil {
			os.Symlink(p, filepath.Join(base, "minbin", tool))
		}
	}
	realSum, _ := exec.LookPath("sha256sum")
	os.WriteFile(filepath.Join(base, "bin", "sha256sum"), []byte(fmt.Sprintf("#!/bin/sh\nif [ -e %q ]; then cat > /dev/null; echo '%s  -'; else exec %s \"$@\"; fi\n",
		filepath.Join(base, "CORRUPT"), strings.Repeat("0", 64), realSum)), 0o755)
	script := fmt.Sprintf(`#!/bin/sh
while [ $# -gt 0 ]; do case "$1" in -o) shift 2;; -T) shift;; *) break;; esac; done
shift
cmd=$(printf '%%s' "$*" | sed -e 's#bash -lc#bash -c#')
printf '%%s\n' "$cmd" >> %[1]s/ssh.log
[ -e %[1]s/DOWN ] && exit 255
case "$cmd" in
*"rsync --server"*)
  if [ -f %[1]s/DROP ]; then
    read d n < %[1]s/DROP; rm -f %[1]s/DROP
    me=$$
    ( while kill -0 $me 2>/dev/null; do s=$(cat "$d"/.[!.]* "$d"/.vesqira-partial/* 2>/dev/null | wc -c); [ "$s" -ge "$n" ] && break; sleep 0.01; done; kill -KILL $me 2>/dev/null ) >/dev/null 2>&1 &
  fi;;
esac
if [ -e %[1]s/NOSHA ]; then PATH=%[1]s/minbin; else PATH=%[1]s/bin:$PATH; fi
export PATH
# The rsync server must BE this process: dash (Ubuntu's /bin/sh) forks for "sh -c", so killing $me would orphan the server and
# the copy would finish under the final name instead of being cut.
case "$cmd" in *"rsync --server"*) eval "exec $cmd";; *) exec sh -c "$cmd";; esac
`, base)
	sshBin := filepath.Join(base, "ssh")
	os.WriteFile(sshBin, []byte(script), 0o755)

	mgr := hosts.NewManager(sshBin, nil)
	mgr.ControlDir = t.TempDir()
	t.Cleanup(mgr.Close)
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	hs, _ := hosts.Open(cat.DB())
	ts, err := transfers.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	ns, _ := notify.Open(cat.DB())
	au, _ := audit.Open(cat.DB())
	dv, _ := derived.Open(cat.DB())
	rs, _ := sourceroot.New([]string{local})
	srv := &Server{Roots: rs, Store: cat, HostDB: hs, Remote: mgr, Transfers: ts, Notes: ns, Audit: au, Derived: dv}
	api := httptest.NewServer(srv.Handler())
	t.Cleanup(api.Close)
	h, err := hs.Create(hosts.Host{Name: "Cluster", Dest: "login1", Roots: []string{remote}})
	if err != nil {
		t.Fatal(err)
	}
	return &xferEnv{t: t, srv: srv, api: func(p string) string { return api.URL + p }, base: base, local: local, remote: remote, host: h, store: ts, notes: ns, cat: cat, au: au, mgr: mgr}
}

func (e *xferEnv) flag(name string, on bool, content string) {
	p := filepath.Join(e.base, name)
	if on {
		os.WriteFile(p, []byte(content), 0o644)
	} else {
		os.Remove(p)
	}
}

func (e *xferEnv) file(dir, name string, size int) []byte {
	e.t.Helper()
	b := make([]byte, size)
	rand.New(rand.NewSource(int64(size))).Read(b)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		e.t.Fatal(err)
	}
	return b
}

func (e *xferEnv) create(in map[string]any) (int, transfers.Transfer, string) {
	e.t.Helper()
	if _, ok := in["hostId"]; !ok {
		in["hostId"] = e.host.ID
	}
	r, b := postJSON(e.t, e.api("/api/transfers"), in)
	var out struct {
		Transfer transfers.Transfer `json:"transfer"`
	}
	_ = json.Unmarshal(b, &out)
	return r.StatusCode, out.Transfer, string(b)
}

func (e *xferEnv) mustCreate(in map[string]any) transfers.Transfer {
	e.t.Helper()
	code, tr, body := e.create(in)
	if code != 201 {
		e.t.Fatalf("create: %d %s", code, body)
	}
	return tr
}

func (e *xferEnv) download(name string, extra map[string]any) transfers.Transfer {
	in := map[string]any{"direction": "download", "sourcePath": filepath.Join(e.remote, name), "destDir": e.local}
	for k, v := range extra {
		in[k] = v
	}
	return e.mustCreate(in)
}

func (e *xferEnv) upload(name string, extra map[string]any) transfers.Transfer {
	in := map[string]any{"direction": "upload", "sourcePath": filepath.Join(e.local, name), "destDir": e.remote}
	for k, v := range extra {
		in[k] = v
	}
	return e.mustCreate(in)
}

// pass runs one pass of the queue at time now and waits for the transfers it started.
func (e *xferEnv) pass(now time.Time) {
	e.srv.transferOnce(context.Background(), now)
	e.srv.waitTransfers()
}

// run passes until nothing is due, as the queue would over time; it fails if a transfer keeps being retried.
func (e *xferEnv) run() {
	e.t.Helper()
	for i := 0; i < 6; i++ {
		due, _ := e.store.Due(time.Now())
		if len(due) == 0 {
			return
		}
		e.pass(time.Now())
	}
}

func (e *xferEnv) get(id int64) transfers.Transfer {
	e.t.Helper()
	tr, err := e.store.Get(id)
	if err != nil {
		e.t.Fatal(err)
	}
	return tr
}

func (e *xferEnv) notifications() []notify.Notification {
	ns, _, _ := e.notes.List(workspace.Local, "local-owner", 0)
	return ns
}

func (e *xferEnv) sshCalls(sub string) int {
	b, _ := os.ReadFile(filepath.Join(e.base, "ssh.log"))
	return strings.Count(string(b), sub)
}

// sshLines is how many commands were sent to the host so far.
func (e *xferEnv) sshLines() int {
	b, _ := os.ReadFile(filepath.Join(e.base, "ssh.log"))
	return strings.Count(string(b), "\n")
}

func sha(b []byte) string { return hosts.SHA256Hex(b) }

// ---- the ordinary path ----

func TestDownloadArrivesVerifiedAndIsAnnouncedOnce(t *testing.T) {
	t.Parallel()
	e := newXferEnv(t)
	want := e.file(e.remote, "cube.fits", 3<<20)
	tr := e.download("cube.fits", nil)
	if tr.State != transfers.StateQueued || tr.Policy != transfers.PolicyAsk || !tr.Compress {
		t.Fatalf("%+v", tr)
	}
	e.run()
	got := e.get(tr.ID)
	if got.State != transfers.StateDone || got.Verif.Status != transfers.Verified || got.Verif.Method != transfers.MethodSHA256 {
		t.Fatalf("%+v", got)
	}
	if got.Verif.SourceSHA256 != sha(want) || got.Verif.DestSHA256 != sha(want) || got.Verif.Bytes != int64(len(want)) || got.Kind != "file" || got.TotalBytes != int64(len(want)) {
		t.Fatalf("%+v", got)
	}
	b, _ := os.ReadFile(filepath.Join(e.local, "cube.fits"))
	if !bytes.Equal(b, want) {
		t.Fatal("the downloaded bytes differ")
	}
	if es, _ := os.ReadDir(e.local); len(es) != 1 {
		t.Errorf("leftovers next to the download: %v", es)
	}
	ns := e.notifications()
	if len(ns) != 1 || ns[0].Kind != notify.KindTransferDone || !strings.Contains(ns[0].Title, "verified") || !strings.Contains(ns[0].Title, "cube.fits") {
		t.Fatalf("%+v", ns)
	}
	e.pass(time.Now().Add(time.Hour)) // nothing is left to do, and nothing is announced twice
	if len(e.notifications()) != 1 {
		t.Errorf("announced twice")
	}
	if rows, _ := e.au.List(workspace.Local, "", 0, 50); !strings.Contains(fmt.Sprint(rows), "transfer queued") || !strings.Contains(fmt.Sprint(rows), "transfer done") {
		t.Errorf("audit: %v", rows)
	}
}

func TestUploadArrivesVerified(t *testing.T) {
	t.Parallel()
	e := newXferEnv(t)
	want := e.file(e.local, "maps.fits", 1<<20)
	tr := e.upload("maps.fits", map[string]any{"bwlimitKBps": 0, "compress": false})
	if tr.Kind != "file" || tr.TotalBytes != int64(len(want)) || tr.Compress {
		t.Fatalf("%+v", tr)
	}
	e.run()
	got := e.get(tr.ID)
	b, _ := os.ReadFile(filepath.Join(e.remote, "maps.fits"))
	if got.State != transfers.StateDone || got.Verif.Status != transfers.Verified || !bytes.Equal(b, want) {
		t.Fatalf("%+v", got)
	}
}

func TestFolderTransfersAreComparedByContentAndSaySo(t *testing.T) {
	t.Parallel()
	e := newXferEnv(t)
	e.file(filepath.Join(e.remote, "run1"), "a.fits", 5000)
	e.file(filepath.Join(e.remote, "run1", "sub dir"), "b it's.fits", 7000)
	tr := e.download("run1", nil)
	e.run()
	got := e.get(tr.ID)
	if got.State != transfers.StateDone || got.Kind != "dir" || got.Verif.Status != transfers.Verified || got.Verif.Method != transfers.MethodChecksum {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Verif.Note, "not SHA-256") {
		t.Errorf("the note must say this is rsync's checksum: %q", got.Verif.Note)
	}
	if _, err := os.Stat(filepath.Join(e.local, "run1", "sub dir", "b it's.fits")); err != nil {
		t.Error(err)
	}
	// uploading it back under another name goes the other way and is verified the same way
	up := e.mustCreate(map[string]any{"direction": "upload", "sourcePath": filepath.Join(e.local, "run1"), "destDir": e.remote, "name": "run1-copy"})
	e.run()
	if g := e.get(up.ID); g.State != transfers.StateDone || g.Verif.Status != transfers.Verified || g.Verif.Method != transfers.MethodChecksum {
		t.Fatalf("%+v", g)
	}
	if _, err := os.Stat(filepath.Join(e.remote, "run1-copy", "sub dir", "b it's.fits")); err != nil {
		t.Error(err)
	}
}

func TestOneTransferPerHostAtATime(t *testing.T) {
	t.Parallel()
	e := newXferEnv(t)
	e.file(e.remote, "a.fits", 4<<20)
	e.file(e.remote, "b.fits", 4<<20)
	// a second host on the same machine
	h2, _ := e.srv.HostDB.Create(hosts.Host{Name: "Other", Dest: "login2", Roots: []string{e.remote}})
	a := e.download("a.fits", map[string]any{"bwlimitKBps": 2000})
	b := e.download("b.fits", map[string]any{"bwlimitKBps": 2000})
	c := e.mustCreate(map[string]any{"direction": "download", "hostId": h2.ID, "sourcePath": filepath.Join(e.remote, "b.fits"), "destDir": e.local, "name": "b2.fits", "bwlimitKBps": 2000})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.srv.transferOnce(ctx, time.Now())
	states := map[int64]string{a.ID: e.get(a.ID).State, b.ID: e.get(b.ID).State, c.ID: e.get(c.ID).State}
	if states[a.ID] != transfers.StateRunning || states[b.ID] != transfers.StateQueued || states[c.ID] != transfers.StateRunning {
		t.Fatalf("one at a time per host, hosts in parallel: %v", states)
	}
	e.srv.transferOnce(ctx, time.Now()) // another pass while a is running does not start b
	if e.get(b.ID).State != transfers.StateQueued {
		t.Fatalf("a second transfer started on a busy host")
	}
	cancel()
	e.srv.waitTransfers()
}

// ---- authorisation ----

func TestPathsAreAuthorisedOnBothSides(t *testing.T) {
	t.Parallel()
	e := newXferEnv(t)
	e.file(e.remote, "ok.fits", 100)
	e.file(e.local, "ok.fits", 100)
	outside := t.TempDir()
	e.file(outside, "secret.fits", 100)
	os.Symlink(outside, filepath.Join(e.local, "out"))
	os.Symlink(filepath.Join(outside, "secret.fits"), filepath.Join(e.local, "secret-link.fits"))
	for name, c := range map[string]struct {
		in   map[string]any
		code int
	}{
		"download from outside the host's roots":         {map[string]any{"direction": "download", "sourcePath": "/etc/passwd", "destDir": e.local}, 422},
		"download into a folder outside the local roots": {map[string]any{"direction": "download", "sourcePath": filepath.Join(e.remote, "ok.fits"), "destDir": outside}, 403},
		"download into a link that leaves the roots":     {map[string]any{"direction": "download", "sourcePath": filepath.Join(e.remote, "ok.fits"), "destDir": filepath.Join(e.local, "out")}, 403},
		"download into a file":                           {map[string]any{"direction": "download", "sourcePath": filepath.Join(e.remote, "ok.fits"), "destDir": filepath.Join(e.local, "ok.fits")}, 403},
		"download with a newline in the path":            {map[string]any{"direction": "download", "sourcePath": e.remote + "/a\nb", "destDir": e.local}, 422},
		"download with a glob in the path":               {map[string]any{"direction": "download", "sourcePath": e.remote + "/*.fits", "destDir": e.local}, 422},
		"download with a path that climbs":               {map[string]any{"direction": "download", "sourcePath": e.remote + "/../ok.fits", "destDir": e.local}, 422},
		"download with a name that is a path":            {map[string]any{"direction": "download", "sourcePath": filepath.Join(e.remote, "ok.fits"), "destDir": e.local, "name": "../x"}, 422},
		"download named like the partial folder":         {map[string]any{"direction": "download", "sourcePath": filepath.Join(e.remote, "ok.fits"), "destDir": e.local, "name": ".vesqira-partial"}, 422},
		"upload of a file outside the local roots":       {map[string]any{"direction": "upload", "sourcePath": filepath.Join(outside, "secret.fits"), "destDir": e.remote}, 403},
		"upload of a link that leaves the local roots":   {map[string]any{"direction": "upload", "sourcePath": filepath.Join(e.local, "secret-link.fits"), "destDir": e.remote}, 403},
		"upload through a folder link":                   {map[string]any{"direction": "upload", "sourcePath": filepath.Join(e.local, "out", "secret.fits"), "destDir": e.remote}, 403},
		"upload of a path that does not exist":           {map[string]any{"direction": "upload", "sourcePath": filepath.Join(e.local, "nope.fits"), "destDir": e.remote}, 403},
		"upload into a folder outside the host's roots":  {map[string]any{"direction": "upload", "sourcePath": filepath.Join(e.local, "ok.fits"), "destDir": "/tmp"}, 422},
		"upload to a host folder with a newline":         {map[string]any{"direction": "upload", "sourcePath": filepath.Join(e.local, "ok.fits"), "destDir": e.remote + "/a\nb"}, 422},
		"unknown direction":                              {map[string]any{"direction": "sideways", "sourcePath": filepath.Join(e.local, "ok.fits"), "destDir": e.remote}, 422},
		"unknown policy":                                 {map[string]any{"direction": "upload", "sourcePath": filepath.Join(e.local, "ok.fits"), "destDir": e.remote, "policy": "overwrite"}, 422},
		"negative bandwidth":                             {map[string]any{"direction": "upload", "sourcePath": filepath.Join(e.local, "ok.fits"), "destDir": e.remote, "bwlimitKBps": -5}, 422},
		"unknown host":                                   {map[string]any{"direction": "upload", "hostId": 999, "sourcePath": filepath.Join(e.local, "ok.fits"), "destDir": e.remote}, 404},
		"an unknown field":                               {map[string]any{"direction": "upload", "sourcePath": filepath.Join(e.local, "ok.fits"), "destDir": e.remote, "rsyncArgs": "--delete"}, 400},
	} {
		if code, _, body := e.create(c.in); code != c.code {
			t.Errorf("%s: %d, want %d (%s)", name, code, c.code, body)
		}
	}
	if list, _ := e.store.List(0); len(list) != 0 {
		t.Fatalf("a refused request was queued: %v", list)
	}
	if e.sshCalls("rsync --server") != 0 {
		t.Fatal("something was sent to the host for a refused request")
	}
	// an upload whose source is a link inside the roots to a file inside the roots is the file it points to, canonicalised
	os.Symlink(filepath.Join(e.local, "ok.fits"), filepath.Join(e.local, "inside-link.fits"))
	tr := e.mustCreate(map[string]any{"direction": "upload", "sourcePath": filepath.Join(e.local, "inside-link.fits"), "destDir": e.remote})
	if tr.SourcePath != filepath.Join(e.local, "ok.fits") || tr.Name != "ok.fits" {
		t.Errorf("the source is stored canonicalised: %+v", tr)
	}
}

// A folder on the host that is inside a root by its name but leaves it through a link is refused by the host's own check.
func TestAHostLinkOutOfTheRootFailsTheTransferWithTheReason(t *testing.T) {
	t.Parallel()
	e := newXferEnv(t)
	outside := t.TempDir()
	e.file(outside, "secret.fits", 100)
	os.Symlink(outside, filepath.Join(e.remote, "out"))
	tr := e.download("out/secret.fits", nil)
	e.run()
	got := e.get(tr.ID)
	if got.State != transfers.StateFailed || !strings.Contains(got.Detail, "outside") {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(filepath.Join(e.local, "secret.fits")); err == nil {
		t.Fatal("a file outside the host's roots was downloaded")
	}
	// the same for the destination of an upload
	e.file(e.local, "ok.fits", 100)
	up := e.mustCreate(map[string]any{"direction": "upload", "sourcePath": filepath.Join(e.local, "ok.fits"), "destDir": filepath.Join(e.remote, "out")})
	e.run()
	if g := e.get(up.ID); g.State != transfers.StateFailed {
		t.Fatalf("%+v", g)
	}
	if _, err := os.Stat(filepath.Join(outside, "ok.fits")); err == nil {
		t.Fatal("a file was written outside the host's roots")
	}
	// a link as the source itself is not followed
	os.Symlink(filepath.Join(e.remote, "a.fits"), filepath.Join(e.remote, "l.fits"))
	e.file(e.remote, "a.fits", 10)
	l := e.download("l.fits", nil)
	e.run()
	if g := e.get(l.ID); g.State != transfers.StateFailed || !strings.Contains(g.Detail, "symbolic link") {
		t.Fatalf("%+v", g)
	}
}

func TestRsyncMissingIsRefusedWithTheReasonAndNoFallback(t *testing.T) {
	t.Parallel()
	e := newXferEnv(t)
	e.file(e.remote, "a.fits", 10)
	e.mgr.Rsync = filepath.Join(e.base, "no-such-rsync")
	code, _, body := e.create(map[string]any{"direction": "download", "sourcePath": filepath.Join(e.remote, "a.fits"), "destDir": e.local})
	if code != 422 || !strings.Contains(body, "rsync is not installed on this computer") {
		t.Fatalf("%d %s", code, body)
	}
	e.mgr.Rsync = ""
	// the host has none: a canned answer to the probe
	bin := filepath.Join(e.base, "norsync")
	os.WriteFile(bin, []byte("#!/bin/sh\necho 'vesqira-probe: path '\necho 'vesqira-probe: sha yes'\n"), 0o755)
	e.mgr.Ssh = bin
	tr := e.download("a.fits", nil)
	e.run()
	got := e.get(tr.ID)
	if got.State != transfers.StateFailed || !strings.Contains(got.Detail, "rsync is not installed on Cluster") || !strings.Contains(got.Detail, "no scp or sftp fallback") {
		t.Fatalf("%+v", got)
	}
}

func TestUploadWarnsWhenTheHostsLastLookSaysItWillNotFit(t *testing.T) {
	t.Parallel()
	e := newXferEnv(t)
	e.file(e.local, "big.fits", 2<<20)
	avail := int64(1 << 20)
	soft, hard := int64(10<<20), int64(12<<20)
	e.srv.disk.mu.Lock()
	e.srv.disk.look = map[int64]*storageEntry{e.host.ID: {rep: &hosts.StorageReport{CheckedAt: time.Now(),
		Filesystems: []hosts.Filesystem{{Root: e.remote, Mount: "/", AvailBytes: avail, TotalBytes: 1 << 40, UsedBytes: 1 << 39}}}}}
	e.srv.disk.mu.Unlock()
	tr := e.upload("big.fits", nil)
	if !strings.Contains(tr.Warning, "available") || !strings.Contains(tr.Warning, "probably fail") {
		t.Fatalf("no free-space warning: %q", tr.Warning)
	}
	// the quota says the upload would pass the limits
	e.srv.disk.mu.Lock()
	e.srv.disk.look[e.host.ID].rep = &hosts.StorageReport{CheckedAt: time.Now(),
		Filesystems: []hosts.Filesystem{{Root: e.remote, AvailBytes: 1 << 40}},
		Quota:       &hosts.Quota{Tool: "beegfs-ctl", Entries: []hosts.QuotaEntry{{Scope: "home", UsedBytes: 9 << 20, SoftBytes: &soft, HardBytes: &hard}}}}
	e.srv.disk.mu.Unlock()
	if w := e.upload("big.fits", map[string]any{"name": "again.fits"}).Warning; !strings.Contains(w, "soft limit") {
		t.Fatalf("no quota warning: %q", w)
	}
	// a roomy host and a host never looked at say nothing; the warning never blocks the upload
	e.srv.disk.mu.Lock()
	e.srv.disk.look[e.host.ID].rep = &hosts.StorageReport{CheckedAt: time.Now(), Filesystems: []hosts.Filesystem{{Root: e.remote, AvailBytes: 1 << 40}}}
	e.srv.disk.mu.Unlock()
	if w := e.upload("big.fits", map[string]any{"name": "third.fits"}).Warning; w != "" {
		t.Fatalf("a warning for a roomy host: %q", w)
	}
	e.srv.disk.mu.Lock()
	delete(e.srv.disk.look, e.host.ID)
	e.srv.disk.mu.Unlock()
	if w := e.upload("big.fits", map[string]any{"name": "fourth.fits"}).Warning; w != "" {
		t.Fatalf("a warning with no storage look: %q", w)
	}
	if e.sshLines() != 0 {
		t.Errorf("the warning called the host")
	}
}
