package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
)

// Golden scripts for the draft paths that go through the shared builder (ADR 0068): a new job, a sweep and a configuration run on
// another cube, for VGT and ROHSA. They were written from the code as it was before VGT drafts moved onto the generic builder and
// before sweeps and reruns could carry the profile pin, and they must not change for these inputs: the drafts are the same bytes.
// The output folder's random suffix and the host's temporary root are replaced by placeholders. Regenerate only with
// VESQIRA_UPDATE_GOLDEN=1, and only on purpose.

var outSuffixRe = regexp.MustCompile(`(vesqira_runs/[A-Za-z0-9._-]+)-[0-9a-f]{8}`)

func goldenDraftText(root, s string) string {
	if root != "" {
		s = strings.ReplaceAll(s, root, "<root>")
	}
	return outSuffixRe.ReplaceAllString(s, "$1-<suffix>")
}

func checkDraftGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden-drafts", name+".golden")
	if os.Getenv("VESQIRA_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (regenerate with VESQIRA_UPDATE_GOLDEN=1)", name, err)
	}
	if string(want) != got {
		t.Errorf("%s changed:\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

const goldenROHSAText = "[input]\ninput = \"/data/a.fits\"\noutput_dir = \"x\"\n[rohsa]\nn_gauss = [4]\nlambda_amp = 10.0\nlambda_mu = 10.0\nlambda_sig = 10.0\n"

func rohsaDraft(e *watchEnv, name string) jobs.Job {
	return e.draft(name, map[string]any{"template": "rohsa", "rohsa": map[string]any{"repoDir": "/shared/home/u/vgt", "configPath": "/shared/home/u/vgt/c.toml",
		"configText": goldenROHSAText, "cpus": 4, "mem": "8G", "time": "01:00:00"}})
}

func TestGoldenDraftScripts(t *testing.T) {
	e := newWatchEnv(t)
	text := func(s string) string { return goldenDraftText(e.remoteRoot, s) }

	// a new job
	checkDraftGolden(t, "new-vgt", text(e.draft("g-vgt", nil).Script))
	checkDraftGolden(t, "new-vgt-pinned", text(e.draft("g-vgtp", map[string]any{"vgt": map[string]any{
		"repoDir": "/shared/home/u/vgt", "configText": e.baseConfig, "cpus": 8, "mem": "16G", "time": "02:00:00", "set": map[string]string{"ker": "8"}, "pinProfileCommit": true}}).Script))
	checkDraftGolden(t, "new-rohsa", text(rohsaDraft(e, "g-rohsa").Script))

	// a sweep: every draft's script, in the order the grid makes them
	sweepScripts := func(sw sweepOut) string {
		var b strings.Builder
		for _, m := range sw.Jobs {
			b.WriteString("=== " + m.Label + "\n" + e.job(m.JobID).Script)
		}
		return text(b.String())
	}
	vgtBase := e.draft("vgt-base", nil)
	checkDraftGolden(t, "sweep-vgt", sweepScripts(e.createSweep(map[string]any{"name": "dnker", "baseJobId": vgtBase.ID, "params": []map[string]any{p("dn", "8", "16"), p("ker", "2", "4")}})))
	rohsaBase := rohsaDraft(e, "rohsa-base")
	checkDraftGolden(t, "sweep-rohsa-lambda", sweepScripts(e.createSweep(map[string]any{"name": "smooth", "baseJobId": rohsaBase.ID, "params": []map[string]any{p("lambda", "5", "10"), p("n_gauss", "3", "5")}})))
}

func TestGoldenRerunOnAnotherCube(t *testing.T) {
	t.Parallel()
	e := newRerunEnv(t)
	e.cube("/data/old.fits", e.host.ID, cubeHeader(256, 256, 40, 0.01, "K"))
	nw := e.cube("/data/new.fits", e.host.ID, cubeHeader(600, 600, 40, 0.01, "K"))
	for _, c := range []struct {
		name, template string
		body           map[string]any
	}{
		{"rerun-on-cube-vgt", "vgt", vgtBody(rerunVGTConfig, map[string]string{"ker": "6", "noise_mode": "channel"})},
		{"rerun-on-cube-rohsa", "rohsa", map[string]any{"rohsa": map[string]any{"repoDir": "/shared/vgt", "configText": rerunROHSAConfig, "set": map[string]string{"n_gauss": "5"}, "cpus": 8, "mem": "32G", "time": "04:00:00"}}},
	} {
		src, _ := e.recorded(c.template, c.body, "src-"+c.template)
		code, out := e.post(src.ID, map[string]any{"datasetId": nw.ID})
		if code != 201 {
			t.Fatalf("%s: %d %s", c.name, code, out)
		}
		var res struct {
			Job jobs.Job `json:"job"`
		}
		json.Unmarshal([]byte(out), &res)
		checkDraftGolden(t, c.name, goldenDraftText("", res.Job.Script))
	}
}
