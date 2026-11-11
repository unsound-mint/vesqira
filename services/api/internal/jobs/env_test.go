package jobs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// The preamble only reads. This pins it on the text (the commands that appear) and, below, on the effect (a real checkout is
// left byte for byte as it was).
func TestPreambleOnlyReadsTheCodeFolder(t *testing.T) {
	p := EnvPreamble(EnvSpec{RepoDir: "/r/vgt", OutputDir: "/r/out", InputFile: "/r/in.fits", ExpectCommit: strings.Repeat("a", 40)})
	probe, _, err := EnvProbe("/r/vgt", "/r/in.fits")
	if err != nil || strings.ContainsAny(probe, "'\"\\!;|&$`\n") {
		t.Fatalf("probe command %q (%v) must be plain words", probe, err)
	}
	banned := regexp.MustCompile(`\bgit\s+(-\S+\s+(\S+\s+)?)*(commit|add|stash|checkout|fetch|pull|push|reset|clean|config|gc|merge|rebase|apply|am|tag|branch|remote|submodule|worktree|restore|switch|rm|mv|init|clone|update-index|maintenance|prune|repack)\b`)
	if m := banned.FindString(p); m != "" {
		t.Errorf("the preamble runs a git command that can write: %q", m)
	}
	call := regexp.MustCompile(`(?:ve_t |\$\()git (\S+) (\S+) (\S+) (\S+)`)
	calls := call.FindAllStringSubmatch(p, -1)
	if len(calls) < 5 {
		t.Fatalf("expected the collector's and the strict guard's git calls, found %d", len(calls))
	}
	for _, c := range calls {
		if c[1] != "--no-optional-locks" || c[2] != "-C" || (c[4] != "rev-parse" && c[4] != "status" && c[4] != "diff") {
			t.Errorf("git call outside the allowed read-only forms: %q", c[0])
		}
	}
	for _, bad := range []string{"git diff HEAD |", "rm ", "mv ", "tee ", ">> ", "chmod", "ln -s", "mkdir"} {
		if strings.Contains(strings.ReplaceAll(p, "--no-ext-diff --no-textconv HEAD", ""), bad) && bad != "git diff HEAD |" {
			t.Errorf("the preamble contains %q", bad)
		}
	}
	// every diff is taken without external drivers (a configured diff.external would run a program)
	if strings.Contains(p, "diff HEAD") && !strings.Contains(p, "diff --no-ext-diff --no-textconv HEAD") {
		t.Error("git diff must run with --no-ext-diff --no-textconv")
	}
	// every reason the shell can print is in the character set the reader accepts, or it would be dropped
	for _, m := range regexp.MustCompile(`ve_no(?:_all)? "([^"]+)"`).FindAllStringSubmatch(envCollector, -1) {
		if !strings.HasPrefix(m[1], "$") && !regexp.MustCompile(`^[A-Za-z0-9 ._:/+=,@%()-]{1,200}$`).MatchString(m[1]) {
			t.Errorf("reason %q has characters the reader drops", m[1])
		}
	}
}

// Every key the collector can emit is one the reader knows, and the other way round (except the job id, which the reader takes
// from the file's own header).
func TestCollectorAndReaderAgreeOnKeys(t *testing.T) {
	emitted := map[string]bool{}
	for _, m := range regexp.MustCompile(`ve_(?:ok|no) ([a-z_.0-9]+)|ve_no_all "[^"]+" ((?:[a-z_.0-9]+ ?)+)`).FindAllStringSubmatch(envCollector, -1) {
		if m[1] != "" {
			emitted[m[1]] = true
		}
		for _, k := range strings.Fields(m[2]) {
			emitted[k] = true
		}
	}
	for _, k := range runs.EnvKeys {
		if k.Key != "slurm.job_id" && !emitted[k.Key] {
			t.Errorf("the reader knows %s but the collector never emits it", k.Key)
		}
		delete(emitted, k.Key)
	}
	for k := range emitted {
		t.Errorf("the collector emits %s, which the reader would drop", k)
	}
}

