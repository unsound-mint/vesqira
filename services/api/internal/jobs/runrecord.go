package jobs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/BurntSushi/toml"
)

// RunRecordName is the file Vesqira writes next to a job's outputs. It is a record format discovery already reads
// (run.json: top-level and `settings` entries are the run's settings), so a rescan of the folder finds the run.
const RunRecordName = "run.json"

// RunRecordMarker is in every record Vesqira writes. Rewriting the record at completion is allowed only for a file that
// carries it; a run.json made by anything else is never replaced.
const RunRecordMarker = "vesqira-run-record"

// RunInput is a file the job reads, as Vesqira knew it. It is a reference, not an identity: no bytes were hashed.
type RunInput struct {
	DatasetID int64  `json:"dataset_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Path      string `json:"path"`
	Source    string `json:"source"`
}

const (
	maxRecordSettings = 100
	maxSettingValue   = 500
)

var (
	sbatchRe = regexp.MustCompile(`(?m)^#SBATCH --(cpus-per-task|mem|time|partition|account|nodes|ntasks)=(\S+)$`)
	cfgBlock = regexp.MustCompile(`(?s)cat > "\$(?:cfg|config)" <<'` + configEnd + `'\n(.*?)\n` + configEnd + `\n`)
)

// StateEvidence says where the state in a record came from and when it was obtained, which is not when the record is
// written: the write may come later (the output folder did not exist yet, or a retry).
type StateEvidence struct {
	Value  string    // the state as of AsOf
	Detail string    // the scheduler's detail line, if any
	Source string    // what produced it, e.g. "sbatch returned job id 4242" or "reported by squeue/sacct"
	Label  string    // the provenance label for it ("verified: ...")
	AsOf   time.Time // when the state was obtained
}

// SubmittedEvidence is the state right after sbatch answered: all that is known is that Slurm accepted the script.
func SubmittedEvidence(slurmID string, at time.Time) StateEvidence {
	return StateEvidence{Value: StateSubmitted, Detail: "submitted", Source: "sbatch returned job id " + slurmID, AsOf: at,
		Label: "verified: sbatch returned this job id at as_of; the job's progress has not been read since, so this is not a queue or run state"}
}

// ReportedEvidence is a state read from squeue or sacct.
func ReportedEvidence(state, detail string, at time.Time) StateEvidence {
	return StateEvidence{Value: state, Detail: detail, Source: "reported by squeue/sacct on the host", AsOf: at,
		Label: "verified: reported by squeue/sacct at as_of; not a scientific validation"}
}

// FoundEvidence is the state of a job that squeue/sacct listed after an unconfirmed submission.
func FoundEvidence(slurmID string, at time.Time) StateEvidence {
	return StateEvidence{Value: StateSubmitted, Detail: "submitted", Source: "found by squeue/sacct on the host as job id " + slurmID + " after an unconfirmed submission", AsOf: at,
		Label: "verified: squeue/sacct listed the job at as_of; its progress was not read"}
}

