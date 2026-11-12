package runs

import (
	"strings"
	"testing"
)

const goodEnv = `{"record":"vesqira-env","version":1,"slurm_job_id":"42","facts":{
 "code.commit":{"status":"verified","value":"0123456789abcdef0123456789abcdef01234567"},
 "code.dirty":{"status":"verified","value":"no"},
 "julia.version":{"status":"verified","value":"julia version 1.10.0"},
 "python.version":{"status":"unresolved","reason":"the job template does not run Python"},
 "node.name":{"status":"verified","value":"node01"},
 "mystery":{"status":"verified","value":"x"}}}`

func envByKey(fs []EnvFact) map[string]EnvFact {
	m := map[string]EnvFact{}
	for _, f := range fs {
		m[f.Key] = f
	}
	return m
}

func TestParseEnvKeepsLabelsReasonsAndDropsWhatItDoesNotKnow(t *testing.T) {
	fs := ParseEnv([]byte(goodEnv), "42")
	if len(fs) != len(EnvKeys) {
		t.Fatalf("%d facts, want one per known key (%d)", len(fs), len(EnvKeys))
	}
	m := envByKey(fs)
	if f := m["code.commit"]; f.Status != EnvVerified || f.Value != "0123456789abcdef0123456789abcdef01234567" || f.Label != "Code commit" {
		t.Errorf("commit: %+v", f)
	}
	if f := m["python.version"]; f.Status != EnvUnresolved || f.Value != "" || f.Reason != "the job template does not run Python" {
		t.Errorf("a fact the job could not read keeps its reason: %+v", f)
	}
	if f := m["modules.sha256"]; f.Status != EnvUnresolved || f.Reason != "the job did not report it" {
		t.Errorf("a key the job left out is unresolved, never defaulted: %+v", f)
	}
	if f := m["slurm.job_id"]; f.Status != EnvVerified || f.Value != "42" {
		t.Errorf("job id: %+v", f)
	}
	if _, ok := m["mystery"]; ok {
		t.Error("an unknown key was kept")
	}
}

func TestParseEnvDoesNotTrustAFileThatIsNotThisJobsOrNotWellFormed(t *testing.T) {
	for name, c := range map[string]struct{ data, id string }{
		"another job":  {goodEnv, "43"},
		"no job id":    {goodEnv, ""},
		"not json":     {"{nope", "42"},
		"wrong record": {`{"record":"something","slurm_job_id":"42","facts":{}}`, "42"},
		"too large":    {goodEnv + strings.Repeat(" ", MaxEnvBytes), "42"},
	} {
		for _, f := range ParseEnv([]byte(c.data), c.id) {
			if f.Status != EnvUnresolved || f.Value != "" || f.Reason == "" {
				t.Fatalf("%s: %+v", name, f)
			}
		}
	}
}

func TestParseEnvRejectsValuesOutsideTheirForm(t *testing.T) {
	bad := `{"record":"vesqira-env","slurm_job_id":"1","facts":{
	 "code.commit":{"status":"verified","value":"main; rm -rf ~"},
	 "code.dirty":{"status":"verified","value":"maybe"},
	 "julia.manifest_sha256":{"status":"verified","value":"abc"},
	 "node.name":{"status":"verified","value":"node\"01"},
	 "input.size_bytes":{"status":"verified","value":"-5"},
	 "cpu.model":{"status":"unresolved","reason":"<script>alert(1)</script>"},
	 "modules.list":{"status":"sure","value":"x"}}}`
	m := envByKey(ParseEnv([]byte(bad), "1"))
	for _, k := range []string{"code.commit", "code.dirty", "julia.manifest_sha256", "node.name", "input.size_bytes", "modules.list"} {
		if m[k].Status != EnvUnresolved || m[k].Value != "" {
			t.Errorf("%s was trusted: %+v", k, m[k])
		}
	}
	if r := m["cpu.model"].Reason; strings.ContainsAny(r, "<>") {
		t.Errorf("markup in a reason: %q", r)
	}
}

func TestParseEnvLinesReadsTheProbe(t *testing.T) {
	fs := ParseEnvLines("code.commit|verified|0123456789abcdef0123456789abcdef01234567\ncode.dirty|unresolved|git status failed\nbanner text\nunknown.key|verified|x\n")
	if len(fs) != 2 || fs[0].Status != EnvVerified || fs[1].Status != EnvUnresolved || fs[1].Reason != "git status failed" {
		t.Fatalf("%+v", fs)
	}
}

