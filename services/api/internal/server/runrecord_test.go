package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

// A job submitted to a host leaves a run record next to its outputs, in a format discovery reads, so a rescan of the
// folder finds the run with its settings; the record is rewritten with the end state; and a run.json that Vesqira did
// not write is never replaced.
func TestSubmittedJobLeavesARunRecordDiscoveryReads(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	remoteHome := filepath.Join(base, "remote-home")
	remoteRoot := filepath.Join(base, "remote_data")
	os.MkdirAll(remoteHome, 0o755)
	os.MkdirAll(filepath.Join(remoteRoot, "configs"), 0o755)
	baseConfig := "[input]\ninput_file = \"x.fits\"\noutput_dir = \"\"\n\n[noise]\nnoise = 0.17\nnoise_mode = \"scalar\"\n\n[vgt]\ndn = 20\nker = 4\n"
	os.WriteFile(filepath.Join(remoteRoot, "configs", "vgt.toml"), []byte(baseConfig), 0o644)
	bin := filepath.Join(base, "slurm-bin")
	fakeSlurm(t, bin)
	sshScript := fmt.Sprintf(`#!/bin/sh
while [ $# -gt 0 ]; do case "$1" in -o) shift 2;; -T) shift;; *) break;; esac; done
shift
cmd=$(printf '%%s' "$*" | sed -e 's#\$HOME#%s#g' -e 's#bash -lc#bash -c#')
PATH=%s:$PATH exec sh -c "$cmd"
`, remoteHome, bin)
	sshBin := filepath.Join(base, "ssh")
	os.WriteFile(sshBin, []byte(sshScript), 0o755)

	mgr := hosts.NewManager(sshBin, map[string][]byte{})
	mgr.ControlDir = t.TempDir()
	t.Cleanup(mgr.Close)
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	hs, _ := hosts.Open(cat.DB())
	js, _ := jobs.Open(cat.DB())
	rn, _ := runs.Open(cat.DB())
	au, _ := audit.Open(cat.DB())
	rs, _ := sourceroot.New([]string{filepath.Join(base, "localroot")})
	os.MkdirAll(filepath.Join(base, "localroot"), 0o755)
	ts := httptest.NewServer((&Server{Roots: rs, Store: cat, Worker: &fakeWorker{}, Runs: rn, HostDB: hs, Remote: mgr, Jobs: js, Audit: au}).Handler())
	defer ts.Close()
	api := func(p string) string { return ts.URL + p }
	h, err := hs.Create(hosts.Host{Name: "Cluster", Dest: "login1", Python: "python3", Roots: []string{remoteRoot}})
	if err != nil {
		t.Fatal(err)
	}
	draft := func() jobs.Job {
		r, b := postJSON(t, api("/api/jobs"), map[string]any{"hostId": h.ID, "template": "vgt", "name": "vgt-ker8", "vgt": map[string]any{
			"repoDir": "/shared/home/u/vgt", "configText": baseConfig, "cpus": 8, "mem": "16G", "time": "02:00:00",
			"set": map[string]string{"ker": "8", "noise_mode": "channel"}}})
		if r.StatusCode != 201 {
			t.Fatalf("%d %s", r.StatusCode, b)
		}
		var j jobs.Job
		json.Unmarshal(b, &j)
		return j
	}
	submit := func(j jobs.Job) jobs.Job {
		r, b := postJSON(t, api("/api/jobs/"+itoa(int(j.ID))+"/submit"), map[string]any{"scriptHash": j.ScriptHash})
		if r.StatusCode != 200 {
			t.Fatalf("submit %d %s", r.StatusCode, b)
		}
		var out jobs.Job
		json.Unmarshal(b, &out)
		return out
	}
	scanRuns := func() []discover.Run {
		res := discover.Scan(context.Background(), discover.LocalFS{Root: remoteRoot}, discover.Options{})
		return res.Runs
	}
	setting := func(r discover.Run, k string) string {
		for _, s := range r.Settings {
			if s.Key == k {
				return s.Value
			}
		}
		return "<absent>"
	}

	// 1. submit: the record is next to the outputs; a rescan finds the run with the settings the job was given
	j := draft()
	sub := submit(j)
	rec := filepath.Join(j.OutputDir, "run.json")
	data, err := os.ReadFile(rec)
	if err != nil {
		t.Fatalf("no run record after submit: %v", err)
	}
	if !strings.Contains(string(data), jobs.RunRecordMarker) {
		t.Fatalf("the record must carry the marker: %s", data)
	}
	found := scanRuns()
	if len(found) != 1 {
		t.Fatalf("a rescan must find the run: %+v", found)
	}
	r := found[0]
	if r.RecordKind != "run-json" || !strings.HasPrefix(r.Path, "vesqira_runs/vgt-ker8-") {
		t.Fatalf("%+v", r)
	}
	for k, want := range map[string]string{"vgt.ker": "8", "noise.noise_mode": "channel", "vgt.dn": "20", "slurm.cpus-per-task": "8", "slurm.mem": "16G"} {
		if got := setting(r, k); got != want {
			t.Errorf("setting %s = %q, want %q (%+v)", k, got, want, r.Settings)
		}
	}
	for _, f := range r.Fields { // nothing Vesqira cannot establish is declared: no commit, no start time
		if f.Key == "commit" || f.Key == "startedAt" {
			t.Errorf("the record claimed %+v", f)
		}
	}
	var doc struct {
		Vesqira struct {
			State struct{ Value string }
			Job   struct {
				SlurmID    string  `json:"slurm_id"`
				ScriptSHA  string  `json:"script_sha256"`
				FinishedAt *string `json:"finished_at"`
			}
			Code       struct{ Commit *string }
			Provenance map[string]string
		}
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Vesqira.State.Value != "submitted" || doc.Vesqira.Job.SlurmID != sub.SlurmID || doc.Vesqira.Job.ScriptSHA != sub.ScriptHash || doc.Vesqira.Job.FinishedAt != nil ||
		doc.Vesqira.Code.Commit != nil || !strings.HasPrefix(doc.Vesqira.Provenance["code"], "unresolved") || !strings.HasPrefix(doc.Vesqira.Provenance["script_sha256"], "verified") ||
		!strings.HasPrefix(doc.Vesqira.Provenance["settings"], "declared") {
		t.Fatalf("%s", data)
	}

	// 2. the end state is added when a refresh sees it; nothing else about the settings changes
	os.WriteFile(filepath.Join(bin, "state", sub.SlurmID), []byte("COMPLETED\n"), 0o644)
	postJSON(t, api("/api/jobs/"+itoa(int(j.ID))+"/refresh"), map[string]any{})
	data, _ = os.ReadFile(rec)
	doc.Vesqira.State.Value = ""
	json.Unmarshal(data, &doc)
	if doc.Vesqira.State.Value != "completed" || doc.Vesqira.Job.FinishedAt == nil {
		t.Fatalf("the final state was not recorded: %s", data)
	}
	if got := setting(scanRuns()[0], "vgt.ker"); got != "8" {
		t.Errorf("after the rewrite: %s", got)
	}

	// 3. a run.json that is not ours is never replaced; the job is still submitted and the job says the record is missing
	j2 := draft()
	if err := os.MkdirAll(j2.OutputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	theirs := `{"params": {"lr": 0.1}}`
	os.WriteFile(filepath.Join(j2.OutputDir, "run.json"), []byte(theirs), 0o644)
	sub2 := submit(j2)
	if b, _ := os.ReadFile(filepath.Join(j2.OutputDir, "run.json")); string(b) != theirs {
		t.Fatalf("a foreign run.json was replaced: %s", b)
	}
	if sub2.State != "submitted" || !strings.Contains(sub2.Detail, "run.json not written") {
		t.Fatalf("%+v", sub2)
	}

	// 4. a link where the output folder's parent should be is not written through
	j3 := draft()
	outside := filepath.Join(base, "outside")
	os.MkdirAll(outside, 0o755)
	os.RemoveAll(filepath.Join(remoteRoot, "vesqira_runs"))
	if err := os.Symlink(outside, filepath.Join(remoteRoot, "vesqira_runs")); err != nil {
		t.Fatal(err)
	}
	sub3 := submit(j3)
	if ents, _ := os.ReadDir(outside); len(ents) != 0 || sub3.State != "submitted" {
		t.Fatalf("a record was written through a link: %v %+v", ents, sub3)
	}

	entries, _ := au.List("job", 0, 100)
	var wrote, missed int
	for _, en := range entries {
		switch en.Action {
		case "run record written":
			wrote++
		case "run record not written":
			missed++
		}
	}
	if wrote < 2 || missed != 2 {
		t.Fatalf("the audit log must say what was written and what was not: %+v", entries)
	}
}

// An analysis job's record carries the operation, its options and the catalog files it reads, as references.
func TestRunRecordOfAnAnalysisJobRoundTripsThroughDiscovery(t *testing.T) {
	dir := t.TempDir()
	j := jobs.Job{ID: 7, Name: "moment-3", Template: "analysis", State: "submitted", SlurmID: "4242", ScriptHash: strings.Repeat("a", 64),
		Script: "#!/bin/bash -l\n#SBATCH --job-name=moment-3\n#SBATCH --cpus-per-task=4\n#SBATCH --mem=16G\n#SBATCH --time=02:00:00\nset -e\n",
		Params: `{"op":"moment","options":{"order":"0","vmin":"-5"},"sources":[3]}`}
	data, err := jobs.RunRecord(j, "Cluster", []jobs.RunInput{{DatasetID: 3, Name: "cube.fits", Path: "/data/cube.fits", Source: "catalog"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "vesqira_derived", "moment-3-ab12"), 0o755)
	os.WriteFile(filepath.Join(dir, "vesqira_derived", "moment-3-ab12", "run.json"), data, 0o644)
	res := discover.Scan(context.Background(), discover.LocalFS{Root: dir}, discover.Options{})
	if len(res.Runs) != 1 {
		t.Fatalf("%+v", res.Runs)
	}
	got := map[string]string{}
	for _, s := range res.Runs[0].Settings {
		got[s.Key] = s.Value
	}
	if got["analysis"] != "moment" || got["option.order"] != "0" || got["option.vmin"] != "-5" || got["slurm.cpus-per-task"] != "4" {
		t.Fatalf("%v", got)
	}
	if !strings.Contains(string(data), `"/data/cube.fits"`) || !strings.Contains(string(data), "the files were not hashed") {
		t.Fatalf("%s", data)
	}
}
