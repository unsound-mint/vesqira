package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/transfers"
)

// hostDataset registers a file of the stand-in host as a dataset, with or without the full SHA-256 recorded for it.
func (e *xferEnv) hostDataset(name string, withFull bool) catalog.Dataset {
	e.t.Helper()
	p := filepath.Join(e.remote, name)
	info, err := os.Stat(p)
	if err != nil {
		e.t.Fatal(err)
	}
	d, err := e.cat.Upsert(catalog.Dataset{HostID: e.host.ID, Path: p, Name: name, SizeBytes: info.Size(), ModifiedAt: info.ModTime(), Header: json.RawMessage(`{}`)})
	if err != nil {
		e.t.Fatal(err)
	}
	if withFull {
		b, _ := os.ReadFile(p)
		for _, l := range d.Locations {
			if err := e.cat.SetFingerprint(l.ID, "full", sha(b), "test", info.Size(), info.ModTime().UnixNano()); err != nil {
				e.t.Fatal(err)
			}
		}
	}
	return d
}

func TestAVerifiedDownloadOfARegisteredHostFileBecomesAVerifiedLocation(t *testing.T) {
	e := newXferEnv(t)
	b := e.file(e.remote, "cube.fits", 5000)
	d := e.hostDataset("cube.fits", true)
	e.download("cube.fits", nil)
	e.run()
	locs, err := e.cat.Locations(d.ID)
	if err != nil || len(locs) != 2 {
		t.Fatalf("%+v %v", locs, err)
	}
	var got catalog.Location
	for _, l := range locs {
		if !l.Primary {
			got = l
		}
	}
	if got.HostID != 0 || got.Path != filepath.Join(e.local, "cube.fits") || got.Kind != catalog.LocDownloaded || got.Link != catalog.LinkVerified ||
		got.Full == nil || got.Full.Value != sha(b) || got.SizeBytes != 5000 {
		t.Fatalf("%+v", got)
	}
}

func (e *xferEnv) auditActions(action string) []string {
	var out []string
	es, _ := e.srv.Audit.List("", 0, 500)
	for _, a := range es {
		if a.Action == action {
			out = append(out, a.Detail)
		}
	}
	return out
}

func TestAVerifiedDownloadRecordsTheSourcesFullHashWhenItStillIsTheRegisteredFile(t *testing.T) {
	e := newXferEnv(t)
	b := e.file(e.remote, "nohash.fits", 3000)
	d := e.hostDataset("nohash.fits", false) // registered, never hashed in full
	tr := e.download("nohash.fits", nil)
	e.run()
	locs, _ := e.cat.Locations(d.ID)
	if len(locs) != 2 {
		t.Fatalf("%+v", locs)
	}
	for _, l := range locs {
		if l.Full == nil || l.Full.Value != sha(b) {
			t.Fatalf("no full hash on %+v", l)
		}
		if l.Primary && l.Full.By != "full sha256 computed by transfer "+itoa(int(tr.ID)) {
			t.Errorf("method %q", l.Full.By)
		}
		if !l.Primary && l.Link != catalog.LinkVerified {
			t.Errorf("copy link %q", l.Link)
		}
	}
}

func TestADownloadIsNotLinkedAndNothingIsRecordedWhenTheSourceChangedSinceRegistration(t *testing.T) {
	e := newXferEnv(t)
	e.file(e.remote, "nohash.fits", 100)
	d := e.hostDataset("nohash.fits", false)
	// the file on the host changed after it was registered: the transfer's hash says nothing about the registered dataset
	os.WriteFile(filepath.Join(e.remote, "nohash.fits"), []byte("other bytes entirely"), 0o644)
	e.file(e.remote, "differs.fits", 100)
	d2 := e.hostDataset("differs.fits", true)
	os.WriteFile(filepath.Join(e.remote, "differs.fits"), []byte("other bytes entirely"), 0o644)
	e.download("nohash.fits", nil)
	e.download("differs.fits", nil)
	e.file(e.remote, "unrelated.fits", 77) // not a dataset at all
	e.download("unrelated.fits", nil)
	e.run()
	for _, id := range []int64{d.ID, d2.ID} {
		locs, _ := e.cat.Locations(id)
		if len(locs) != 1 {
			t.Errorf("dataset %d got a location without evidence: %+v", id, locs)
		}
	}
	if locs, _ := e.cat.Locations(d.ID); locs[0].Full != nil {
		t.Errorf("a hash was recorded for bytes that are not the registered ones: %+v", locs[0].Full)
	}
	if got := e.auditActions("downloaded copy not linked"); len(got) != 2 || !strings.Contains(strings.Join(got, "\n"), "may be stale") {
		t.Errorf("audit: %q", got)
	}
}

func TestRegisteringAVerifiedDownloadWithTheSameBytesAsADatasetOffersALocation(t *testing.T) {
	e := newXferEnv(t)
	e.srv.Worker = hdrWorker{}
	b := e.file(e.remote, "twin.fits", 4000)
	// a dataset on this computer whose registered file has the same bytes (full hash recorded)
	own := filepath.Join(e.local, "elsewhere.fits")
	os.WriteFile(own, b, 0o644)
	info, _ := os.Stat(own)
	d, err := e.cat.Upsert(catalog.Dataset{Path: own, Name: "elsewhere.fits", SizeBytes: info.Size(), ModifiedAt: info.ModTime(), Header: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range d.Locations {
		if err := e.cat.SetFingerprint(l.ID, "full", sha(b), "test", info.Size(), info.ModTime().UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	tr := e.download("twin.fits", nil)
	e.run()
	if g := e.get(tr.ID); g.Verif.Status != transfers.Verified {
		t.Fatalf("%+v", g)
	}
	r, body := postJSON(t, e.api("/api/transfers/"+itoa(int(tr.ID))+"/register"), map[string]any{})
	var offer struct {
		Code    string `json:"code"`
		Matches []struct {
			DatasetID int64  `json:"datasetId"`
			Relation  string `json:"relation"`
		} `json:"matches"`
	}
	_ = json.Unmarshal(body, &offer)
	if r.StatusCode != 409 || offer.Code != "same_content" || len(offer.Matches) != 1 || offer.Matches[0].DatasetID != d.ID || offer.Matches[0].Relation != "verified" {
		t.Fatalf("%d %s", r.StatusCode, body)
	}
	if all, _ := e.cat.List(); len(all) != 1 {
		t.Fatalf("a duplicate dataset was created: %d datasets", len(all))
	}
	// the researcher's choice to keep it as its own dataset is honoured
	r, body = postJSON(t, e.api("/api/transfers/"+itoa(int(tr.ID))+"/register"), map[string]any{"asNew": true})
	if r.StatusCode != 201 {
		t.Fatalf("%d %s", r.StatusCode, body)
	}
}