func run(t *testing.T, dir string, env []string, script string) (string, string, int) {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return so.String(), se.String(), code
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type fixture struct{ repo, out, input, jobdir, commit string }

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{repo: filepath.Join(root, "vgt"), out: filepath.Join(root, "out"), input: filepath.Join(root, "in.fits"), jobdir: filepath.Join(root, "job")}
	for _, d := range []string{f.repo, f.out, f.jobdir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	must := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(f.repo, "main.jl"), "println(1)\n")
	must(filepath.Join(f.repo, "Manifest.toml"), "julia_version = \"1.10\"\n")
	must(f.input, strings.Repeat("x", 2880))
	gitIn(t, f.repo, "init", "-q")
	gitIn(t, f.repo, "add", ".")
	gitIn(t, f.repo, "commit", "-q", "-m", "one")
	f.commit = gitIn(t, f.repo, "rev-parse", "HEAD")
	return f
}

func (f fixture) script(e EnvSpec, after string) string {
	return "set -euo pipefail\njobdir=" + f.jobdir + "\n" + EnvPreamble(e) + after
}

func (f fixture) spec() EnvSpec {
	return EnvSpec{RepoDir: f.repo, OutputDir: f.out, InputFile: f.input}
}

func factOf(t *testing.T, facts []runs.EnvFact, key string) runs.EnvFact {
	t.Helper()
	for _, x := range facts {
		if x.Key == key {
			return x
		}
	}
	t.Fatalf("no fact %s", key)
	return runs.EnvFact{}
}

func TestPreambleRecordsVerifiedFactsAndLeavesTheCheckoutAlone(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.repo, "scratch.txt"), []byte("untracked"), 0o644); err != nil { // an untracked file would make a plain git status refresh the index
		t.Fatal(err)
	}
	treeBefore := gitIn(t, f.repo, "status", "--porcelain") // a plain status refreshes the index: do it first, so the comparison below sees only what the preamble does
	idx := filepath.Join(f.repo, ".git", "index")
	before, _ := os.ReadFile(idx)
	stBefore, _ := os.Stat(idx)
	so, se, code := run(t, f.repo, []string{"SLURM_JOB_ID=4242"}, f.script(f.spec(), "echo SCIENCE\n"))
	if code != 0 || !strings.Contains(so, "SCIENCE") {
		t.Fatalf("exit %d, out %q, err %q", code, so, se)
	}
	after, _ := os.ReadFile(idx)
	stAfter, _ := os.Stat(idx)
	if !bytes.Equal(before, after) || !stBefore.ModTime().Equal(stAfter.ModTime()) {
		t.Error("the preamble changed .git/index")
	}
	if gitIn(t, f.repo, "status", "--porcelain") != treeBefore || gitIn(t, f.repo, "rev-parse", "HEAD") != f.commit {
		t.Error("the preamble changed the checkout")
	}
	data, err := os.ReadFile(filepath.Join(f.out, runs.EnvFile))
	if err != nil {
		t.Fatalf("no record next to the outputs: %v", err)
	}
	if len(data) > 6000 {
		t.Errorf("the record is %d bytes", len(data))
	}
	facts := runs.ParseEnv(data, "4242")
	sum := sha256.Sum256([]byte("julia_version = \"1.10\"\n"))
	want := map[string]string{"code.commit": f.commit, "code.dirty": "no", "code.diff_sha256": "none", "code.untracked_files": "1",
		"julia.manifest_sha256": hex.EncodeToString(sum[:]), "input.size_bytes": "2880", "slurm.job_id": "4242"}
	for k, v := range want {
		if x := factOf(t, facts, k); x.Status != runs.EnvVerified || x.Value != v {
			t.Errorf("%s = %+v, want verified %q", k, x, v)
		}
	}
	if x := factOf(t, facts, "node.name"); x.Status != runs.EnvVerified || x.Value == "" {
		t.Errorf("node: %+v", x)
	}
	if x := factOf(t, facts, "python.version"); x.Status != runs.EnvUnresolved || !strings.Contains(x.Reason, "does not run Python") {
		t.Errorf("python: %+v", x)
	}
	// the same file read for another job is not used
	for _, x := range runs.ParseEnv(data, "9999") {
		if x.Status != runs.EnvUnresolved || !strings.Contains(x.Reason, "another job") {
			t.Fatalf("a record from another job was used: %+v", x)
		}
	}
}

