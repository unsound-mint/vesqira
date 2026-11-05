package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/review"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

func chunkLog(done, total int) string {
	var b strings.Builder
	b.WriteString("=== ./vgt-123.err\n")
	for i := done; i >= 1; i-- { // out of order on purpose
		b.WriteString("[ Info: Done chunk\n│   chunk = " + itoa(i) + "\n│   chunks = " + itoa(total) + "\n└   elapsed_s = 1.0\n")
	}
	return b.String()
}

func getBody(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestABoardCanFollowAJobAndAReviewerSeesOnlyAPlaceholder(t *testing.T) {
	base := t.TempDir()
	rs, _ := sourceroot.New([]string{base})
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	bs, _ := boards.Open(cat.DB(), base)
	js, err := jobs.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Roots: rs, Store: cat, Worker: hdrWorker{}, Boards: bs, Jobs: js}
	var reads atomic.Int32
	srv.progress.read = func(ctx context.Context, j jobs.Job) (string, error) {
		reads.Add(1)
		return chunkLog(4, 16), nil
	}
	owner := httptest.NewServer(srv.Handler())
	defer owner.Close()
	rv := httptest.NewServer((&review.Handler{Store: bs}).Handler())
	defer rv.Close()
	api := func(p string) string { return owner.URL + p }

	secretDir := "/home/user/secret-project/cube"
	j, err := js.CreateDraft(jobs.Job{HostID: 1, Name: "vgt-ker16", Template: "vgt", Script: "#!/bin/bash\n# " + secretDir + "\n", OutputDir: secretDir, Params: `{"secret":"param"}`})
	if err != nil {
		t.Fatal(err)
	}
	js.MarkSubmitted(j.ID, "98765")
	js.SetState(j.ID, jobs.StateRunning, "running")

	_, bout := postJSON(t, api("/api/boards"), map[string]any{"title": "Live"})
	var b boards.Board
	json.Unmarshal(bout, &b)
	items := api("/api/boards/" + itoa(int(b.ID)) + "/items")

	if r, _ := postJSON(t, items, map[string]any{"jobId": 9999}); r.StatusCode != 422 {
		t.Errorf("unknown job: %d", r.StatusCode)
	}
	if r, _ := postJSON(t, items, map[string]any{}); r.StatusCode != 422 {
		t.Errorf("neither: %d", r.StatusCode)
	}
	if r, _ := postJSON(t, items, map[string]any{"jobId": j.ID, "figureId": 1}); r.StatusCode != 422 {
		t.Errorf("both: %d", r.StatusCode)
	}
	r, out := postJSON(t, items, map[string]any{"jobId": j.ID})
	if r.StatusCode != 200 {
		t.Fatalf("add job: %d %s", r.StatusCode, out)
	}
	if reads.Load() != 0 {
		t.Error("an edit must not read a job log")
	}
	code, got := getBody(t, api("/api/boards/"+itoa(int(b.ID))))
	if code != 200 {
		t.Fatal(code)
	}
	var gb struct {
		Items []struct {
			Kind string
			Job  struct {
				ID       int64
				Name     string
				State    string
				Progress *struct {
					Done, Total int
					ETASeconds  *int64
				}
			}
		}
	}
	json.Unmarshal(got, &gb)
	if len(gb.Items) != 1 || gb.Items[0].Kind != "job" || gb.Items[0].Job.Name != "vgt-ker16" || gb.Items[0].Job.State != "running" {
		t.Fatalf("%s", got)
	}
	p := gb.Items[0].Job.Progress
	if p == nil || p.Done != 4 || p.Total != 16 {
		t.Fatalf("progress: %s", got)
	}
	getBody(t, api("/api/boards/"+itoa(int(b.ID))))
	if reads.Load() != 1 {
		t.Errorf("the log is cached for 30 s: %d reads", reads.Load())
	}
	if strings.Contains(string(got), "98765") || strings.Contains(string(got), "secret") {
		t.Errorf("the owner's board must not carry scheduler id, script or params either: %s", got)
	}

	// preflight lists it; publish freezes it; the reviewer reads a placeholder with exactly five fields
	_, pf := getBody(t, api("/api/boards/"+itoa(int(b.ID))+"/preflight"))
	if !strings.Contains(string(pf), "Panel 1 follows a run that has not finished; the snapshot shows it as running") {
		t.Errorf("preflight: %s", pf)
	}
	r, pout := postJSON(t, api("/api/boards/"+itoa(int(b.ID))+"/publish"), map[string]any{"label": "v1"})
	if r.StatusCode != 201 {
		t.Fatalf("publish: %d %s", r.StatusCode, pout)
	}
	var pub struct{ Token string }
	json.Unmarshal(pout, &pub)
	code, rvOut := getBody(t, rv.URL+"/review-api/"+pub.Token)
	if code != 200 {
		t.Fatalf("review: %d %s", code, rvOut)
	}
	var rb struct {
		Snapshot struct{ Items []json.RawMessage }
	}
	json.Unmarshal(rvOut, &rb)
	var m map[string]any
	json.Unmarshal(rb.Snapshot.Items[0], &m)
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	if strings.Join(ks, ",") != "at,kind,note,state,title" || m["title"] != "vgt-ker16" || m["state"] != "running" {
		t.Fatalf("frozen job panel: %s", rb.Snapshot.Items[0])
	}
	for _, leak := range []string{"98765", "secret", "/home", "workDir", "hostId", "script", "slurm"} {
		if strings.Contains(string(rvOut), leak) {
			t.Errorf("the reviewer's response mentions %q: %s", leak, rvOut)
		}
	}

	// pin: only a job panel can be pinned over, and only once
	if r, _ := postJSON(t, api("/api/boards/"+itoa(int(b.ID))+"/items/0/pin"), map[string]any{"figureId": 9999}); r.StatusCode != 422 {
		t.Errorf("pin to a missing figure: %d", r.StatusCode)
	}
	f, err := bs.CreateFigure(boards.NewFigure{DatasetID: 1, Title: "result", Provenance: json.RawMessage(`{}`)}, pngBytesForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	r, out = postJSON(t, api("/api/boards/"+itoa(int(b.ID))+"/items/0/pin"), map[string]any{"figureId": f.ID})
	if r.StatusCode != 200 || !strings.Contains(string(out), `"kind":"figure"`) || !strings.Contains(string(out), `"figure":{`) {
		t.Fatalf("pin: %d %s", r.StatusCode, out)
	}
	if r, _ := postJSON(t, api("/api/boards/"+itoa(int(b.ID))+"/items/0/pin"), map[string]any{"figureId": f.ID}); r.StatusCode != 409 {
		t.Errorf("pinning a figure panel: %d, want 409", r.StatusCode)
	}
	if r, _ := postJSON(t, api("/api/boards/"+itoa(int(b.ID))+"/items/5/pin"), map[string]any{"figureId": f.ID}); r.StatusCode != 404 {
		t.Errorf("no such panel: %d", r.StatusCode)
	}
	// the published snapshot did not change
	_, again := getBody(t, rv.URL+"/review-api/"+pub.Token)
	if !strings.Contains(string(again), `"state":"running"`) {
		t.Errorf("a snapshot must not follow the job: %s", again)
	}
}

func TestBoardFetchNeverWaitsLongForAJobLog(t *testing.T) {
	base := t.TempDir()
	cat, _ := catalog.Open("file:" + filepath.Join(base, "t.db"))
	defer cat.Close()
	bs, _ := boards.Open(cat.DB(), base)
	js, _ := jobs.Open(cat.DB())
	srv := &Server{Boards: bs, Jobs: js}
	srv.progress.read = func(ctx context.Context, j jobs.Job) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(30 * time.Second):
			return chunkLog(1, 2), nil
		}
	}
	j, _ := js.CreateDraft(jobs.Job{HostID: 1, Name: "slow", Template: "vgt", Script: "#!/bin/sh\n"})
	js.MarkSubmitted(j.ID, "5")
	js.SetState(j.ID, jobs.StateRunning, "")
	b, _ := bs.CreateBoard("B")
	bs.AddJobItem(b.ID, j.ID)
	start := time.Now()
	view := srv.boardView(context.Background(), mustBoard(t, bs, b.ID), true)
	if time.Since(start) > 7*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	if view.Items[0].Job.Progress != nil || view.Items[0].Job.State != "running" {
		t.Fatalf("on timeout there is a state and no progress: %+v", view.Items[0].Job)
	}
}

func pngBytesForTest(t *testing.T) []byte {
	b, err := base64.StdEncoding.DecodeString(testPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustBoard(t *testing.T, bs *boards.Store, id int64) boards.Board {
	b, err := bs.Board(id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