// RunRecord renders the run.json for a job. Everything in it is either something Vesqira did itself and can state
// (the script it submitted, what Slurm answered, the state it last read) or something the researcher declared (the
// settings, as they appear in the script that was submitted). Nothing is inferred: the commit of the code on the host is
// not read, so it is marked unresolved, and an input is a reference to a path, not a verified file. Completion says that
// the scheduler finished the job and nothing about whether its result is scientifically valid.
//
// Top-level keys are limited to `settings` on purpose: discovery would show any other top-level scalar as a setting.
func RunRecord(j Job, hostName string, inputs []RunInput, ev StateEvidence, now time.Time) ([]byte, error) {
	settings := map[string]string{}
	add := func(k, v string) {
		if k == "" || len(k) > 80 || len(settings) >= maxRecordSettings {
			return
		}
		if len(v) > maxSettingValue {
			v = v[:maxSettingValue]
		}
		if _, dup := settings[k]; !dup {
			settings[k] = v
		}
	}
	var input []RunInput
	input = append(input, inputs...)
	if m := cfgBlock.FindStringSubmatch(j.Script); m != nil {
		var cfg map[string]any
		if _, err := toml.Decode(m[1], &cfg); err == nil {
			for _, f := range flatten(cfg) {
				add(f[0], f[1])
				if f[0] == "input.input_file" && f[1] != "" {
					input = append(input, RunInput{Path: f[1], Source: "the config the job was submitted with (input.input_file), not checked"})
				}
			}
		}
	}
	if j.Params != "" {
		var p struct {
			Op      string            `json:"op"`
			Options map[string]string `json:"options"`
		}
		if json.Unmarshal([]byte(j.Params), &p) == nil {
			add("analysis", p.Op)
			for k, v := range p.Options {
				add("option."+k, v)
			}
		}
	}
	for _, m := range sbatchRe.FindAllStringSubmatch(j.Script, -1) {
		add("slurm."+m[1], m[2])
	}
	rec := map[string]any{
		"settings": settings,
		"vesqira": map[string]any{
			"record":     RunRecordMarker,
			"version":    1,
			"written_at": now.UTC().Format(time.RFC3339),
			"state": map[string]any{
				"value":  ev.Value,
				"as_of":  ev.AsOf.UTC().Format(time.RFC3339),
				"source": ev.Source,
				"detail": ev.Detail,
				"meaning": "the last state Vesqira obtained, when (as_of) and from what (source), not necessarily the state now; " +
					"a finished job says only that it ran to its end, not that its result is scientifically valid",
			},
			"job": map[string]any{
				"id":            j.ID,
				"output_dir":    j.OutputDir,
				"name":          j.Name,
				"template":      j.Template,
				"slurm_id":      j.SlurmID,
				"script_sha256": j.ScriptHash,
				"host":          hostName,
				"submitted_at":  stamp(j.SubmittedAt),
				"finished_at":   stamp(j.FinishedAt),
			},
			"code":   codeIdentity(j),
			"inputs": inputsOrEmpty(input),
			"provenance": map[string]string{
				"settings":      "declared: read from the script that was submitted (and the analysis options); the program may have changed or ignored them",
				"script_sha256": "verified: computed over the exact script text sent to the host",
				"slurm_id":      "verified: returned by sbatch",
				"state":         ev.Label,
				"inputs":        "declared: paths named in the job; the files were not hashed and may have changed",
				"code":          "unresolved: the commit of the code on the host was not read",
			},
		},
	}
	return json.MarshalIndent(rec, "", "  ")
}

func codeIdentity(j Job) map[string]any {
	c := map[string]any{"commit": nil, "status": "unresolved"}
	if r, ok := j.VGTRequest(); ok && r.RepoDir != "" {
		c["repo_dir"] = r.RepoDir
	}
	return c
}

func inputsOrEmpty(in []RunInput) []RunInput {
	if in == nil {
		return []RunInput{}
	}
	return in
}

func stamp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// flatten turns a decoded TOML document into "key" and "section.key" pairs of scalar values, sorted. Arrays and deeper
// tables are left out: a setting here is a single value.
func flatten(m map[string]any) [][2]string {
	var out [][2]string
	var walk func(prefix string, m map[string]any, depth int)
	walk = func(prefix string, m map[string]any, depth int) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			name := k
			if prefix != "" {
				name = prefix + "." + k
			}
			switch v := m[k].(type) {
			case map[string]any:
				if depth < 1 {
					walk(name, v, depth+1)
				}
			case []any:
				// a list of one is that value (ROHSA's n_gauss = [16]); a longer list is not one setting
				if len(v) == 1 {
					switch e := v[0].(type) {
					case string:
						out = append(out, [2]string{name, e})
					case int64, float64, bool:
						out = append(out, [2]string{name, fmt.Sprint(e)})
					}
				}
			case string:
				out = append(out, [2]string{name, v})
			case int64, float64, bool:
				out = append(out, [2]string{name, fmt.Sprint(v)})
			}
		}
	}
	walk("", m, 0)
	return out
}
