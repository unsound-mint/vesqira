package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// ROHSA runs can be run again with changed settings and swept (ADR 0046, 0048). These tests use the stand-in host of
// jobwatch_test.go. The config below is written for the test in the shape of the pipeline's configs.

const rohsaCfg = `# Run: test region 2048, N=16
[input]
input = "data/interim/test_region.fits"
output_dir = "validation_output/rohsa/test_region_n16"
crop_size = 2048
crop_x0 = 1
crop_y0 = 1

[rohsa]
n_gauss = [16]
lambda_amp = 10.0
lambda_mu = 10.0
lambda_sig = 10.0
lb_sig = 1.0
maxiter = 800
noise = true

[auto_config]
enabled = false
`

func (e *watchEnv) rohsaDraft(name string, set map[string]string, extra map[string]any) jobs.Job {
	e.t.Helper()
	body := map[string]any{"repoDir": "/shared/home/u/vgt", "configText": rohsaCfg, "configPath": "/shared/home/u/vgt/configs/rohsa/test.toml",
		"cpus": 32, "mem": "140G", "time": "1-12:00:00"}
	if set != nil {
		body["set"] = set
	}
	in := map[string]any{"hostId": e.host.ID, "template": "rohsa", "name": name, "rohsa": body}
	for k, v := range extra {
		in[k] = v
	}
	r, b := postJSON(e.t, e.api("/api/jobs"), in)
	if r.StatusCode != 201 {
		e.t.Fatalf("create %d %s", r.StatusCode, b)
	}
	var j jobs.Job
	json.Unmarshal(b, &j)
	return j
}

// configOf is the config text a script writes for the job (the heredoc between the markers).
func configOf(t *testing.T, script string) string {
	t.Helper()
	a := strings.Index(script, "<<'VESQIRA_CONFIG_END'\n")
	b := strings.Index(script, "\nVESQIRA_CONFIG_END\n")
	if a < 0 || b < a {
		t.Fatalf("no embedded config:\n%s", script)
	}
	return script[a+len("<<'VESQIRA_CONFIG_END'\n") : b]
}

func TestROHSAJobEmbedsItsConfigAndSetsTheOutputFolderPerJob(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	j := e.rohsaDraft("rohsa-n12", map[string]string{"n_gauss": "12", "lambda": "30"}, nil)
	cfg := configOf(t, j.Script)
	if !strings.Contains(cfg, "n_gauss = [12]") || strings.Count(cfg, "lambda_amp = 30.0") != 1 || !strings.Contains(cfg, "lambda_sig = 30.0") || !strings.Contains(cfg, "lambda_mu = 30.0") {
		t.Errorf("settings not applied:\n%s", cfg)
	}
	if !strings.Contains(cfg, `input = "data/interim/test_region.fits"`) || !strings.Contains(cfg, "# Run: test region 2048, N=16") || !strings.Contains(cfg, "lb_sig = 1.0") {
		t.Errorf("the rest of the config changed:\n%s", cfg)
	}
	if !strings.HasPrefix(j.OutputDir, filepath.Join(e.remoteRoot, "vesqira_runs", "rohsa-n12-")) || !strings.Contains(cfg, `output_dir = "`+j.OutputDir+`"`) {
		t.Errorf("output folder %q not Vesqira's per-job folder:\n%s", j.OutputDir, cfg)
	}
	if !strings.Contains(j.Script, `preprocess.jl --config "$config"`) || !strings.Contains(j.Script, `config="$jobdir/config.toml"`) {
		t.Errorf("the script does not run the embedded config:\n%s", j.Script)
	}
	// a second job from the same base gets its own folder: a repeated run never writes over an earlier one
	k := e.rohsaDraft("rohsa-n12b", map[string]string{"n_gauss": "12"}, nil)
	if k.OutputDir == j.OutputDir {
		t.Error("two jobs share an output folder")
	}
	if !j.AutoImport {
		t.Error("a ROHSA job's fit is imported from its host when it completes (ADR 0046)")
	}
	// the stored request keeps the base text and the overrides apart, as VGT's does
	got, _ := e.jobs.Get(j.ID)
	req, ok := got.ROHSARequest()
	if !ok || req.BaseConfigText != rohsaCfg || req.Set["n_gauss"] != "12" || req.Set["lambda"] != "30" {
		t.Errorf("request: %+v %v", req, ok)
	}
	// the owner's config is only read: nothing is run on the host and nothing reaches it until submit
	if e.submissions() != 0 {
		t.Error("a draft reached sbatch")
	}
}

