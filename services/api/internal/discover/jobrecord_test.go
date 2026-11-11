package discover

import (
	"strings"
	"testing"
)

const rohsaRecordFor = `{"settings": {"rohsa.n_gauss": "4"}, "vesqira": {"record": "vesqira-run-record", "version": 1, "job": {"id": 12, "template": "rohsa", "output_dir": "/h/out"}}}`

// Vesqira's run.json for a ROHSA job sits in the job's output folder and the fit (with its trace) in it or below it: one run, the
// fit, which names the job. The record alone, a record of another program, and a record of another kind of job stay what they are.
func TestAROHSAJobsRecordAndItsFitAreOneRun(t *testing.T) {
	trace := "n_gauss = 4\nregion = \"r\"\nstatus = \"ran\"\n[config]\nlambda_amp = 10.0\n"
	field := func(r *Run, k string) string {
		for _, f := range r.Fields {
			if f.Key == k {
				return f.Value + "/" + f.Status
			}
		}
		return ""
	}
	// the fit one folder below the record
	root := canonical(t)
	write(t, root, "runs/job1/run.json", rohsaRecordFor)
	write(t, root, "runs/job1/r/rohsa_n4_run_trace.toml", trace)
	write(t, root, "runs/job1/r/rohsa_n4.dat", "# x\n")
	r := scan(t, LocalFS{root}, Options{})
	if len(r.Runs) != 1 || r.Runs[0].Path != "runs/job1/r" || r.Runs[0].RecordKind != "rohsa" {
		t.Fatalf("record and fit listed as %d runs: %+v", len(r.Runs), r.Runs)
	}
	if got := field(&r.Runs[0], "job"); got != "Vesqira job 12/declared" {
		t.Errorf("the fit does not name its job: %q", got)
	}
	for _, n := range r.Runs[0].Notes {
		if strings.Contains(n, "is inside the run") {
			t.Errorf("a note still speaks of an outer run that is no longer listed: %q", n)
		}
	}
	// the fit in the same folder as the record: the trace is the run's record, the job is added from run.json
	root = canonical(t)
	write(t, root, "runs/job2/run.json", rohsaRecordFor)
	write(t, root, "runs/job2/rohsa_n4_run_trace.toml", trace)
	r = scan(t, LocalFS{root}, Options{})
	if len(r.Runs) != 1 || r.Runs[0].RecordKind != "rohsa" || field(&r.Runs[0], "job") != "Vesqira job 12/declared" {
		t.Fatalf("same folder: %+v", r.Runs)
	}
	// a job that has not produced a fit yet: the record is all there is, and it is listed
	root = canonical(t)
	write(t, root, "runs/job3/run.json", rohsaRecordFor)
	r = scan(t, LocalFS{root}, Options{})
	if len(r.Runs) != 1 || r.Runs[0].RecordKind != "run-json" {
		t.Fatalf("record only: %+v", r.Runs)
	}
	// a run.json that is not Vesqira's (no marker), or is one for another template, is not merged with the fit below it
	for name, rec := range map[string]string{
		"foreign": `{"settings": {"a": "1"}, "vesqira": {"record": "something-else", "job": {"id": 3, "template": "rohsa"}}}`,
		"vgt":     strings.Replace(rohsaRecordFor, `"template": "rohsa"`, `"template": "vgt"`, 1),
		"nojob":   `{"settings": {"a": "1"}}`,
	} {
		root = canonical(t)
		write(t, root, "runs/j/run.json", rec)
		write(t, root, "runs/j/r/rohsa_n4_run_trace.toml", trace)
		r = scan(t, LocalFS{root}, Options{})
		if len(r.Runs) != 2 {
			t.Errorf("%s: %d runs, want the record and the fit as before", name, len(r.Runs))
		}
	}
	// two fits below one record: both stay (each names the job), the record is not a third run
	root = canonical(t)
	write(t, root, "runs/job4/run.json", rohsaRecordFor)
	write(t, root, "runs/job4/a/rohsa_n4_run_trace.toml", trace)
	write(t, root, "runs/job4/b/rohsa_n6_run_trace.toml", trace)
	r = scan(t, LocalFS{root}, Options{})
	if len(r.Runs) != 2 || field(&r.Runs[0], "job") == "" || field(&r.Runs[1], "job") == "" {
		t.Errorf("two fits: %+v", r.Runs)
	}
}
