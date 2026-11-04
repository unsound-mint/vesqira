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

	switch {
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
	add("environment", "Software environment", "unresolved", "Language and package versions are not recorded in the run metadata.")
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