func TestPreambleReportsUncommittedChangesWithAHashOfThem(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.repo, "main.jl"), []byte("println(2)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, se, code := run(t, f.repo, []string{"SLURM_JOB_ID=7"}, f.script(f.spec(), "")); code != 0 {
		t.Fatal(se)
	}
	data, _ := os.ReadFile(filepath.Join(f.out, runs.EnvFile))
	facts := runs.ParseEnv(data, "7")
	h := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if d := factOf(t, facts, "code.dirty"); d.Value != "yes" || d.Status != runs.EnvVerified {
		t.Errorf("dirty: %+v", d)
	}
	if d := factOf(t, facts, "code.diff_sha256"); d.Status != runs.EnvVerified || !h.MatchString(d.Value) {
		t.Errorf("diff hash: %+v", d)
	}
}

func TestFactsThatCannotBeReadStayUnresolvedWithReasonsAndTheJobGoesOn(t *testing.T) {
	f := newFixture(t)
	notRepo := t.TempDir()
	e := EnvSpec{RepoDir: notRepo, OutputDir: f.out, InputFile: filepath.Join(notRepo, "missing.fits")}
	so, se, code := run(t, notRepo, []string{"SLURM_JOB_ID=5"}, f.script(e, "echo SCIENCE\n"))
	if code != 0 || !strings.Contains(so, "SCIENCE") {
		t.Fatalf("a failing preamble must not fail the job: exit %d %q %q", code, so, se)
	}
	data, _ := os.ReadFile(filepath.Join(f.out, runs.EnvFile))
	facts := runs.ParseEnv(data, "5")
	for key, why := range map[string]string{"code.commit": "not a repository", "julia.manifest_sha256": "no Manifest.toml", "input.size_bytes": "not readable"} {
		if x := factOf(t, facts, key); x.Status != runs.EnvUnresolved || !strings.Contains(x.Reason, why) {
			t.Errorf("%s: %+v, want unresolved containing %q", key, x, why)
		}
	}
	// a path the script cannot use safely is left out, with the reason, instead of being interpolated
	so, _, code = run(t, notRepo, []string{"SLURM_JOB_ID=6"}, f.script(EnvSpec{RepoDir: f.repo, OutputDir: f.out, InputFile: "/x/$(touch " + notRepo + "/pwned)"}, "echo SCIENCE\n"))
	if code != 0 || !strings.Contains(so, "SCIENCE") {
		t.Fatal("job failed")
	}
	if _, err := os.Stat(filepath.Join(notRepo, "pwned")); err == nil {
		t.Fatal("a command in the input path ran")
	}
	data, _ = os.ReadFile(filepath.Join(f.out, runs.EnvFile))
	if x := factOf(t, runs.ParseEnv(data, "6"), "input.size_bytes"); x.Status != runs.EnvUnresolved || !strings.Contains(x.Reason, "plain absolute path") {
		t.Errorf("input: %+v", x)
	}
}

func TestRecordIsBoundedWhateverTheNodePrints(t *testing.T) {
	f := newFixture(t)
	huge := "module() { yes 'x/1.0 \"quoted\" \\back|pipe' | head -c 300000; }\nexport -f module\n"
	if _, se, code := run(t, f.repo, []string{"SLURM_JOB_ID=8"}, huge+f.script(f.spec(), "")); code != 0 {
		t.Fatal(se)
	}
	data, _ := os.ReadFile(filepath.Join(f.out, runs.EnvFile))
	if len(data) > 6000 {
		t.Fatalf("the record is %d bytes", len(data))
	}
	if x := factOf(t, runs.ParseEnv(data, "8"), "modules.list"); x.Status != runs.EnvVerified || len(x.Value) > 200 || strings.ContainsAny(x.Value, "\"\\|") {
		t.Errorf("modules.list: %+v", x)
	}
}

