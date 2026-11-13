package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs/runstest"
)

// A tool that is neither VGT nor ROHSA, declared only by a profile (runs/testdata/synthetic_tool_profile.json), gets a draft,
// typed and range-checked settings, a script the owner reads, and a submission on the fake Slurm path (ADR 0068 slice 5).

const synthConfig = "[input]\ninput_cube = \"x.fits\"\n\n[fit]\nn_bins = 16\nmode = \"fast\"\n"

func (e *watchEnv) synthDraft(set map[string]string, tool map[string]any) (int, []byte) {
	e.t.Helper()
	body := map[string]any{"repoDir": "/shared/home/u/synthfit", "configText": synthConfig, "cpus": 4, "mem": "8G", "time": "02:00:00", "set": set}
	for k, v := range tool {
		body[k] = v
	}
	r, b := postJSON(e.t, e.api("/api/jobs"), map[string]any{"hostId": e.host.ID, "template": "synthfit", "name": "synth", "tool": body})
	return r.StatusCode, b
}

func TestSyntheticToolDraftsSubmitsAndRefuses(t *testing.T) {
	e := newWatchEnv(t)
	e.srv.Runs.Profiles = runstest.WithSyntheticTool(t)

	// the template list carries the declared keys the job form is drawn from
	r, err := http.Get(e.api("/api/job-templates"))
	if err != nil {
		t.Fatal(err)
	}
	lb, _ := io.ReadAll(r.Body)
	r.Body.Close()
	var list struct {
		Templates []struct {
			Profile, ID string
			Keys        []struct {
				Name, Kind string
				Min, Max   *float64
			}
		}
	}
	if err := json.Unmarshal(lb, &list); err != nil || r.StatusCode != 200 {
		t.Fatalf("job-templates: %d %v %s", r.StatusCode, err, lb)
	}
	var ids []string
	for _, x := range list.Templates {
		ids = append(ids, x.ID+"="+x.Profile)
		if x.ID == "synthfit" {
			var bins bool
			for _, k := range x.Keys {
				if k.Name == "n_bins" && k.Kind == "int" && k.Min != nil && *k.Min == 2 && k.Max != nil && *k.Max == 64 {
					bins = true
				}
			}
			if !bins {
				t.Errorf("n_bins and its range are not listed: %s", lb)
			}
		}
	}
	if strings.Join(ids, " ") != "vgt=VGT.jl@1 rohsa=ROHSA-LMC@1 synthfit=SynthFit@1" {
		t.Fatalf("templates listed: %v", ids)
	}

	// a draft: the settings are applied, the output folder is Vesqira's, the command is the template's
	code, b := e.synthDraft(map[string]string{"n_bins": "32", "threshold": "2.5", "mode": "exact", "smooth": "true"}, nil)
	if code != 201 {
		t.Fatalf("create %d %s", code, b)
	}
	var j jobs.Job
	json.Unmarshal(b, &j)
	for _, want := range []string{"n_bins = 32", "threshold = 2.5", "mode = \"exact\"", "smooth = true", "input_cube = \"x.fits\"",
		"export OMP_NUM_THREADS=\"$threads\"", "srun --ntasks=1 python3 synthfit.py --config \"$cfg\"", "output_dir = \"" + e.remoteRoot} {
		if !strings.Contains(j.Script, want) {
			t.Errorf("the script lacks %q:\n%s", want, j.Script)
		}
	}
	if strings.Contains(j.Script, "0123456789abcdef0123456789abcdef01234567") {
		t.Errorf("the profile's commit is checked only when the person asks for it")
	}
	var rq jobs.VGTRequest
	stored, _ := e.jobs.Get(j.ID)
	json.Unmarshal([]byte(stored.Request), &rq)
	if rq.Profile != "SynthFit@1" || rq.Set["n_bins"] != "32" {
		t.Errorf("the request keeps the profile label and the settings: %s", stored.Request)
	}
	if j.AutoImport {
		t.Errorf("a tool Vesqira has no importer for is not imported by itself")
	}

	// refusals: out of range, injection, a key the template does not offer, the output folder, a folder that is not plain
	hostile := []struct {
		name string
		set  map[string]string
		tool map[string]any
		want string
	}{
		{"above range", map[string]string{"n_bins": "65"}, nil, "n_bins"},
		{"below range", map[string]string{"n_bins": "1"}, nil, "n_bins"},
		{"not an int", map[string]string{"n_bins": "8.5"}, nil, "n_bins"},
		{"threshold above range", map[string]string{"threshold": "10.5"}, nil, "threshold"},
		{"injection in a number", map[string]string{"threshold": "1; touch /tmp/pwned"}, nil, "threshold"},
		{"injection in an enum", map[string]string{"mode": "fast\"\nrm -rf ~"}, nil, "mode"},
		{"newline in a bool", map[string]string{"smooth": "true\n[fit]"}, nil, "smooth"},
		{"unknown key", map[string]string{"shell": "x"}, nil, "not an editable"},
		{"the output folder", map[string]string{"output_dir": "/etc"}, nil, "not an editable"},
		{"repo folder", nil, map[string]any{"repoDir": "/x; rm -rf ~"}, "SynthFit folder"},
		{"repo folder substitution", nil, map[string]any{"repoDir": "/x/$(id)"}, "SynthFit folder"},
	}
	for _, h := range hostile {
		code, b := e.synthDraft(h.set, h.tool)
		if code != 422 || !strings.Contains(string(b), h.want) {
			t.Errorf("%s: %d %s, want 422 naming %q", h.name, code, b, h.want)
		}
	}
	// the boundaries themselves are accepted
	for _, v := range []string{"2", "64"} {
		if code, b := e.synthDraft(map[string]string{"n_bins": v}, nil); code != 201 {
			t.Errorf("n_bins %s: %d %s", v, code, b)
		}
	}

	// the pin is the profile's commit, and only when asked for
	code, b = e.synthDraft(nil, map[string]any{"pinProfileCommit": true})
	if code != 201 {
		t.Fatalf("pinned draft %d %s", code, b)
	}
	var pinned jobs.Job
	json.Unmarshal(b, &pinned)
	if !strings.Contains(pinned.Script, "0123456789abcdef0123456789abcdef01234567") {
		t.Errorf("a pinned draft insists on the profile's commit:\n%s", pinned.Script)
	}

	// submitted on the fake Slurm path: the host receives exactly the script that was shown
	sub := e.submit(j, nil)
	if sub.State != "submitted" || sub.SlurmID == "" {
		t.Fatalf("submitted job: %+v", sub)
	}
	sent, err := os.ReadFile(filepath.Join(e.home, "vesqira-jobs", sub.WorkDir, "job.sh"))
	if err != nil || string(sent) != j.Script {
		t.Errorf("the host must have received exactly the reviewed script (err %v)", err)
	}

	// progress is read with the profile's own log pattern
	e.srv.progress.read = func(_ context.Context, _ jobs.Job) (string, error) { return "start\nsynthfit: 3/8 tiles\n", nil }
	if p := e.srv.logProgress(context.Background(), sub, true); p == nil || p.Done != 3 || p.Total != 8 {
		t.Errorf("progress by the profile's pattern: %+v", p)
	}

	// a server without the profile does not know the template
	e.srv.Runs.Profiles = nil
	if code, b := e.synthDraft(nil, nil); code != 400 || !strings.Contains(string(b), "job-templates") {
		t.Errorf("an unknown template: %d %s", code, b)
	}
}
