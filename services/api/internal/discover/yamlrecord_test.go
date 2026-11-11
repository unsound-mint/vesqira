package discover

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func settingsMap(r *Run) map[string]string {
	m := map[string]string{}
	for _, s := range r.Settings {
		m[s.Key] = s.Value
	}
	return m
}

func hasNote(r *Run, sub string) bool {
	for _, n := range r.Notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

func TestParamsYAMLMatchesParamsJSON(t *testing.T) {
	root := canonical(t)
	write(t, root, "y/params.yaml", "commit: abc1234\nalpha: 2\nbeta: 0.5\nflag: true\nname: smooth\nout_dir: /abs\nparams:\n  gamma: 3\n  label: x\n  nested:\n    deep: 1\n  items: [1, 2]\n")
	write(t, root, "y/metrics.json", "{}")
	write(t, root, "j/params.json", `{"commit":"abc1234","alpha":2,"beta":0.5,"flag":true,"name":"smooth","out_dir":"/abs","params":{"gamma":3,"label":"x","nested":{"deep":1},"items":[1,2]}}`)
	write(t, root, "j/metrics.json", "{}")
	r := scan(t, LocalFS{root}, Options{})
	y, j := runByPath(r, "y"), runByPath(r, "j")
	if y == nil || j == nil || y.RecordKind != "params" {
		t.Fatalf("runs: %+v", r.Runs)
	}
	if !reflect.DeepEqual(y.Settings, j.Settings) || len(y.Settings) == 0 {
		t.Fatalf("yaml %+v json %+v", y.Settings, j.Settings)
	}
	if len(y.Fields) != len(j.Fields) {
		t.Fatalf("fields yaml %+v json %+v", y.Fields, j.Fields)
	}
	for i, f := range y.Fields {
		if f.Status != "declared" || f.Key != j.Fields[i].Key || f.Value != j.Fields[i].Value {
			t.Fatalf("field %+v vs %+v", f, j.Fields[i])
		}
	}
	if hasNote(y, "no YAML reader") {
		t.Fatalf("notes: %v", y.Notes)
	}
}

func TestMetaYAMLRun(t *testing.T) {
	root := canonical(t)
	write(t, root, "m/meta.yaml", "run_id: 0123abcd\nstatus: 3\nlifecycle_stage: active\nparams:\n  lr: 0.01\n")
	write(t, root, "n/meta.yaml", "title: not mlflow\n")
	r := scan(t, LocalFS{root}, Options{})
	m := runByPath(r, "m")
	if m == nil || m.RecordKind != "mlflow" {
		t.Fatalf("runs: %+v", r.Runs)
	}
	got := settingsMap(m)
	if got["lr"] != "0.01" || got["status"] != "3" {
		t.Fatalf("settings: %v", got)
	}
	if runByPath(r, "n") != nil {
		t.Fatal("a meta.yaml without run_id is not a run")
	}
}

func yamlRun(t *testing.T, content string) (*Run, time.Duration) {
	t.Helper()
	root := canonical(t)
	write(t, root, "r/params.yaml", content)
	write(t, root, "r/metrics.json", "{}")
	start := time.Now()
	res := scan(t, LocalFS{root}, Options{})
	d := time.Since(start)
	r := runByPath(res, "r")
	if r == nil {
		t.Fatalf("scan continued without the run: %+v", res.Runs)
	}
	return r, d
}

func TestYAMLRefusals(t *testing.T) {
	bomb := "a: &a [x,x,x,x,x,x,x,x,x]\nb: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a]\nc: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b]\nd: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c]\ne: [*d,*d,*d,*d,*d,*d,*d,*d,*d]\n"
	cases := map[string]struct{ in, note string }{
		"malformed": {"alpha: [1, 2\n  : :\n\t- x", "is not valid YAML"},
		"list":      {"- a\n- b\n", "is not a mapping"},
		"scalar":    {"just text\n", "is not a mapping"},
		"empty":     {"", "is not a mapping"},
		"bomb":      {bomb, "anchors or aliases"},
		"anchor":    {"a: &x 1\n", "anchors or aliases"},
		"merge":     {"base: &b {k: 1}\nother:\n  <<: *b\n", "anchors or aliases"},
		"deep":      {"a: " + strings.Repeat("[", 5000) + strings.Repeat("]", 5000), "nested too deeply"},
		"oversize":  {"alpha: 1\n# " + strings.Repeat("x", 300<<10) + "\n", "too large"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r, d := yamlRun(t, c.in)
			if len(r.Settings) != 0 {
				t.Fatalf("settings shown: %+v", r.Settings)
			}
			if !hasNote(r, c.note) {
				t.Fatalf("notes: %v", r.Notes)
			}
			if d > 2*time.Second {
				t.Fatalf("took %v", d)
			}
		})
	}
}

func TestYAMLDepthBound(t *testing.T) {
	in := "a: " + strings.Repeat("[", 100) + strings.Repeat("]", 100)
	if _, err := parseYAMLRecord([]byte(in)); err != errYAMLDeep {
		t.Fatalf("err: %v", err)
	}
}

func TestYAMLTagsAreNotExecuted(t *testing.T) {
	r, _ := yamlRun(t, "alpha: 1\nweird: !!python/object/apply:os.system [echo hi]\nwhen: 2026-01-02\n")
	got := settingsMap(r)
	if got["alpha"] != "1" || got["when"] != "2026-01-02T00:00:00Z" {
		t.Fatalf("settings: %v", got)
	}
}
