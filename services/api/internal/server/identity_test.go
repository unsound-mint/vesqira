package server

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	idty "github.com/unsound-mint/vesqira/services/api/internal/identity"
	"github.com/unsound-mint/vesqira/services/api/internal/publish"
	"github.com/unsound-mint/vesqira/services/api/internal/review"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// idEnv is a server with a stub header reader (any bytes register), runs, boards and a managed directory: enough to exercise
// identity and locations without FITS parsing. Tests that read planes build their own with the real worker.
type idEnv struct {
	t       *testing.T
	srv     *Server
	ts      *httptest.Server
	root    string
	managed string
	db      *sql.DB
}

func newIDEnv(t *testing.T) *idEnv {
	t.Helper()
	base := t.TempDir()
	root, managed := filepath.Join(base, "data"), filepath.Join(base, "managed")
	for _, d := range []string{root, managed} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rs, _ := sourceroot.New([]string{root})
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	bs, err := boards.Open(cat.DB(), base)
	if err != nil {
		t.Fatal(err)
	}
	dv, _ := derived.Open(cat.DB())
	au, _ := audit.Open(cat.DB())
	rn, err := runs.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Roots: rs, Store: cat, Worker: hdrWorker{}, Boards: bs, Derived: dv, ManagedDir: managed, Audit: au, Runs: rn}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { srv.waitHashes(); ts.Close() })
	return &idEnv{t: t, srv: srv, ts: ts, root: root, managed: managed, db: cat.DB()}
}

