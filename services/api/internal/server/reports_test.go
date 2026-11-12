package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/publish"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

func callJSON(t *testing.T, method, url string, in any) (int, []byte) {
	t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, body)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// The whole report path (ADR 0036): a report with a figure and a run is frozen, pushed to a publish server, read
// through a named reader link, commented on, and the comment comes back to the owner. What the reader receives is
// checked on the wire for the source path, the root, the run directory and the publisher key.
func TestReportPublishReadCommentSyncExposesNoSource(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root := filepath.Join(base, "private-research-root")
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
	keyFile := filepath.Join(base, "publish.key")
	owner := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: hdrWorker{}, Boards: bs, Runs: rn, Reports: rp, PublishKeyFile: keyFile}).Handler())
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

	file := filepath.Join(root, "secret-cube-name-not-a-secret.fits")
	os.WriteFile(file, []byte("x"), 0o644)
	var d catalog.Dataset
	json.NewDecoder(post(t, owner, file, nil).Body).Decode(&d)
	view := map[string]any{"kind": "plane", "channel": 3, "colormap": "viridis", "stretch": "linear", "range": "percentile"}
	_, fout := postJSON(t, owner.URL+"/api/figures", map[string]any{"datasetId": d.ID, "title": "Peak channel", "caption": "H I at 8 km/s", "view": view, "previewPng": testPNG(t)})
	var fig boards.Figure
	json.Unmarshal(fout, &fig)
	run, err := rn.Save(filepath.Join(root, "runs", "run-dir-name-private"), runs.Parsed{Run: runs.Run{Name: "2026-11-08_a1f3c", Kind: "vgt"}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	api := owner.URL
	st, b := callJSON(t, "POST", api+"/api/reports", map[string]any{"title": "Noise masking threshold sweep", "body": "not shared", "author": "agent"})
	if st != 201 {
		t.Fatalf("create linked report: %d %s", st, b)
	}
	body := "# Block size 32 vs 64\n\nWe compare [[run:" + itoa(int(run.ID)) + "]] inline.\n\n![[figure:" + fig.Revision + "]]\n\n[[run:" + itoa(int(run.ID)) + "]]\n\nSee [[Noise masking threshold sweep]] and [[No such report]]."
	st, b = callJSON(t, "POST", api+"/api/reports", map[string]any{"title": "Block size 32 vs 64", "body": body, "author": "agent"})
	if st != 201 {
		t.Fatalf("create: %d %s", st, b)
	}
	var rep reports.Report
	json.Unmarshal(b, &rep)
	rid := itoa(int(rep.ID))

	// Without a publish server the preflight says so and publishing is refused.
	if st, b = callJSON(t, "POST", api+"/api/reports/"+rid+"/publish", map[string]int{"revision": rep.Revision}); st != 409 {
		t.Fatalf("publish without a server: %d %s", st, b)
	}
	if st, b = callJSON(t, "POST", api+"/api/publish/settings", map[string]any{"url": "http://example.org"}); st != 422 {
		t.Errorf("plain http to another host accepted: %d %s", st, b)
	}
	if st, b = callJSON(t, "POST", api+"/api/publish/settings", map[string]any{"url": pub.URL, "key": pubKey, "ownerName": "Denis", "agentName": "VGT agent"}); st != 200 {
		t.Fatalf("settings: %d %s", st, b)
	}
	if fi, _ := os.Stat(keyFile); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file must be 0600: %v", fi)
	}
	if st, b = callJSON(t, "GET", api+"/api/publish", nil); strings.Contains(string(b), pubKey) {
		t.Fatal("the key is returned by the API")
	}

	st, b = callJSON(t, "GET", api+"/api/reports/"+rid+"/preflight", nil)
	if st != 200 {
		t.Fatalf("preflight: %d %s", st, b)
	}
	var pf struct {
		Revision publish.Revision
		Warnings []string
		Errors   []string
	}
	json.Unmarshal(b, &pf)
	if len(pf.Errors) != 0 {
		t.Fatalf("preflight errors: %v", pf.Errors)
	}
	joined := strings.Join(pf.Warnings, "\n")
	for _, want := range []string{"Noise masking threshold sweep", "No such report", "not been assessed"} {
		if !strings.Contains(joined, want) {
			t.Errorf("preflight should warn about %q: %v", want, pf.Warnings)
		}
	}
	// A save after the preview makes the publish stale.
	callJSON(t, "POST", api+"/api/reports/"+rid, map[string]any{"body": body + "\n\nOne more line.", "base": rep.Revision, "author": "owner"})
	if st, b = callJSON(t, "POST", api+"/api/reports/"+rid+"/publish", map[string]int{"revision": rep.Revision}); st != 409 {
		t.Fatalf("stale publish: %d %s", st, b)
	}
	st, b = callJSON(t, "POST", api+"/api/reports/"+rid+"/publish", map[string]int{"revision": rep.Revision + 1})
	if st != 200 {
		t.Fatalf("publish: %d %s", st, b)
	}
	if st, b = callJSON(t, "POST", api+"/api/reports/"+rid+"/publish", map[string]int{"revision": rep.Revision + 1}); st != 409 {
		t.Errorf("republishing unchanged content: %d %s", st, b)
	}

	st, b = callJSON(t, "POST", api+"/api/publish/readers", map[string]any{"name": "Supervisor"})
	if st != 201 {
		t.Fatalf("reader: %d %s", st, b)
	}
	var made struct {
		Reader publish.Reader
		Link   string
	}
	json.Unmarshal(b, &made)
	tok := made.Reader.Token
	if !strings.HasSuffix(made.Link, "/read/"+tok) {
		t.Errorf("link %q", made.Link)
	}

	// Everything a reader can fetch, checked on the wire.
	var wire bytes.Buffer
	st, b = callJSON(t, "GET", pub.URL+"/read-api/"+tok, nil)
	wire.Write(b)
	var space publish.SpaceView
	json.Unmarshal(b, &space)
	if st != 200 || len(space.Reports) != 1 {
		t.Fatalf("space: %d %s", st, b)
	}
	st, b = callJSON(t, "GET", pub.URL+"/read-api/"+tok+"/reports/"+space.Reports[0].Key, nil)
	wire.Write(b)
	var rv publish.ReportView
	json.Unmarshal(b, &rv)
	var figBlock, textBlock *publish.ReaderBlock
	for i, blk := range rv.Revision.Blocks {
		switch {
		case blk.Kind == publish.BlockFigure:
			figBlock = &rv.Revision.Blocks[i]
		case blk.Kind == publish.BlockText && strings.Contains(blk.Text, "See "):
			textBlock = &rv.Revision.Blocks[i]
		}
	}
	if figBlock == nil || textBlock == nil {
		t.Fatalf("blocks: %s", b)
	}
	st, img := callJSON(t, "GET", pub.URL+"/read-api/"+tok+"/assets/"+figBlock.Figure.Asset, nil)
	if st != 200 || !bytes.HasPrefix(img, []byte("\x89PNG")) {
		t.Fatalf("figure picture: %d", st)
	}
	if !strings.Contains(textBlock.Text, "[[report:") || strings.Contains(textBlock.Text, "[[No such report") {
		t.Errorf("links not frozen: %q", textBlock.Text)
	}
	for _, leak := range []string{root, base, "private-research-root", "run-dir-name-private", "datasetId", `"path"`, pubKey} {
		if strings.Contains(wire.String(), leak) {
			t.Errorf("reader response leaks %q", leak)
		}
	}
	if !strings.Contains(wire.String(), "secret-cube-name-not-a-secret.fits") || !strings.Contains(wire.String(), "Drafted by VGT agent, edited by Denis") {
		t.Errorf("reader should see the file name and the attribution")
	}

	// A reader comment comes back to the owner, attributed to the link's name.
	st, b = callJSON(t, "POST", pub.URL+"/read-api/"+tok+"/comments", map[string]any{"report": rv.Revision.Report, "revision": rv.Revision.ID, "block": textBlock.ID, "body": "Why lower?", "author": "Someone else"})
	if st != 201 {
		t.Fatalf("comment: %d %s", st, b)
	}
	if st, b = callJSON(t, "POST", api+"/api/publish/sync", map[string]any{}); st != 200 {
		t.Fatalf("sync: %d %s", st, b)
	}
	_, b = callJSON(t, "GET", api+"/api/report-comments", nil)
	var cm struct {
		Comments []publish.Comment
	}
	json.Unmarshal(b, &cm)
	if len(cm.Comments) != 1 || cm.Comments[0].Author != "Supervisor" {
		t.Fatalf("mirrored: %s", b)
	}
	// The owner's draft knows which of its blocks the comment is on.
	_, b = callJSON(t, "GET", api+"/api/reports/"+rid, nil)
	var draftView struct {
		BlockIDs []string `json:"blockIds"`
	}
	json.Unmarshal(b, &draftView)
	if !strings.Contains(strings.Join(draftView.BlockIDs, ","), textBlock.ID) {
		t.Errorf("the commented block %s is not placed on the draft: %v", textBlock.ID, draftView.BlockIDs)
	}

	// The agent drafts; only the owner's send reaches the reader.
	st, b = callJSON(t, "POST", api+"/api/report-comments/"+itoa(int(cm.Comments[0].ID))+"/draft", map[string]any{"body": "Block 64 smooths more.", "author": "agent"})
	if st != 201 {
		t.Fatalf("draft: %d %s", st, b)
	}
	_, b = callJSON(t, "GET", pub.URL+"/read-api/"+tok+"/reports/"+space.Reports[0].Key, nil)
	if strings.Contains(string(b), "Block 64 smooths more.") {
		t.Fatal("a drafted reply reached the reader before the owner sent it")
	}
	var draft reports.ReplyDraft
	_, db := callJSON(t, "GET", api+"/api/report-comments", nil)
	var withDrafts struct{ Drafts []reports.ReplyDraft }
	json.Unmarshal(db, &withDrafts)
	draft = withDrafts.Drafts[0]
	if st, b = callJSON(t, "POST", api+"/api/reply-drafts/"+itoa(int(draft.ID))+"/send", map[string]any{}); st != 201 {
		t.Fatalf("send: %d %s", st, b)
	}
	_, b = callJSON(t, "GET", pub.URL+"/read-api/"+tok+"/reports/"+space.Reports[0].Key, nil)
	if !strings.Contains(string(b), "Block 64 smooths more.") {
		t.Fatal("sent reply not visible to the reader")
	}
	if st, _ = callJSON(t, "POST", api+"/api/reply-drafts/"+itoa(int(draft.ID))+"/send", map[string]any{}); st != 409 {
		t.Errorf("a draft sent twice: %d", st)
	}

	// Revoking the link closes it at once.
	callJSON(t, "POST", api+"/api/publish/readers/"+itoa(int(made.Reader.ID))+"/revoke", map[string]any{})
	if st, _ = callJSON(t, "GET", pub.URL+"/read-api/"+tok, nil); st != 404 {
		t.Errorf("revoked link: %d", st)
	}
	// The published report cannot be deleted; the unpublished one can.
	if st, _ = callJSON(t, "POST", api+"/api/reports/"+rid+"/delete", map[string]any{}); st != 409 {
		t.Errorf("deleting a published report: %d", st)
	}
}

