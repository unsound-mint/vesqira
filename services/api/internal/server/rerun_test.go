package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

// An imported run, or a run from a job recorded before requests were kept, gives the "run again" form nothing it
// would have to guess; and a parent run that does not exist is refused.
func TestRerunPrefillNeverGuessesWhatIsNotKnown(t *testing.T) {
	base := t.TempDir()
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	hs, _ := hosts.Open(cat.DB())
	js, _ := jobs.Open(cat.DB())
	rn, _ := runs.Open(cat.DB())
	rs, _ := sourceroot.New([]string{base})
	mgr := hosts.NewManager("/nonexistent/ssh", nil)
	t.Cleanup(mgr.Close)
	ts := httptest.NewServer((&Server{Roots: rs, Store: cat, Runs: rn, HostDB: hs, Remote: mgr, Jobs: js}).Handler())
	defer ts.Close()
	api := func(p string) string { return ts.URL + p }

	h, err := hs.Create(hosts.Host{Name: "Cluster", Dest: "login1", Roots: []string{"/data"}})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := rn.Save("/data/run-a", runs.Parsed{Run: runs.Run{Name: "run a/with spaces", Kind: "vgt",
		Params: []runs.Param{{Key: "ker", Value: "4", Source: "config"}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	type prefill struct {
		ParentRunID int64  `json:"parentRunId"`
		Source      string `json:"source"`
		HostID      *int64 `json:"hostId"`
		Template    string `json:"template"`
		Name        string `json:"name"`
		VGT         map[string]any
		Params      []runs.Param `json:"params"`
		Notes       []string     `json:"notes"`
		Changed     *bool        `json:"configChanged"`
	}
	read := func(id int64) (prefill, string) {
		resp, b := get(t, api("/api/runs/"+itoa(int(id))+"/rerun"))
		if resp.StatusCode != 200 {
			t.Fatalf("rerun %d: %d %s", id, resp.StatusCode, b)
		}
		var raw struct {
			prefill
			VGT map[string]any `json:"vgt"`
		}
		json.Unmarshal(b, &raw)
		p := raw.prefill
		p.VGT = raw.VGT
		return p, string(b)
	}

	// imported: everything about how it ran is null, the declared parameters are listed
	p, raw := read(imported.ID)
	if p.Source != "imported" || p.HostID != nil || p.Template != "vgt" || p.ParentRunID != imported.ID || p.Name != "run-a-with-spaces-again" || p.Changed != nil {
		t.Errorf("imported prefill: %s", raw)
	}
	for _, k := range []string{"repoDir", "configPath", "baseConfigText", "cpus", "mem", "time", "partition", "account"} {
		if v, ok := p.VGT[k]; !ok || v != nil {
			t.Errorf("imported run: vgt.%s must be an explicit null, got %v", k, v)
		}
	}
	if set, _ := p.VGT["set"].(map[string]any); set == nil || len(set) != 0 {
		t.Errorf("imported run: set must be empty: %s", raw)
	}
	if len(p.Params) != 1 || p.Params[0].Key != "ker" || len(p.Notes) != 1 || !strings.Contains(p.Notes[0], "imported") {
		t.Errorf("imported run: params and note: %s", raw)
	}
	if r, _ := get(t, api("/api/runs/9999/rerun")); r.StatusCode != 404 {
		t.Errorf("unknown run: %d, want 404", r.StatusCode)
	}

	// a job from before requests were kept: the host is known, nothing else
	old, err := js.CreateDraft(jobs.Job{HostID: h.ID, Name: "old", Template: "vgt", Script: "#!/bin/bash\n", OutputDir: "/data/run-a"})
	if err != nil {
		t.Fatal(err)
	}
	js.LinkRun(old.ID, imported.ID)
	p, raw = read(imported.ID)
	if p.Source != "imported" || p.HostID == nil || *p.HostID != h.ID || p.VGT["repoDir"] != nil || p.VGT["baseConfigText"] != nil || len(p.Notes) != 1 {
		t.Errorf("old job prefill: %s", raw)
	}

	// a job that kept its request: the prefill is that request; a host that cannot be reached leaves the comparison
	// unknown (null) instead of failing the call
	rq := `{"repoDir":"/shared/vgt","configPath":"/data/configs/a.toml","baseConfigText":"[vgt]\nker = 4\n","set":{"ker":"8"},"cpus":4,"mem":"8G","time":"02:00:00","partition":"","account":""}`
	kept, err := js.CreateDraft(jobs.Job{HostID: h.ID, Name: "kept", Template: "vgt", Script: "#!/bin/bash\n", OutputDir: "/data/run-b", Request: rq})
	if err != nil {
		t.Fatal(err)
	}
	withReq, _ := rn.Save("/data/run-b", runs.Parsed{Run: runs.Run{Name: "run-b", Kind: "vgt"}}, nil)
	js.LinkRun(kept.ID, withReq.ID)
	p, raw = read(withReq.ID)
	if p.Source != "job" || p.HostID == nil || p.VGT["repoDir"] != "/shared/vgt" || p.VGT["baseConfigText"] != "[vgt]\nker = 4\n" || p.VGT["cpus"] != float64(4) || p.Changed != nil || len(p.Notes) != 0 {
		t.Errorf("job prefill with an unreachable host: %s", raw)
	}

	// a parent run must exist; a real one is recorded and shown on the job
	body := func(parent any) map[string]any {
		return map[string]any{"hostId": h.ID, "template": "vgt", "name": "again", "parentRunId": parent,
			"vgt": map[string]any{"repoDir": "/shared/vgt", "configText": "[input]\ninput_file = \"x.fits\"\n[vgt]\nker = 4\n", "set": map[string]string{"ker": "8"},
				"cpus": 2, "mem": "4G", "time": "01:00:00"}}
	}
	for name, parent := range map[string]any{"unknown": 4242, "zero": 0} {
		if r, b := postJSON(t, api("/api/jobs"), body(parent)); r.StatusCode != 422 {
			t.Errorf("%s parent: %d %s, want 422", name, r.StatusCode, b)
		}
	}
	// the host's roots are /data, so the output folder is fine; a parent run is accepted
	r, b := postJSON(t, api("/api/jobs"), body(imported.ID))
	var child jobs.Job
	json.Unmarshal(b, &child)
	if r.StatusCode != 201 || child.ParentRunID == nil || *child.ParentRunID != imported.ID {
		t.Fatalf("draft with a parent: %d %s", r.StatusCode, b)
	}
	// once its outputs are a run, that run reports its parent and its job
	out, _ := rn.Save("/data/vesqira_runs/again", runs.Parsed{Run: runs.Run{Name: "again", Kind: "vgt"}}, nil)
	js.LinkRun(child.ID, out.ID)
	_, gb := get(t, api("/api/runs/"+itoa(int(out.ID))))
	var rv struct {
		JobID       *int64 `json:"jobId"`
		ParentRunID *int64 `json:"parentRunId"`
	}
	json.Unmarshal(gb, &rv)
	if rv.JobID == nil || *rv.JobID != child.ID || rv.ParentRunID == nil || *rv.ParentRunID != imported.ID {
		t.Errorf("child run lineage: %s", gb)
	}
	// without a job, the links are null (imported and unrelated runs)
	lone, _ := rn.Save("/data/lone", runs.Parsed{Run: runs.Run{Name: "lone", Kind: "vgt"}}, nil)
	_, lb := get(t, api("/api/runs/"+itoa(int(lone.ID))))
	if !strings.Contains(string(lb), `"jobId":null`) || !strings.Contains(string(lb), `"parentRunId":null`) {
		t.Errorf("a run without a job must show null links: %s", lb)
	}
}