func TestROHSAJobRefusesWhatVesqiraOwnsOrCannotEditSafely(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	for name, set := range map[string]map[string]string{
		"output_dir":  {"output_dir": "/shared/home/u/elsewhere"},
		"input":       {"input": "/etc/passwd"},
		"vgt key":     {"dn": "8"},
		"bad N":       {"n_gauss": "0"},
		"injection":   {"lambda": "10\n[x]"},
		"unknown key": {"reuse": "true"},
	} {
		body := map[string]any{"hostId": e.host.ID, "template": "rohsa", "name": "x", "rohsa": map[string]any{"repoDir": "/shared/home/u/vgt", "configText": rohsaCfg, "set": set, "cpus": 1, "mem": "1G", "time": "01:00:00"}}
		if r, b := postJSON(t, e.api("/api/jobs"), body); r.StatusCode != 422 {
			t.Errorf("%s: %d %s, want 422", name, r.StatusCode, b)
		}
	}
	// a config that runs the pipeline's own candidate sweep is not edited
	auto := strings.Replace(rohsaCfg, "enabled = false", "enabled = true", 1)
	body := map[string]any{"hostId": e.host.ID, "template": "rohsa", "name": "x", "rohsa": map[string]any{"repoDir": "/shared/home/u/vgt", "configText": auto, "set": map[string]string{"n_gauss": "8"}, "cpus": 1, "mem": "1G", "time": "01:00:00"}}
	if r, b := postJSON(t, e.api("/api/jobs"), body); r.StatusCode != 422 || !strings.Contains(string(b), "auto_config") {
		t.Errorf("auto_config: %d %s", r.StatusCode, b)
	}
	if js, _ := e.jobs.List(); len(js) != 0 {
		t.Errorf("refusals left %d jobs", len(js))
	}
}