func (e *idEnv) write(name string, data []byte) string {
	e.t.Helper()
	p := filepath.Join(e.root, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// sparse makes a file of n zero bytes, above the size up to which registration also takes a full hash.
func (e *idEnv) sparse(name string, n int64, mark byte) string {
	e.t.Helper()
	p := e.write(name, []byte{mark})
	if err := os.Truncate(p, n); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *idEnv) call(method, path string, body any) (int, []byte) {
	e.t.Helper()
	return callJSON(e.t, method, e.ts.URL+path, body)
}

func (e *idEnv) register(path string, asNew bool) (int, catalog.Dataset, []byte) {
	e.t.Helper()
	code, b := e.call("POST", "/api/datasets", map[string]any{"path": path, "asNew": asNew})
	var d catalog.Dataset
	if code == 201 {
		_ = json.Unmarshal(b, &d)
	}
	return code, d, b
}

func (e *idEnv) mustRegister(path string) catalog.Dataset {
	e.t.Helper()
	code, d, b := e.register(path, false)
	if code != 201 {
		e.t.Fatalf("register %s: %d %s", path, code, b)
	}
	return d
}

func (e *idEnv) dataset(id int64) catalog.Dataset {
	e.t.Helper()
	code, b := e.call("GET", "/api/datasets/"+itoa(int(id)), nil)
	if code != 200 {
		e.t.Fatalf("dataset %d: %d %s", id, code, b)
	}
	var d catalog.Dataset
	if err := json.Unmarshal(b, &d); err != nil {
		e.t.Fatal(err)
	}
	return d
}

func (e *idEnv) addLocation(id int64, path string, declare bool) (int, catalog.Location, map[string]any) {
	e.t.Helper()
	code, b := e.call("POST", "/api/datasets/"+itoa(int(id))+"/locations", map[string]any{"path": path, "declare": declare})
	var l catalog.Location
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if code == 201 {
		_ = json.Unmarshal(b, &l)
	}
	return code, l, m
}

func fileSHA(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return sha(b)
}

func TestRegistrationRecordsTheSampledFingerprintAndForSmallFilesTheFullHash(t *testing.T) {
	t.Parallel()
	e := newIDEnv(t)
	small := e.write("small.fits", []byte("small file bytes"))
	big := e.sparse("big.fits", idty.SmallFull+1, 0)
	ds, db := e.mustRegister(small), e.mustRegister(big)
	if len(ds.Locations) != 1 || !ds.Locations[0].Primary || ds.Locations[0].Link != catalog.LinkRegistered || ds.Locations[0].Kind != catalog.LocRegistered {
		t.Fatalf("locations: %+v", ds.Locations)
	}
	l := ds.Locations[0]
	want, _ := idty.Sampled(small)
	if l.Sampled == nil || l.Sampled.Value != want || l.Sampled.By != thisComputer || l.Sampled.At.IsZero() {
		t.Fatalf("sampled: %+v", l.Sampled)
	}
	if l.Full == nil || l.Full.Value != fileSHA(t, small) {
		t.Fatalf("a small file gets its full SHA-256 at registration: %+v", l.Full)
	}
	bl := db.Locations[0]
	if bl.Sampled == nil || bl.Full != nil {
		t.Fatalf("a large file gets the sampled fingerprint only (never a hash of every byte on the registration path): %+v %+v", bl.Sampled, bl.Full)
	}
	// registering again after the file changed drops the old fingerprints and takes new ones: a hash never stays on other bytes
	old := l.Full.Value
	e.write("small.fits", []byte("small file bytes, changed"))
	again := e.mustRegister(small)
	if got := again.Locations[0]; got.Full == nil || got.Full.Value == old || got.Full.Value != fileSHA(t, small) {
		t.Fatalf("after a change: %+v", got.Full)
	}
}

func TestRegisteringAFileWithTheSameContentOffersALocationInstead(t *testing.T) {
	t.Parallel()
	e := newIDEnv(t)
	a := e.write("a.fits", []byte("identical bytes"))
	first := e.mustRegister(a)
	b := e.write("copy_of_a.fits", []byte("identical bytes"))
	code, _, raw := e.register(b, false)
	var offer struct {
		Code    string `json:"code"`
		Matches []struct {
			DatasetID int64  `json:"datasetId"`
			Relation  string `json:"relation"`
		} `json:"matches"`
	}
	_ = json.Unmarshal(raw, &offer)
	if code != 409 || offer.Code != "same_content" || len(offer.Matches) != 1 || offer.Matches[0].DatasetID != first.ID || offer.Matches[0].Relation != "verified" {
		t.Fatalf("offer: %d %s", code, raw)
	}
	var list struct{ Datasets []catalog.Dataset }
	_, lb := e.call("GET", "/api/datasets", nil)
	_ = json.Unmarshal(lb, &list)
	if len(list.Datasets) != 1 {
		t.Fatalf("the offer must not create a dataset: %d", len(list.Datasets))
	}
	// the researcher can still register it as its own dataset
	if code, _, raw := e.register(b, true); code != 201 {
		t.Fatalf("as new: %d %s", code, raw)
	}
	// different bytes of the same size are not offered; re-registering the same path refreshes it (nothing to offer)
	c := e.write("other.fits", []byte("different byte"))
	if code, _, raw := e.register(c, false); code != 201 {
		t.Fatalf("different content: %d %s", code, raw)
	}
	if code, _, raw := e.register(a, false); code != 201 {
		t.Fatalf("same path again: %d %s", code, raw)
	}
}

func TestSampledEqualityIsOfferedAsProbableNeverAsVerified(t *testing.T) {
	t.Parallel()
	e := newIDEnv(t)
	first := e.mustRegister(e.sparse("big_a.fits", idty.SmallFull+4096, 7))
	code, _, raw := e.register(e.sparse("big_b.fits", idty.SmallFull+4096, 7), false)
	var offer struct {
		Matches []struct {
			DatasetID int64  `json:"datasetId"`
			Relation  string `json:"relation"`
		} `json:"matches"`
	}
	_ = json.Unmarshal(raw, &offer)
	if code != 409 || len(offer.Matches) != 1 || offer.Matches[0].Relation != "probable" || offer.Matches[0].DatasetID != first.ID {
		t.Fatalf("a large file with equal size and sampled fingerprint is only probably the same: %d %s", code, raw)
	}
	// linking it records "probable", and the viewer does not read from it
	code, l, m := e.addLocation(first.ID, filepath.Join(e.root, "big_b.fits"), false)
	if code != 201 || l.Link != catalog.LinkProbable || l.Full != nil || l.Sampled == nil {
		t.Fatalf("probable link: %d %+v %v", code, l, m)
	}
	if got := catalog.ReadOrder(e.dataset(first.ID).Locations); len(got) != 1 || !got[0].Primary {
		t.Fatalf("a probable location must not be a read source: %+v", got)
	}
}

func TestLinkingRulesVerifiedProbableDeclaredAndRefused(t *testing.T) {
	t.Parallel()
	e := newIDEnv(t)
	a := e.write("a.fits", []byte("same bytes"))
	d := e.mustRegister(a)

	// equal full SHA-256: verified, and it is a copy of the dataset, not a second dataset
	code, l, _ := e.addLocation(d.ID, e.write("a_copy.fits", []byte("same bytes")), false)
	if code != 201 || l.Link != catalog.LinkVerified || l.Full == nil || l.Kind != catalog.LocCopy || l.Primary {
		t.Fatalf("verified: %d %+v", code, l)
	}
	// a file under the managed directory is a managed location
	mp := filepath.Join(e.managed, "m.fits")
	os.WriteFile(mp, []byte("same bytes"), 0o444)
	e.srv.Roots, _ = sourceroot.New([]string{e.root, e.managed})
	if code, l, _ := e.addLocation(d.ID, mp, false); code != 201 || l.Kind != catalog.LocManaged || l.Link != catalog.LinkVerified {
		t.Fatalf("managed: %d %+v", code, l)
	}
	// the same file twice, and a file that is another dataset's registered file
	if code, _, m := e.addLocation(d.ID, filepath.Join(e.root, "a_copy.fits"), false); code != 409 || m["code"] != "already_location" {
		t.Fatalf("twice: %d %v", code, m)
	}
	other := e.mustRegister(e.write("other.fits", []byte("another!!!!")))
	if code, _, m := e.addLocation(d.ID, filepath.Join(e.root, "other.fits"), false); code != 409 || m["code"] != "already_location" || int64(m["datasetId"].(float64)) != other.ID {
		t.Fatalf("another dataset's file: %d %v", code, m)
	}
	// proven different (the sizes differ): refused, and a declaration does not override proof
	diff := e.write("diff.fits", []byte("not the same bytes at all"))
	for _, declare := range []bool{false, true} {
		if code, _, m := e.addLocation(d.ID, diff, declare); code != 409 || m["code"] != "bytes_differ" {
			t.Fatalf("different, declare=%v: %d %v", declare, code, m)
		}
	}
	// outside the roots, and unknown datasets
	if code, _, _ := e.addLocation(d.ID, filepath.Join(filepath.Dir(e.root), "nope.fits"), false); code != 403 {
		t.Fatalf("outside the roots: %d", code)
	}
	if code, _, _ := e.addLocation(9999, a, false); code != 404 {
		t.Fatalf("unknown dataset: %d", code)
	}

	// nothing computed can tell: the registered file is gone and its fingerprints were never taken. Without a declaration the link
	// is refused; with one it is recorded as declared, which is the researcher's word and nothing more.
	gone := e.mustRegister(e.write("gone.fits", []byte("will vanish")))
	if _, err := e.db.Exec(`UPDATE dataset_locations SET sampled_fp='', sampled_at=0, sampled_by='', full_sha256='', full_at=0, full_by='' WHERE dataset_id=?`, gone.ID); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(e.root, "gone.fits"))
	cand := e.write("gone_elsewhere.fits", []byte("will vanish"))
	if code, _, m := e.addLocation(gone.ID, cand, false); code != 409 || m["code"] != "identity_not_established" {
		t.Fatalf("undeclared: %d %v", code, m)
	}
	if code, l, _ := e.addLocation(gone.ID, cand, true); code != 201 || l.Link != catalog.LinkDeclared || l.Sampled == nil {
		t.Fatalf("declared: %d %+v", code, l)
	}

	// a declaration is not evidence: a file whose hash equals a declared location's is not "verified" by it
	again := e.write("gone_again.fits", []byte("will vanish"))
	if code, _, m := e.addLocation(gone.ID, again, false); code != 409 || m["code"] != "identity_not_established" {
		t.Fatalf("equal to a declared location only: %d %v", code, m)
	}
	if got := catalog.ReadOrder(e.dataset(gone.ID).Locations); len(got) != 1 {
		t.Fatalf("a declared location is not read from: %+v", got)
	}

	// removing a location forgets it; the registered file stays; the file on disk is untouched
	locs := e.dataset(d.ID).Locations
	if code, _ := e.call("POST", "/api/datasets/"+itoa(int(d.ID))+"/locations/"+itoa(int(locs[0].ID))+"/delete", map[string]any{}); code != 409 {
		t.Fatalf("removing the registered file: %d", code)
	}
	if code, _ := e.call("POST", "/api/datasets/"+itoa(int(d.ID))+"/locations/"+itoa(int(locs[1].ID))+"/delete", map[string]any{}); code != 200 {
		t.Fatalf("removing a location: %d", code)
	}
	if _, err := os.Stat(filepath.Join(e.root, "a_copy.fits")); err != nil {
		t.Fatalf("the file must stay: %v", err)
	}
}

func TestFullHashRunsInTheBackgroundIsCappedAndCancellable(t *testing.T) {
	t.Parallel()
	e := newIDEnv(t)
	big := e.sparse("big.fits", idty.SmallFull+1<<20, 3)
	d := e.mustRegister(big)
	lid := d.Locations[0].ID
	fp := func(mode string) (int, []byte) {
		return e.call("POST", "/api/datasets/"+itoa(int(d.ID))+"/locations/"+itoa(int(lid))+"/fingerprint", map[string]any{"mode": mode})
	}
	status := func() map[string]hashStatus {
		_, b := e.call("GET", "/api/datasets/"+itoa(int(d.ID))+"/locations", nil)
		var out struct {
			Hashing map[string]hashStatus `json:"hashing"`
		}
		_ = json.Unmarshal(b, &out)
		return out.Hashing
	}
	// the cap is enforced before anything is read
	e.srv.FullHashCap = 1 << 20
	if code, b := fp("full"); code != 422 || !strings.Contains(string(b), "cap") {
		t.Fatalf("over the cap: %d %s", code, b)
	}
	e.srv.FullHashCap = 0
	// start it, hold it before it reads, cancel it: nothing is stored
	hold, release := make(chan struct{}), make(chan struct{})
	e.srv.hashing.beforeRead = func() { close(hold); <-release }
	if code, b := fp("full"); code != 202 {
		t.Fatalf("start: %d %s", code, b)
	}
	<-hold
	if st := status()[itoa(int(lid))]; st.State != "running" || st.TotalBytes != d.SizeBytes {
		t.Fatalf("status: %+v", st)
	}
	if code, _ := fp("full"); code != 409 {
		t.Fatalf("a second start while running: %d", code)
	}
	if code, _ := fp("cancel"); code != 202 {
		t.Fatalf("cancel: %d", code)
	}
	close(release)
	e.srv.waitHashes()
	if loc := e.dataset(d.ID).Locations[0]; loc.Full != nil {
		t.Fatalf("a cancelled hash stores nothing: %+v", loc.Full)
	}
	if st, ok := status()[itoa(int(lid))]; ok {
		t.Fatalf("a cancelled task is gone: %+v", st)
	}
	if code, _ := fp("cancel"); code != 409 {
		t.Fatalf("cancel with nothing running: %d", code)
	}
	// run it to the end: the value is the SHA-256 of every byte, labelled with who computed it and when
	e.srv.hashing.beforeRead = nil
	if code, b := fp("full"); code != 202 {
		t.Fatalf("restart: %d %s", code, b)
	}
	e.srv.waitHashes()
	loc := e.dataset(d.ID).Locations[0]
	if loc.Full == nil || loc.Full.Value != fileSHA(t, big) || loc.Full.By != thisComputer || loc.Full.At.IsZero() {
		t.Fatalf("full: %+v", loc.Full)
	}
	// a sampled fingerprint can be asked for at any time
	if code, b := fp("sampled"); code != 200 {
		t.Fatalf("sampled: %d %s", code, b)
	}
	if code, _ := fp("bogus"); code != 400 {
		t.Fatalf("unknown mode: %d", code)
	}
	// a file that changed while queued is refused, not hashed as if it were the registered one
	os.WriteFile(big, []byte("changed"), 0o644)
	e.srv.FullHashCap = 0
	if code, _ := fp("sampled"); code != 409 {
		t.Fatalf("a changed file: %d", code)
	}
}

func TestNewerResultsForARerunAFileChangedInPlaceAndFiguresDrawnFromIt(t *testing.T) {
	t.Parallel()
	e := newIDEnv(t)
	psi := e.write("psi_hnv1.fits", []byte("first run output, version one"))
	d := e.mustRegister(psi)
	type notes struct {
		Datasets map[string][]Newer `json:"datasets"`
		Figures  map[string][]Newer `json:"figures"`
	}
	newer := func() notes {
		e.t.Helper()
		code, b := e.call("GET", "/api/datasets/newer", nil)
		if code != 200 {
			e.t.Fatalf("newer: %d %s", code, b)
		}
		var n notes
		if err := json.Unmarshal(b, &n); err != nil {
			e.t.Fatal(err)
		}
		return n
	}
	id := itoa(int(d.ID))
	// a figure records the identity of what it was drawn from, owner-side only
	view := map[string]any{"kind": "plane", "channel": 3, "colormap": "viridis", "stretch": "linear", "range": "percentile"}
	code, fb := e.call("POST", "/api/figures", map[string]any{"datasetId": d.ID, "title": "Psi", "view": view, "previewPng": testPNG(t)})
	if code != 201 {
		t.Fatalf("figure: %d %s", code, fb)
	}
	var fig boards.Figure
	json.Unmarshal(fb, &fig)
	var ident figureIdentity
	if err := json.Unmarshal(fig.Identity, &ident); err != nil || ident.Schema != "figure-identity/1" || len(ident.Datasets) != 1 {
		t.Fatalf("identity: %s", fig.Identity)
	}
	src := ident.Datasets[0]
	if src.Role != "source" || src.DatasetID != d.ID || src.SizeBytes != d.SizeBytes || src.Sampled == nil || src.Sampled.Algorithm != "sampled/1" || src.Sampled.Covers != idty.Covers || src.Full == nil || src.Full.Value != fileSHA(t, psi) {
		t.Fatalf("figure identity: %+v", src)
	}
	if strings.Contains(string(fig.Provenance), src.Sampled.Value) || strings.Contains(string(fig.Provenance), src.Full.Value) {
		t.Fatal("the reviewer-safe provenance must not carry the identity")
	}

	// the dataset is an output of a run (this is what makes a sibling "the same role")
	if _, err := e.db.Exec(`INSERT INTO runs(id,dir,name,kind,imported_at,generated_at,git_commit,input_name,input_size,params,diagnostics) VALUES(1,'/x','run','vgt',0,'','','',0,'{}','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO run_outputs(run_id,role,dataset_id,name) VALUES(1,'psi',?,'psi_hnv1.fits')`, d.ID); err != nil {
		t.Fatal(err)
	}
	if n := newer(); len(n.Datasets) != 0 || len(n.Figures) != 0 {
		t.Fatalf("nothing is newer yet: %+v", n)
	}

	// the re-run writes psi_hnv1_1.fits next to it instead of overwriting; unrelated names and other extensions are ignored
	e.write("psi_hnv10_1.fits", []byte("another family"))
	e.write("psi_hnv1_1.txt", []byte("not the same extension"))
	e.write("psi_hnv1.fits.bak", []byte("nor this"))
	e.write("psi_hnv1_1.fits", []byte("second run output, longer than the first one"))
	e.write("psi_hnv1_2.fits", []byte("third run output"))
	n := newer()
	got := n.Datasets[id]
	if len(got) != 1 || got[0].Reason != "newer_file" || got[0].Name != "psi_hnv1_2.fits" || got[0].DatasetID != 0 {
		t.Fatalf("newer file: %+v", got)
	}
	if strings.Join(got[0].Differs, ",") != "size,time,fingerprint" {
		t.Fatalf("what differs: %v", got[0].Differs)
	}
	// the figure drawn from the older output says so, and nothing was swapped
	if f := n.Figures[itoa(int(fig.ID))]; len(f) != 1 || f[0].Reason != "newer_file" || f[0].Name != "psi_hnv1_2.fits" {
		t.Fatalf("figure notice: %+v", n.Figures)
	}
	if e.dataset(d.ID).Path != psi {
		t.Fatal("a newer result is never swapped in")
	}
	// registered: the notice names the dataset
	nd := e.mustRegister(filepath.Join(e.root, "psi_hnv1_2.fits"))
	if got := newer().Datasets[id]; len(got) != 1 || got[0].DatasetID != nd.ID {
		t.Fatalf("registered newer file: %+v", got)
	}
}

func TestAFileChangedInPlaceReportsWhatDiffers(t *testing.T) {
	t.Parallel()
	e := newIDEnv(t)
	p := e.write("map.fits", []byte("0123456789abcdef"))
	d := e.mustRegister(p)
	id := itoa(int(d.ID))
	notes := func() []Newer {
		e.t.Helper()
		_, b := e.call("GET", "/api/datasets/newer", nil)
		var n struct {
			Datasets map[string][]Newer `json:"datasets"`
		}
		json.Unmarshal(b, &n)
		return n.Datasets[id]
	}
	if len(notes()) != 0 {
		t.Fatal("unchanged")
	}
	later := time.Now().Add(time.Hour)
	// touched only: the time differs, and the fingerprint is the same: that is said, not guessed
	os.Chtimes(p, later, later)
	if n := notes(); len(n) != 1 || n[0].Reason != "changed_in_place" || strings.Join(n[0].Differs, ",") != "time" {
		t.Fatalf("touched: %+v", n)
	}
	// other bytes of the same size
	os.WriteFile(p, []byte("0123456789abcdeX"), 0o644)
	os.Chtimes(p, later, later)
	if n := notes(); len(n) != 1 || strings.Join(n[0].Differs, ",") != "time,fingerprint" {
		t.Fatalf("changed: %+v", n)
	}
	// reading it again takes the new fingerprint; a figure drawn from the old bytes then says its data changed
	view := map[string]any{"kind": "plane", "channel": 3, "colormap": "viridis", "stretch": "linear", "range": "percentile"}
	os.WriteFile(p, []byte("0123456789abcdef"), 0o644) // back to the first bytes for the figure
	e.mustRegister(p)
	_, fb := e.call("POST", "/api/figures", map[string]any{"datasetId": d.ID, "title": "Map", "view": view, "previewPng": testPNG(t)})
	var fig boards.Figure
	json.Unmarshal(fb, &fig)
	os.WriteFile(p, []byte("0123456789abcdeX"), 0o644)
	e.mustRegister(p)
	_, nb := e.call("GET", "/api/datasets/newer", nil)
	var n struct {
		Figures map[string][]Newer `json:"figures"`
	}
	json.Unmarshal(nb, &n)
	f := n.Figures[itoa(int(fig.ID))]
	if len(f) != 1 || f[0].Reason != "figure_data_changed" || !strings.Contains(strings.Join(f[0].Differs, ","), "fingerprint") {
		t.Fatalf("figure drawn from other bytes: %+v", n.Figures)
	}
}

func TestDeletingADatasetForgetsItsLocationsAndSaysSoInTheImpact(t *testing.T) {
	t.Parallel()
	e := newIDEnv(t)
	d := e.mustRegister(e.write("a.fits", []byte("bytes")))
	if code, _, m := e.addLocation(d.ID, e.write("a2.fits", []byte("bytes")), false); code != 201 {
		t.Fatalf("add: %d %v", code, m)
	}
	_, ib := e.call("GET", "/api/datasets/"+itoa(int(d.ID))+"/impact", nil)
	var im struct {
		Digest string `json:"digest"`
		Groups []struct {
			Key, Effect, Text string
			Count             int
		} `json:"groups"`
	}
	json.Unmarshal(ib, &im)
	found := false
	for _, g := range im.Groups {
		if g.Key == "locations" {
			found = g.Effect == "removed" && g.Count == 1 && strings.Contains(g.Text, "never deleted")
		}
	}
	if !found {
		t.Fatalf("impact: %s", ib)
	}
	if code, b := e.call("POST", "/api/datasets/"+itoa(int(d.ID))+"/delete", map[string]any{"impactDigest": im.Digest}); code != 200 {
		t.Fatalf("delete: %d %s", code, b)
	}
	var n int
	e.db.QueryRow(`SELECT COUNT(*) FROM dataset_locations`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d locations left behind", n)
	}
	if _, err := os.Stat(filepath.Join(e.root, "a2.fits")); err != nil {
		t.Fatal("files are never deleted")
	}
}

// With the real worker: the viewer reads from a verified copy when the registered file is gone, says which, and keeps
// reading the registered file while it is there.
func TestViewerReadsFromTheBestAvailableLocation(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	pw := &worker.Persistent{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	ts, root, _ := newPlaneServerWith(t, headerWorker(t, "python3", repo), pw)
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cube, _ := os.ReadFile(filepath.Join(root, "hi_cube.fits"))
	copyPath := filepath.Join(root, "hi_cube_backup.fits")
	os.WriteFile(copyPath, cube, 0o644)
	var d catalog.Dataset
	resp := post(t, ts, filepath.Join(root, "hi_cube.fits"), nil)
	json.NewDecoder(resp.Body).Decode(&d)
	code, b := callJSON(t, "POST", ts.URL+"/api/datasets/"+itoa(int(d.ID))+"/locations", map[string]any{"path": copyPath})
	var loc catalog.Location
	json.Unmarshal(b, &loc)
	if code != 201 || loc.Link != catalog.LinkVerified {
		t.Fatalf("copy: %d %s", code, b)
	}
	plane := ts.URL + "/api/datasets/" + itoa(int(d.ID)) + "/plane?channel=40"
	r, pb := get(t, plane)
	if r.StatusCode != 200 || r.Header.Get("X-Vesqira-Location") != itoa(int(d.Locations[0].ID)) {
		t.Fatalf("registered file: %d location=%q", r.StatusCode, r.Header.Get("X-Vesqira-Location"))
	}
	os.Remove(filepath.Join(root, "hi_cube.fits"))
	r2, pb2 := get(t, plane)
	if r2.StatusCode != 200 || r2.Header.Get("X-Vesqira-Location") != itoa(int(loc.ID)) || string(pb) != string(pb2) {
		t.Fatalf("verified copy: %d location=%q same pixels=%v", r2.StatusCode, r2.Header.Get("X-Vesqira-Location"), string(pb) == string(pb2))
	}
	_, lb := get(t, ts.URL+"/api/datasets/"+itoa(int(d.ID))+"/locations")
	var out struct {
		ReadsFrom *readsFrom `json:"readsFrom"`
	}
	json.Unmarshal(lb, &out)
	if out.ReadsFrom == nil || out.ReadsFrom.LocationID != loc.ID || out.ReadsFrom.Primary || !strings.Contains(out.ReadsFrom.Reason, "verified") {
		t.Fatalf("readsFrom: %s", lb)
	}
	os.Remove(copyPath)
	if r3, _ := get(t, plane); r3.StatusCode != 403 { // the same refusal a vanished file has always had
		t.Fatalf("nothing left to read: %d", r3.StatusCode)
	}
}

// A file on a host is fingerprinted by that host's own worker, bounded, and the value is the one this computer would compute.
func TestRemoteFingerprintsAreComputedByTheHostsWorker(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	base := t.TempDir()
	remoteHome, remoteRoot, localRoot := filepath.Join(base, "remote-home"), filepath.Join(base, "remote_data"), filepath.Join(base, "localroot")
	for _, d := range []string{remoteHome, remoteRoot, localRoot} {
		os.MkdirAll(d, 0o755)
	}
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), remoteRoot)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	mgr := hosts.NewManager(fakeSSH(t, remoteHome), workerFiles(t, repo))
	mgr.ControlDir = t.TempDir()
	t.Cleanup(mgr.Close)
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	hs, _ := hosts.Open(cat.DB())
	rs, _ := sourceroot.New([]string{localRoot})
	srv := &Server{Roots: rs, Store: cat, Worker: headerWorker(t, "python3", repo), HostDB: hs, Remote: mgr}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	py, _ := exec.LookPath("python3")
	_, hb := postJSON(t, ts.URL+"/api/hosts", map[string]any{"name": "Cluster", "dest": "login1", "python": py, "roots": []string{remoteRoot}})
	var h hosts.Host
	json.Unmarshal(hb, &h)
	r, rb := postJSON(t, ts.URL+"/api/hosts/"+itoa(int(h.ID))+"/register", map[string]any{"path": filepath.Join(remoteRoot, "hi_cube.fits")})
	if r.StatusCode != 201 {
		t.Fatalf("register remote: %d %s", r.StatusCode, rb)
	}
	var d catalog.Dataset
	json.Unmarshal(rb, &d)
	if len(d.Locations) != 1 {
		t.Fatalf("locations: %+v", d.Locations)
	}
	l := d.Locations[0]
	local, err := idty.Sampled(filepath.Join(remoteRoot, "hi_cube.fits"))
	if err != nil {
		t.Fatal(err)
	}
	if l.HostID != h.ID || l.Sampled == nil || l.Sampled.Value != local || l.Sampled.By != "worker on Cluster" || l.Full != nil {
		t.Fatalf("the host's worker and this computer must compute the same sampled fingerprint, and a remote file has no full hash yet: %+v", l)
	}
	// the full hash is computed on the host, within its cap, in the background
	srv.FullHashCap = 1000
	fpURL := ts.URL + "/api/datasets/" + itoa(int(d.ID)) + "/locations/" + itoa(int(l.ID)) + "/fingerprint"
	if r, _ := postJSON(t, fpURL, map[string]any{"mode": "full"}); r.StatusCode != 422 {
		t.Fatalf("over the cap: %d", r.StatusCode)
	}
	srv.FullHashCap = 0
	if r, b := postJSON(t, fpURL, map[string]any{"mode": "full"}); r.StatusCode != 202 {
		t.Fatalf("remote full: %d %s", r.StatusCode, b)
	}
	srv.waitHashes()
	got, _ := cat.Get(d.ID)
	if f := got.Locations[0].Full; f == nil || f.Value != fileSHA(t, filepath.Join(remoteRoot, "hi_cube.fits")) || f.By != "worker on Cluster" {
		t.Fatalf("remote full: %+v", got.Locations[0].Full)
	}
	// the host's worker refuses a file above the cap it is given (bounded on the host as well)
	if _, err := mgr.Do(context.Background(), h, map[string]any{"op": "fingerprint_full", "path": filepath.Join(remoteRoot, "hi_cube.fits"), "max_bytes": 1000}); err == nil {
		t.Fatal("the worker must refuse a file above max_bytes")
	}
	// a file on the host changed since it was registered: the value is refused, not attached to the old record
	os.WriteFile(filepath.Join(remoteRoot, "hi_cube.fits"), []byte("changed on the host"), 0o644)
	if r, _ := postJSON(t, fpURL, map[string]any{"mode": "sampled"}); r.StatusCode != http.StatusConflict {
		t.Fatalf("changed on the host: %d", r.StatusCode)
	}
}

