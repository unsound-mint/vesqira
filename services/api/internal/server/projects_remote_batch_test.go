package server

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

type scanned struct {
	rows   []string // one line per dataset: everything that is stored about it and its primary location
	calls  map[string]int
	failed int
	errs   []string
}

// scanHost opens proj as a project on a fresh host and catalog, with the worker's files changed by patch, and reports what the
// scan registered and how many worker requests of each op it took.
func scanHost(t *testing.T, repo, proj string, patch func(map[string][]byte)) scanned {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	remoteHome := filepath.Join(base, "remote-home")
	seed := filepath.Join(base, "seed_root")
	os.MkdirAll(remoteHome, 0o755)
	os.MkdirAll(seed, 0o755)
	files := workerFiles(t, repo)
	if patch != nil {
		patch(files)
	}
	mgr := hosts.NewManager(fakeSSH(t, remoteHome), files)
	mgr.ControlDir = t.TempDir()
	t.Cleanup(mgr.Close)
	var mu sync.Mutex
	calls := map[string]int{}
	mgr.OnDo = func(op string) { mu.Lock(); calls[op]++; mu.Unlock() }
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	hs, _ := hosts.Open(cat.DB())
	ps, _ := projects.Open(cat.DB())
	rn, _ := runs.Open(cat.DB())
	au, _ := audit.Open(cat.DB())
	rs, _ := sourceroot.New([]string{seed})
	srv := &Server{Roots: rs, Store: cat, Worker: headerWorker(t, "python3", repo), Projects: ps, Runs: rn, Audit: au, HostDB: hs, Remote: mgr}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	e := &projEnv{ts: ts, srv: srv, cat: cat, base: base, home: remoteHome}
	py, _ := exec.LookPath("python3")
	h, err := hs.Create(hosts.Host{Name: "Cluster", Dest: "cluster", Python: py, Roots: []string{seed}})
	if err != nil {
		t.Fatal(err)
	}
	var o sumJSON
	e.json(t, "POST", "/api/projects/open", map[string]any{"hostId": h.ID, "path": proj}, 200, &o)
	e.settle(t, o.ID)
	var reg struct {
		Registration struct {
			Failed int      `json:"failed"`
			Errors []string `json:"errors"`
		} `json:"registration"`
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d", o.ID), nil, 200, &reg)
	out := scanned{calls: calls, failed: reg.Registration.Failed, errs: reg.Registration.Errors}
	ds, _ := cat.List(workspace.Local)
	for _, d := range ds {
		locs, _ := cat.Locations(workspace.Local, d.ID)
		row := fmt.Sprintf("%s|%d|%d|%d|%s|%s", d.Path, d.HostID, d.SizeBytes, d.ModifiedAt.UnixNano(), d.Name, d.Header)
		for _, l := range locs {
			row += fmt.Sprintf("|loc %s %d %v %s %d %d", l.Path, l.HostID, l.Primary, l.Link, l.SizeBytes, l.ModifiedAt.UnixNano())
			if l.Sampled != nil {
				row += fmt.Sprintf(" sampled=%s by=%s", l.Sampled.Value, l.Sampled.By)
			}
			if l.Full != nil {
				row += " full"
			}
		}
		out.rows = append(out.rows, row)
	}
	sort.Strings(out.rows)
	return out
}

// A project scan on a host registers files in batches of one worker request, and what it stores is what registering them one
// by one stores: the same header, size, time and sampled fingerprint with the same label. A host whose worker does not know
// the batch operation is registered one by one.
func TestRemoteScanBatchesRegistrationAndStoresWhatOneByOneStores(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	base, _ := filepath.EvalSymlinks(t.TempDir())
	proj := filepath.Join(base, "remote_proj")
	data := filepath.Join(proj, "data")
	os.MkdirAll(data, 0o755)
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), data)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	src, err := os.ReadFile(filepath.Join(data, "vgt_psi_deg.fits"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ { // with the fixtures, more than two batches of 32
		put(t, data, fmt.Sprintf("many/copy_%02d.fits", i), string(src))
	}
	put(t, data, "many/broken.fits", "this is not a FITS file\n")

	batched := scanHost(t, repo, proj, nil)
	oneByOne := scanHost(t, repo, proj, func(files map[string][]byte) {
		old := []byte(`"stat_many", "register_many"):`)
		if !bytes.Contains(files["plane_worker.py"], old) {
			t.Fatal("the worker's op list changed; update this test")
		}
		files["plane_worker.py"] = bytes.Replace(files["plane_worker.py"], old, []byte(`"stat_many"):`), 1)
	})

	n := len(oneByOne.rows)
	if n < 60 || oneByOne.failed != 3 || len(oneByOne.errs) != 3 {
		t.Fatalf("the project did not register what the test needs: %d rows, %d failed %v", n, oneByOne.failed, oneByOne.errs)
	}
	if len(batched.rows) != n || batched.failed != 3 {
		t.Fatalf("batched registered %d datasets and failed %d; one by one %d and 3", len(batched.rows), batched.failed, n)
	}
	for i := range batched.rows {
		if batched.rows[i] != oneByOne.rows[i] {
			t.Fatalf("row %d differs\nbatched:    %s\none by one: %s", i, batched.rows[i], oneByOne.rows[i])
		}
	}
	if fmt.Sprint(batched.errs) != fmt.Sprint(oneByOne.errs) {
		t.Fatalf("the refusal text differs:\n%q\n%q", batched.errs, oneByOne.errs)
	}
	for _, r := range batched.rows {
		if !bytes.Contains([]byte(r), []byte(" sampled=")) {
			t.Fatalf("no sampled fingerprint stored: %s", r)
		}
	}

	files := oneByOne.calls["resolve"] - batched.calls["resolve"] // files registered by the scan itself (the rest of the rows come from run imports)
	if files < 60 {
		t.Fatalf("only %d files went through the scan", files)
	}
	for _, op := range []string{"resolve", "header", "fingerprint"} {
		if oneByOne.calls[op] < files-oneByOne.failed { // a refused header stops before its fingerprint
			t.Fatalf("one by one: %d %s requests for %d files", oneByOne.calls[op], op, files)
		}
		if extra := oneByOne.calls[op] - batched.calls[op]; extra < files-oneByOne.failed { // the rest is the scan's own reads (run folders), the same in both
			t.Fatalf("the batch saved only %d %s requests (%d vs %d)", extra, op, batched.calls[op], oneByOne.calls[op])
		}
	}
	if oneByOne.calls["register_many"] != 1 { // asked once, learned the worker is old, never again
		t.Fatalf("an old worker was asked for register_many %d times", oneByOne.calls["register_many"])
	}
	if got, most := batched.calls["register_many"], (files+remoteBatchMax-1)/remoteBatchMax; got < most || got > most+1 {
		t.Fatalf("%d register_many requests for %d files", got, files)
	}
	t.Logf("worker requests per file: one by one %.2f (resolve+header+fingerprint), batched %.3f (register_many), %d files", float64(oneByOne.calls["resolve"]+oneByOne.calls["header"]+oneByOne.calls["fingerprint"])/float64(files), float64(batched.calls["register_many"])/float64(files), files)
}

func TestBatchOfCutsByCountAndByBytes(t *testing.T) {
	t.Parallel()
	short := make([]string, 100)
	for i := range short {
		short[i] = "/data/x.fits"
	}
	if got := batchOf(short); got != remoteBatchMax {
		t.Fatalf("%d", got)
	}
	long := make([]string, 10)
	for i := range long {
		long[i] = "/" + string(bytes.Repeat([]byte("a"), 2000))
	}
	if got := batchOf(long); got < 1 || got > 3 {
		t.Fatalf("2000-byte paths must not overflow the 8 KiB request: %d in a batch", got)
	}
	if got := batchOf([]string{"/" + string(bytes.Repeat([]byte("a"), 4000))}); got != 1 {
		t.Fatalf("a long path alone is still sent: %d", got)
	}
}