// linkRun makes a run for the job, as an import of its results would, so the run page can offer "run again".
func (e *watchEnv) linkRun(j jobs.Job) runs.Run {
	e.t.Helper()
	run, err := e.runs.Save(j.OutputDir, runs.Parsed{Run: runs.Run{Name: j.Name, Kind: "rohsa"}}, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.jobs.LinkRun(j.ID, run.ID); err != nil {
		e.t.Fatal(err)
	}
	return run
}

type rerunReply struct {
	Source   string `json:"source"`
	Template string `json:"template"`
	HostID   *int64 `json:"hostId"`
	Name     string `json:"name"`
	ROHSA    *struct {
		ConfigPath     *string           `json:"configPath"`
		BaseConfigText *string           `json:"baseConfigText"`
		Set            map[string]string `json:"set"`
		Mem            *string           `json:"mem"`
	} `json:"rohsa"`
	Settings map[string]string `json:"settings"`
	Notes    []string          `json:"notes"`
}

func TestRerunDraftForAROHSARunWithChangedN(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	first := e.submit(e.rohsaDraft("rohsa-n16", map[string]string{"lambda": "10"}, nil), nil)
	run := e.linkRun(first)
	code, b := getBody(t, e.api("/api/runs/"+itoa(int(run.ID))+"/rerun"))
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	var rr rerunReply
	json.Unmarshal(b, &rr)
	if rr.Source != "job" || rr.Template != "rohsa" || rr.ROHSA == nil || rr.ROHSA.BaseConfigText == nil || *rr.ROHSA.BaseConfigText != rohsaCfg || rr.HostID == nil {
		t.Fatalf("rerun: %s", b)
	}
	// the baseline of every setting is what the job ran with: the config's own value, with the job's changes over it
	if rr.Settings["n_gauss"] != "16" || rr.Settings["lambda_amp"] != "10" || rr.Settings["noise"] != "true" || rr.Settings["crop_size"] != "2048" {
		t.Errorf("settings: %v", rr.Settings)
	}
	// "run again with N = 12": the draft starts from the text the run started from, whatever the host's file says now
	again := e.rohsaDraft("rohsa-n12", map[string]string{"n_gauss": "12"}, map[string]any{"parentRunId": run.ID})
	if again.ParentRunID == nil || *again.ParentRunID != run.ID {
		t.Errorf("parent run: %v", again.ParentRunID)
	}
	d := diffLines(configOf(t, first.Script), configOf(t, again.Script))
	if len(d) != 4 || !strings.Contains(strings.Join(d, "|"), "n_gauss = [16]") || !strings.Contains(strings.Join(d, "|"), "n_gauss = [12]") {
		t.Errorf("the configs differ in more than N and the output folder: %v", d)
	}
	if e.submissions() != 1 {
		t.Errorf("submissions: %d (only the first job was submitted)", e.submissions())
	}
	// a run that is neither VGT nor ROHSA still has no rerun
	other, _ := e.runs.Save("/k/other", runs.Parsed{Run: runs.Run{Name: "x", Kind: "something"}}, nil)
	if r, _ := getBody(t, e.api("/api/runs/"+itoa(int(other.ID))+"/rerun")); r != 422 {
		t.Errorf("another kind: %d", r)
	}
}

// diffLines lists the lines that are in one text and not the other, marked - and +.
func diffLines(a, b string) []string {
	in := func(lines []string, l string) bool {
		for _, x := range lines {
			if x == l {
				return true
			}
		}
		return false
	}
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out []string
	for _, l := range la {
		if !in(lb, l) {
			out = append(out, "-"+l)
		}
	}
	for _, l := range lb {
		if !in(la, l) {
			out = append(out, "+"+l)
		}
	}
	return out
}

func TestRerunOfAnImportedROHSARunUsesTheTraceFirstAndTheNamelistOnlyToFillGaps(t *testing.T) {
	t.Parallel()
	e := newROHSAEnv(t)
	dir := filepath.Join(e.root, "rohsa_run_demo", "region_a")
	run, _ := e.importRun(t, dir, 201)
	code, b := getBody(t, e.ts.URL+"/api/runs/"+itoa(int(run.ID))+"/rerun")
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	var rr rerunReply
	json.Unmarshal(b, &rr)
	if rr.Source != "imported" || rr.Template != "rohsa" || rr.ROHSA == nil || rr.HostID != nil || rr.Name != "rohsa_n3-again" {
		t.Fatalf("rerun: %s", b)
	}
	// the trace's [config] gave these; `noise` is only in this run's namelist (the pipeline writes both from the same options),
	// so it fills the gap, spelled as a person types it
	want := map[string]string{"n_gauss": "3", "lambda_amp": "10", "lb_sig": "1", "noise": "true"}
	for k, v := range want {
		if rr.Settings[k] != v {
			t.Errorf("%s = %q, want %q (%v)", k, rr.Settings[k], v, rr.Settings)
		}
	}
	if len(rr.Settings) != len(want) {
		t.Errorf("a setting that is not editable, or a path, leaked into the baseline: %v", rr.Settings)
	}
	if len(rr.Notes) < 2 || !strings.Contains(strings.Join(rr.Notes, " "), "trace declares") {
		t.Errorf("notes: %v", rr.Notes)
	}
	if strings.Contains(string(b), "/shared/home") {
		t.Errorf("a path of the machine it ran on came through: %s", b)
	}
}

func TestSweepOverLambdaFromABaseThatSetsAMemberKeepsEveryLineOnTheSweptValue(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	for name, baseSet := range map[string]map[string]string{
		"base sets lambda_amp": {"lambda_amp": "5", "n_gauss": "10"},
		"base sets lambda":     {"lambda": "7"},
	} {
		base := e.rohsaDraft("rohsa-"+strings.ReplaceAll(name, " ", "-"), baseSet, nil)
		sw := e.createSweep(map[string]any{"name": "lam", "baseJobId": base.ID, "params": []map[string]any{p("lambda", "3", "30")}})
		for _, m := range sw.Jobs {
			cfg := configOf(t, e.job(m.JobID).Script)
			for _, k := range []string{"lambda_amp", "lambda_mu", "lambda_sig"} {
				if !strings.Contains(cfg, k+" = "+m.Values["lambda"]+".0\n") {
					t.Errorf("%s: %s is not the swept %s in %s:\n%s", name, k, m.Values["lambda"], m.Label, cfg)
				}
			}
		}
		// the base's own N stays; only the overlapping setting is dropped
		if baseSet["n_gauss"] != "" && !strings.Contains(configOf(t, e.job(sw.Jobs[0].JobID).Script), "n_gauss = [10]") {
			t.Errorf("%s: the base's N was lost", name)
		}
		// sweeping lambda over a base with lambda_amp set also works the other way: lambda_amp swept, lambda from the base
		sw2 := e.createSweep(map[string]any{"name": "amp", "baseJobId": base.ID, "params": []map[string]any{p("lambda_amp", "2")}})
		cfg := configOf(t, e.job(sw2.Jobs[0].JobID).Script)
		if !strings.Contains(cfg, "lambda_amp = 2.0\n") {
			t.Errorf("%s: lambda_amp not swept:\n%s", name, cfg)
		}
	}
	// a grid that names lambda and one of its members is refused: two parameters for the same lines
	base := e.rohsaDraft("rohsa-both", nil, nil)
	r, b := postJSON(t, e.api("/api/sweeps"), map[string]any{"name": "both", "baseJobId": base.ID, "params": []map[string]any{p("lambda", "3"), p("lambda_sig", "5")}})
	if r.StatusCode != 422 || !strings.Contains(string(b), "same config lines") {
		t.Errorf("lambda with lambda_sig: %d %s", r.StatusCode, b)
	}
}

func TestROHSAJobReadsItsConfigFromThePathOnTheHost(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	dir := filepath.Join(e.remoteRoot, "configs", "rohsa")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "a.toml")
	if err := os.WriteFile(path, []byte(rohsaCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"hostId": e.host.ID, "template": "rohsa", "name": "rohsa-path", "rohsa": map[string]any{"repoDir": "/shared/home/u/vgt", "configPath": path, "set": map[string]string{"n_gauss": "8"}, "cpus": 16, "mem": "40G", "time": "12:00:00"}}
	r, b := postJSON(t, e.api("/api/jobs"), body)
	if r.StatusCode != 201 {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	var j jobs.Job
	json.Unmarshal(b, &j)
	cfg := configOf(t, j.Script)
	if !strings.Contains(cfg, "n_gauss = [8]") || !strings.Contains(cfg, "crop_size = 2048") || !strings.Contains(cfg, "# Run: test region 2048, N=16") {
		t.Errorf("the host's file was not the base:\n%s", cfg)
	}
	got, _ := e.jobs.Get(j.ID)
	if req, ok := got.ROHSARequest(); !ok || req.BaseConfigText != rohsaCfg || req.ConfigPath != path {
		t.Errorf("request: %+v", req)
	}
	// a path outside the host's source roots is refused, not read
	body["rohsa"].(map[string]any)["configPath"] = "/etc/passwd"
	if r, _ := postJSON(t, e.api("/api/jobs"), body); r.StatusCode < 400 {
		t.Errorf("a config outside the roots was read: %d", r.StatusCode)
	}
}

func TestDeclaredROHSASettingsNormaliseAndDropWhatIsNotAnEditableValue(t *testing.T) {
	t.Parallel()
	got := declaredROHSASettings([]runs.Param{
		{Key: "noise", Value: ".true.", Source: "config"},
		{Key: "lambda_amp", Value: "10.0d0", Source: "config"},
		{Key: "lambda_mu", Value: "1.5d-3", Source: "config"},
		{Key: "lambda_sig", Value: "20.0d0", Source: "parameters"}, // the namelist only fills what the trace's config does not say
		{Key: "ustd", Value: "20", Source: "parameters"},           // not an editable setting
		{Key: "n_gauss", Value: "16", Source: "run_trace"},
		{Key: "maxiter", Value: "800", Source: "run_trace"}, // a top-level trace entry other than n_gauss is not a setting
		{Key: "init_option", Value: "mean", Source: "config"},
		{Key: "ub_sig", Value: "oops", Source: "config"},
		{Key: "lb_sig", Value: "1.2", Source: "config"},
		{Key: "lb_sig", Value: "1.21", Source: "parameters"}, // disagrees with the config's: the config wins
	})
	want := map[string]string{"noise": "true", "lambda_amp": "10.0", "lambda_mu": "1.5e-3", "lambda_sig": "20.0", "n_gauss": "16", "lb_sig": "1.2"}
	if len(got) != len(want) {
		t.Errorf("%v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestROHSASizingRouteMirrorsTheScriptAndSaysWhatItAssumed(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	get := func(q string) (int, jobs.Sizing) {
		code, b := getBody(t, e.api("/api/rohsa/sizing"+q))
		var z jobs.Sizing
		json.Unmarshal(b, &z)
		return code, z
	}
	if code, z := get("?cropSize=2048&nGauss=16"); code != 200 || z.Mem != "140G" || z.CPUs != 32 || z.Time != "1-12:00:00" || len(z.Assumed) != 0 || !strings.Contains(z.Basis, "submit_rohsa.sh") {
		t.Errorf("2048 N16: %d %+v", code, z)
	}
	if _, z := get("?cropSize=2048&nGauss=6"); z.Mem != "100G" {
		t.Errorf("2048 N6: %+v", z)
	}
	if _, z := get("?cropSize=512&nGauss=16"); z.Mem != "12G" || z.CPUs != 16 {
		t.Errorf("512: %+v", z)
	}
	if _, z := get(""); z.CropSize != 0 || z.NGauss != 6 || len(z.Assumed) != 2 || z.Mem != "180G" {
		t.Errorf("nothing given: %+v", z)
	}
	if code, _ := get("?cropSize=-1"); code != 400 {
		t.Errorf("negative: %d", code)
	}
	if code, _ := get("?nGauss=x"); code != 400 {
		t.Errorf("not a number: %d", code)
	}
}

func TestSweepOverROHSAKeys(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	base := e.rohsaDraft("rohsa-base", map[string]string{"lambda": "10"}, nil)
	req := map[string]any{"name": "rsweep", "baseJobId": base.ID, "params": []map[string]any{
		{"key": "n_gauss", "label": "N", "values": []string{"4", "8"}}, {"key": "lambda", "label": "λ", "values": []string{"3", "30"}}}}
	sw := e.createSweep(req)
	if len(sw.Jobs) != 4 || sw.State != "draft" || sw.Template != "rohsa" || sw.Concurrency != 1 {
		t.Fatalf("%+v", sw)
	}
	want := []string{"N = 4, λ = 3", "N = 4, λ = 30", "N = 8, λ = 3", "N = 8, λ = 30"}
	for i, m := range sw.Jobs {
		if m.Label != want[i] || m.State != "draft" {
			t.Errorf("job %d: %q %s", i, m.Label, m.State)
		}
		j := e.job(m.JobID)
		cfg := configOf(t, j.Script)
		if j.Template != "rohsa" || !j.AutoImport || !strings.Contains(cfg, "n_gauss = ["+m.Values["n_gauss"]+"]") ||
			!strings.Contains(cfg, "lambda_amp = "+m.Values["lambda"]+".0") || !strings.Contains(cfg, "lambda_sig = "+m.Values["lambda"]+".0") {
			t.Errorf("draft %d: auto %v\n%s", i, j.AutoImport, cfg)
		}
	}
	if e.submissions() != 0 {
		t.Error("creating a sweep reached sbatch")
	}
	// the scripts differ from the base only by the swept settings (the tied lambda is three lines), the output folder and the job name
	hash, rev := e.sweepReview(sw.ID)
	if rev["template"] != "rohsa" || rev["defaultConcurrency"].(float64) != 1 || !strings.Contains(rev["concurrencyNote"].(string), "one job at a time") ||
		!strings.Contains(rev["concurrencyNote"].(string), "replaces the binary") {
		t.Errorf("concurrency default and its reason: %v %v", rev["defaultConcurrency"], rev["concurrencyNote"])
	}
	mem := map[string]string{}
	for _, jv := range rev["jobs"].([]any) {
		m := jv.(map[string]any)
		if m["unexpected"].(float64) != 0 {
			t.Errorf("unexpected lines in %v: %v", m["label"], m["diff"])
		}
		z := m["sizing"].(map[string]any)
		mem[m["values"].(map[string]any)["n_gauss"].(string)] = z["mem"].(string)
		if !strings.Contains(z["basis"].(string), "submit_rohsa.sh") {
			t.Errorf("sizing without its source: %v", z)
		}
	}
	// N crosses a row of the owner's table (6), the drafts all request what the base did: the review says so per draft
	if mem["4"] != "100G" || mem["8"] != "140G" {
		t.Errorf("per-draft sizing: %v", mem)
	}
	// approved without naming a concurrency, the sweep runs one job at a time, and the unchanged submit path sends one job
	r, b := postJSON(t, e.api("/api/sweeps/"+itoa(int(sw.ID))+"/submit"), map[string]any{"sweepHash": hash})
	if r.StatusCode != 200 {
		t.Fatalf("approve %d %s", r.StatusCode, b)
	}
	e.srv.waitSweeps()
	e.srv.waitRecords()
	got := e.sweep(sw.ID)
	if got.Concurrency != 1 || got.State != "running" || e.submissions() != 1 || got.Counts["draft"] != 3 {
		t.Errorf("default concurrency 1: %+v submissions %d", got, e.submissions())
	}
	// approving again changes nothing: one job still, exactly once
	if r, _ := postJSON(t, e.api("/api/sweeps/"+itoa(int(sw.ID))+"/submit"), map[string]any{"sweepHash": hash}); r.StatusCode < 400 {
		t.Errorf("a second approval was accepted")
	}
	e.srv.waitSweeps()
	if e.submissions() != 1 {
		t.Errorf("submissions %d", e.submissions())
	}
	// the first ends; the next goes, still one at a time
	e.end(got.Jobs[0].JobID, "COMPLETED")
	if e.submissions() != 2 {
		t.Errorf("after one ended: %d submissions", e.submissions())
	}
	// an explicit limit is still the owner's to raise, and 0 is refused rather than read as "default"
	r2 := e.createSweep(map[string]any{"name": "r2", "baseJobId": base.ID, "params": []map[string]any{p("n_gauss", "5", "6")}})
	h2, _ := e.sweepReview(r2.ID)
	if r, _ := postJSON(t, e.api("/api/sweeps/"+itoa(int(r2.ID))+"/submit"), map[string]any{"sweepHash": h2, "concurrency": 0}); r.StatusCode != 422 {
		t.Errorf("concurrency 0: %d", r.StatusCode)
	}
}

func TestSweepRefusesROHSAKeysItMustNotVaryAndVGTKeysOnAROHSABase(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	base := e.rohsaDraft("rohsa-base", nil, nil)
	vgt := e.draft("vgt-base", nil)
	before, _ := e.jobs.List()
	for name, in := range map[string]map[string]any{
		"output_dir on rohsa": {"baseJobId": base.ID, "params": []map[string]any{p("output_dir", "x")}},
		"input on rohsa":      {"baseJobId": base.ID, "params": []map[string]any{p("input", "x")}},
		"vgt key on rohsa":    {"baseJobId": base.ID, "params": []map[string]any{p("dn", "8")}},
		"bad value":           {"baseJobId": base.ID, "params": []map[string]any{p("n_gauss", "0")}},
		"rohsa key on vgt":    {"baseJobId": vgt.ID, "params": []map[string]any{p("n_gauss", "8")}},
		"shell":               {"baseJobId": base.ID, "params": []map[string]any{p("lambda", "1;rm")}},
	} {
		body := map[string]any{"name": "bad"}
		for k, v := range in {
			body[k] = v
		}
		if r, b := postJSON(t, e.api("/api/sweeps"), body); r.StatusCode != 422 {
			t.Errorf("%s: %d %s, want 422", name, r.StatusCode, b)
		}
	}
	if after, _ := e.jobs.List(); len(after) != len(before) {
		t.Errorf("refusals left jobs behind: %d -> %d", len(before), len(after))
	}
	// a ROHSA job drafted before it kept its settings (it ran a config by path) cannot be a base
	old, err := e.jobs.CreateDraft(jobs.Job{HostID: e.host.ID, Name: "rohsa-old", Template: "rohsa", Script: "#!/bin/bash\n"})
	if err != nil {
		t.Fatal(err)
	}
	if r, b := postJSON(t, e.api("/api/sweeps"), map[string]any{"name": "old", "baseJobId": old.ID, "params": []map[string]any{p("n_gauss", "8")}}); r.StatusCode != 422 || !strings.Contains(string(b), "kept its settings") {
		t.Errorf("old base: %d %s", r.StatusCode, b)
	}
}

func TestROHSASweepReviewHasNoUnexpectedLinesWhenTheBaseScriptWasEditedByHand(t *testing.T) {
	t.Parallel()
	e := newWatchEnv(t)
	base := e.rohsaDraft("rohsa-base", map[string]string{"lambda": "10"}, nil)
	edited := strings.Replace(base.Script, `echo "job_id`, `echo "reviewed; job_id`, 1)
	if edited == base.Script {
		t.Fatal("the script has no echo line to edit")
	}
	if r, b := postJSON(t, e.api("/api/jobs/"+itoa(int(base.ID))+"/script"), map[string]any{"script": edited}); r.StatusCode != 200 {
		t.Fatalf("edit %d %s", r.StatusCode, b)
	}
	sw := e.createSweep(map[string]any{"name": "rsweep", "baseJobId": base.ID, "params": []map[string]any{
		{"key": "n_gauss", "label": "N", "values": []string{"4", "8"}}, {"key": "lambda", "label": "λ", "values": []string{"3", "30"}}}})
	_, rev := e.sweepReview(sw.ID)
	if rev["baseEditedLines"].(float64) != 2 {
		t.Errorf("base edit not reported once: %v", rev["baseEditedLines"])
	}
	for _, jv := range rev["jobs"].([]any) {
		if m := jv.(map[string]any); m["unexpected"].(float64) != 0 {
			t.Errorf("unexpected lines in %v: %v", m["label"], m["diff"])
		}
	}
}
