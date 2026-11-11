// Package jobs records Slurm jobs submitted to a remote host: the exact script (and its hash), the settings it was
// rendered from, and the state followed through squeue and sacct. The remote commands are fixed templates whose only
// variable parts are validated identifiers and numbers; the script itself is data sent on stdin, never part of a
// command line.
package jobs

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
)

const (
	StateDraft      = "draft"
	StateSubmitting = "submitting" // sbatch was started and its answer was not seen; Slurm may or may not hold the job
	StateSubmitted  = "submitted"  // accepted by Slurm, waiting
	StateRunning    = "running"
	StateCompleted  = "completed"
	StateFailed     = "failed"
	StateCancelled  = "cancelled"
	MaxScript       = 64 << 10
	MaxActive       = 20
)

type Job struct {
	ID          int64      `json:"id"`
	HostID      int64      `json:"hostId"`
	Name        string     `json:"name"`
	Template    string     `json:"template"` // vgt | custom
	Script      string     `json:"script"`
	ScriptHash  string     `json:"scriptHash"`
	State       string     `json:"state"`
	Detail      string     `json:"detail"`
	SlurmID     string     `json:"slurmId"`
	WorkDir     string     `json:"workDir"`   // job directory name under the host's ~/vesqira-jobs
	OutputDir   string     `json:"outputDir"` // where the run will write, inside the host's source roots (VGT template)
	CreatedAt   time.Time  `json:"createdAt"`
	SubmittedAt *time.Time `json:"submittedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
	RunID       *int64     `json:"runId"`
	// Params records what an analysis job was asked to do (operation, source dataset ids) so its result can be described.
	Params string `json:"params"`
	// ParentRunID is the run this job was drafted from with "run again with...", or nil.
	ParentRunID *int64 `json:"parentRunId"`
	// Note is why the owner made this job (at most MaxNote characters), kept with it.
	Note string `json:"note"`
	// Request is the JSON of the VGT request the script was rendered from (see VGTRequest). It holds the owner's config
	// text, so it is not part of the job JSON: only the owner-only rerun route returns it, parsed.
	Request string `json:"-"`
	// RecordOwed means the run record next to the outputs on the host still has to be written or rewritten: the output
	// folder did not exist yet, a rewrite at a terminal state failed, or the host was unreachable (ADR 0039). Not part of
	// the job JSON.
	RecordOwed bool `json:"-"`
	// AutoImport asks for the finished job's results to be imported as a run by itself, once, when it completes (VGT
	// jobs only). AutoImportTried is set when that attempt was claimed, so it never runs twice and a failure is not retried.
	AutoImport      bool `json:"autoImport"`
	AutoImportTried bool `json:"-"`
	// Usage is what the scheduler reported the job used when it ended (ADR 0044), or nil while it has not ended or when
	// the end was never read from sacct.
	Usage *Usage `json:"usage"`
}

// VGTRequest is what a VGT job was asked to do, kept so a later run can start from an exact copy. BaseConfigText is
// the config as read from the host (or as given), before the user's Set overrides and before output_dir was replaced.
type VGTRequest struct {
	RepoDir        string            `json:"repoDir"`
	ConfigPath     string            `json:"configPath"`
	BaseConfigText string            `json:"baseConfigText"`
	Set            map[string]string `json:"set"`
	CPUs           int               `json:"cpus"`
	Mem            string            `json:"mem"`
	Time           string            `json:"time"`
	Partition      string            `json:"partition"`
	Account        string            `json:"account"`
}

// VGTRequest returns the stored request, or false for a job that has none (not VGT, or made before it was recorded).
func (j Job) VGTRequest() (VGTRequest, bool) {
	var r VGTRequest
	if j.Template != "vgt" || j.Request == "" || json.Unmarshal([]byte(j.Request), &r) != nil {
		return VGTRequest{}, false
	}
	return r, true
}

// Lineage ties a run to the job that produced it.
type Lineage struct {
	JobID       int64
	ParentRunID *int64
}

func Hash(script string) string { h := sha256.Sum256([]byte(script)); return hex.EncodeToString(h[:]) }

func NewWorkDir() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "job-" + hex.EncodeToString(b[:])
}

// ---- settings validation ----

var (
	nameRe    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)
	pathRe    = regexp.MustCompile(`^/[A-Za-z0-9_./+-]{0,300}$`) // no spaces, quotes, $ or backticks: safe inside the script
	memRe     = regexp.MustCompile(`^[0-9]{1,4}[MG]$`)
	timeRe    = regexp.MustCompile(`^([0-9]{1,2}-)?[0-9]{1,2}:[0-9]{2}:[0-9]{2}$`)
	slurmWord = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)
	idRe      = regexp.MustCompile(`^[0-9]{1,12}$`)
)

func ValidPath(p string) bool {
	return pathRe.MatchString(p) && !strings.Contains(p, "..") && !strings.HasSuffix(p, "/")
}
func ValidSlurmID(id string) bool { return idRe.MatchString(id) }

// VGTSettings are the fields of the VGT job template.
type VGTSettings struct {
	Name      string
	RepoDir   string // the VGT checkout on the host
	Config    string // the full config.toml text, written into the job directory by the script
	CPUs      int
	Mem       string
	Time      string
	Partition string
	Account   string
}

const configEnd = "VESQIRA_CONFIG_END"

// RenderVGT renders the batch script for one VGT run, modelled on the pipeline's own VGT cluster script. The user
// reads and submits exactly this text.
func RenderVGT(p VGTSettings) (string, error) {
	switch {
	case !nameRe.MatchString(p.Name):
		return "", fmt.Errorf("%w: the job name may use letters, digits, . _ - (up to 40)", ErrInvalid)
	case !ValidPath(p.RepoDir):
		return "", fmt.Errorf("%w: the VGT folder must be an absolute path without spaces or quotes", ErrInvalid)
	case p.CPUs < 1 || p.CPUs > 256:
		return "", fmt.Errorf("%w: CPUs must be between 1 and 256", ErrInvalid)
	case !memRe.MatchString(p.Mem):
		return "", fmt.Errorf("%w: memory looks like 64G or 4096M", ErrInvalid)
	case !timeRe.MatchString(p.Time):
		return "", fmt.Errorf("%w: time looks like 1-00:00:00 or 12:00:00", ErrInvalid)
	case p.Partition != "" && !slurmWord.MatchString(p.Partition), p.Account != "" && !slurmWord.MatchString(p.Account):
		return "", fmt.Errorf("%w: partition and account are plain words", ErrInvalid)
	case len(p.Config) == 0 || len(p.Config) > MaxScript/2 || strings.Contains(p.Config, configEnd) || strings.ContainsRune(p.Config, 0):
		return "", fmt.Errorf("%w: the config text is empty, too large or malformed", ErrInvalid)
	}
	var b strings.Builder
	b.WriteString("#!/bin/bash -l\n")
	fmt.Fprintf(&b, "#SBATCH --job-name=%s\n#SBATCH --nodes=1\n#SBATCH --ntasks=1\n#SBATCH --cpus-per-task=%d\n#SBATCH --mem=%s\n#SBATCH --time=%s\n", p.Name, p.CPUs, p.Mem, p.Time)
	if p.Partition != "" {
		fmt.Fprintf(&b, "#SBATCH --partition=%s\n", p.Partition)
	}
	if p.Account != "" {
		fmt.Fprintf(&b, "#SBATCH --account=%s\n", p.Account)
	}
	b.WriteString("#SBATCH --output=%x-%j.out\n#SBATCH --error=%x-%j.err\n\n")
	b.WriteString("# Rendered by Vesqira from the settings in the job record. This exact text is what is submitted.\n")
	b.WriteString("set -euo pipefail\n")
	b.WriteString("jobdir=\"$SLURM_SUBMIT_DIR\"\ncfg=\"$jobdir/config.toml\"\n")
	fmt.Fprintf(&b, "cat > \"$cfg\" <<'%s'\n%s\n%s\n\n", configEnd, strings.TrimRight(p.Config, "\n"), configEnd)
	fmt.Fprintf(&b, "cd %s\n", p.RepoDir)
	fmt.Fprintf(&b, "export JULIA_DEPOT_PATH=\"${JULIA_DEPOT_PATH:-%s/.julia-vesqira}\"\n", p.RepoDir)
	b.WriteString("threads=\"${SLURM_CPUS_PER_TASK:-1}\"\nexport JULIA_NUM_THREADS=\"$threads\"\n")
	b.WriteString("echo \"job_id=$SLURM_JOB_ID host=$(hostname) threads=$threads\"\ndate --iso-8601=seconds\n")
	b.WriteString("command -v julia >/dev/null || { echo \"Julia is not on PATH\" >&2; exit 2; }\n")
	b.WriteString("srun --ntasks=1 julia --project=. --threads=\"$threads\" main.jl \"$cfg\"\ndate --iso-8601=seconds\n")
	return b.String(), nil
}

// ROHSASettings are the fields of the ROHSA job template.
type ROHSASettings struct {
	Name       string
	RepoDir    string // the VGT checkout on the host, which holds the ROHSA sources and its command line
	ConfigPath string // an existing ROHSA run config on the host
	CPUs       int
	Mem        string
	Time       string
	Partition  string
	Account    string
}

// RenderROHSA renders the batch script for one ROHSA run, following the pipeline's own ROHSA cluster script: build
// the OpenMP Fortran source when it is newer than the binary, then run the preprocessing command on the given config
// with one OpenMP thread per allocated CPU. The results go where the config's output_dir says; they are not imported
// as a run, because there is no ROHSA run description to read.
func RenderROHSA(p ROHSASettings) (string, error) {
	switch {
	case !nameRe.MatchString(p.Name):
		return "", fmt.Errorf("%w: the job name may use letters, digits, . _ - (up to 40)", ErrInvalid)
	case !ValidPath(p.RepoDir), !ValidPath(p.ConfigPath):
		return "", fmt.Errorf("%w: the folder and the config must be absolute paths without spaces or quotes", ErrInvalid)
	case p.CPUs < 1 || p.CPUs > 256:
		return "", fmt.Errorf("%w: CPUs must be between 1 and 256", ErrInvalid)
	case !memRe.MatchString(p.Mem):
		return "", fmt.Errorf("%w: memory looks like 64G or 4096M", ErrInvalid)
	case !timeRe.MatchString(p.Time):
		return "", fmt.Errorf("%w: time looks like 1-00:00:00 or 12:00:00", ErrInvalid)
	case p.Partition != "" && !slurmWord.MatchString(p.Partition), p.Account != "" && !slurmWord.MatchString(p.Account):
		return "", fmt.Errorf("%w: partition and account are plain words", ErrInvalid)
	}
	var b strings.Builder
	b.WriteString("#!/bin/bash -l\n")
	fmt.Fprintf(&b, "#SBATCH --job-name=%s\n#SBATCH --nodes=1\n#SBATCH --ntasks=1\n#SBATCH --cpus-per-task=%d\n#SBATCH --mem=%s\n#SBATCH --time=%s\n", p.Name, p.CPUs, p.Mem, p.Time)
	if p.Partition != "" {
		fmt.Fprintf(&b, "#SBATCH --partition=%s\n", p.Partition)
	}
	if p.Account != "" {
		fmt.Fprintf(&b, "#SBATCH --account=%s\n", p.Account)
	}
	b.WriteString("#SBATCH --output=%x-%j.out\n#SBATCH --error=%x-%j.err\n\n")
	b.WriteString("# Rendered by Vesqira from the settings in the job record. This exact text is what is submitted.\n")
	b.WriteString("set -euo pipefail\n")
	fmt.Fprintf(&b, "cd %s\n", p.RepoDir)
	fmt.Fprintf(&b, "config=%s\n", p.ConfigPath)
	fmt.Fprintf(&b, "export JULIA_DEPOT_PATH=\"${JULIA_DEPOT_PATH:-%s/.julia-vesqira}\"\n", p.RepoDir)
	b.WriteString("echo \"job_id=$SLURM_JOB_ID host=$(hostname) config=$config\"\ndate --iso-8601=seconds\n")
	b.WriteString("module purge\nmodule load GCC\n")
	b.WriteString("rohsa_src=rohsa/upstream/ROHSA/src\n")
	b.WriteString("# rebuild only when a source is newer than the binary; jobs share this tree, so take the lock\n")
	b.WriteString("flock \"$rohsa_src/.build.lock\" bash -c '\n")
	b.WriteString("    if [ ! -x \"$1/ROHSA\" ] || [ -n \"$(find \"$1\" -maxdepth 1 \\( -name \"*.f\" -o -name \"*.f90\" -o -name Makefile \\) -newer \"$1/ROHSA\")\" ]; then\n")
	b.WriteString("        make -C \"$1\" clean all\n    fi' _ \"$rohsa_src\"\n")
	b.WriteString("command -v julia >/dev/null || { echo \"Julia is not on PATH\" >&2; exit 2; }\n")
	b.WriteString("# the optimiser loop is OpenMP-parallel: match threads to the allocation\n")
	b.WriteString("export OMP_NUM_THREADS=\"${SLURM_CPUS_PER_TASK:-1}\"\nulimit -s unlimited\nexport OMP_STACKSIZE=256M\n")
	b.WriteString("srun --ntasks=1 julia --project=. rohsa/cli/preprocess.jl --config \"$config\"\ndate --iso-8601=seconds\n")
	return b.String(), nil
}

// ---- editing a config.toml: only a small, typed set of keys ----

type configKey struct {
	Section string
	Kind    string // number | enum | path | int
	Enum    []string
}

var configKeys = map[string]configKey{
	"noise":                  {"noise", "number", nil},
	"noise_mode":             {"noise", "enum", []string{"scalar", "channel", "spatial", "separable", "separable_shrinkage", "auto"}},
	"noise_tile_size":        {"noise", "int", nil},
	"noise_sigma_multiplier": {"noise", "number", nil},
	"dn":                     {"vgt", "int", nil},
	"ker":                    {"vgt", "int", nil},
	"min_valid":              {"vgt", "int", nil},
	"input_file":             {"input", "path", nil},
	"output_dir":             {"input", "path", nil},
}

var (
	numRe = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)
	intRe = regexp.MustCompile(`^[0-9]{1,9}$`)
)

// SetConfigValues returns config text with the given keys set. Only the keys in the table above are accepted, each
// with a value of its own type, so the config can be adjusted from the UI without free-form text reaching a script.
func SetConfigValues(text string, values map[string]string) (string, error) {
	lines := strings.Split(text, "\n")
	for key, val := range values {
		spec, ok := configKeys[key]
		if !ok {
			return "", fmt.Errorf("%w: %q is not an editable setting", ErrInvalid, key)
		}
		var rendered string
		switch spec.Kind {
		case "number":
			if !numRe.MatchString(val) {
				return "", fmt.Errorf("%w: %s must be a number", ErrInvalid, key)
			}
			rendered = val
		case "int":
			if !intRe.MatchString(val) {
				return "", fmt.Errorf("%w: %s must be a whole number", ErrInvalid, key)
			}
			rendered = val
		case "enum":
			found := false
			for _, e := range spec.Enum {
				found = found || e == val
			}
			if !found {
				return "", fmt.Errorf("%w: %s must be one of %s", ErrInvalid, key, strings.Join(spec.Enum, ", "))
			}
			rendered = `"` + val + `"`
		case "path":
			if !ValidPath(val) {
				return "", fmt.Errorf("%w: %s must be an absolute path without spaces or quotes", ErrInvalid, key)
			}
			rendered = `"` + val + `"`
		}
		lines = setLine(lines, spec.Section, key, rendered)
	}
	return strings.Join(lines, "\n"), nil
}

// setLine replaces `key = ...` inside [section] or adds it at the end of that section (or a new section).
func setLine(lines []string, section, key, rendered string) []string {
	inSection, secEnd := false, -1
	keyRe := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			if inSection && secEnd < 0 {
				secEnd = i
			}
			inSection = t == "["+section+"]"
			continue
		}
		if inSection && keyRe.MatchString(l) {
			lines[i] = key + " = " + rendered
			return lines
		}
	}
	if inSection && secEnd < 0 {
		secEnd = len(lines)
	}
	if secEnd >= 0 {
		return append(lines[:secEnd], append([]string{key + " = " + rendered}, lines[secEnd:]...)...)
	}
	return append(lines, "", "["+section+"]", key+" = "+rendered)
}

// ---- remote command templates ----

func jobDir(workDir string) string { return `"$HOME/vesqira-jobs/` + workDir + `"` }

var workDirRe = regexp.MustCompile(`^job-[0-9a-f]{16}$`)

// SubmitCommand reads the script from stdin into the job directory and submits it.
func SubmitCommand(workDir string) (string, error) {
	if !workDirRe.MatchString(workDir) {
		return "", fmt.Errorf("%w: bad job directory", ErrInvalid)
	}
	d := jobDir(workDir)
	// The comment marks the job as ours, so a submission whose answer was lost can be found again.
	return `mkdir -p ` + d + ` && cd ` + d + ` && cat > job.sh && chmod 700 job.sh && sbatch --parsable --comment=` + Marker(workDir) + ` job.sh`, nil
}

// Marker is the Slurm comment that ties a Slurm job to one job record.
func Marker(workDir string) string { return "vesqira-" + workDir }

var outFileRe = regexp.MustCompile(`^.+-([0-9]{1,12})\.(out|err)$`)

// LookupCommand asks the host whether Slurm already holds the job that was being submitted: by the comment in the
// queue, by the accounting record, and by the output files a started job leaves in its own directory.
func LookupCommand(workDir string) (string, error) {
	if !workDirRe.MatchString(workDir) {
		return "", fmt.Errorf("%w: bad job directory", ErrInvalid)
	}
	return `squeue -h -u "$USER" -o '%i %k' 2>/dev/null; echo '---'; sacct -n -P -X -S now-14days -o JobID,Comment 2>/dev/null; echo '---'; ls -1 ` + jobDir(workDir) + ` 2>/dev/null; true`, nil
}

// ParseLookup returns the Slurm id of the job that carries this record's marker, if the output shows one.
func ParseLookup(out, workDir string) (string, bool) {
	want := Marker(workDir)
	section := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "---" {
			section++
			continue
		}
		switch section {
		case 0:
			f := strings.Fields(line)
			if len(f) == 2 && f[1] == want && ValidSlurmID(f[0]) {
				return f[0], true
			}
		case 1:
			f := strings.SplitN(line, "|", 2)
			if len(f) == 2 && f[1] == want && ValidSlurmID(f[0]) {
				return f[0], true
			}
		case 2:
			if m := outFileRe.FindStringSubmatch(line); m != nil {
				return m[1], true
			}
		}
	}
	return "", false
}

func StatusCommand(slurmID string) (string, error) {
	if !ValidSlurmID(slurmID) {
		return "", fmt.Errorf("%w: bad job id", ErrInvalid)
	}
	return BatchStatusCommand([]string{slurmID}) // one format for the Refresh button and the watcher, so both record usage
}

// BatchStatusCommand asks about several jobs on one host with one squeue and one sacct call, which is how the background
// watcher keeps to one status call per host per interval. The answer is split per job by ParseBatchStatus.
func BatchStatusCommand(ids []string) (string, error) {
	return BatchStatusCommandFields(ids, Fields(UsageFields))
}

// sacctExitMark starts the line that closes the command's output: sacct's exit status, then what it printed on stderr.
const sacctExitMark = "@@accounting-exit "

// knownFields are the only sacct columns a command may ask for.
var knownFields = map[string]bool{}

func init() {
	for _, f := range Fields(UsageFields) {
		knownFields[strings.ToLower(f)] = true
	}
}

// BatchStatusCommandFields is BatchStatusCommand for a chosen column list (a subset of UsageFields that starts with the
// six minimal columns). The minimal list keeps sacct's -X (jobs only); steps are asked for only when a column needs them
// (MaxRSS is recorded on steps, TotalCPU is summed from them). sacct's stderr is not discarded but kept in the closing
// line, with its exit status, so a site whose sacct rejects a column can be told from a site that has no jobs to list.
func BatchStatusCommandFields(ids, fields []string) (string, error) {
	for _, id := range ids {
		if !ValidSlurmID(id) {
			return "", fmt.Errorf("%w: bad job id", ErrInvalid)
		}
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("%w: no job ids", ErrInvalid)
	}
	steps := false
	for _, f := range fields {
		if !knownFields[strings.ToLower(f)] {
			return "", fmt.Errorf("%w: bad sacct column", ErrInvalid)
		}
		if l := strings.ToLower(f); l == "maxrss" || l == "totalcpu" {
			steps = true
		}
	}
	list := strings.Join(ids, ",")
	x := " -X"
	if steps {
		x = ""
	}
	return `squeue -h -j ` + list + ` -o '%i|%T' 2>/dev/null; echo '---'; e=$(mktemp 2>/dev/null) || e=/dev/null; sacct -n -P` + x + ` -j ` + list + ` -o ` + strings.Join(fields, ",") + ` 2>"$e"; r=$?; echo '` + sacctExitMark + `'$r; head -c 400 "$e" 2>/dev/null; [ "$e" = /dev/null ] || rm -f "$e"; true`, nil
}

// SplitSacctExit cuts the closing line off the answer to BatchStatusCommandFields: the output to parse, sacct's exit
// status and its stderr. An answer without the line (an older host command) reads as exit 0.
func SplitSacctExit(out string) (body string, exit int, stderr string) {
	i := strings.LastIndex(out, sacctExitMark)
	if i < 0 {
		return out, 0, ""
	}
	tail := out[i+len(sacctExitMark):]
	num, msg, _ := strings.Cut(tail, "\n")
	n, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil {
		return out, 0, ""
	}
	return out[:i], n, strings.TrimSpace(msg)
}

var invalidField = regexp.MustCompile(`(?i)invalid field requested:?\s*"?([A-Za-z0-9_]+)"?`)

// RejectedField is the column sacct's error names ("Invalid field requested: "ReqTRES""), or "" when it names none.
func RejectedField(stderr string) string {
	if m := invalidField.FindStringSubmatch(stderr); m != nil {
		for _, f := range Fields(UsageFields) {
			if strings.EqualFold(f, m[1]) {
				return f
			}
		}
	}
	return ""
}

// RowsFit reports whether the sacct rows in body (the part after the first ---) have the number of columns asked for.
// Rows that all have another number mean the output is not in the requested format. No rows fit trivially.
func RowsFit(body string, columns int) bool {
	parts := strings.SplitN(body, "---", 2)
	if len(parts) < 2 {
		return true
	}
	seen := false
	for _, line := range strings.Split(parts[1], "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		seen = true
		if strings.Count(line, "|")+1 == columns {
			return true
		}
	}
	return !seen
}

// ParseBatchStatus splits the answer to BatchStatusCommand into one Status per requested id, each parsed by the same rules
// as a single job's answer (the queue wins over the accounting record). A job Slurm did not list has an empty State.
func ParseBatchStatus(out string, ids []string) map[string]Status {
	return ParseBatchStatusFields(out, ids, Fields(UsageFields))
}

// ParseBatchStatusFields is ParseBatchStatus for the columns the command was built with. out is the body of the answer
// (SplitSacctExit already applied).
func ParseBatchStatusFields(out string, ids, fields []string) map[string]Status {
	parts := strings.SplitN(out, "---", 2)
	queue, acct := map[string]string{}, map[string]string{}
	rows := map[string][][]string{} // every sacct row per job: the job's own and its steps (12, 12.batch, 12.0)
	for _, line := range strings.Split(parts[0], "\n") {
		if f := strings.SplitN(strings.TrimSpace(line), "|", 2); len(f) == 2 && queue[f[0]] == "" {
			queue[f[0]] = f[1]
		}
	}
	if len(parts) == 2 {
		for _, line := range strings.Split(parts[1], "\n") {
			line = strings.TrimSpace(line)
			f := strings.SplitN(line, "|", 2)
			if len(f) != 2 {
				continue
			}
			if id, _, step := strings.Cut(f[0], "."); step {
				rows[id] = append(rows[id], strings.Split(line, "|"))
				continue
			}
			rows[f[0]] = append(rows[f[0]], strings.Split(line, "|"))
			if acct[f[0]] == "" {
				acct[f[0]] = f[1]
			}
		}
	}
	res := make(map[string]Status, len(ids))
	for _, id := range ids {
		st := ParseStatusFull(queue[id] + "\n---\n" + acct[id])
		st.Usage = ParseUsageFields(rows[id], fields)
		res[id] = st
	}
	return res
}

func CancelCommand(slurmID string) (string, error) {
	if !ValidSlurmID(slurmID) {
		return "", fmt.Errorf("%w: bad job id", ErrInvalid)
	}
	return `scancel ` + slurmID, nil
}

func LogCommand(workDir, slurmID string) (string, error) {
	if !workDirRe.MatchString(workDir) || !ValidSlurmID(slurmID) {
		return "", fmt.Errorf("%w: bad job", ErrInvalid)
	}
	d := jobDir(workDir)
	return `cd ` + d + ` && for f in ./*-` + slurmID + `.out ./*-` + slurmID + `.err; do [ -f "$f" ] && { echo "=== $f"; tail -c 12000 "$f"; }; done; true`, nil
}

// ParseSubmit extracts the Slurm job id from `sbatch --parsable` output ("12345" or "12345;cluster").
func ParseSubmit(out string) (string, error) {
	id := strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), ";", 2)[0])
	if !ValidSlurmID(id) {
		return "", errors.New("Slurm did not return a job id")
	}
	return id, nil
}

// ParseStatus turns squeue and sacct output into one of our states plus a short detail line. A job Slurm does not
// know yet is reported as unknown (the empty state), not as failed.
func ParseStatus(out string) (state, detail string) {
	st := ParseStatusFull(out)
	return st.State, st.Detail
}

// Status is what squeue and sacct said about one job. Slurm is the scheduler's own state word (COMPLETED, TIMEOUT,
// OUT_OF_MEMORY...), empty when it did not list the job. ExitCode is sacct's "code:signal" and is empty when sacct gave none.
type Status struct {
	State, Detail, Slurm, ExitCode string
	// Usage is what sacct recorded about the job's use (nil when sacct listed no row for it); it is final only once the
	// job has ended.
	Usage *Usage
}

// ParseStatusFull is ParseStatus with the scheduler's own word and exit code kept, so a message can say "time limit"
// rather than guess it from a folded state.
func ParseStatusFull(out string) (st Status) {
	defer func() { st.Detail = strings.TrimSpace(st.Detail) }()
	state, detail := parseStatus(out)
	st = Status{State: state, Detail: detail}
	parts := strings.SplitN(out, "---", 2)
	queue := strings.TrimSpace(parts[0])
	acct := ""
	if len(parts) == 2 {
		acct = strings.TrimSpace(parts[1])
	}
	f := strings.Split(strings.SplitN(acct, "\n", 2)[0], "|")
	raw := queue
	if raw == "" && acct != "" {
		raw = strings.TrimSpace(f[0])
	}
	if w := strings.Fields(raw); len(w) > 0 {
		st.Slurm = strings.ToUpper(w[0])
	}
	if len(f) >= 3 && acct != "" {
		st.ExitCode = strings.TrimSpace(f[1])
	}
	return st
}

func parseStatus(out string) (state, detail string) {
	parts := strings.SplitN(out, "---", 2)
	queue := strings.TrimSpace(parts[0])
	acct := ""
	if len(parts) == 2 {
		acct = strings.TrimSpace(parts[1])
	}
	fields := func(s string) []string { return strings.Split(strings.SplitN(s, "\n", 2)[0], "|") }
	raw := queue
	if raw == "" && acct != "" {
		raw = strings.TrimSpace(fields(acct)[0])
	}
	if w := strings.Fields(raw); len(w) > 0 {
		raw = strings.ToUpper(w[0])
	} else {
		raw = ""
	}
	switch {
	case raw == "":
		return "", "not found in squeue or sacct yet"
	case raw == "PENDING" || raw == "CONFIGURING" || raw == "SUSPENDED" || raw == "REQUEUED":
		state = StateSubmitted
	case raw == "RUNNING" || raw == "COMPLETING" || raw == "RESIZING":
		state = StateRunning
	case raw == "COMPLETED":
		state = StateCompleted
	case strings.HasPrefix(raw, "CANCELLED"):
		state = StateCancelled
	default: // FAILED, TIMEOUT, OUT_OF_MEMORY, NODE_FAIL, BOOT_FAIL, DEADLINE, ...
		state = StateFailed
	}
	detail = raw
	if acct != "" {
		f := fields(acct)
		if len(f) >= 3 {
			detail = fmt.Sprintf("%s, exit %s, elapsed %s", strings.ToLower(raw), f[1], f[2])
		}
	}
	return state, detail
}

// ---- store ----

// MaxNote is the longest "why" kept with a job, in characters.
const MaxNote = 500

type Store struct{ db *sql.DB }

func Open(db *sql.DB) (*Store, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS jobs(
		id INTEGER PRIMARY KEY AUTOINCREMENT, host_id INTEGER NOT NULL, name TEXT NOT NULL, template TEXT NOT NULL,
		script TEXT NOT NULL, script_hash TEXT NOT NULL, state TEXT NOT NULL, detail TEXT NOT NULL, slurm_id TEXT NOT NULL,
		work_dir TEXT NOT NULL, output_dir TEXT NOT NULL, created_at INTEGER NOT NULL, submitted_at INTEGER,
		finished_at INTEGER, run_id INTEGER)`)
	if err == nil {
		err = schema.AddColumn(db, "jobs", "params", "TEXT NOT NULL DEFAULT ''")
	}
	if err == nil {
		err = schema.AddColumn(db, "jobs", "request", "TEXT NOT NULL DEFAULT ''")
	}
	if err == nil {
		err = schema.AddColumn(db, "jobs", "parent_run_id", "INTEGER")
	}
	if err == nil {
		err = schema.AddColumn(db, "jobs", "note", "TEXT NOT NULL DEFAULT ''")
	}
	if err == nil {
		err = schema.AddColumn(db, "jobs", "record_owed", "INTEGER NOT NULL DEFAULT 0")
	}
	if err == nil {
		err = schema.AddColumn(db, "jobs", "auto_import", "INTEGER NOT NULL DEFAULT 0")
	}
	if err == nil {
		err = schema.AddColumn(db, "jobs", "auto_import_tried", "INTEGER NOT NULL DEFAULT 0")
	}
	if err == nil {
		err = schema.AddColumn(db, "jobs", "usage", "TEXT NOT NULL DEFAULT ''")
	}
	if err != nil {
		return nil, err
	}
	return &Store{db}, nil
}

const cols = `id,host_id,name,template,script,script_hash,state,detail,slurm_id,work_dir,output_dir,created_at,submitted_at,finished_at,run_id,params,request,parent_run_id,note,record_owed,auto_import,auto_import_tried,usage`

func scan(row interface{ Scan(...any) error }) (Job, error) {
	var j Job
	var c int64
	var sub, fin, run, parent sql.NullInt64
	var owed, auto, tried int
	var usage string
	if err := row.Scan(&j.ID, &j.HostID, &j.Name, &j.Template, &j.Script, &j.ScriptHash, &j.State, &j.Detail, &j.SlurmID, &j.WorkDir, &j.OutputDir, &c, &sub, &fin, &run, &j.Params, &j.Request, &parent, &j.Note, &owed, &auto, &tried, &usage); err != nil {
		return Job{}, err
	}
	if usage != "" {
		var u Usage
		if json.Unmarshal([]byte(usage), &u) == nil {
			j.Usage = &u
		}
	}
	j.RecordOwed, j.AutoImport, j.AutoImportTried = owed != 0, auto != 0, tried != 0
	j.CreatedAt = time.Unix(c, 0).UTC()
	if sub.Valid {
		t := time.Unix(sub.Int64, 0).UTC()
		j.SubmittedAt = &t
	}
	if fin.Valid {
		t := time.Unix(fin.Int64, 0).UTC()
		j.FinishedAt = &t
	}
	if run.Valid {
		j.RunID = &run.Int64
	}
	if parent.Valid {
		j.ParentRunID = &parent.Int64
	}
	return j, nil
}

func (s *Store) CreateDraft(j Job) (Job, error) {
	if len(j.Script) == 0 || len(j.Script) > MaxScript || !strings.HasPrefix(j.Script, "#!") {
		return Job{}, fmt.Errorf("%w: the script must start with #! and be at most %d bytes", ErrInvalid, MaxScript)
	}
	if !nameRe.MatchString(j.Name) {
		return Job{}, fmt.Errorf("%w: bad job name", ErrInvalid)
	}
	res, err := s.db.Exec(`INSERT INTO jobs(host_id,name,template,script,script_hash,state,detail,slurm_id,work_dir,output_dir,created_at,params,request,parent_run_id,note,auto_import) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.HostID, j.Name, j.Template, j.Script, Hash(j.Script), StateDraft, "", "", NewWorkDir(), j.OutputDir, time.Now().Unix(), j.Params, j.Request, j.ParentRunID, j.Note, j.AutoImport)
	if err != nil {
		return Job{}, err
	}
	id, _ := res.LastInsertId()
	return s.Get(id)
}

func (s *Store) Get(id int64) (Job, error) {
	j, err := scan(s.db.QueryRow(`SELECT `+cols+` FROM jobs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return j, err
}

func (s *Store) List() ([]Job, error) {
	rows, err := s.db.Query(`SELECT ` + cols + ` FROM jobs ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// SetScript replaces a draft's script (the user edited it). Only drafts can change; the hash follows.
func (s *Store) SetScript(id int64, script string) error {
	if len(script) == 0 || len(script) > MaxScript || !strings.HasPrefix(script, "#!") {
		return fmt.Errorf("%w: the script must start with #! and be at most %d bytes", ErrInvalid, MaxScript)
	}
	res, err := s.db.Exec(`UPDATE jobs SET script=?, script_hash=? WHERE id=? AND state=?`, script, Hash(script), id, StateDraft)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: only a draft can be edited", ErrInvalid)
	}
	return nil
}

// BeginSubmit moves a draft to "submitting" before sbatch runs. Only one caller can win, so a double click or a retry
// cannot start a second submission of the same record.
func (s *Store) BeginSubmit(id int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE jobs SET state=?, detail='submitting: waiting for sbatch to answer' WHERE id=? AND state=?`, StateSubmitting, id, StateDraft)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// MarkSubmitted records the Slurm id. It accepts a job that is submitting, or a draft from before this state existed.
func (s *Store) MarkSubmitted(id int64, slurmID string) error {
	_, err := s.db.Exec(`UPDATE jobs SET state=?, slurm_id=?, detail='submitted', submitted_at=COALESCE(submitted_at, ?) WHERE id=? AND state IN (?,?)`, StateSubmitted, slurmID, time.Now().Unix(), id, StateDraft, StateSubmitting)
	return err
}

// Reopen returns a submission that never reached Slurm to a draft. The caller must have checked the host first.
func (s *Store) Reopen(id int64) error {
	_, err := s.db.Exec(`UPDATE jobs SET state=?, detail='' WHERE id=? AND state=?`, StateDraft, id, StateSubmitting)
	return err
}

// NoteSubmitting keeps the job in "submitting" and records why it is unresolved.
func (s *Store) NoteSubmitting(id int64, detail string) error {
	_, err := s.db.Exec(`UPDATE jobs SET detail=? WHERE id=? AND state=?`, detail, id, StateSubmitting)
	return err
}

func (s *Store) SetState(id int64, state, detail string) error {
	var fin any
	if state == StateCompleted || state == StateFailed || state == StateCancelled {
		fin = time.Now().Unix()
	}
	_, err := s.db.Exec(`UPDATE jobs SET state=?, detail=?, finished_at=COALESCE(finished_at, ?) WHERE id=?`, state, detail, fin, id)
	return err
}

// ApplyStatus records what Slurm reported for a job that is queued or running, and only for such a job: the update is
// guarded by the state, so a late "running" answer cannot overwrite an end state and, when two callers (the background
// watcher, the Refresh button, the page's own timer) read the same end state, exactly one of them wins. It reports whether
// this call changed the row; a winner with a terminal state is the one that announces the end.
func (s *Store) ApplyStatus(id int64, state, detail string) (bool, error) {
	var fin any
	if state == StateCompleted || state == StateFailed || state == StateCancelled {
		fin = time.Now().Unix()
	}
	res, err := s.db.Exec(`UPDATE jobs SET state=?, detail=?, finished_at=COALESCE(finished_at, ?) WHERE id=? AND state IN (?,?)`, state, detail, fin, id, StateSubmitted, StateRunning)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ClaimAutoImport reports whether the caller may import this job's results now: the option is on, the job is a finished
// VGT job with an output folder that has not become a run, and nobody claimed it before. One caller can win, once, for the
// life of the catalog, so a restart or a second refresh never imports twice and a failed import is not retried.
func (s *Store) ClaimAutoImport(id int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE jobs SET auto_import_tried=1 WHERE id=? AND auto_import=1 AND auto_import_tried=0 AND template='vgt' AND output_dir<>'' AND run_id IS NULL AND state=?`, id, StateCompleted)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetAutoImport changes the option on a draft; once a job is submitted the choice it was submitted with stands.
func (s *Store) SetAutoImport(id int64, on bool) error {
	res, err := s.db.Exec(`UPDATE jobs SET auto_import=? WHERE id=? AND state=?`, on, id, StateDraft)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: only a draft can change this", ErrInvalid)
	}
	return nil
}

// OwedWindow is how long after it finished a job's run record is still retried by the background watcher.
const OwedWindow = 24 * time.Hour

// Watchable returns what the background watcher has work for: jobs queued or running on a host, and jobs that finished
// within OwedWindow whose run record is still owed.
func (s *Store) Watchable(now time.Time) ([]Job, error) {
	rows, err := s.db.Query(`SELECT `+cols+` FROM jobs WHERE (state IN (?,?) AND slurm_id<>'')
		OR (record_owed=1 AND slurm_id<>'' AND state IN (?,?,?) AND COALESCE(finished_at,0) > ?) ORDER BY id`,
		StateSubmitted, StateRunning, StateCompleted, StateFailed, StateCancelled, now.Add(-OwedWindow).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// SetUsage keeps what sacct reported for a job that has ended. It does nothing for a job that has not ended.
func (s *Store) SetUsage(id int64, u Usage) error {
	b, err := json.Marshal(u)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE jobs SET usage=? WHERE id=? AND state IN (?,?,?)`, string(b), id, StateCompleted, StateFailed, StateCancelled)
	return err
}

// SetRecordOwed marks whether the run record on the host still has to be written (see Job.RecordOwed).
func (s *Store) SetRecordOwed(id int64, owed bool) error {
	_, err := s.db.Exec(`UPDATE jobs SET record_owed=? WHERE id=?`, owed, id)
	return err
}

// AppendDetail adds text to the job's detail line unless it already mentions run.json. It is one statement, so it cannot
// overwrite a state a refresh set at the same moment.
func (s *Store) AppendDetail(id int64, text string) error {
	_, err := s.db.Exec(`UPDATE jobs SET detail=detail || ? WHERE id=? AND detail NOT LIKE '%run.json%'`, text, id)
	return err
}

func (s *Store) LinkRun(id, runID int64) error {
	_, err := s.db.Exec(`UPDATE jobs SET run_id=? WHERE id=?`, runID, id)
	return err
}

// Lineages maps every run that a job's outputs became to that job (the latest one, if a run was registered twice).
// One query for the whole run list.
func (s *Store) Lineages() (map[int64]Lineage, error) {
	rows, err := s.db.Query(`SELECT run_id, id, parent_run_id FROM jobs WHERE run_id IS NOT NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Lineage{}
	for rows.Next() {
		var run, id int64
		var parent sql.NullInt64
		if err := rows.Scan(&run, &id, &parent); err != nil {
			return nil, err
		}
		l := Lineage{JobID: id}
		if parent.Valid {
			l.ParentRunID = &parent.Int64
		}
		out[run] = l
	}
	return out, rows.Err()
}

// ForRun returns the latest job whose outputs became this run.
func (s *Store) ForRun(runID int64) (Job, error) {
	j, err := scan(s.db.QueryRow(`SELECT `+cols+` FROM jobs WHERE run_id=? ORDER BY id DESC LIMIT 1`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return j, err
}

func (s *Store) Active() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE state IN (?,?,?)`, StateSubmitting, StateSubmitted, StateRunning).Scan(&n)
	return n, err
}

// AnalysisSettings describe one analysis.py run on a host. Every value is validated, because it lands in a script.
type AnalysisSettings struct {
	Name, Python, ScriptDir string // ScriptDir is the deployed worker directory, as the shell on the host will expand it
	Op                      string // stats | moment | cutout | slab | align
	Input, Reference        string // absolute host paths
	OutDir                  string // absolute host path inside the source roots
	Args                    map[string]string
	CPUs                    int
	Mem, Time               string
	// A map the person declared as an axial orientation map (ADR 0042): its unit, its frame when declared, and its reliability
	// map's absolute host path. align and convolve then resample the doubled angle and write result.reliability.fits.
	AxialUnit, AxialFrame, Weights string
}

var (
	pyRe       = regexp.MustCompile(`^[A-Za-z0-9_./~-]{1,200}$`)
	scriptDirR = regexp.MustCompile(`^\$HOME/\.cache/vesqira/[0-9a-f]{16}$`)
	analysisOp = map[string]bool{"axial_diff": true, "scalar_diff": true, "stats": true, "moment": true, "cutout": true, "slab": true, "align": true, "convolve": true}
	argKinds   = map[string]*regexp.Regexp{
		"order": regexp.MustCompile(`^[012]$`), "chan-start": intRe, "chan-end": intRe, "x0": intRe, "x1": intRe, "y0": intRe, "y1": intRe,
		"threshold": numRe, "target-bmaj": numRe, "target-bmin": numRe, "target-bpa": numRe, "method": regexp.MustCompile(`^(interp|nearest|exact)$`),
	}
)

// RenderAnalysis renders the batch script for one analysis. The output is result.fits and result.json in OutDir.
func RenderAnalysis(p AnalysisSettings) (string, error) {
	switch {
	case !nameRe.MatchString(p.Name):
		return "", fmt.Errorf("%w: bad job name", ErrInvalid)
	case !pyRe.MatchString(p.Python), !scriptDirR.MatchString(p.ScriptDir):
		return "", fmt.Errorf("%w: bad interpreter or worker directory", ErrInvalid)
	case !analysisOp[p.Op]:
		return "", fmt.Errorf("%w: unknown analysis", ErrInvalid)
	case !ValidPath(p.Input), p.Op == "align" && !ValidPath(p.Reference), !ValidPath(p.OutDir):
		return "", fmt.Errorf("%w: file and folder paths must be absolute, without spaces, quotes or ..", ErrInvalid)
	case p.CPUs < 1 || p.CPUs > 256, !memRe.MatchString(p.Mem), !timeRe.MatchString(p.Time):
		return "", fmt.Errorf("%w: bad resources", ErrInvalid)
	case p.AxialUnit != "" && p.AxialUnit != "deg" && p.AxialUnit != "rad", p.AxialFrame != "" && p.AxialFrame != "pixel" && p.AxialFrame != "sky",
		p.Weights != "" && !ValidPath(p.Weights), p.AxialUnit == "" && (p.AxialFrame != "" || p.Weights != ""):
		return "", fmt.Errorf("%w: bad orientation declaration", ErrInvalid)
	}
	var args strings.Builder
	keys := make([]string, 0, len(p.Args))
	for k := range p.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		re, ok := argKinds[k]
		if !ok || !re.MatchString(p.Args[k]) {
			return "", fmt.Errorf("%w: bad analysis option %q", ErrInvalid, k)
		}
		fmt.Fprintf(&args, " --%s %s", k, p.Args[k])
	}
	var b strings.Builder
	b.WriteString("#!/bin/bash -l\n")
	fmt.Fprintf(&b, "#SBATCH --job-name=%s\n#SBATCH --nodes=1\n#SBATCH --ntasks=1\n#SBATCH --cpus-per-task=%d\n#SBATCH --mem=%s\n#SBATCH --time=%s\n", p.Name, p.CPUs, p.Mem, p.Time)
	b.WriteString("#SBATCH --output=%x-%j.out\n#SBATCH --error=%x-%j.err\n\n")
	b.WriteString("# Rendered by Vesqira. This exact text is what is submitted. Analyses run here, on a compute node, never on the login node.\n")
	b.WriteString("set -euo pipefail\n")
	fmt.Fprintf(&b, "out=%s\nmkdir -p \"$out\"\n", p.OutDir)
	ref := ""
	if p.Op == "align" {
		ref = " --reference " + p.Reference
	}
	if p.AxialUnit != "" {
		fmt.Fprintf(&args, " --axial-unit %s", p.AxialUnit)
		if p.AxialFrame != "" {
			fmt.Fprintf(&args, " --axial-frame %s", p.AxialFrame)
		}
		if p.Weights != "" {
			fmt.Fprintf(&args, " --weights %s", p.Weights)
		}
		args.WriteString(" --rel-out \"$out/result.reliability.fits\"")
	}
	fmt.Fprintf(&b, "%s -P \"%s/analysis.py\" %s --input %s%s%s --out \"$out/result.fits\" > \"$out/result.json\"\n", p.Python, p.ScriptDir, p.Op, p.Input, ref, args.String())
	return b.String(), nil
}
