package runs

import (
	"encoding/json"
	"strings"
	"testing"
)

// A project's profile file is held to the checks a shipped profile passes and to three more: it cannot take a name or a run kind of
// this program, it carries no job template, and it declares something (ADR 0068, slice 6).

const hogFile = `{"format": 1, "about": "x", "profile": {
  "name": "HOG-mine", "version": "2", "kind": "hog-mine", "label": "HOG",
  "record": {"file": "hog.toml", "reader": "run_metadata", "anyKey": ["hog.version"]},
  "outputs": {"table": "products", "files": {"o": {"product": "orientation_map", "role": "orientation"}}}}}`

func mutate(t *testing.T, edit func(profile map[string]any)) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(hogFile), &doc); err != nil {
		t.Fatal(err)
	}
	edit(doc["profile"].(map[string]any))
	b, _ := json.Marshal(doc)
	return string(b)
}

func TestProjectProfileIsReadWithItsOrigin(t *testing.T) {
	reg, p, err := Shipped().WithProject([]byte(hogFile), ".vesqira/profiles/hog.json")
	if err != nil {
		t.Fatal(err)
	}
	if p.Label() != "HOG-mine@2" || p.Origin == nil || p.Origin.File != ".vesqira/profiles/hog.json" || len(p.Origin.SHA) != 64 || !p.FromProject() {
		t.Fatalf("profile %+v origin %+v", p, p.Origin)
	}
	if reg.ByLabel("HOG-mine@2") != p || reg.ByLabel("VGT.jl@1") == nil {
		t.Errorf("the project's registry holds the program's profiles and its own")
	}
	if Shipped().ByLabel("HOG-mine@2") != nil {
		t.Errorf("loading a project profile changed the program's registry")
	}
	if Shipped().ByName("VGT.jl").FromProject() {
		t.Errorf("a shipped profile has no project origin")
	}
}

func TestProjectProfileRefusals(t *testing.T) {
	cases := []struct {
		name, file, want string
	}{
		{"unknown field in the document", `{"format": 1, "profile": {}, "extra": 1}`, "unknown field"},
		{"unknown field in the profile", mutate(t, func(p map[string]any) { p["runs"] = "x" }), "unknown field"},
		{"wrong format", strings.Replace(hogFile, `"format": 1`, `"format": 2`, 1), "expected"},
		{"a shipped name at another version", mutate(t, func(p map[string]any) { p["name"] = "VGT.jl"; p["version"] = "9" }), "cannot redefine"},
		{"a shipped name on its own label", mutate(t, func(p map[string]any) { p["name"] = "ROHSA-LMC"; p["version"] = "1" }), "cannot redefine"},
		{"a shipped run kind", mutate(t, func(p map[string]any) { p["kind"] = "vgt" }), "run kind"},
		{"a name that is not a label part", mutate(t, func(p map[string]any) { p["name"] = "a@b" }), "letters, digits"},
		{"a name with a path in it", mutate(t, func(p map[string]any) { p["name"] = "../x" }), "letters, digits"},
		{"a job template", mutate(t, func(p map[string]any) { p["template"] = map[string]any{"id": "x"} }), "job template cannot come from a project folder"},
		{"nothing declared", `{"format": 1, "profile": {"name": "Empty", "version": "1", "kind": "empty"}}`, "declares nothing"},
		{"an unknown reader", mutate(t, func(p map[string]any) { p["record"].(map[string]any)["reader"] = "python" }), "unknown reader"},
		{"a record with no key to know it by", mutate(t, func(p map[string]any) { p["record"].(map[string]any)["anyKey"] = []string{} }), "by content"},
		{"a role that is not generic and has no label", mutate(t, func(p map[string]any) {
			p["outputs"].(map[string]any)["files"].(map[string]any)["o"] = map[string]any{"product": "m", "role": "made_up"}
		}), "neither a generic role"},
		{"a product tree under the run_metadata reader", mutate(t, func(p map[string]any) {
			p["outputs"] = map[string]any{"tree": "out/{label}", "folders": []any{}}
		}), "ROHSA trace reader only"},
		{"a pattern that does not compile", mutate(t, func(p map[string]any) {
			p["files"] = map[string]any{"component_table": map[string]any{"pattern": "([a-"}}
		}), "missing closing"},
		{"a pattern that is too long", mutate(t, func(p map[string]any) {
			p["files"] = map[string]any{"component_table": map[string]any{"pattern": strings.Repeat("a", 301)}}
		}), "longer than"},
		{"a label that is too long", mutate(t, func(p map[string]any) { p["label"] = strings.Repeat("x", 81) }), "too long"},
		{"a component link to nobody", mutate(t, func(p map[string]any) { p["components"] = "Nobody" }), "unknown profile"},
		{"larger than the bound", hogFile + strings.Repeat(" ", MaxProjectProfileBytes), "larger than"},
	}
	for _, c := range cases {
		_, _, err := Shipped().WithProject([]byte(c.file), ".vesqira/profiles/x.json")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want one naming %q", c.name, err, c.want)
		}
	}
}

func TestProjectProfilesCannotShadowEachOtherOrTheirOwnLabel(t *testing.T) {
	reg, _, err := Shipped().WithProject([]byte(hogFile), "a.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reg.WithProject([]byte(hogFile), "b.json"); err == nil || !strings.Contains(err.Error(), "already defined") {
		t.Errorf("the same label twice: %v", err)
	}
	// another version of the same tool, and another tool with the same run kind, are the project's own business
	v3 := mutate(t, func(p map[string]any) { p["version"] = "3" })
	if _, _, err := reg.WithProject([]byte(v3), "c.json"); err != nil {
		t.Errorf("a second version of a project's tool: %v", err)
	}
}

// Two profiles may read a record of the same name; the keys inside tell them apart, and a record with neither's keys is nobody's.
func TestOneRecordNameTwoToolsToldApartByContent(t *testing.T) {
	other := mutate(t, func(p map[string]any) {
		p["name"], p["kind"] = "Other", "other"
		p["record"].(map[string]any)["anyKey"] = []string{"other.version"}
	})
	reg, _, err := Shipped().WithProject([]byte(hogFile), "a.json")
	if err != nil {
		t.Fatal(err)
	}
	if reg, _, err = reg.WithProject([]byte(other), "b.json"); err != nil {
		t.Fatal(err)
	}
	if p := reg.ByRecord("hog.toml", ReaderRunMetadata, []byte("[other]\nversion = 1\n")); p == nil || p.Name != "Other" {
		t.Errorf("by content: %+v", p)
	}
	if p := reg.ByRecord("hog.toml", ReaderRunMetadata, []byte("[hog]\nversion = 1\n")); p == nil || p.Name != "HOG-mine" {
		t.Errorf("by content: %+v", p)
	}
	if p := reg.ByRecord("hog.toml", ReaderRunMetadata, []byte("[nothing]\nat = 1\n")); p != nil {
		t.Errorf("a record with nobody's keys was taken for %s", p.Label())
	}
	if !reg.HasRecordFile("hog.toml", ReaderRunMetadata) || reg.HasRecordFile("nope.toml", ReaderRunMetadata) {
		t.Errorf("HasRecordFile")
	}
}
