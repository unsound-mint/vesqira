package discover

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func canonical(t *testing.T) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const vgtMeta = `generated_at_utc = "2026-08-01T10:00:00Z"
gitcommit = "abc1234"
[config]
lambda = 0.5
width = 3
[input]
path = "/somewhere/cube.fits"
size_bytes = 100
[outputs]
psi_path = "/x/psi.fits"
`

func vgtLike(t *testing.T) string {
	root := canonical(t)
	write(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	write(t, root, ".git/refs/heads/main", "0123456789abcdef0123456789abcdef01234567\n")
	write(t, root, "validation_output/a/run_metadata.toml", vgtMeta)
	write(t, root, "validation_output/a/psi.fits", "x")
	write(t, root, "validation_output/b/run_metadata.toml", strings.Replace(vgtMeta, "lambda = 0.5", "lambda = 0.9", 1))
	write(t, root, "validation_output/b/psi.fits", "x")
	write(t, root, "data/exp_raw/cube1.fits", "x")
	write(t, root, "data/sims/sim.fits.gz", "x")
	write(t, root, "configs/rohsa/base.toml", "a=1")
	write(t, root, "hpc/cluster/run.slurm", "#!/bin/bash")
	write(t, root, "hpc/cluster/helper.sh", "#!/bin/bash\n#SBATCH -n 4\n")
	write(t, root, "hpc/cluster/plain.sh", "#!/bin/bash\necho hi\n")
	write(t, root, "old/stale.fits", "x")
	write(t, root, "node_modules/pkg/x.fits", "x")
	write(t, root, ".venv/lib/y.fits", "x")
	return root
}

func scan(t *testing.T, fs FS, opt Options) *Result {
	t.Helper()
	return Scan(context.Background(), fs, opt)
}

func paths(fs []File) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Path)
	}
	return out
}

func runByPath(r *Result, p string) *Run {
	for i := range r.Runs {
		if r.Runs[i].Path == p {
			return &r.Runs[i]
		}
	}
	return nil
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%s: got %v want %v", what, got, want)
	}
}

func TestVGTLikeProject(t *testing.T) {
	root := vgtLike(t)
	r := scan(t, LocalFS{root}, Options{Layout: Layout{Skip: []string{"old/**"}}})
	if len(r.Runs) != 2 {
		t.Fatalf("runs: %+v", r.Runs)
	}
	a := runByPath(r, "validation_output/a")
	if a == nil || a.Record != "run_metadata.toml" || a.RecordKind != "vgt" {
		t.Fatalf("run a: %+v", a)
	}
	// the files inside a run belong to the run and are not listed as data
	eq(t, "data", paths(r.Data), []string{"data/exp_raw/cube1.fits", "data/sims/sim.fits.gz"})
	if a.OutputCount != 1 || a.Outputs[0].Path != "validation_output/a/psi.fits" || a.Outputs[0].Format != "FITS" {
		t.Fatalf("outputs: %+v", a.Outputs)
	}
	eq(t, "configs", paths(r.Configs), []string{"configs/rohsa/base.toml"})
	eq(t, "jobs", paths(r.Jobs), []string{"hpc/cluster/helper.sh", "hpc/cluster/run.slurm"}) // plain.sh has no #SBATCH
	// declared values come from the record, labelled as such
	var commit Field
	for _, f := range a.Fields {
		if f.Key == "commit" {
			commit = f
		}
	}
	if commit.Value != "abc1234" || commit.Status != "declared" || commit.Source != "run_metadata.toml" {
		t.Fatalf("commit: %+v", commit)
	}
	// only the setting that differs between the sibling runs is marked as differing
	if len(a.Differs) != 1 || a.Differs[0].Key != "lambda" || a.Differs[0].Value != "0.5" {
		t.Fatalf("differs: %+v", a.Differs)
	}
	b := runByPath(r, "validation_output/b")
	if len(b.Differs) != 1 || b.Differs[0].Value != "0.9" {
		t.Fatalf("differs b: %+v", b.Differs)
	}
	// node_modules and .venv are tool environments: counted in the summary line, not listed; the project's own pattern is listed
	if r.Skipped.ToolEnvs != 2 {
		t.Fatalf("tool environments skipped: %d, want 2", r.Skipped.ToolEnvs)
	}
	if !slices.Contains(r.Skipped.Dirs, "old") {
		t.Fatalf("old not reported as skipped: %v", r.Skipped.Dirs)
	}
	for _, d := range r.Data {
		if strings.HasPrefix(d.Path, "old/") || strings.Contains(d.Path, "node_modules") || strings.Contains(d.Path, ".venv") {
			t.Fatalf("walked into a skipped folder: %s", d.Path)
		}
	}
	// version control comes from .git/HEAD without running git
	if !r.Git.Present || r.Git.Commit != "0123456" || r.Git.Dirty != nil {
		t.Fatalf("git: %+v", r.Git)
	}
}