// A copy was verified against the registered file as it was. When that file is read again with other bytes the copy must not go on
// being read under the new header; when the bytes come back (a file only touched) the comparison restores it.
func TestAVerifiedLocationGoesStaleWhenTheRegisteredFileChangesAndIsComparedAgain(t *testing.T) {
	t.Parallel()
	e := newIDEnv(t)
	p := e.write("a.fits", []byte("version one"))
	d := e.mustRegister(p)
	cp := e.write("a_copy.fits", []byte("version one"))
	if code, l, m := e.addLocation(d.ID, cp, false); code != 201 || l.Link != catalog.LinkVerified {
		t.Fatalf("add: %d %v", code, m)
	}
	links := func() (string, int) {
		got := e.dataset(d.ID).Locations
		return got[1].Link, len(catalog.ReadOrder(got))
	}
	e.write("a.fits", []byte("version two!"))
	e.mustRegister(p) // "Read it again"
	if link, readable := links(); link != catalog.LinkStale || readable != 1 {
		t.Fatalf("after the registered file changed: link %q, %d readable", link, readable)
	}
	_, lb := e.call("GET", "/api/datasets/"+itoa(int(d.ID))+"/locations", nil)
	var out struct {
		ReadsFrom *readsFrom `json:"readsFrom"`
	}
	json.Unmarshal(lb, &out)
	if out.ReadsFrom == nil || !out.ReadsFrom.Primary {
		t.Fatalf("the registered file is read: %s", lb)
	}
	// the bytes are back (the file was touched, not changed): compared again, the copy is verified again
	e.write("a.fits", []byte("version one"))
	e.mustRegister(p)
	if link, readable := links(); link != catalog.LinkVerified || readable != 2 {
		t.Fatalf("after the same bytes returned: link %q, %d readable", link, readable)
	}
}