func TestCompareEnvNeverCallsAFactMatchedUnlessBothSidesObservedIt(t *testing.T) {
	a := ParseEnv([]byte(goodEnv), "42")
	b := ParseEnv([]byte(strings.ReplaceAll(strings.ReplaceAll(goodEnv, "0123456789abcdef0123456789abcdef01234567", "ffffffffffffffffffffffffffffffffffffffff"), "node01", "node02")), "42")
	m := map[string]EnvDifference{}
	for _, d := range CompareEnv(a, b) {
		m[d.Key] = d
	}
	if d := m["code.commit"]; d.Result != "differed" || !d.Blocking {
		t.Errorf("commit: %+v", d)
	}
	if d := m["code.dirty"]; d.Result != "matched" {
		t.Errorf("dirty: %+v", d)
	}
	if d := m["node.name"]; d.Result != "differed" || d.Blocking {
		t.Errorf("a different node is shown but never blocks: %+v", d)
	}
	if d := m["julia.manifest_sha256"]; d.Result != "unknown" || d.Why != "neither side recorded it" {
		t.Errorf("not recorded on either side: %+v", d)
	}
	if d := m["python.version"]; d.Result != "unknown" {
		t.Errorf("unresolved on both sides is unknown, not matched: %+v", d)
	}
	if _, ok := m["slurm.job_id"]; ok {
		t.Error("the job id differs by construction and is not compared")
	}
	only := ParseEnv([]byte(strings.Replace(goodEnv, `"julia.version":{"status":"verified","value":"julia version 1.10.0"},`, "", 1)), "42")
	for _, d := range CompareEnv(a, only) {
		if d.Key == "julia.version" && (d.Result != "unknown" || !strings.Contains(d.Why, "not observed")) {
			t.Errorf("one side did not observe it: %+v", d)
		}
	}
}

func TestReproducibilityLabelsWhatTheJobObservedVerifiedAndKeepsTheDeclaredCommitBeside(t *testing.T) {
	r := Run{GitCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Environment: ParseEnv([]byte(goodEnv), "42")}
	facts := map[string]Fact{}
	for _, f := range Reproducibility(r, false) {
		facts[f.Key] = f
	}
	code := facts["code"]
	if code.Status != EnvVerified || !strings.Contains(code.Detail, "0123456789ab") || !strings.Contains(code.Detail, "declares commit aaaaaaaaaaaa, which differs") {
		t.Errorf("code: %+v", code)
	}
	if f := facts["environment"]; f.Status != EnvVerified || !strings.Contains(f.Detail, "Julia version julia version 1.10.0") || !strings.Contains(f.Detail, "Not observed") {
		t.Errorf("environment: %+v", f)
	}
	if f := facts["input_file"]; f.Status != EnvUnresolved {
		t.Errorf("an input file nobody observed is unresolved: %+v", f)
	}
	// a run no job observed keeps the earlier wording and says nothing is verified
	plain := map[string]Fact{}
	for _, f := range Reproducibility(Run{GitCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, false) {
		plain[f.Key] = f
	}
	if plain["code"].Status != "declared" || plain["environment"].Status != "unresolved" {
		t.Errorf("without an environment: %+v %+v", plain["code"], plain["environment"])
	}
}

func TestControlsAreNotEmptyUntilSomethingIsStated(t *testing.T) {
	var c *Controls
	if !c.Empty() || !(&Controls{}).Empty() {
		t.Error("no block means nothing stated")
	}
	if (&Controls{Noise: &NoiseModel{Kind: "other"}}).Empty() {
		t.Error("a stated block is not empty")
	}
}

// A job that hashed its input reports input.full_sha256: a verified 64-hex value is kept, anything else is unresolved, and two
// runs whose inputs differ show it as a difference a strict rerun would not accept.
func TestInputFullHashIsAFactTheRunKeeps(t *testing.T) {
	good, other := strings.Repeat("a", 64), strings.Repeat("b", 64)
	env := func(status, v string) []EnvFact {
		b := `{"record":"vesqira-env","version":1,"slurm_job_id":"42","facts":{"input.full_sha256":{"status":"` + status + `","` + map[string]string{"verified": "value", "unresolved": "reason"}[status] + `":"` + v + `"}}}`
		return ParseEnv([]byte(b), "42")
	}
	if v, ok := EnvValue(env("verified", good), "input.full_sha256"); !ok || v != good {
		t.Errorf("verified hash lost: %q %v", v, ok)
	}
	if _, ok := EnvValue(env("verified", "not-a-hash"), "input.full_sha256"); ok {
		t.Error("a malformed hash was trusted")
	}
	if _, ok := EnvValue(env("unresolved", "the input file changed while it was being read"), "input.full_sha256"); ok {
		t.Error("an unresolved fact has a value")
	}
	for _, d := range CompareEnv(env("verified", good), env("verified", other)) {
		if d.Key == "input.full_sha256" {
			if d.Result != "differed" || !d.Blocking {
				t.Errorf("%+v", d)
			}
			return
		}
	}
	t.Error("the full hash is not compared between runs")
}