func TestSiblingWithoutTheKeyStillDiffers(t *testing.T) {
	root := canonical(t)
	write(t, root, "r/a/run_metadata.toml", vgtMeta)
	write(t, root, "r/b/run_metadata.toml", strings.Replace(vgtMeta, "width = 3\n", "", 1))
	r := scan(t, LocalFS{root}, Options{})
	a, b := runByPath(r, "r/a"), runByPath(r, "r/b")
	if len(a.Differs) != 1 || a.Differs[0].Key != "width" || len(b.Differs) != 0 {
		t.Fatalf("a=%+v b=%+v", a.Differs, b.Differs)
	}
}

func TestTimestampAndShaRuns(t *testing.T) {
	root := canonical(t)
	write(t, root, "results/2026-09-10T213350Z_53fb6c2/provenance.json", `{"commit":"53fb6c2deadbeef","params":{"alpha":2,"out":"/abs/path"},"seed":7}`)
	write(t, root, "results/2026-09-10T213350Z_53fb6c2/config.toml", "k=1")
	write(t, root, "results/2026-09-10T213350Z_53fb6c2/masks/x.tif", "x")
	write(t, root, "results/2026-09-11T080000Z_aaaaaaa/run.json", `{"settings":{"alpha":3}}`)
	write(t, root, "data/a.tif", "x")
	write(t, root, "data/b.ome.tiff", "x")
	write(t, root, "data/c.czi", "x")
	write(t, root, "data/d.zarr/.zgroup", "{}")
	write(t, root, "manifest.csv", "a,b")
	r := scan(t, LocalFS{root}, Options{})
	if len(r.Runs) != 2 {
		t.Fatalf("runs: %+v", r.Runs)
	}
	a := runByPath(r, "results/2026-09-10T213350Z_53fb6c2")
	if a.Record != "provenance.json" || a.RecordKind != "provenance" {
		t.Fatalf("record: %+v", a)
	}
	got := map[string]Field{}
	for _, f := range a.Fields {
		got[f.Key] = f
	}
	// the record's own commit is declared; the time, which only the folder name gives, is inferred and labelled so
	if got["commit"].Status != "declared" || got["commit"].Value != "53fb6c2deadbeef" {
		t.Fatalf("commit: %+v", got["commit"])
	}
	if got["startedAt"].Status != "inferred" || got["startedAt"].Value != "2026-09-10T21:33:50Z" || got["startedAt"].Source != "folder name" {
		t.Fatalf("startedAt: %+v", got["startedAt"])
	}
	// a run folder without a declared commit gets the sha from its name, inferred
	b := runByPath(r, "results/2026-09-11T080000Z_aaaaaaa")
	for _, f := range b.Fields {
		if f.Key == "commit" && (f.Status != "inferred" || f.Value != "aaaaaaa") {
			t.Fatalf("inferred commit: %+v", f)
		}
	}
	// locations are not kept as settings; the masks belong to the run
	for _, s := range a.Settings {
		if s.Key == "out" {
			t.Fatalf("a location was kept as a setting: %+v", a.Settings)
		}
	}
	if a.OutputCount != 2 { // config.toml and masks/x.tif
		t.Fatalf("outputs: %d", a.OutputCount)
	}
	for _, d := range r.Data {
		if strings.HasPrefix(d.Path, "results/") {
			t.Fatalf("a run output was listed as data: %s", d.Path)
		}
	}
	formats := map[string]string{}
	for _, d := range r.Data {
		formats[d.Path] = d.Format
		if d.Viewer {
			t.Fatalf("%s: no viewer yet, but Viewer is true", d.Path)
		}
	}
	if formats["data/a.tif"] != "TIFF" || formats["data/b.ome.tiff"] != "OME-TIFF" || formats["data/c.czi"] != "CZI" || formats["data/d.zarr"] != "Zarr" {
		t.Fatalf("formats: %v", formats)
	}
	if len(r.ResultLike) != 0 {
		t.Fatalf("results/ holds runs and is not result-like without a record: %v", r.ResultLike)
	}
}