// What a figure records about its data, the owner-side identity, must never reach a reviewer: not the frozen snapshot, not the review
// API, not a published report.
func TestFigureIdentityNeverReachesAReviewerOrAReport(t *testing.T) {
	t.Parallel()
	e := newIDEnv(t)
	d := e.mustRegister(e.write("a.fits", []byte("figure source bytes")))
	view := map[string]any{"kind": "plane", "channel": 3, "colormap": "viridis", "stretch": "linear", "range": "percentile"}
	_, fb := e.call("POST", "/api/figures", map[string]any{"datasetId": d.ID, "title": "Fig", "view": view, "previewPng": testPNG(t)})
	var fig boards.Figure
	json.Unmarshal(fb, &fig)
	var id figureIdentity
	if json.Unmarshal(fig.Identity, &id) != nil || len(id.Datasets) != 1 || id.Datasets[0].Full == nil {
		t.Fatalf("identity: %s", fig.Identity)
	}
	secrets := []string{id.Datasets[0].Full.Value, id.Datasets[0].Sampled.Value, "figure-identity", "fullSha256", "sampledFingerprint"}
	_, bb := e.call("POST", "/api/boards", map[string]any{"title": "Board"})
	var b boards.Board
	json.Unmarshal(bb, &b)
	e.call("POST", "/api/boards/"+itoa(int(b.ID))+"/items", map[string]any{"figureId": fig.ID})
	code, pb := e.call("POST", "/api/boards/"+itoa(int(b.ID))+"/publish", map[string]any{"label": "v1", "hours": 24})
	if code != 201 {
		t.Fatalf("publish: %d %s", code, pb)
	}
	var pub struct{ Token string }
	json.Unmarshal(pb, &pub)
	rv := httptest.NewServer((&review.Handler{Store: e.srv.Boards}).Handler())
	defer rv.Close()
	_, review := get(t, rv.URL+"/review-api/"+pub.Token)
	if !strings.Contains(string(review), "Fig") {
		t.Fatalf("the reviewer sees the board: %s", review)
	}
	_, snaps := e.call("GET", "/api/boards/"+itoa(int(b.ID))+"/snapshots", nil)
	for _, secret := range secrets {
		if strings.Contains(string(review), secret) || strings.Contains(string(snaps), secret) {
			t.Errorf("%q reached the review API or the snapshot list", secret)
		}
	}
	// and the owner-side snapshot's frozen copy has no field that could carry it
	for _, v := range []any{boards.FrozenItem{}, publish.Figure{}} {
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			if n := rt.Field(i).Name; n == "Identity" || n == "Scene" || n == "DatasetID" {
				t.Errorf("%s has a field %s: an owner-side field must not be copied into what a reviewer or a report holds", rt, n)
			}
		}
	}
}