func TestStrictGuardStopsTheJobBeforeTheScienceWhenTheCodeMoved(t *testing.T) {
	f := newFixture(t)
	e := f.spec()
	e.ExpectCommit = f.commit
	if so, se, code := run(t, f.repo, []string{"SLURM_JOB_ID=1"}, f.script(e, "echo SCIENCE\n")); code != 0 || !strings.Contains(so, "SCIENCE") {
		t.Fatalf("same commit, clean: exit %d %q %q", code, so, se)
	}
	e.ExpectCommit = strings.Repeat("b", 40)
	so, se, code := run(t, f.repo, []string{"SLURM_JOB_ID=2"}, f.script(e, "echo SCIENCE\n"))
	if code != 3 || strings.Contains(so, "SCIENCE") || !strings.Contains(se, "strict rerun stopped") {
		t.Fatalf("other commit: exit %d %q %q", code, so, se)
	}
	if data, err := os.ReadFile(filepath.Join(f.out, runs.EnvFile)); err != nil || factOf(t, runs.ParseEnv(data, "2"), "code.commit").Value != f.commit {
		t.Error("the record of what was found must remain when the job stops")
	}
	if err := os.WriteFile(filepath.Join(f.repo, "main.jl"), []byte("println(3)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.ExpectCommit = f.commit
	so, se, code = run(t, f.repo, []string{"SLURM_JOB_ID=3"}, f.script(e, "echo SCIENCE\n"))
	if code != 3 || strings.Contains(so, "SCIENCE") || !strings.Contains(se, "tracked files") {
		t.Fatalf("changed tracked file: exit %d %q %q", code, so, se)
	}
}

// The record is copied again on exit: a pipeline that creates its output folder only after it starts still ends with the file.
func TestRecordIsCopiedWhenTheOutputFolderAppearsLater(t *testing.T) {
	f := newFixture(t)
	late := filepath.Join(filepath.Dir(f.out), "late")
	e := EnvSpec{RepoDir: f.repo, OutputDir: late, InputFile: f.input}
	if _, se, code := run(t, f.repo, []string{"SLURM_JOB_ID=9"}, f.script(e, "mkdir "+late+"\n")); code != 0 {
		t.Fatal(se)
	}
	if _, err := os.Stat(filepath.Join(late, runs.EnvFile)); err != nil {
		t.Fatalf("not copied on exit: %v", err)
	}
}

func TestEnvProbePrintsTheFactsWithoutAJob(t *testing.T) {
	f := newFixture(t)
	cmd, script, err := EnvProbe(f.repo, f.input)
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command("bash", "-s", "--", f.repo, f.input)
	c.Stdin = strings.NewReader(script)
	out, err := c.Output()
	if err != nil || !strings.HasPrefix(cmd, "bash -s -- ") {
		t.Fatalf("%v %q", err, cmd)
	}
	facts := runs.ParseEnvLines(string(out))
	if v, ok := runs.EnvValue(facts, "code.commit"); !ok || v != f.commit {
		t.Errorf("commit: %q %v", v, ok)
	}
	for _, x := range facts {
		if x.Key == "node.name" || x.Key == "julia.version" || x.Key == "modules.list" {
			t.Errorf("the probe reports %s, which depends on the job's own shell", x.Key)
		}
	}
	for _, bad := range [][2]string{{"relative/path", ""}, {"/a b", ""}, {f.repo, "/x;y"}} {
		if _, _, err := EnvProbe(bad[0], bad[1]); err == nil {
			t.Errorf("EnvProbe accepted %q %q", bad[0], bad[1])
		}
	}
}
