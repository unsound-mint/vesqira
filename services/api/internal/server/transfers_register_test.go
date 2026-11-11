package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/transfers"
)

// tinyFits is a 4x4 float32 image, small enough to write by hand.
func tinyFits() []byte {
	var hdr strings.Builder
	for _, c := range []string{"SIMPLE  =                    T", "BITPIX  =                  -32", "NAXIS   =                    2", "NAXIS1  =                    4", "NAXIS2  =                    4", "END"} {
		hdr.WriteString(fmt.Sprintf("%-80s", c))
	}
	b := []byte(fmt.Sprintf("%-2880s", hdr.String()))
	return append(b, make([]byte, 2880)...)
}

func TestAVerifiedDownloadIsRegisteredWithAnExternalReferenceAndNoHostPathInTheSharedSummary(t *testing.T) {
	e := newXferEnv(t)
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	repo, _ := filepath.Abs("../../../..")
	e.srv.Worker = headerWorker(t, "python3", repo)
	if err := os.WriteFile(filepath.Join(e.remote, "tiny.fits"), tinyFits(), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := e.download("tiny.fits", nil)
	e.run()
	g := e.get(tr.ID)
	if g.Verif.Status != transfers.Verified {
		t.Fatalf("%+v", g)
	}
	r, b := postJSON(t, e.api("/api/transfers/"+itoa(int(tr.ID))+"/register"), map[string]any{})
	if r.StatusCode != 201 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	var out struct {
		Dataset  catalog.Dataset    `json:"dataset"`
		Transfer transfers.Transfer `json:"transfer"`
	}
	_ = json.Unmarshal(b, &out)
	if out.Dataset.ID == 0 || out.Transfer.DatasetID == nil || *out.Transfer.DatasetID != out.Dataset.ID || out.Dataset.Path != filepath.Join(e.local, "tiny.fits") {
		t.Fatalf("%s", b)
	}
	rec, err := e.srv.Derived.Get(out.Dataset.ID)
	if err != nil || rec.Op != "transfer" {
		t.Fatalf("%+v %v", rec, err)
	}
	var sum map[string]any
	_ = json.Unmarshal(rec.Summary, &sum)
	if sum["host"] != "Cluster" || sum["path"] != filepath.Join(e.remote, "tiny.fits") || sum["sha256"] != g.Verif.DestSHA256 ||
		!strings.Contains(fmt.Sprint(sum["status"]), "external reference") {
		t.Fatalf("%v", sum)
	}
	// what travels with a figure carries the policy sentence, never the host path or the host name
	shared := e.srv.derivedSummary(out.Dataset.ID)
	blob, _ := json.Marshal(shared)
	if shared == nil || strings.Contains(string(blob), e.remote) || strings.Contains(string(blob), "Cluster") || !strings.Contains(shared.Policy, "external reference") {
		t.Fatalf("%s", blob)
	}
	// asking again returns the same dataset
	r, b = postJSON(t, e.api("/api/transfers/"+itoa(int(tr.ID))+"/register"), map[string]any{})
	var again struct{ Dataset catalog.Dataset }
	_ = json.Unmarshal(b, &again)
	if r.StatusCode != 200 || again.Dataset.ID != out.Dataset.ID {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	// a file that changed after it was verified is refused, a folder and an upload are not datasets
	e.file(e.remote, "other.fits", 10)
	o := e.download("other.fits", nil)
	e.run()
	os.WriteFile(filepath.Join(e.local, "other.fits"), []byte("changed after the check"), 0o644)
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(o.ID))+"/register"), map[string]any{}); r.StatusCode != 409 {
		t.Errorf("registered a file that changed after verification: %d", r.StatusCode)
	}
	e.file(e.local, "up.fits", 10)
	up := e.upload("up.fits", nil)
	e.run()
	if r, _ := postJSON(t, e.api("/api/transfers/"+itoa(int(up.ID))+"/register"), map[string]any{}); r.StatusCode != 409 {
		t.Errorf("registered an upload: %d", r.StatusCode)
	}
}

func TestCapabilitiesSayWhetherTransfersAreAvailable(t *testing.T) {
	e := newXferEnv(t)
	_, b := get(t, e.api("/api/capabilities"))
	var c Capabilities
	_ = json.Unmarshal(b, &c)
	if !c.Features.Transfers || c.Schema < 14 {
		t.Fatalf("%s", b)
	}
	e.srv.Transfers = nil
	srvOff := httpTestServer(t, e.srv)
	_, b = get(t, srvOff+"/api/capabilities")
	_ = json.Unmarshal(b, &c)
	if c.Features.Transfers {
		t.Fatalf("%s", b)
	}
	if r, _ := get(t, srvOff+"/api/transfers"); r.StatusCode == 200 {
		t.Fatalf("the transfer routes exist without a queue")
	}
}

func httpTestServer(t *testing.T, srv *Server) string {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}