func TestWhatAFigureSaysAboutTheBytesIsWhatWasRecorded(t *testing.T) {
	t.Parallel()
	e := newIDEnv(t)
	small := e.mustRegister(e.write("small.fits", []byte("small")))
	big := e.mustRegister(e.sparse("big.fits", idty.SmallFull+1, 1))
	view := map[string]any{"kind": "plane", "channel": 3, "colormap": "viridis", "stretch": "linear", "range": "percentile"}
	identityOf := func(d catalog.Dataset) string {
		_, fb := e.call("POST", "/api/figures", map[string]any{"datasetId": d.ID, "title": "Fig", "view": view, "previewPng": testPNG(t)})
		var fig boards.Figure
		json.Unmarshal(fb, &fig)
		var pr Provenance
		json.Unmarshal(fig.Provenance, &pr)
		return pr.Source.Identity
	}
	if got := identityOf(small); !strings.Contains(got, "the SHA-256 of its bytes was recorded") || strings.Contains(got, "not hashed") {
		t.Errorf("a small file has a full hash: %q", got)
	}
	if got := identityOf(big); !strings.Contains(got, "sampled fingerprint of it (not a hash of the whole file)") || strings.Contains(got, "SHA-256 of its bytes") {
		t.Errorf("a large file has only the sampled fingerprint: %q", got)
	}
	// a file registered before fingerprints existed gets its cheap one when a figure is saved, and the figure says so
	if _, err := e.db.Exec(`UPDATE dataset_locations SET sampled_fp='', sampled_at=0, sampled_by='', full_sha256='', full_at=0, full_by='' WHERE dataset_id=?`, big.ID); err != nil {
		t.Fatal(err)
	}
	if got := identityOf(big); !strings.Contains(got, "sampled fingerprint of it") {
		t.Errorf("an old registration: %q", got)
	}
}

