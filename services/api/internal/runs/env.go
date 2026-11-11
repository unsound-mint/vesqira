package runs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// The environment a job saw when it ran (ADR 0054). The job's own script observes it with read-only commands and writes
// vesqira-env.json next to the outputs; the API reads that file when the run is imported. Everything here treats the file as
// untrusted input: only the listed keys are accepted, each value must match its own pattern, and a field that does not is
// unresolved with the reason, never repaired.

// EnvFile is the name of the file the job writes next to its outputs.
const EnvFile = "vesqira-env.json"

// MaxEnvBytes bounds what is read of EnvFile. A genuine file is under 4 kB.
const MaxEnvBytes = 16 << 10

// Status words of an environment fact. Only the job's own observation can be verified; a fact that could not be read stays
// unresolved and says why.
const (
	EnvVerified   = "verified"
	EnvUnresolved = "unresolved"
)

// EnvFact is one observed fact about where a run executed.
type EnvFact struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"` // verified | unresolved
	Value  string `json:"value"`  // empty when unresolved
	Reason string `json:"reason"` // why it is unresolved; empty when verified
}

var (
	hex64      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hexSHA     = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	digits     = regexp.MustCompile(`^[0-9]{1,15}$`)
	plain      = regexp.MustCompile(`^[A-Za-z0-9 ._:/+=,@%()-]{1,200}$`)
	yesNoRe    = regexp.MustCompile(`^(yes|no)$`)
	hashOrNone = regexp.MustCompile(`^([0-9a-f]{64}|none)$`)
)

type envKey struct {
	Key, Label string
	Valid      *regexp.Regexp
	// Compare says how the fact takes part in a rerun comparison: "strict" facts must match for a strict rerun, "context" facts
	// are shown as matched or differed but never block, and "" facts are not compared (they differ by construction).
	Compare string
}

// EnvKeys are the facts a job may report, in display order.
var EnvKeys = []envKey{
	{"code.commit", "Code commit", hexSHA, "strict"},
	{"code.dirty", "Tracked files changed since that commit", yesNoRe, "strict"},
	{"code.untracked_files", "Untracked files in the code folder", digits, ""},
	{"code.diff_sha256", "Hash of the uncommitted change (git diff HEAD, tracked files)", hashOrNone, "strict"},
	{"julia.version", "Julia version", plain, "context"},
	{"julia.manifest_sha256", "Hash of Manifest.toml in the code folder", hex64, "strict"},
	{"python.version", "Python version", plain, "context"},
	{"modules.list", "Loaded modules", plain, ""},
	{"modules.sha256", "Hash of the loaded modules list", hex64, "context"},
	{"node.name", "Node", plain, "context"},
	{"cpu.model", "CPU model", plain, "context"},
	{"input.size_bytes", "Input file size (bytes)", digits, "strict"},
	{"input.mtime_epoch", "Input file modification time (seconds since 1970)", digits, "strict"},
	{"slurm.job_id", "Slurm job id", digits, ""},
}

func envKeyOf(key string) (envKey, bool) {
	for _, k := range EnvKeys {
		if k.Key == key {
			return k, true
		}
	}
	return envKey{}, false
}

// UnresolvedEnv is every known fact, unresolved for one reason.
func UnresolvedEnv(reason string) []EnvFact {
	out := make([]EnvFact, 0, len(EnvKeys))
	for _, k := range EnvKeys {
		out = append(out, EnvFact{Key: k.Key, Label: k.Label, Status: EnvUnresolved, Reason: reason})
	}
	return out
}

// normalize checks one reported fact against its key. Anything off makes the fact unresolved with a reason.
func normalize(k envKey, status, value, reason string) EnvFact {
	f := EnvFact{Key: k.Key, Label: k.Label}
	switch {
	case status == EnvVerified && k.Valid.MatchString(value):
		f.Status, f.Value = EnvVerified, value
	case status == EnvVerified:
		f.Status, f.Reason = EnvUnresolved, "the reported value is not in the expected form, so it is not trusted"
	default:
		f.Status = EnvUnresolved
		f.Reason = strings.TrimSpace(reason)
		if f.Reason == "" || !plain.MatchString(f.Reason) {
			f.Reason = "the job did not say why"
		}
	}
	return f
}

