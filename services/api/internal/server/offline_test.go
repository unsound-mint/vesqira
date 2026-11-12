package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/offline"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/review"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

type offEnv struct {
	ts         *httptest.Server
	srv        *Server
	cat        *catalog.Store
	mgr        *hosts.Manager
	host       hosts.Host
	home       string
	remoteFile string
	cube       []byte
	remote     catalog.Dataset
	local      catalog.Dataset
	store      *offline.Store
	dataDir    string
	bs         *boards.Store
}

// newOfflineEnv is a cube on a "host" reached through the fake ssh (which can be taken down), the real plane worker, and a copy
// of the same cube registered as a local file.
func newOfflineEnv(t *testing.T) *offEnv {
	t.Helper()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	base := t.TempDir()
	home := filepath.Join(base, "remote-home")
	remoteRoot := filepath.Join(base, "remote_data")
	localRoot := filepath.Join(base, "localroot")
	for _, d := range []string{home, remoteRoot, localRoot} {
		os.MkdirAll(d, 0o755)
	}
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), remoteRoot)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cube, _ := os.ReadFile(filepath.Join(remoteRoot, "hi_cube.fits"))
	os.WriteFile(filepath.Join(localRoot, "hi_cube.fits"), cube, 0o644)
	mgr := hosts.NewManager(fakeSSH(t, home), workerFiles(t, repo))
	mgr.ControlDir = t.TempDir()
	t.Cleanup(mgr.Close)
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	hs, _ := hosts.Open(cat.DB())
	bs, _ := boards.Open(cat.DB(), base)
	rg, _ := regions.Open(cat.DB())
	au, _ := audit.Open(cat.DB())
	dataDir := filepath.Join(base, "offline")
	off, err := offline.Open(cat.DB(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	rs, _ := sourceroot.New([]string{localRoot})
	srv := &Server{Roots: rs, Store: cat, Worker: headerWorker(t, "python3", repo), Planes: headerWorker(t, "python3", repo), Cache: NewPlaneCache(8 << 20),
		Boards: bs, HostDB: hs, Remote: mgr, Regions: rg, Audit: au, Offline: off}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	py, _ := exec.LookPath("python3")
	h, err := hs.Create(hosts.Host{Name: "Cluster login", Dest: "laptop2", Python: py, Roots: []string{remoteRoot}})
	if err != nil {
		t.Fatal(err)
	}
	old := offlineProbeAfter
	offlineProbeAfter = 0
	t.Cleanup(func() { offlineProbeAfter = old })
	e := &offEnv{ts: ts, srv: srv, cat: cat, mgr: mgr, host: h, home: home, remoteFile: filepath.Join(remoteRoot, "hi_cube.fits"), cube: cube, store: off, dataDir: dataDir, bs: bs}
	r, b := postJSON(t, ts.URL+"/api/hosts/"+itoa(int(h.ID))+"/register", map[string]any{"path": e.remoteFile})
	if r.StatusCode != 201 {
		t.Fatalf("register remote: %d %s", r.StatusCode, b)
	}
	json.Unmarshal(b, &e.remote)
	if err := json.NewDecoder(post(t, ts, filepath.Join(localRoot, "hi_cube.fits"), nil).Body).Decode(&e.local); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *offEnv) down(t *testing.T, down bool) {
	t.Helper()
	p := filepath.Join(e.home, "DOWN")
	if down {
		os.WriteFile(p, nil, 0o644)
		e.mgr.Forget(e.host.ID) // the open ssh session dies with the VPN
		return
	}
	os.Remove(p)
}

// waitLive asks until an answer is no longer a copy: after the VPN is back, a dataset with a copy goes on being answered from it
// until the background look at the host has said the host answers (at most a request or two later).
func (e *offEnv) waitLive(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	for i := 0; i < 100; i++ {
		r, b := e.get(t, path)
		if r.Header.Get("X-Vesqira-Offline") == "" {
			return r, b
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the host is back but answers are still copies")
	return nil, nil
}

func (e *offEnv) get(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	return get(t, e.ts.URL+path)
}

func (e *offEnv) ds(path string) string { return "/api/datasets/" + itoa(int(e.remote.ID)) + path }

// frame splits a binary frame into its JSON header and payload.
func frame(t *testing.T, b []byte) (map[string]any, []byte) {
	t.Helper()
	if len(b) < 4 {
		t.Fatalf("short frame")
	}
	n := int(binary.LittleEndian.Uint32(b))
	var head map[string]any
	if err := json.Unmarshal(b[4:4+n], &head); err != nil {
		t.Fatalf("head: %v", err)
	}
	return head, b[4+n:]
}

func sansOffline(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if k != "offline" {
			out[k] = v
		}
	}
	return out
}

func putJSON(t *testing.T, url string, body any) (*http.Response, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("PUT", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

type prepState struct {
	State string
	Error string
	Items []offlineItem
}

func (e *offEnv) prepare(t *testing.T, in map[string]any) prepState {
	t.Helper()
	r, b := postJSON(t, e.ts.URL+"/api/offline/prepare", in)
	if r.StatusCode != 202 {
		t.Fatalf("prepare: %d %s", r.StatusCode, b)
	}
	e.srv.waitOffline()
	_, b = e.get(t, "/api/offline/prepare")
	var st prepState
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// The whole promise: prepared while connected, a plane, the view, the header and a saved region's spectrum are served byte for byte
// the same when the host is unreachable, marked as a copy; what was not kept fails with a sentence naming it; and when the host is
// back the answer is live again.
func TestPreparedCopyServesWhenTheHostIsUnreachable(t *testing.T) {
	e := newOfflineEnv(t)
	region := map[string]any{"name": "core", "shape": map[string]any{"shape": "circle", "cx": 20, "cy": 9, "r": 4}}
	if r, b := postJSON(t, e.ts.URL+e.ds("/regions"), region); r.StatusCode != 201 {
		t.Fatalf("region: %d %s", r.StatusCode, b)
	}
	_, vb := e.get(t, e.ds("/view"))
	var v struct{ Width, Height, Channels int }
	json.Unmarshal(vb, &v)
	if v.Channels < 20 {
		t.Fatalf("the fixture cube has %d channels", v.Channels)
	}
	step := previewStepFor(v.Width, v.Height)
	want := map[string][]byte{} // live bodies
	paths := map[string]string{
		"view": e.ds("/view"), "cards": e.ds("/header-cards"),
		"full12": e.ds("/plane?channel=12"), "prev12": e.ds("/plane?channel=12&step=" + itoa(step)),
		"spec": e.ds("/spectrum?shape=circle&cx=20&cy=9&r=4"),
	}
	for k, p := range paths {
		r, b := e.get(t, p)
		if r.StatusCode != 200 || r.Header.Get("X-Vesqira-Offline") != "" {
			t.Fatalf("live %s: %d %v", k, r.StatusCode, r.Header)
		}
		want[k] = b
	}

	// the plan reads and copies nothing, and lists the dataset with its reason
	r, b := postJSON(t, e.ts.URL+"/api/offline/plan", map[string]any{"datasetIds": []int64{e.remote.ID, e.local.ID}, "channels": map[string]int{"from": 10, "to": 12}, "detail": "full", "budgetBytes": 8 << 20})
	var plan offlinePlan
	json.Unmarshal(b, &plan)
	if r.StatusCode != 200 || len(plan.Items) != 2 || !plan.Fits || plan.EstimateBytes <= 0 {
		t.Fatalf("plan: %d %s", r.StatusCode, b)
	}
	if it := plan.Items[0]; it.Status != statusPartial || !strings.Contains(it.Reason, "planes 10–12 (full resolution)") || !strings.Contains(it.Reason, "Needs the host") {
		t.Fatalf("remote item: %+v", it)
	}
	if it := plan.Items[1]; it.Status != statusOffline || !strings.Contains(it.Reason, "on this computer") {
		t.Fatalf("local item: %+v", it)
	}
	if e.store.Has(e.remote.ID) {
		t.Fatal("planning copied something")
	}
	// over the budget: refused up front, nothing copied
	if r, b := postJSON(t, e.ts.URL+"/api/offline/prepare", map[string]any{"datasetIds": []int64{e.remote.ID}, "channels": map[string]int{"from": 0, "to": 15}, "detail": "full", "budgetBytes": 1000}); r.StatusCode != 409 || e.store.Has(e.remote.ID) {
		t.Fatalf("over budget: %d %s", r.StatusCode, b)
	}

	st := e.prepare(t, map[string]any{"datasetIds": []int64{e.remote.ID}, "channels": map[string]int{"from": 10, "to": 12}, "detail": "full", "budgetBytes": 8 << 20})
	if st.State != "done" || len(st.Items) != 1 || st.Items[0].Status != statusPartial || st.Items[0].KeptBytes <= 0 {
		t.Fatalf("prepared: %+v", st)
	}

	e.down(t, true)
	if r, _ := e.get(t, e.ds("/view")); r.StatusCode != 200 || r.Header.Get("X-Vesqira-Offline") == "" {
		t.Fatalf("the view was not served from the copy: %d %v", r.StatusCode, r.Header)
	}
	for k, p := range paths {
		r, b := e.get(t, p)
		if r.StatusCode != 200 {
			t.Fatalf("offline %s: %d %s", k, r.StatusCode, b)
		}
		var mark offlineMark
		if err := json.Unmarshal([]byte(r.Header.Get("X-Vesqira-Offline")), &mark); err != nil || !mark.Copy || mark.TakenAt.IsZero() || mark.Signature.Size != e.remote.SizeBytes || mark.Signature.HostID != e.host.ID {
			t.Fatalf("%s: the answer is not marked as a copy of this file: %q", k, r.Header.Get("X-Vesqira-Offline"))
		}
		if strings.Contains(string(b), "laptop2") || strings.Contains(r.Header.Get("X-Vesqira-Offline"), "laptop2") {
			t.Errorf("%s: the copy leaks the host's address", k)
		}
		switch k {
		case "view", "cards":
			var live, cp map[string]any
			json.Unmarshal(want[k], &live)
			json.Unmarshal(b, &cp)
			if cp["offline"] == nil || !reflect.DeepEqual(sansOffline(cp), live) {
				t.Errorf("%s: the copy differs from the live answer apart from the offline field", k)
			}
		default:
			lh, lp := frame(t, want[k])
			ch, cp := frame(t, b)
			if !bytes.Equal(lp, cp) {
				t.Errorf("%s: the payload differs from the live answer", k)
			}
			if ch["offline"] == nil || !reflect.DeepEqual(sansOffline(ch), lh) {
				t.Errorf("%s: the head differs from the live one apart from the offline field", k)
			}
		}
	}
	// what was not kept fails with a sentence that names it
	r, b = e.get(t, e.ds("/plane?channel=30"))
	var fe struct{ Error, Code string }
	json.Unmarshal(b, &fe)
	if r.StatusCode != 503 || fe.Code != "offline_missing" || !strings.Contains(fe.Error, "channel 30") || !strings.Contains(fe.Error, "planes 10–12") || r.Header.Get("X-Vesqira-Offline") != "" {
		t.Fatalf("a plane that was not kept: %d %s", r.StatusCode, b)
	}
	if r, b := e.get(t, e.ds("/coord?x=1&y=1&channel=12")); r.StatusCode != 503 || !strings.Contains(string(b), "coordinate readout needs the host") {
		t.Fatalf("coord: %d %s", r.StatusCode, b)
	}
	if r, b := e.get(t, e.ds("/spectrum?shape=rect&x0=0&y0=0&x1=3&y1=3")); r.StatusCode != 503 || !strings.Contains(string(b), "only the spectra of regions saved") {
		t.Fatalf("another region: %d %s", r.StatusCode, b)
	}
	// routes that were never copied stay what they were: the host's failure, not an offline answer
	if r, _ := e.get(t, e.ds("/slab?from=1&to=3")); r.Header.Get("X-Vesqira-Offline") != "" || r.StatusCode == 200 {
		t.Fatalf("slab was answered offline: %d", r.StatusCode)
	}

	// the host is back: live again, no copy marker
	e.down(t, false)
	r, b = e.waitLive(t, e.ds("/plane?channel=12"))
	if r.StatusCode != 200 || r.Header.Get("X-Vesqira-Offline") != "" {
		t.Fatalf("host back: %d %v", r.StatusCode, r.Header)
	}
	if _, p := frame(t, b); !bytes.Equal(p, func() []byte { _, x := frame(t, want["full12"]); return x }()) {
		t.Error("live payload changed")
	}
}

// A copy is for the file it was made from. Once a live look finds the file different, the copy is stale: kept so it can be seen,
// never served, and said to be stale. A file that changed with no live look since is the catalog's last-seen file, which is
// documented in the ADR.
func TestChangedFileMakesTheCopyStaleAndItIsRefused(t *testing.T) {
	e := newOfflineEnv(t)
	e.prepare(t, map[string]any{"datasetIds": []int64{e.remote.ID}, "channels": map[string]int{"from": 5, "to": 6}, "detail": "full", "budgetBytes": 8 << 20})
	e.down(t, true)
	if r, _ := e.get(t, e.ds("/plane?channel=5")); r.StatusCode != 200 {
		t.Fatalf("fresh copy: %d", r.StatusCode)
	}
	e.down(t, false)
	e.waitLive(t, e.ds("/view"))
	os.WriteFile(e.remoteFile, append(append([]byte(nil), e.cube...), 0), 0o644)
	if r, _ := e.get(t, e.ds("/plane?channel=5")); r.StatusCode != 409 {
		t.Fatalf("a changed file while the host is reachable must be refused, not served from the copy: %d", r.StatusCode)
	}
	_, b := e.get(t, "/api/offline")
	var ov struct{ Datasets []struct{ Stale bool } }
	json.Unmarshal(b, &ov)
	if len(ov.Datasets) != 1 || !ov.Datasets[0].Stale {
		t.Fatalf("the overview must show the copy as stale: %s", b)
	}
	e.down(t, true)
	r, b := e.get(t, e.ds("/plane?channel=5"))
	var fe struct{ Error, Code string }
	json.Unmarshal(b, &fe)
	if r.StatusCode != 409 || fe.Code != "offline_stale" || r.Header.Get("X-Vesqira-Offline") != "" {
		t.Fatalf("a stale copy was served or hidden: %d %s", r.StatusCode, b)
	}
}

// The content identity recorded for the location is part of the match: a copy made without one, or with another, is not served as the
// file once an identity is recorded that it cannot vouch for, and one made with the same identity is served and says so.
func TestCopyIsServedOnlyForTheSameRecordedIdentity(t *testing.T) {
	e := newOfflineEnv(t)
	d, _ := e.cat.Get(e.remote.ID)
	loc := d.Locations[0]
	if err := e.cat.SetFingerprint(loc.ID, "full", strings.Repeat("ab", 32), "test", loc.SizeBytes, loc.ModifiedAt.UnixNano()); err != nil {
		t.Fatal(err)
	}
	e.prepare(t, map[string]any{"datasetIds": []int64{e.remote.ID}, "channels": map[string]int{"from": 2, "to": 2}, "detail": "full", "budgetBytes": 8 << 20})
	e.down(t, true)
	r, _ := e.get(t, e.ds("/plane?channel=2"))
	var mark offlineMark
	_ = json.Unmarshal([]byte(r.Header.Get("X-Vesqira-Offline")), &mark)
	if r.StatusCode != 200 || mark.Signature.Identity != "sha256:"+strings.Repeat("ab", 32) {
		t.Fatalf("same identity: %d %q", r.StatusCode, r.Header.Get("X-Vesqira-Offline"))
	}
	// the catalog now records another full hash for the same file: the copy cannot vouch for it
	if _, err := e.cat.DB().Exec(`UPDATE dataset_locations SET full_sha256=? WHERE id=?`, strings.Repeat("cd", 32), loc.ID); err != nil {
		t.Fatal(err)
	}
	r, b := e.get(t, e.ds("/plane?channel=2"))
	if r.StatusCode != 409 || !strings.Contains(string(b), "offline_stale") {
		t.Fatalf("another identity: %d %s", r.StatusCode, b)
	}
}

// The review listener has no route to any offline copy, or to the routes that read through one.
func TestReviewerCannotReachOfflineCopies(t *testing.T) {
	e := newOfflineEnv(t)
	e.prepare(t, map[string]any{"datasetIds": []int64{e.remote.ID}, "channels": map[string]int{"from": 5, "to": 5}, "detail": "preview", "budgetBytes": 8 << 20})
	rv := httptest.NewServer((&review.Handler{Store: e.bs}).Handler())
	defer rv.Close()
	for _, p := range []string{"/api/offline", "/api/offline/prepare", "/api/datasets/" + itoa(int(e.remote.ID)) + "/plane?channel=5", "/api/datasets/" + itoa(int(e.remote.ID)) + "/view", "/review-api/offline", "/offline"} {
		if r, b := get(t, rv.URL+p); r.StatusCode == 200 && strings.Contains(string(b), "offline") {
			t.Errorf("%s reached an offline copy from the review listener: %s", p, b)
		}
		if r, _ := get(t, rv.URL+p); r.StatusCode == 200 && strings.Contains(p, "/api/") {
			t.Errorf("%s answered 200 on the review listener", p)
		}
	}
	for _, p := range []string{"/api/offline/plan", "/api/offline/forget", "/api/offline/prepare"} {
		r, err := http.Post(rv.URL+p, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode < 400 {
			t.Errorf("POST %s on the review listener: %d", p, r.StatusCode)
		}
	}
	// the store's directory is private and is not a source root
	if info, err := os.Stat(e.dataDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("offline directory: %v %v", info, err)
	}
}

// Deleting a dataset names its offline copy in the impact preview and removes the copy, files included; "forget" does the same alone.
func TestDeleteImpactNamesTheOfflineCopyAndRemovesIt(t *testing.T) {
	e := newOfflineEnv(t)
	e.prepare(t, map[string]any{"datasetIds": []int64{e.remote.ID}, "channels": map[string]int{"from": 5, "to": 6}, "detail": "preview", "budgetBytes": 8 << 20})
	dir := filepath.Join(e.dataDir, itoa(int(e.remote.ID)))
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("no copy folder: %v", err)
	}
	_, b := e.get(t, "/api/datasets/"+itoa(int(e.remote.ID))+"/impact")
	var im struct {
		Digest string
		Groups []struct{ Key, Effect, Text string }
	}
	json.Unmarshal(b, &im)
	found := false
	for _, g := range im.Groups {
		if g.Key == "offline" && g.Effect == "removed" && strings.Contains(g.Text, "offline copy") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the impact preview does not name the offline copy: %s", b)
	}
	if r, b := postJSON(t, e.ts.URL+"/api/datasets/"+itoa(int(e.remote.ID))+"/delete", map[string]any{"impactDigest": im.Digest}); r.StatusCode != 200 {
		t.Fatalf("delete: %d %s", r.StatusCode, b)
	}
	if e.store.Has(e.remote.ID) {
		t.Error("the index rows stayed")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the files stayed: %v", err)
	}
}

func TestForgetAndTheStoreLimit(t *testing.T) {
	e := newOfflineEnv(t)
	e.prepare(t, map[string]any{"datasetIds": []int64{e.remote.ID}, "channels": map[string]int{"from": 5, "to": 6}, "detail": "preview", "budgetBytes": 8 << 20})
	used := e.store.Used()
	if used == 0 {
		t.Fatal("nothing kept")
	}
	// a limit below what is kept deletes nothing, and the next preparation is refused with a sentence
	if r, b := putJSON(t, e.ts.URL+"/api/offline/settings", map[string]any{"capBytes": used / 2}); r.StatusCode != 200 {
		t.Fatalf("settings: %d %s", r.StatusCode, b)
	}
	if e.store.Used() != used {
		t.Fatal("lowering the limit deleted copies")
	}
	if r, b := postJSON(t, e.ts.URL+"/api/offline/prepare", map[string]any{"datasetIds": []int64{e.remote.ID}, "channels": map[string]int{"from": 7, "to": 8}, "detail": "preview", "budgetBytes": 8 << 20}); r.StatusCode != 409 || !strings.Contains(string(b), "offline store has room") {
		t.Fatalf("over the limit: %d %s", r.StatusCode, b)
	}
	if r, _ := putJSON(t, e.ts.URL+"/api/offline/settings", map[string]any{"capBytes": -1}); r.StatusCode != 400 && r.StatusCode != 422 {
		t.Errorf("negative limit: %d", r.StatusCode)
	}
	r, b := postJSON(t, e.ts.URL+"/api/offline/forget", map[string]any{"datasetId": e.remote.ID})
	if r.StatusCode != 200 || e.store.Has(e.remote.ID) || e.store.Used() != 0 {
		t.Fatalf("forget: %d %s", r.StatusCode, b)
	}
	e.down(t, true)
	if r, _ := e.get(t, e.ds("/plane?channel=5")); r.StatusCode != 502 {
		t.Fatalf("after forgetting, the host's failure is what is reported: %d", r.StatusCode)
	}
}

// When the host goes away between the stat and the read, ask falls back to the copy made from exactly the signature just verified,
// and to nothing else.
func TestHostLostBetweenStatAndReadFallsBackToTheVerifiedCopy(t *testing.T) {
	e := newOfflineEnv(t)
	e.prepare(t, map[string]any{"datasetIds": []int64{e.remote.ID}, "channels": map[string]int{"from": 5, "to": 5}, "detail": "full", "budgetBytes": 8 << 20})
	k := cacheKey{id: e.remote.ID, host: e.host.ID, size: e.remote.SizeBytes, mtime: e.remote.ModifiedAt.UnixNano(), op: "plane", channel: 5}
	try := func(k cacheKey) (worker.Reply, bool, http.Header) {
		w := httptest.NewRecorder()
		rep, ok := e.srv.afterFailure(w, httptest.NewRequest("GET", "/", nil), k)
		return rep, ok, w.Header()
	}
	rep, ok, h := try(k)
	if !ok || h.Get("X-Vesqira-Offline") == "" || len(rep.Payload) == 0 {
		t.Fatalf("the verified copy was not used: %v %v", ok, h)
	}
	if _, p := frame(t, append(append(binary.LittleEndian.AppendUint32(nil, uint32(len(rep.Head))), rep.Head...), rep.Payload...)); len(p) == 0 {
		t.Fatal("empty payload")
	}
	other := k
	other.channel = 6
	if _, ok, _ := try(other); ok {
		t.Error("a plane that was not kept was served")
	}
	changed := k
	changed.size++
	if _, ok, _ := try(changed); ok {
		t.Error("a copy was served for another file signature")
	}
	local := k
	local.host = 0
	if _, ok, _ := try(local); ok {
		t.Error("a file on this computer answered from the offline store")
	}
}

func TestPreviewStepMatchesTheViewer(t *testing.T) {
	// apps/web/src/api/planes.ts previewStep: the same formula, so a stored preview is the one the viewer asks for
	for _, c := range []struct{ w, h, step int }{{100, 100, 2}, {512, 512, 2}, {1024, 1024, 2}, {2048, 2048, 4}, {4096, 4096, 8}, {100000, 100000, 64}} {
		if got := previewStepFor(c.w, c.h); got != c.step {
			t.Errorf("%dx%d: %d, want %d", c.w, c.h, got, c.step)
		}
	}
	if got := ranges([]int{12, 10, 11, 20, 30, 31}); got != "10–12, 20, 30–31" {
		t.Errorf("ranges = %q", got)
	}
}

// The offline routes answer as the spec says, and so does an offline plane head (its `offline` field).
func TestOfflineResponsesMatchTheSpec(t *testing.T) {
	doc := loadSpec(t)
	e := newOfflineEnv(t)
	check := func(path, method string, status int, label string, body []byte) {
		t.Helper()
		conforms(t, responseSchemaAt(t, doc, path, method, status, "application/json"), label, body)
	}
	_, b := e.get(t, "/api/offline")
	check("/api/offline", "GET", 200, "empty overview", b)
	in := map[string]any{"datasetIds": []int64{e.remote.ID}, "boardId": nil, "channels": map[string]int{"from": 3, "to": 4}, "detail": "full", "budgetBytes": 8 << 20}
	r, b := postJSON(t, e.ts.URL+"/api/offline/plan", in)
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	check("/api/offline/plan", "POST", 200, "plan", b)
	op := doc.Paths.Find("/api/offline/plan").GetOperation("POST")
	var v any
	_ = json.Unmarshal(mustJSON(t, in), &v)
	if err := op.RequestBody.Value.Content.Get("application/json").Schema.Value.VisitJSON(v); err != nil {
		t.Errorf("the plan request does not match the spec: %v", err)
	}
	v.(map[string]any)["extra"] = 1
	if err := op.RequestBody.Value.Content.Get("application/json").Schema.Value.VisitJSON(v); err == nil {
		t.Error("the spec accepts an unknown field")
	}
	r, b = postJSON(t, e.ts.URL+"/api/offline/prepare", in)
	if r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	check("/api/offline/prepare", "POST", 202, "started", b)
	e.srv.waitOffline()
	_, b = e.get(t, "/api/offline/prepare")
	check("/api/offline/prepare", "GET", 200, "state", b)
	_, b = e.get(t, "/api/offline")
	check("/api/offline", "GET", 200, "overview", b)
	_, b = putJSON(t, e.ts.URL+"/api/offline/settings", map[string]any{"capBytes": 1 << 30})
	check("/api/offline/settings", "PUT", 200, "settings", b)
	e.down(t, true)
	_, b = e.get(t, e.ds("/plane?channel=3"))
	head, _ := frame(t, b)
	conforms(t, doc.Components.Schemas["PlaneHeader"], "offline plane head", mustJSON(t, head))
	_, b = e.get(t, e.ds("/view"))
	conforms(t, doc.Components.Schemas["View"], "offline view", b)
	_, b = postJSON(t, e.ts.URL+"/api/offline/forget", map[string]any{"datasetId": e.remote.ID})
	check("/api/offline/forget", "POST", 200, "forget", b)
}
