package server

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/access"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/publish"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Publishing a report to a publish server is a local-instance path (the group server leaves it out), so there is no workspace policy
// to block it; what it owes is the same explicit confirmation and the label on the revision readers get (ADR 0069).
func TestReportWithLabelledFigureNeedsConfirmationAndCarriesTheLabel(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	os.MkdirAll(root, 0o755)
	rs, _ := sourceroot.New([]string{root})
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	bs, _ := boards.Open(cat.DB(), base)
	rn, _ := runs.Open(cat.DB())
	rp, err := reports.Open(cat.DB(), base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := derived.Open(cat.DB()); err != nil {
		t.Fatal(err)
	}
	ac, err := access.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	owner := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: hdrWorker{}, Boards: bs, Runs: rn, Reports: rp, Access: ac, PublishKeyFile: filepath.Join(base, "publish.key")}).Handler())
	defer owner.Close()
	pdb, err := sql.Open("sqlite", "file:"+filepath.Join(base, "publish.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer pdb.Close()
	ps, err := publish.Open(pdb, filepath.Join(base, "pubdata"))
	if err != nil {
		t.Fatal(err)
	}
	pubKey, _ := ps.AddPublisher("laptop")
	pub := httptest.NewServer((&publish.Handler{Store: ps}).Handler())
	defer pub.Close()
	api := owner.URL
	if st, b := callJSON(t, "POST", api+"/api/publish/settings", map[string]any{"url": pub.URL, "key": pubKey, "ownerName": "Denis", "agentName": "agent"}); st != 200 {
		t.Fatalf("settings: %d %s", st, b)
	}

	file := filepath.Join(root, "cube.fits")
	os.WriteFile(file, []byte("x"), 0o644)
	var d catalog.Dataset
	json.NewDecoder(post(t, owner, file, nil).Body).Decode(&d)
	view := map[string]any{"kind": "plane", "channel": 3, "colormap": "viridis", "stretch": "linear", "range": "percentile"}
	_, fout := postJSON(t, owner.URL+"/api/figures", map[string]any{"datasetId": d.ID, "title": "Peak channel", "caption": "", "view": view, "previewPng": testPNG(t)})
	var fig boards.Figure
	json.Unmarshal(fout, &fig)
	st, b := callJSON(t, "POST", api+"/api/reports", map[string]any{"title": "Notes", "body": "![[figure:" + fig.Revision + "]]", "author": "agent"})
	if st != 201 {
		t.Fatalf("create: %d %s", st, b)
	}
	var rep reports.Report
	json.Unmarshal(b, &rep)
	rid := itoa(int(rep.ID))
	if _, _, err := ac.Set(workspace.Local, d.ID, d.Name, access.Collaboration, "", "local-owner"); err != nil {
		t.Fatal(err)
	}

	st, b = callJSON(t, "GET", api+"/api/reports/"+rid+"/preflight", nil)
	var pf struct {
		Restricted []restrictedPanel `json:"restricted"`
		Access     string            `json:"access"`
		Revision   publish.Revision
	}
	json.Unmarshal(b, &pf)
	if st != 200 || len(pf.Restricted) != 1 || pf.Access != "collaboration" || pf.Revision.Access != "collaboration" {
		t.Fatalf("preflight: %d %s", st, b)
	}
	st, b = callJSON(t, "POST", api+"/api/reports/"+rid+"/publish", map[string]any{"revision": rep.Revision})
	var refusal struct{ Error, Code string }
	json.Unmarshal(b, &refusal)
	if st != 409 || refusal.Code != "access_confirmation_required" || !strings.Contains(refusal.Error, d.Name) {
		t.Fatalf("without confirmation: %d %s", st, b)
	}
	if st, b = callJSON(t, "POST", api+"/api/reports/"+rid+"/publish", map[string]any{"revision": rep.Revision, "confirmAccess": true}); st != 201 && st != 200 {
		t.Fatalf("confirmed: %d %s", st, b)
	}
	// readers' copy: the label name. The file name stays in the figure's provenance as before (ADR 0036); the label adds no
	// name, no id, and not the list of labelled sources the owner sees.
	var doc string
	if err := pdb.QueryRow(`SELECT doc FROM revisions ORDER BY rowid DESC LIMIT 1`).Scan(&doc); err != nil {
		t.Skipf("cannot read the publish store's revision table: %v", err)
	}
	if !strings.Contains(doc, `"access":"collaboration"`) {
		t.Errorf("published revision lacks the label: %s", doc)
	}
	for _, banned := range []string{`"via"`, `datasetId`, `"restricted"`} {
		if strings.Contains(doc, banned) {
			t.Errorf("published revision holds %s: %s", banned, doc)
		}
	}
}
