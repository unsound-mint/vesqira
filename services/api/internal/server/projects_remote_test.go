package server

import (
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

// A project on a host: the folder is added to the host's roots, walked by the host's own worker (same skip rules, never
// reading the blinding key), its FITS headers registered there, and nothing outside the folder becomes readable.
func TestRemoteProjectIsWalkedByTheHostWorkerWithFakeSSH(t *testing.T) {
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	base, _ := filepath.EvalSymlinks(t.TempDir())
	remoteHome := filepath.Join(base, "remote-home")
	seed := filepath.Join(base, "seed_root")
	proj := filepath.Join(base, "remote_proj")
	outside := filepath.Join(base, "private_elsewhere")
	for _, d := range []string{remoteHome, seed, filepath.Join(proj, "data"), filepath.Join(proj, "results_v3"), filepath.Join(proj, "blind_set_key"), filepath.Join(proj, "old"), filepath.Join(proj, ".git"), outside, filepath.Join(proj, "cube.zarr")} {
		os.MkdirAll(d, 0o755)
	}
	realHome, _ := os.UserHomeDir() // the fake ssh runs the worker as this user, so this is the host's home too
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), filepath.Join(proj, "data"))
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	put(t, proj, "results_v3/README.md", "x")
	put(t, proj, "blind_set_key/key_DO_NOT_OPEN.csv", "secret")
	put(t, proj, "old/stale.fits", "x")
	put(t, proj, "cube.zarr/0/c", "x")
	put(t, proj, ".git/HEAD", "ref: refs/heads/main\n")
	put(t, proj, ".git/refs/heads/main", "fedcba9876543210fedcba9876543210fedcba98\n")
	put(t, proj, ".vesqira/project.toml", "[layout]\nskip = [\"old/**\"]\n")
	os.Symlink(outside, filepath.Join(proj, "door"))
	put(t, outside, "secret.txt", "s")

	files := map[string][]byte{}
	for _, f := range []string{"plane_worker.py", "fits_header.py", "convert.py", "orientation.py", "rohsa.py"} {
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
	ps, _ := projects.Open(cat.DB())
	rn, _ := runs.Open(cat.DB())
	au, _ := audit.Open(cat.DB())
	rs, _ := sourceroot.New([]string{seed})
	hdr := headerWorker(t, "python3", repo)
	srv := &Server{Roots: rs, Store: cat, Worker: hdr, Projects: ps, Runs: rn, Audit: au, HostDB: hs, Remote: mgr}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	e := &projEnv{ts: ts, srv: srv, cat: cat, base: base, home: remoteHome}

	py, _ := exec.LookPath("python3")
	h, err := hs.Create(hosts.Host{Name: "Second laptop", Dest: "laptop2", Python: py, Roots: []string{seed}})
	if err != nil {
		t.Fatal(err)
	}

	// refusals first: the host's home, the disk, a file, a path that is not a folder; none adds a root
	for name, p := range map[string]string{"home": realHome, "disk": "/", "missing": filepath.Join(base, "nope"), "file": filepath.Join(proj, "results_v3", "README.md")} {
		if code, b := e.call(t, "POST", "/api/projects/open", map[string]any{"hostId": h.ID, "path": p}); code != 422 {
			t.Fatalf("%s: %d %s", name, code, b)
		}
	}
	if got, _ := hs.Get(h.ID); len(got.Roots) != 1 {
		t.Fatalf("a refused folder became a root: %v", got.Roots)
	}
	if code, _ := e.call(t, "POST", "/api/projects/open", map[string]any{"hostId": 999, "path": proj}); code != 404 {
		t.Fatalf("%d", code)
	}

	var o sumJSON
	e.json(t, "POST", "/api/projects/open", map[string]any{"hostId": h.ID, "path": proj}, 200, &o)
	s := e.settle(t, o.ID)
	if got, _ := hs.Get(h.ID); len(got.Roots) != 2 || got.Roots[1] != proj {
		t.Fatalf("the project folder is a root of the host now: %v", got.Roots)
	}
	if !s.Git.Present || s.Git.Commit != "fedcba9" {
		t.Fatalf("the commit is read through the host: %+v", s.Git)
	}
	if s.Counts.Data < 1 || s.Counts.ByFormat["Zarr"] != 1 || s.Counts.NeverRead != 1 || s.Links.Outside != 1 {
		t.Fatalf("%+v %+v", s.Counts, s.Links)
	}
	// the skip rule from the project file held on the host: nothing under old/ was listed or registered
	ds, _ := cat.List()
	if len(ds) < s.Counts.ByFormat["FITS"] {
		t.Fatalf("%d datasets for %d data files", len(ds), s.Counts.ByFormat["FITS"])
	}
	for _, d := range ds {
		if d.HostID != h.ID || !strings.HasPrefix(d.Path, proj+"/data/") {
			t.Fatalf("%+v", d)
		}
	}
	// the project root is the only new thing readable on the host
	if code, _ := e.call(t, "GET", fmt.Sprintf("/api/hosts/%d/text?path=%s", h.ID, filepath.Join(outside, "secret.txt")), nil); code != 403 {
		t.Fatalf("outside the roots: %d", code)
	}
	var needs struct {
		Needs []struct{ Kind, ID string }
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/needs", o.ID), nil, 200, &needs)
	found := false
	for _, n := range needs.Needs {
		found = found || n.ID == "results:results_v3"
	}
	if !found {
		t.Fatalf("%+v", needs)
	}
	var ans struct {
		WrittenToProject bool
		Note             string
	}
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/answers", o.ID), map[string]any{"need": "results:results_v3", "answer": "not_run", "write": true}, 200, &ans)
	if ans.WrittenToProject || !strings.Contains(ans.Note, "not written to the project") || !strings.Contains(ans.Note, "not written by Vesqira") {
		t.Fatalf("a project file the researcher wrote on the host is not touched: %+v", ans)
	}
	hostFile := filepath.Join(proj, ".vesqira", "project.toml")
	const theirs = "[layout]\nskip = [\"old/**\"]\n"
	if code, _ := e.call(t, "POST", fmt.Sprintf("/api/projects/%d/project-file", o.ID), map[string]any{"write": true}); code != 409 {
		t.Fatalf("%d", code)
	}
	if code, _ := e.call(t, "POST", fmt.Sprintf("/api/projects/%d/project-file", o.ID), map[string]any{"write": true, "text": "[layout]\nskip = []\n"}); code != 409 {
		t.Fatalf("replacing a foreign file: %d", code)
	}
	if code, _ := e.call(t, "POST", fmt.Sprintf("/api/projects/%d/project-file", o.ID), map[string]any{"write": false}); code != 400 {
		t.Fatalf("no confirmation: %d", code)
	}
	if b, _ := os.ReadFile(hostFile); string(b) != theirs {
		t.Fatalf("the researcher's file changed: %q", b)
	}

	// with no file there, the confirmed write creates it on the host (marker line first, the proposal after it), and
	// from then on answers are appended to it
	os.Remove(hostFile)
	e.call(t, "POST", fmt.Sprintf("/api/projects/%d/scan", o.ID), nil) // a project with no file gets a proposal
	e.settle(t, o.ID)
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/project-file", o.ID), map[string]any{"write": true}, 200, nil)
	b, err := os.ReadFile(hostFile)
	if err != nil || !strings.HasPrefix(string(b), projectMarker) || !strings.Contains(string(b), "[project]") {
		t.Fatalf("%v %q", err, b)
	}
	e.settle(t, o.ID)
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/answers", o.ID), map[string]any{"need": "results:results_v3", "answer": "not_run"}, 200, &ans)
	if !ans.WrittenToProject {
		t.Fatalf("the file exists and is Vesqira's: %+v", ans)
	}
	if b, _ = os.ReadFile(hostFile); !strings.Contains(string(b), `need = "results:results_v3"`) || !strings.Contains(string(b), `declared_by = "you"`) || !strings.HasPrefix(string(b), projectMarker) {
		t.Fatalf("%s", b)
	}
	if ents, _ := os.ReadDir(filepath.Join(proj, ".vesqira")); len(ents) != 1 {
		t.Fatalf("a temporary file was left: %v", ents)
	}
	// a link where .vesqira should be is never written through
	os.RemoveAll(filepath.Join(proj, ".vesqira"))
	os.Symlink(outside, filepath.Join(proj, ".vesqira"))
	if code, _ := e.call(t, "POST", fmt.Sprintf("/api/projects/%d/project-file", o.ID), map[string]any{"write": true, "text": "[layout]\nskip = []\n"}); code != 409 {
		t.Fatalf("a link: %d", code)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 1 { // only secret.txt
		t.Fatalf("something was written through the link: %v", ents)
	}
	entries, _ := au.List("project", o.ID, 50)
	var wrote, refused int
	for _, en := range entries {
		switch en.Action {
		case "project file written on host":
			wrote++
		case "project file write on host refused":
			refused++
		}
	}
	if wrote != 2 || refused != 1 {
		t.Fatalf("the audit log must record writes and refusals on the host: %+v", entries)
	}
}

// Where the code lives on a host is asked, with the folder an earlier job used offered but never applied; the answer
// goes into the project file as the researcher's.
func TestHostRepoIsAskedForAndAnsweredByYou(t *testing.T) {
	e := newProjEnv(t)
	hs, _ := hosts.Open(e.cat.DB())
	js, _ := jobs.Open(e.cat.DB())
	e.srv.HostDB, e.srv.Jobs = hs, js
	h, err := hs.Create(hosts.Host{Name: "Cluster", Dest: "login1", Roots: []string{"/data"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.CreateDraft(jobs.Job{HostID: h.ID, Name: "old", Template: "vgt", Script: "#!/bin/bash\n", Request: `{"repoDir":"/home/me/vgt"}`}); err != nil {
		t.Fatal(err)
	}
	root := e.project(t, "p")
	put(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	put(t, root, "hpc/a.slurm", "#!/bin/bash")
	put(t, root, ".vesqira/project.toml", "[project]\nname = \"p\"\n")
	s := e.open(t, root)
	var needs struct {
		Needs []struct {
			ID, Kind, Suggestion string
		}
	}
	e.json(t, "GET", fmt.Sprintf("/api/projects/%d/needs", s.ID), nil, 200, &needs)
	var n struct{ ID, Kind, Suggestion string }
	for _, x := range needs.Needs {
		if x.Kind == "host_repo" {
			n = x
		}
	}
	if n.ID != "host-repo:Cluster" || !strings.Contains(n.Suggestion, "/home/me/vgt") {
		t.Fatalf("%+v", needs)
	}
	for _, bad := range []string{"relative/path", "/a/../b", "", "/x\ny"} {
		if code, _ := e.call(t, "POST", fmt.Sprintf("/api/projects/%d/answers", s.ID), map[string]any{"need": n.ID, "answer": "declare", "value": bad}); code != 422 {
			t.Fatalf("%q: %d", bad, code)
		}
	}
	var ans struct{ WrittenToProject bool }
	e.json(t, "POST", fmt.Sprintf("/api/projects/%d/answers", s.ID), map[string]any{"need": n.ID, "answer": "declare", "value": "/home/me/vgt-main"}, 200, &ans)
	if !ans.WrittenToProject { // the project file existed, so the answer went into it
		t.Fatalf("%+v", ans)
	}
	b, _ := os.ReadFile(filepath.Join(root, ".vesqira", "project.toml"))
	if !strings.Contains(string(b), `need = "host-repo:Cluster"`) || !strings.Contains(string(b), `value = "/home/me/vgt-main"`) || !strings.Contains(string(b), `declared_by = "you"`) {
		t.Fatalf("%s", b)
	}
}