// ParseEnv reads the vesqira-env.json a job wrote. The file counts only for the Slurm job it was written by: a file whose job
// id is not slurmID (copied, left over from another run, edited) makes every fact unresolved. Missing keys are unresolved;
// unknown keys are dropped.
func ParseEnv(data []byte, slurmID string) []EnvFact {
	if len(data) > MaxEnvBytes {
		return UnresolvedEnv("the environment file is larger than expected, so it was not read")
	}
	var d struct {
		Record string `json:"record"`
		JobID  string `json:"slurm_job_id"`
		Facts  map[string]struct {
			Status string `json:"status"`
			Value  string `json:"value"`
			Reason string `json:"reason"`
		} `json:"facts"`
	}
	if err := json.Unmarshal(data, &d); err != nil || d.Record != "vesqira-env" {
		return UnresolvedEnv("the environment file is not a Vesqira environment record")
	}
	if slurmID == "" || d.JobID != slurmID {
		return UnresolvedEnv("the environment file was written by another job (or names none), so it is not used")
	}
	out := make([]EnvFact, 0, len(EnvKeys))
	for _, k := range EnvKeys {
		if k.Key == "slurm.job_id" {
			out = append(out, EnvFact{Key: k.Key, Label: k.Label, Status: EnvVerified, Value: slurmID})
			continue
		}
		f, ok := d.Facts[k.Key]
		if !ok {
			out = append(out, EnvFact{Key: k.Key, Label: k.Label, Status: EnvUnresolved, Reason: "the job did not report it"})
			continue
		}
		out = append(out, normalize(k, f.Status, f.Value, f.Reason))
	}
	return out
}

// ParseEnvLines reads the "key|status|value" lines the same collector prints when Vesqira probes a code folder before a
// submission. Only the keys that were printed are returned.
func ParseEnvLines(out string) []EnvFact {
	var facts []EnvFact
	for _, line := range strings.Split(out, "\n") {
		p := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(p) != 3 {
			continue
		}
		k, ok := envKeyOf(p[0])
		if !ok {
			continue
		}
		facts = append(facts, normalize(k, p[1], p[2], p[2]))
	}
	return facts
}

// EnvValue is the verified value of a fact, if there is one.
func EnvValue(facts []EnvFact, key string) (string, bool) {
	for _, f := range facts {
		if f.Key == key && f.Status == EnvVerified {
			return f.Value, true
		}
	}
	return "", false
}

// EnvDifference is one fact compared between two runs (or a run and the code folder as it is now).
type EnvDifference struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Result   string `json:"result"` // matched | differed | unknown
	Parent   string `json:"parent"`
	This     string `json:"this"`
	Blocking bool   `json:"blocking"` // differed in a fact a strict rerun requires to be equal
	Why      string `json:"why"`      // for unknown: which side did not record it
}

// CompareEnv compares what two runs observed, fact by fact. matched means both are verified and equal; differed means both
// are verified and differ; unknown means at least one side did not record it, which says nothing about whether it matches.
// Facts that differ by construction (job id, module text, untracked count) are not compared.
func CompareEnv(parent, this []EnvFact) []EnvDifference {
	var out []EnvDifference
	for _, k := range EnvKeys {
		if k.Compare == "" {
			continue
		}
		pv, pok := EnvValue(parent, k.Key)
		tv, tok := EnvValue(this, k.Key)
		d := EnvDifference{Key: k.Key, Label: k.Label, Parent: pv, This: tv}
		switch {
		case pok && tok && pv == tv:
			d.Result = "matched"
		case pok && tok:
			d.Result, d.Blocking = "differed", k.Compare == "strict"
		default:
			d.Result = "unknown"
			switch {
			case !pok && !tok:
				d.Why = "neither side recorded it"
			case !pok:
				d.Why = "the parent run did not record it"
			default:
				d.Why = "it was not observed on this side"
			}
		}
		out = append(out, d)
	}
	return out
}

func (d EnvDifference) String() string {
	return fmt.Sprintf("%s: parent %s, now %s", d.Label, d.Parent, d.This)
}
