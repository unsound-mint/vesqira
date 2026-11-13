package discover

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

func profileProject(t *testing.T) (root string, put func(rel, content string)) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root, func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func reasons(files []ProfileFile) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		out[f.Rel] = f.Why
	}
	return out
}

func TestProfileFilesAreReadInsideTheProjectOnly(t *testing.T) {
	root, put := profileProject(t)
	if got := ReadProfileFiles(LocalFS{Root: root}); got != nil {
		t.Fatalf("a project with no profiles folder reads nothing: %+v", got)
	}
	put(".vesqira/profiles/b.json", "{}")
	put(".vesqira/profiles/a.json", "{}")
	put(".vesqira/profiles/notes.txt", "x")
	put(".vesqira/profiles/sub/c.json", "{}")
	put(".vesqira/profiles/.hidden.json", "{}")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.json"), []byte(`{"format":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.json"), filepath.Join(root, ".vesqira/profiles/leak.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".vesqira/profiles/a.json"), filepath.Join(root, ".vesqira/profiles/inner.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "gone.json"), filepath.Join(root, ".vesqira/profiles/dangling.json")); err != nil {
		t.Fatal(err)
	}
	put(".vesqira/profiles/big.json", strings.Repeat(" ", runs.MaxProjectProfileBytes+1))
	got := ReadProfileFiles(LocalFS{Root: root})
	why := reasons(got)
	for _, ok := range []string{"a.json", "b.json", "inner.json"} {
		if w, found := why[ProfilesDir+"/"+ok]; !found || w != "" {
			t.Errorf("%s should be read: %q", ok, w)
		}
	}
	for name, want := range map[string]string{"leak.json": "leaves the project", "dangling.json": "leaves the project", "big.json": "larger than", "notes.txt": "named <tool>.json", ".hidden.json": "named <tool>.json"} {
		if w := why[ProfilesDir+"/"+name]; !strings.Contains(w, want) {
			t.Errorf("%s: %q, want a sentence with %q", name, w, want)
		}
	}
	if _, found := why[ProfilesDir+"/sub"]; found {
		t.Errorf("a subfolder is not a profile and is not listed")
	}
	for _, f := range got {
		if f.Data != nil && strings.Contains(string(f.Data), "format\":1") {
			t.Errorf("the file behind a link that leaves the project was read: %s", f.Rel)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Rel > got[i].Rel {
			t.Errorf("files come in name order: %s before %s", got[i-1].Rel, got[i].Rel)
		}
	}
}

func TestAProfilesFolderThatIsALinkIsNotFollowed(t *testing.T) {
	root, put := profileProject(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	put(".vesqira/project.toml", "")
	if err := os.Symlink(outside, filepath.Join(root, ".vesqira/profiles")); err != nil {
		t.Fatal(err)
	}
	got := ReadProfileFiles(LocalFS{Root: root})
	if len(got) != 1 || got[0].Data != nil || !strings.Contains(got[0].Why, "is a link") {
		t.Fatalf("%+v", got)
	}
	root2, _ := profileProject(t)
	if err := os.Symlink(outside, filepath.Join(root2, ".vesqira")); err != nil {
		t.Fatal(err)
	}
	if got := ReadProfileFiles(LocalFS{Root: root2}); len(got) != 1 || !strings.Contains(got[0].Why, "is a link") {
		t.Fatalf(".vesqira as a link: %+v", got)
	}
}

func TestAtMostSixteenProfileFilesAreRead(t *testing.T) {
	root, put := profileProject(t)
	for i := 0; i < MaxProfileFiles+3; i++ {
		put(fmt.Sprintf(".vesqira/profiles/p%02d.json", i), "{}")
	}
	got := ReadProfileFiles(LocalFS{Root: root})
	read, held := 0, 0
	for _, f := range got {
		if f.Why == "" {
			read++
		} else if strings.Contains(f.Why, "at most 16") {
			held++
		}
	}
	if read != MaxProfileFiles || held != 3 {
		t.Errorf("%d read, %d held back", read, held)
	}
}

// One bad file does not stop the others, and a file's refusal is the sentence the profile loader gives.
func TestProjectRegistryKeepsGoodFilesAndSaysWhyForTheRest(t *testing.T) {
	good := `{"format":1,"profile":{"name":"Mine","version":"1","kind":"mine","record":{"file":"m.toml","reader":"run_metadata","anyKey":["mine.v"]},"outputs":{"table":"o","files":{"x":{"product":"orientation_map","role":"orientation"}}}}}`
	shippedName := strings.Replace(good, `"Mine"`, `"ROHSA"`, 1)
	root, put := profileProject(t)
	put(".vesqira/profiles/a-bad.json", shippedName)
	put(".vesqira/profiles/b-good.json", good)
	put(".vesqira/profiles/c-broken.json", "{not json")
	reg, status := ProjectRegistry(runs.Shipped(), ReadProfileFiles(LocalFS{Root: root}))
	if reg.ByLabel("Mine@1") == nil || reg.ByLabel("ROHSA@1") == nil {
		t.Fatalf("registry: mine %v", reg.ByLabel("Mine@1"))
	}
	if len(status) != 3 || status[0].Profile != "" || !strings.Contains(status[0].Why, "cannot redefine") || status[1].Profile != "Mine@1" || status[2].Why == "" {
		t.Fatalf("status %+v", status)
	}
	// a scan with the registry detects the project's tool by content, and says which profile did
	put("runs/r1/m.toml", "[mine]\nv = 1\n[o]\nx = \"a.fits\"\n")
	res := Scan(t.Context(), LocalFS{Root: root}, Options{Profiles: reg, Layout: Layout{Runs: []string{"runs"}}})
	if len(res.Runs) != 1 || res.Runs[0].Profile != "Mine@1" {
		t.Fatalf("runs %+v", res.Runs)
	}
	// and without it the same folder is nobody's run
	res = Scan(t.Context(), LocalFS{Root: root}, Options{Layout: Layout{Runs: []string{"runs"}}})
	for _, r := range res.Runs {
		if r.Profile == "Mine@1" {
			t.Errorf("a project's profile was applied by a scan that did not load it")
		}
	}
}