func TestAChangedFileKeepsItsChangedFlagEvenWithAVerifiedCopy(t *testing.T) {
	t.Parallel()
	e := newIDEnv(t)
	a := e.write("a.fits", []byte("bytes"))
	d := e.mustRegister(a)
	if code, _, m := e.addLocation(d.ID, e.write("a_copy.fits", []byte("bytes")), false); code != 201 {
		t.Fatalf("add: %d %v", code, m)
	}
	e.write("a.fits", []byte("bytes, edited"))
	_, b := e.call("GET", "/api/datasets/availability", nil)
	var out struct{ Problems map[string]string }
	json.Unmarshal(b, &out)
	if out.Problems[itoa(int(d.ID))] != "modified" {
		t.Fatalf("a file that changed is still said to have changed (so it can be read again): %v", out.Problems)
	}
}

func TestAFileThatIsAnotherDatasetsLocationIsNotRegisteredAsASecondDataset(t *testing.T) {
	t.Parallel()
	e := newIDEnv(t)
	d := e.mustRegister(e.write("a.fits", []byte("bytes")))
	copyPath := e.write("a_copy.fits", []byte("bytes"))
	if code, _, m := e.addLocation(d.ID, copyPath, false); code != 201 {
		t.Fatalf("add: %d %v", code, m)
	}
	if code, _, raw := e.register(copyPath, true); code != 409 || !strings.Contains(string(raw), "already another location of a dataset") {
		t.Fatalf("registering a location as a dataset: %d %s", code, raw)
	}
	var list struct{ Datasets []catalog.Dataset }
	_, lb := e.call("GET", "/api/datasets", nil)
	json.Unmarshal(lb, &list)
	if len(list.Datasets) != 1 || len(list.Datasets[0].Locations) != 2 {
		t.Fatalf("the refusal must change nothing: %+v", list.Datasets)
	}
}

