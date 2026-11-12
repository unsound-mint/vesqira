package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/transfers"
)

func TestTransferResponsesMatchTheSpec(t *testing.T) {
	t.Parallel()
	doc := loadSpec(t)
	e := newXferEnv(t)
	e.file(e.remote, "a.fits", 4000)
	e.file(e.remote, "b.fits", 4000)
	e.file(e.local, "b.fits", 10)
	check := func(path, method string, status int, label string, body []byte) {
		t.Helper()
		conforms(t, responseSchemaAt(t, doc, path, method, status, "application/json"), label, body)
	}
	// the empty queue, then a queued transfer
	_, b := get(t, e.api("/api/transfers"))
	check("/api/transfers", "GET", 200, "GET empty queue", b)
	r, b := postJSON(t, e.api("/api/transfers"), map[string]any{"direction": "download", "hostId": e.host.ID, "sourcePath": filepath.Join(e.remote, "a.fits"), "destDir": e.local,
		"policy": "ask", "bwlimitKBps": 0, "compress": true, "name": "a.fits"})
	if r.StatusCode != 201 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	check("/api/transfers", "POST", 201, "POST queued", b)
	var env struct{ Transfer transfers.Transfer }
	_ = json.Unmarshal(b, &env)
	op := doc.Paths.Find("/api/transfers").GetOperation("POST")
	var v any
	_ = json.Unmarshal([]byte(`{"direction":"upload","hostId":1,"sourcePath":"/a","destDir":"/b","policy":"keep_both"}`), &v)
	if err := op.RequestBody.Value.Content.Get("application/json").Schema.Value.VisitJSON(v); err != nil {
		t.Errorf("the request body does not match the spec: %v", err)
	}
	// finished, in conflict, waiting, unverified: every state a client can meet
	e.run()
	conflict := e.download("b.fits", nil)
	e.run()
	e.flag("CORRUPT", true, "")
	e.file(e.remote, "c.fits", 3000)
	unv := e.download("c.fits", nil)
	e.run()
	e.flag("CORRUPT", false, "")
	e.flag("DOWN", true, "")
	waiting := e.download("a.fits", map[string]any{"name": "a2.fits"})
	e.pass(time.Now())
	_, b = get(t, e.api("/api/transfers"))
	check("/api/transfers", "GET", 200, "GET queue with every state", b)
	var list struct{ Transfers []transfers.Transfer }
	_ = json.Unmarshal(b, &list)
	seen := map[string]bool{}
	for _, tr := range list.Transfers {
		seen[tr.State+"/"+tr.Verif.Status] = true
	}
	for _, want := range []string{"done/verified", "conflict/", "done/unverified", "waiting/"} {
		if !seen[want] {
			t.Errorf("the queue never showed %s: %v", want, seen)
		}
	}
	url := func(id int64, op string) string { return e.api("/api/transfers/" + itoa(int(id)) + "/" + op) }
	r, b = postJSON(t, url(conflict.ID, "resolve"), map[string]any{"policy": "keep_both"})
	check("/api/transfers/{id}/resolve", "POST", 200, "POST resolve", b)
	e.flag("DOWN", false, "")
	r, b = postJSON(t, url(unv.ID, "retry"), map[string]any{})
	check("/api/transfers/{id}/retry", "POST", 200, "POST retry", b)
	r, b = postJSON(t, url(waiting.ID, "cancel"), map[string]any{"discardPartial": true})
	check("/api/transfers/{id}/cancel", "POST", 200, "POST cancel", b)
	_ = r
	r, b = postJSON(t, url(waiting.ID, "cancel"), map[string]any{})
	check("/api/transfers/{id}/cancel", "POST", 409, "POST cancel finished", b)
	r, b = postJSON(t, url(waiting.ID, "remove"), map[string]any{})
	check("/api/transfers/{id}/remove", "POST", 200, "POST remove", b)
	r, b = postJSON(t, e.api("/api/transfers"), map[string]any{"direction": "download", "hostId": e.host.ID, "sourcePath": "/etc/passwd", "destDir": e.local})
	check("/api/transfers", "POST", 422, "POST refused", b)

	// a verified download is registered; the dataset and the transfer match the spec
	if exec.Command("python3", "-c", "import astropy").Run() == nil {
		repo, _ := filepath.Abs("../../../..")
		e.srv.Worker = headerWorker(t, "python3", repo)
		os.WriteFile(filepath.Join(e.remote, "tiny.fits"), tinyFits(), 0o644)
		tr := e.download("tiny.fits", nil)
		e.run()
		r, b = postJSON(t, url(tr.ID, "register"), map[string]any{})
		if r.StatusCode != 201 {
			t.Fatalf("%d %s", r.StatusCode, b)
		}
		check("/api/transfers/{id}/register", "POST", 201, "POST register", b)
		_, b = postJSON(t, url(tr.ID, "register"), map[string]any{})
		check("/api/transfers/{id}/register", "POST", 200, "POST register again", b)
	}
}
