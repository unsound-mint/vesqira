package runs

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Fact is one thing a reader would need to rerun or judge a run, with how well it is known. Status words follow the
// project's provenance vocabulary: declared (the run's own metadata says so, not checked here), inferred (Vesqira
// matched it by a weak rule) and unresolved (nothing says; Vesqira does not guess).
type Fact struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

var seedKey = regexp.MustCompile(`(?i)(^|_)(seed|rng)($|_)`)
var attemptKey = regexp.MustCompile(`(?i)(attempt|evaluation|trial)s?$`)
var failureKey = regexp.MustCompile(`(?i)(fail|failure|failures)s?$`)

// Reproducibility lists what is and is not known about rerunning a run. It never turns an absent record into a
// reassuring one, and it says nothing about whether the science is right: that is the author's assessment.
func Reproducibility(r Run, inputInRegistry bool) []Fact {
	var out []Fact
	add := func(key, label, status, detail string) { out = append(out, Fact{key, label, status, detail}) }

	envCommit, haveEnvCommit := EnvValue(r.Environment, "code.commit")
	switch {
	case haveEnvCommit:
		add("code", "Code version", EnvVerified, envCodeDetail(r, envCommit))
	case r.GitCommit == "":
		add("code", "Code version", "unresolved", "The run does not record a commit.")
	case r.Dirty:
		add("code", "Code version", "declared", "Commit "+short(r.GitCommit)+" with uncommitted changes, so the code cannot be rebuilt from the commit alone.")
	default:
		add("code", "Code version", "declared", "Commit "+short(r.GitCommit)+", reported clean by the run. Not checked against the repository.")
	}
	if len(r.Params) > 0 {
		add("parameters", "Parameters", "declared", fmt.Sprintf("%d parameters recorded; places on any machine are never kept.", len(r.Params)))
	} else {
		add("parameters", "Parameters", "unresolved", "No parameters recorded.")
	}
	switch {
	case r.InputName == "":
		add("input", "Input data", "unresolved", "The run does not name its input.")
	case inputInRegistry:
		add("input", "Input data", "inferred", fmt.Sprintf("%s, %d bytes as declared; a registered file has the same name and size. Same name and size do not prove the same content: no hash is recorded.", r.InputName, r.InputSizeBytes))
	default:
		add("input", "Input data", "declared", fmt.Sprintf("%s, %d bytes as declared. No content hash is recorded.", r.InputName, r.InputSizeBytes))
	}
	if len(r.Environment) == 0 {
		add("environment", "Software environment", "unresolved", "Language and package versions are not recorded in the run metadata, and no job of this catalog observed them.")
	} else {
		out = append(out, envFacts(r.Environment)...)
	}
	seed := ""
	for _, p := range r.Params {
		if seedKey.MatchString(p.Key) {
			seed = p.Key + " = " + p.Value
			break
		}
	}
	if seed != "" {
		add("seed", "Random seed", "declared", seed)
	} else {
		add("seed", "Random seed", "unresolved", "No seed is recorded, so whether a rerun draws the same random numbers is unknown.")
	}
	if len(r.Outputs) > 0 {
		add("outputs", "Output files", "unresolved", fmt.Sprintf("%d files registered by reference; their checksums were not recorded, so a later change to them would not be noticed beyond size and modification time.", len(r.Outputs)))
	}
	add("equivalence", "What counts as the same result", "unresolved", "Not stated. Decide a tolerance before comparing a rerun.")

	// evaluations: only what the run itself declares
	var attempts, failures, haveA, haveF = 0.0, 0.0, false, false
	for k, v := range r.Diagnostics {
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			continue
		}
		if attemptKey.MatchString(k) {
			attempts, haveA = n, true
		} else if failureKey.MatchString(k) {
			failures, haveF = n, true
		}
	}
	switch {
	case haveA && haveF:
		add("evaluations", "Evaluations that failed", "declared", fmt.Sprintf("%g of %g attempts failed, as the run reports. Cancelled or excluded evaluations are not recorded.", failures, attempts))
	default:
		add("evaluations", "Evaluations that failed", "unresolved", "The run does not report how many evaluations failed, were cancelled or were excluded.")
	}
	return out
}

func short(c string) string {
	c = strings.TrimSuffix(c, "-dirty")
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// envCodeDetail says what the job saw of the code folder, and whether the run's own metadata agrees.
func envCodeDetail(r Run, commit string) string {
	d := "Commit " + short(commit)
	switch v, _ := EnvValue(r.Environment, "code.dirty"); v {
	case "no":
		d += ", no tracked file changed"
	case "yes":
		h, _ := EnvValue(r.Environment, "code.diff_sha256")
		d += ", tracked files changed since that commit (hash of the change " + short(h) + "), so the commit alone does not rebuild the code"
	default:
		d += "; whether tracked files had changed was not observed"
	}
	d += ". Observed by the job itself when it ran, from a file next to its outputs (the file is not signed)."
	declared := strings.TrimSuffix(r.GitCommit, "-dirty")
	if declared != "" && !strings.HasPrefix(commit, declared) && !strings.HasPrefix(declared, commit) {
		d += " The run's own metadata declares commit " + short(r.GitCommit) + ", which differs."
	}
	return d
}

// envFacts turns the observed environment into the facts a reader of the run page sees. A group is verified when at least one of
// its facts was observed; the others say why they were not.
func envFacts(env []EnvFact) []Fact {
	group := func(key, label string, keys ...string) Fact {
		var seen, miss []string
		for _, k := range keys {
			for _, f := range env {
				if f.Key != k {
					continue
				}
				if f.Status == EnvVerified {
					v := f.Value
					if len(v) == 64 {
						v = v[:12]
					}
					seen = append(seen, f.Label+" "+v)
				} else {
					miss = append(miss, f.Label+": "+f.Reason)
				}
			}
		}
		detail := strings.Join(seen, "; ")
		status := EnvUnresolved
		if len(seen) > 0 {
			status = EnvVerified
			detail += ". Observed by the job itself when it ran."
		}
		if len(miss) > 0 {
			if detail != "" {
				detail += " "
			}
			detail += "Not observed: " + strings.Join(miss, "; ") + "."
		}
		return Fact{key, label, status, detail}
	}
	return []Fact{
		group("environment", "Software environment", "julia.version", "julia.manifest_sha256", "python.version", "modules.sha256"),
		group("machine", "Machine", "node.name", "cpu.model"),
		group("input_file", "Input file as observed", inputKeys(env)...),
	}
}

// inputKeys are the facts about the input file the run page lists. The full hash is listed only for a run whose job tried to take
// it (verified, or unresolved with a reason of its own): most jobs do not hash their input, and "not reported" on every run would
// be noise.
func inputKeys(env []EnvFact) []string {
	keys := []string{"input.size_bytes", "input.mtime_epoch"}
	for _, f := range env {
		if f.Key == "input.full_sha256" && (f.Status == EnvVerified || f.Reason != "the job did not report it") {
			keys = append(keys, f.Key)
		}
	}
	return keys
}