func TestAvailabilityIgnoresAMissingRegisteredFileWhileAVerifiedCopyHereReads(t *testing.T) {
	t.Parallel()
	e := newIDEnv(t)
	a := e.write("a.fits", []byte("bytes"))
	d := e.mustRegister(a)
	copyPath := e.write("a_copy.fits", []byte("bytes"))
	if code, _, m := e.addLocation(d.ID, copyPath, false); code != 201 {
		t.Fatalf("add: %d %v", code, m)
	}
	problems := func() map[string]string {
		_, b := e.call("GET", "/api/datasets/availability", nil)
		var out struct{ Problems map[string]string }
		json.Unmarshal(b, &out)
		return out.Problems
	}
	if len(problems()) != 0 {
		t.Fatalf("nothing is wrong yet: %v", problems())
	}
	os.Remove(a)
	if p := problems(); len(p) != 0 {
		t.Fatalf("a verified copy on this computer reads, so the dataset is not missing: %v", p)
	}
	os.Remove(copyPath)
	if p := problems(); p[itoa(int(d.ID))] != "missing" {
		t.Fatalf("with nothing left to read it is missing: %v", p)
	}
}

func TestIdentityResponsesMatchTheSpec(t *testing.T) {
	t.Parallel()
	doc := loadSpec(t)
	e := newIDEnv(t)
	d := e.mustRegister(e.write("a.fits", []byte("bytes")))
	code, b := e.call("POST", "/api/datasets/"+itoa(int(d.ID))+"/locations", map[string]any{"path": e.write("a2.fits", []byte("bytes"))})
	if code != 201 {
		t.Fatalf("add: %d %s", code, b)
	}
	conforms(t, responseSchemaAt(t, doc, "/api/datasets/{id}/locations", "POST", 201, "application/json"), "POST locations", b)
	_, lb := e.call("GET", "/api/datasets/"+itoa(int(d.ID))+"/locations", nil)
	conforms(t, responseSchema(t, doc, "/api/datasets/{id}/locations", "GET", "application/json"), "GET locations", lb)
	_, nb := e.call("GET", "/api/datasets/newer", nil)
	conforms(t, responseSchema(t, doc, "/api/datasets/newer", "GET", "application/json"), "GET newer", nb)
	code, refusal := e.call("POST", "/api/datasets/"+itoa(int(d.ID))+"/locations", map[string]any{"path": e.write("far.fits", []byte("different length bytes"))})
	if code != 409 {
		t.Fatalf("refusal: %d %s", code, refusal)
	}
	conforms(t, responseSchemaAt(t, doc, "/api/datasets/{id}/locations", "POST", 409, "application/json"), "409 refusal", refusal)
	lid := d.Locations[0].ID
	_, sb := e.call("POST", "/api/datasets/"+itoa(int(d.ID))+"/locations/"+itoa(int(lid))+"/fingerprint", map[string]any{"mode": "sampled"})
	conforms(t, responseSchema(t, doc, "/api/datasets/{id}/locations/{lid}/fingerprint", "POST", "application/json"), "fingerprint sampled", sb)
	// an offer of a location at registration
	code, offer := e.call("POST", "/api/datasets", map[string]any{"path": e.write("a3.fits", []byte("bytes"))})
	if code != 409 {
		t.Fatalf("offer: %d %s", code, offer)
	}
	conforms(t, doc.Components.Schemas["LocationRefusal"], "same_content offer", offer)
}