// A push whose answer is lost is retried with the same bytes and lands on the same revision; a new server address
// never inherits the key of the old one.
func TestPublishRetryAfterALostAnswerAndKeyNotReusedForANewAddress(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	bs, _ := boards.Open(cat.DB(), base)
	rp, _ := reports.Open(cat.DB(), base)
	keyFile := filepath.Join(base, "publish.key")
	owner := httptest.NewServer((&Server{Store: cat, Boards: bs, Reports: rp, PublishKeyFile: keyFile}).Handler())
	defer owner.Close()
	pdb, _ := sql.Open("sqlite", "file:"+filepath.Join(base, "publish.db"))
	defer pdb.Close()
	ps, _ := publish.Open(pdb, filepath.Join(base, "pubdata"))
	key, _ := ps.AddPublisher("laptop")
	real := (&publish.Handler{Store: ps}).Handler()
	dropped := false
	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/publish-api/revisions" && !dropped { // the server stores it, the answer never arrives
			dropped = true
			real.ServeHTTP(httptest.NewRecorder(), r)
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		real.ServeHTTP(w, r)
	}))
	defer pub.Close()

	api := owner.URL
	if st, b := callJSON(t, "POST", api+"/api/publish/settings", map[string]any{"url": pub.URL, "key": key}); st != 200 {
		t.Fatalf("settings: %d %s", st, b)
	}
	_, b := callJSON(t, "POST", api+"/api/reports", map[string]any{"title": "Weekly", "body": "Text.", "author": "owner"})
	var rep reports.Report
	json.Unmarshal(b, &rep)
	path := api + "/api/reports/" + itoa(int(rep.ID)) + "/publish"
	if st, _ := callJSON(t, "POST", path, map[string]int{"revision": rep.Revision}); st == 200 {
		t.Fatal("the first push should look failed to the owner")
	}
	time.Sleep(1100 * time.Millisecond) // a fresh freeze would now carry a different time
	st, b := callJSON(t, "POST", path, map[string]int{"revision": rep.Revision})
	if st != 200 || !strings.Contains(string(b), `"number":1`) {
		t.Fatalf("retry: %d %s", st, b)
	}

	// a different address drops the key instead of sending it there
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), key) {
			t.Error("the key was sent to a new address")
		}
		w.WriteHeader(401)
	}))
	defer other.Close()
	_, b = callJSON(t, "POST", api+"/api/publish/settings", map[string]any{"url": other.URL})
	if strings.Contains(string(b), `"keySet":true`) {
		t.Fatalf("key kept for a new address: %s", b)
	}
	if _, err := os.Stat(keyFile); !os.IsNotExist(err) {
		t.Fatal("key file still there")
	}
}

func TestPreflightNamesRunSettingsThatIdentifyMachinesOrPeople(t *testing.T) {
	t.Parallel()
	w := identifyingSettings("r1", []runs.Param{{Key: "block_size", Value: "32"}, {Key: "partition", Value: "gpu"}, {Key: "notify", Value: "someone@example.org"}, {Key: "dataset", Value: "login1.hpc.example.edu"}})
	for _, want := range []string{"partition=gpu", "notify=someone@example.org", "dataset=login1.hpc.example.edu"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning misses %s: %q", want, w)
		}
	}
	if strings.Contains(w, "block_size") {
		t.Errorf("an ordinary setting is not identifying: %q", w)
	}
	if identifyingSettings("r2", []runs.Param{{Key: "ker", Value: "4"}, {Key: "noise", Value: "0.17"}}) != "" {
		t.Error("plain scientific settings should not warn")
	}
}