// countingFS records which paths the scanner read or statted.
type countingFS struct {
	FS
	mu      sync.Mutex
	touched []string
	listed  []string
}

func (c *countingFS) ReadDir(rel string) ([]Entry, error) {
	c.mu.Lock()
	c.listed = append(c.listed, rel)
	c.mu.Unlock()
	return c.FS.ReadDir(rel)
}
func (c *countingFS) Stat(rel string) (Entry, error) {
	c.mu.Lock()
	c.touched = append(c.touched, rel)
	c.mu.Unlock()
	return c.FS.Stat(rel)
}
func (c *countingFS) ReadFile(rel string, max int64) ([]byte, bool, error) {
	c.mu.Lock()
	c.touched = append(c.touched, rel)
	c.mu.Unlock()
	return c.FS.ReadFile(rel, max)
}

func TestUntidyProject(t *testing.T) {
	root := canonical(t)
	write(t, root, "results_v3/README.md", "notes")
	write(t, root, "H1299 images/a.tif", "x")
	write(t, root, "H1299 images/b.tif", "x")
	write(t, root, "backup.zip", strings.Repeat("z", 1<<20))
	write(t, root, "blind_set_key/key_DO_NOT_OPEN.csv", "secret")
	write(t, root, "blind_set_key/other.csv", "secret")
	write(t, root, "images/photo_DO_NOT_OPEN_yet.tif", "secret")
	write(t, root, "notes/DO_NOT_OPEN/inner.fits", "secret")
	if err := os.Symlink("/etc", filepath.Join(root, "out_link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "gone"), filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("H1299 images", filepath.Join(root, "inner_link")); err != nil {
		t.Fatal(err)
	}
	c := &countingFS{FS: LocalFS{root}}
	r := scan(t, c, Options{})

	eq(t, "result-like", r.ResultLike, []string{"results_v3"})
	if len(r.Runs) != 0 {
		t.Fatalf("a folder with no run record is not a run: %+v", r.Runs)
	}
	eq(t, "data", paths(r.Data), []string{"H1299 images/a.tif", "H1299 images/b.tif"})
	if r.Skipped.ArchiveCount != 1 || r.Skipped.ArchiveBytes != 1<<20 || r.Skipped.Archives[0] != "backup.zip" {
		t.Fatalf("archives: %+v", r.Skipped)
	}
	if r.Git.Present {
		t.Fatalf("no .git here")
	}
	// the blinding key, and anything marked as not to be opened, is never read, statted or even listed
	for _, p := range c.touched {
		if strings.Contains(p, "blind_set_key") || strings.Contains(strings.ToLower(p), "do_not_open") {
			t.Fatalf("touched %q", p)
		}
	}
	for _, p := range c.listed {
		if strings.Contains(p, "blind_set_key") || strings.Contains(strings.ToLower(p), "do_not_open") {
			t.Fatalf("listed %q", p)
		}
	}
	eq(t, "never read", r.Skipped.NeverRead, []string{"blind_set_key", "images/photo_DO_NOT_OPEN_yet.tif", "notes/DO_NOT_OPEN"})
	for _, d := range r.Data {
		if strings.Contains(d.Path, "DO_NOT_OPEN") {
			t.Fatalf("listed: %s", d.Path)
		}
	}
	// links are reported, never followed
	states := map[string]string{}
	for _, l := range r.Links {
		states[l.Path] = l.State
	}
	if states["out_link"] != "outside" || states["dangling"] != "dangling" || states["inner_link"] != "inside" {
		t.Fatalf("links: %v", states)
	}
	for _, p := range c.listed {
		if p == "out_link" || p == "inner_link" {
			t.Fatalf("followed a link: %s", p)
		}
	}
}

func TestNestedRunInnerWins(t *testing.T) {
	root := canonical(t)
	write(t, root, "outer/run.json", `{"params":{"a":1}}`)
	write(t, root, "outer/out.fits", "x")
	write(t, root, "outer/sub/run_metadata.toml", vgtMeta)
	write(t, root, "outer/sub/psi.fits", "x")
	r := scan(t, LocalFS{root}, Options{})
	outer, inner := runByPath(r, "outer"), runByPath(r, "outer/sub")
	if outer == nil || inner == nil {
		t.Fatalf("runs: %+v", r.Runs)
	}
	if inner.OutputCount != 1 || outer.OutputCount != 1 {
		t.Fatalf("outputs outer=%d inner=%d: the inner run's files belong to it alone", outer.OutputCount, inner.OutputCount)
	}
	if len(outer.Notes) == 0 || !strings.Contains(outer.Notes[0], "outer/sub") || len(inner.Notes) == 0 {
		t.Fatalf("notes: %v %v", outer.Notes, inner.Notes)
	}
}

func TestManualRunsAndLayout(t *testing.T) {
	root := canonical(t)
	write(t, root, "weird/attempt1/log.txt", "x")
	write(t, root, "weird/attempt1/out.fits", "x")
	write(t, root, "sweeps/s1/a.txt", "x")
	write(t, root, "sweeps/s2/a.txt", "x")
	write(t, root, "tables/t.csv", "a")
	write(t, root, "elsewhere/conf/a.toml", "a=1")
	write(t, root, "elsewhere/batch/x.sh", "echo")
	opt := Options{
		ManualRuns: []ManualRun{{Path: "weird/attempt1", Name: "first try", Commit: "feedbee"}},
		Layout:     Layout{Runs: []string{"sweeps"}, Data: []string{"tables"}, Configs: []string{"elsewhere/conf"}, Jobs: []string{"elsewhere/batch"}},
	}
	r := scan(t, LocalFS{root}, opt)
	a := runByPath(r, "weird/attempt1")
	if a == nil || a.RecordKind != "manual" || a.Record != "" {
		t.Fatalf("manual run: %+v", a)
	}
	var f Field
	for _, x := range a.Fields {
		if x.Key == "commit" {
			f = x
		}
	}
	if f.Value != "feedbee" || f.Status != "you" || f.Source != "project.toml" {
		t.Fatalf("manual fields are declared by you: %+v", a.Fields)
	}
	if runByPath(r, "sweeps/s1") == nil || runByPath(r, "sweeps/s2") == nil {
		t.Fatalf("layout runs: %+v", r.Runs)
	}
	eq(t, "layout data", paths(r.Data), []string{"tables/t.csv"})
	eq(t, "layout configs", paths(r.Configs), []string{"elsewhere/conf/a.toml"})
	eq(t, "layout jobs", paths(r.Jobs), []string{"elsewhere/batch/x.sh"})
	if len(r.ResultLike) != 0 {
		t.Fatalf("%v", r.ResultLike)
	}
}

func TestDataRolesNeverSetMeaning(t *testing.T) {
	root := canonical(t)
	write(t, root, "data/raw/a.fits", "x")
	write(t, root, "data/processed/b.fits", "x")
	write(t, root, "data/external/c.fits", "x")
	r := scan(t, LocalFS{root}, Options{})
	got := map[string]File{}
	for _, d := range r.Data {
		got[d.Path] = d
	}
	if got["data/raw/a.fits"].Role != "raw" || !got["data/raw/a.fits"].ReadOnly || !got["data/external/c.fits"].ReadOnly {
		t.Fatalf("%+v", got)
	}
	if got["data/processed/b.fits"].Role != "processed" || got["data/processed/b.fits"].ReadOnly {
		t.Fatalf("%+v", got)
	}
}

func TestBudgetsTruncate(t *testing.T) {
	root := canonical(t)
	for i := 0; i < 30; i++ {
		write(t, root, "d/"+string(rune('a'+i%26))+string(rune('a'+i/26))+".fits", "x")
	}
	r := scan(t, LocalFS{root}, Options{MaxEntries: 10})
	if !r.Truncated || !strings.Contains(r.TruncatedWhy, "entries") {
		t.Fatalf("%+v", r)
	}
	// time budget through an injected clock
	var n int
	base := time.Unix(0, 0)
	r = scan(t, LocalFS{root}, Options{MaxDuration: time.Second, Now: func() time.Time { n++; return base.Add(time.Duration(n) * 400 * time.Millisecond) }})
	if !r.Truncated || !strings.Contains(r.TruncatedWhy, "time") {
		t.Fatalf("%+v", r)
	}
}

func TestCancel(t *testing.T) {
	root := vgtLike(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := Scan(ctx, LocalFS{root}, Options{})
	if !r.Canceled || len(r.Runs) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestResultLikeFolderInsideAnotherIsOneItem(t *testing.T) {
	root := canonical(t)
	write(t, root, "results/old/results_a/x.txt", "x")
	write(t, root, "src/main.py", "x")
	r := scan(t, LocalFS{root}, Options{})
	eq(t, "result-like", r.ResultLike, []string{"results"})
}

func TestLocalFSRefusesEscapes(t *testing.T) {
	root := canonical(t)
	outside := canonical(t)
	write(t, outside, "secret.txt", "s")
	if err := os.Symlink(outside, filepath.Join(root, "door")); err != nil {
		t.Fatal(err)
	}
	fs := LocalFS{root}
	for _, rel := range []string{"../x", "/etc/passwd", "a/../../x"} {
		if _, _, err := fs.ReadFile(rel, 10); err == nil {
			t.Fatalf("read %q", rel)
		}
	}
	if _, _, err := fs.ReadFile("door/secret.txt", 10); err == nil { // through a link that leaves the folder
		t.Fatal("read through an outside link")
	}
	write(t, root, "big.txt", strings.Repeat("a", 100))
	d, trunc, err := fs.ReadFile("big.txt", 10)
	if err != nil || !trunc || len(d) != 10 {
		t.Fatalf("%v %v %d", err, trunc, len(d))
	}
}

func TestMatchGlob(t *testing.T) {
	for _, c := range []struct {
		g, p string
		want bool
	}{
		{"old/**", "old", true}, {"old/**", "old/x/y", true}, {"old/**", "older", false},
		{"*.log", "a/b/c.log", true}, {"**/tmp", "x/tmp", true}, {"data/raw", "data/raw", true}, {"data/raw", "data/rawer", false},
	} {
		if MatchGlob(c.g, c.p) != c.want {
			t.Fatalf("%s vs %s", c.g, c.p)
		}
	}
}

func TestProjectFileRoundTrip(t *testing.T) {
	text := "# mine\n[project]\nname = \"x\"\n[layout]\nskip = [\"old/**\"]\n"
	out, err := Append([]byte(text), AnswerBlock(Answer{Need: "host-repo:cluster", Answer: "declare", Value: "/home/me/vgt", DeclaredBy: "you", Date: "2026-11-05"}))
	if err != nil {
		t.Fatal(err)
	}
	out, err = Append(out, ManualRunBlock("results_v3"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseProjectFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "# mine\n") || p.HostRepo("cluster") != "/home/me/vgt" || len(p.ManualRuns()) != 1 || p.ScanLayout().Skip[0] != "old/**" {
		t.Fatalf("%s %+v", out, p)
	}
	// an append that would break the file is refused and nothing is written
	if _, err := Append([]byte("[runs]\nmanual = [\"x\"]\n"), ManualRunBlock("a")); err == nil {
		t.Fatal("expected a refusal")
	}
	if _, err := ParseProjectFile([]byte("not = = toml")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestProposalKeepsDiscoveredAsComments(t *testing.T) {
	root := vgtLike(t)
	r := scan(t, LocalFS{root}, Options{})
	text := ProposedProject("vgt", r)
	if !strings.Contains(text, "# found: data data/exp_raw, data/sims") || !strings.Contains(text, "data = []") {
		t.Fatalf("%s", text)
	}
	if _, err := ParseProjectFile([]byte(text)); err != nil {
		t.Fatal(err)
	}
}

// A ROHSA fit folder is a run whose record is its run trace: settings are declared by it, paths of the machine it ran on are not shown.
func TestROHSATraceIsARunRecord(t *testing.T) {
	root := canonical(t)
	write(t, root, "full/rohsa_n4_run_trace.toml", "n_gauss = 4\nregion = \"full\"\nstatus = \"ran\"\nrohsa_output = \"a/b/rohsa_n4.dat\"\n[config]\nlambda_amp = 10.0\nconfig_path = \"/x/y.toml\"\n")
	write(t, root, "full/rohsa_n4.dat", "# i, j, A, mean, sigma\n")
	r := scan(t, LocalFS{root}, Options{})
	run := runByPath(r, "full")
	if run == nil || run.RecordKind != "rohsa" || run.Record != "rohsa_n4_run_trace.toml" {
		t.Fatalf("run: %+v", run)
	}
	got := map[string]string{}
	for _, s := range run.Settings {
		got[s.Key] = s.Value
	}
	if got["lambda_amp"] != "10" || got["n_gauss"] != "4" {
		t.Fatalf("settings: %v", got)
	}
	for k, v := range got {
		if strings.Contains(v, "/") || k == "rohsa_output" || k == "config_path" {
			t.Fatalf("a location leaked: %s=%s", k, v)
		}
	}
}

func TestToolEnvironmentsAreSkippedUnderAnyName(t *testing.T) {
	root := canonical(t)
	write(t, root, "data/raw/a.fits", "x")
	write(t, root, ".venv-irgetas-py/pyvenv.cfg", "home = /usr/bin")
	write(t, root, ".venv-irgetas-py/lib/python3.9/site-packages/astropy/tests/data/b.fits", "x")
	write(t, root, "envs/conda1/conda-meta/history", "x")
	write(t, root, "envs/conda1/lib/c.fits", "x")
	write(t, root, ".julia-irgetas/registries/General/Registry.toml", "x")
	write(t, root, ".julia-irgetas/packages/Foo/d.fits", "x")
	write(t, root, "depot/registries/r", "x") // a depot under a plain name, recognised by its two folders
	write(t, root, "depot/packages/Foo/e.fits", "x")
	write(t, root, "half/packages/f.fits", "x") // packages/ alone is not a depot: it is data
	write(t, root, "src/site-packages/g.fits", "x")
	write(t, root, ".julia/h.fits", "x")
	r := scan(t, LocalFS{Root: root}, Options{MaxEntries: 10000, MaxDuration: time.Minute})
	got := paths(r.Data)
	sort.Strings(got)
	eq(t, "data walked", got, []string{"data/raw/a.fits", "half/packages/f.fits"})
	if r.Skipped.ToolEnvs != 6 {
		t.Fatalf("tool environments: %d, want 6 (venv, conda, .julia-*, depot, site-packages, .julia)", r.Skipped.ToolEnvs)
	}
}

// A host's worker recognises an environment from its listing and reports it as a leaf with the env flag.
func TestWorkerFlaggedToolEnvironmentIsCounted(t *testing.T) {
	m := NewMemFS(nil)
	m.Add("a.fits", Entry{Size: 1})
	m.Add("env1", Entry{Dir: true, ToolEnv: true})
	r := scan(t, m, Options{MaxEntries: 100, MaxDuration: time.Minute})
	if r.Skipped.ToolEnvs != 1 || len(r.Skipped.Dirs) != 0 {
		t.Fatalf("skipped: %+v", r.Skipped)
	}
}
