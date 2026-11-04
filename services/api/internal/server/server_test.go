package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

type fakeWorker struct{ calls int }

func (f *fakeWorker) Header(context.Context, string) (json.RawMessage, error) {
	f.calls++
	return json.RawMessage(`{"ok":true,"axes":[]}`), nil
}

func itoa(n int) string { return strconv.Itoa(n) }

func newServer(t *testing.T, w HeaderReader) (*httptest.Server, string, string) {
	return newServerWith(t, w, nil)
}

func newPlaneServer(t *testing.T, p PlaneService) (*httptest.Server, string, string) {
	return newServerWith(t, &fakeWorker{}, p)
}

func newPlaneServerWith(t *testing.T, w HeaderReader, p PlaneService) (*httptest.Server, string, string) {
	return newServerWith(t, w, p)
}

func newServerWith(t *testing.T, w HeaderReader, p PlaneService) (*httptest.Server, string, string) {
	t.Helper()
	base := t.TempDir()
	root, sibling := filepath.Join(base, "data"), filepath.Join(base, "data2")
	for _, d := range []string{root, sibling} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rs, err := sourceroot.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	st, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer((&Server{Roots: rs, Store: st, Worker: w, Planes: p, Cache: NewPlaneCache(8 << 20)}).Handler())
	t.Cleanup(ts.Close)
	return ts, root, sibling
}

func post(t *testing.T, ts *httptest.Server, path string, hdr map[string]string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"path": path})
	req, _ := http.NewRequest("POST", ts.URL+"/api/datasets", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestRegisterRefusesPathsOutsideRootsAndNeverCallsWorker(t *testing.T) {
	fw := &fakeWorker{}
	ts, root, sibling := newServer(t, fw)
	outside := filepath.Join(sibling, "o.fits")
	os.WriteFile(outside, []byte("x"), 0o644)
	os.Symlink(outside, filepath.Join(root, "link.fits"))
	for _, p := range []string{outside, filepath.Join(root, "link.fits"), filepath.Join(root, "..", "data2", "o.fits"), "relative.fits"} {
		if code := post(t, ts, p, nil).StatusCode; code != 403 {
			t.Errorf("%q: status %d, want 403", p, code)
		}
	}
	if fw.calls != 0 {
		t.Fatalf("worker ran %d times for refused paths", fw.calls)
	}
}

func TestRegisterListGet(t *testing.T) {
	ts, root, _ := newServer(t, &fakeWorker{})
	f := filepath.Join(root, "a.fits")
	os.WriteFile(f, []byte("x"), 0o644)
	resp := post(t, ts, f, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var d catalog.Dataset
	json.NewDecoder(resp.Body).Decode(&d)
	if d.Name != "a.fits" || d.ID == 0 {
		t.Fatalf("%+v", d)
	}
	if post(t, ts, f, nil).StatusCode != 201 { // registering again refreshes, no duplicate
		t.Fatal("re-register failed")
	}
	r, _ := http.Get(ts.URL + "/api/datasets")
	var l struct{ Datasets []catalog.Dataset }
	json.NewDecoder(r.Body).Decode(&l)
	if len(l.Datasets) != 1 {
		t.Fatalf("want 1 dataset, got %d", len(l.Datasets))
	}
	if r, _ := http.Get(ts.URL + "/api/datasets/999"); r.StatusCode != 404 {
		t.Fatal("want 404")
	}
}

func TestBrowserGuards(t *testing.T) {
	ts, root, _ := newServer(t, &fakeWorker{})
	f := filepath.Join(root, "a.fits")
	os.WriteFile(f, []byte("x"), 0o644)
	if c := post(t, ts, f, map[string]string{"Sec-Fetch-Site": "cross-site"}).StatusCode; c != 403 {
		t.Errorf("cross-site POST: %d", c)
	}
	if c := post(t, ts, f, map[string]string{"Content-Type": "text/plain"}).StatusCode; c != 415 {
		t.Errorf("text/plain POST: %d", c)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/datasets", nil)
	req.Host = "evil.example"
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Errorf("foreign Host: %d", r.StatusCode)
	}
}

// Real worker end to end on the generated fixtures. Needs python3 with astropy.
func TestRealWorkerOnFixtures(t *testing.T) {
	repo, _ := filepath.Abs("../../../..")
	probe := exec.Command("python3", "-c", "import astropy")
	if probe.Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available; set VESQIRA_REQUIRE_WORKER=1 to make this fatal")
	}
	run := worker.Runner{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/fits_header.py")}, Timeout: 20 * time.Second}
	ts, root, _ := newServer(t, run)
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	resp := post(t, ts, filepath.Join(root, "hi_cube_no_spectral_ref.fits"), nil)
	if resp.StatusCode != 201 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var d struct {
		Header struct {
			Axes []map[string]any `json:"axes"`
		} `json:"header"`
	}
	json.NewDecoder(resp.Body).Decode(&d)
	spec := d.Header.Axes[2]
	if spec["crval"] != nil || spec["crval_status"] != "unresolved" {
		t.Fatalf("missing spectral reference must be unresolved, got %v", spec)
	}
	os.WriteFile(filepath.Join(root, "bad.fits"), bytes.Repeat([]byte("junk"), 1000), 0o644)
	if c := post(t, ts, filepath.Join(root, "bad.fits"), nil).StatusCode; c != 422 {
		t.Errorf("non-FITS: %d", c)
	}
}
