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
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
)

// envSpecFor reads the output folder and the input file out of the config text the job embeds ([input] output_dir and
// input_file), so the preamble describes the files this very script will use.
func envSpecFor(template, repoDir, config, expect string, hashInput bool) EnvSpec {
	_, out := ConfigPaths(config)
	in := InputPathOf(template, config) // a VGT config's input_file, a ROHSA config's input
	return EnvSpec{RepoDir: repoDir, OutputDir: out, InputFile: in, ExpectCommit: expect, HashInput: hashInput}
}

// ConfigPaths returns [input] input_file and output_dir from a config text, or "" for one that is missing or not a string.
func ConfigPaths(config string) (inputFile, outputDir string) {
	var cfg struct {
		Input struct {
			InputFile string `toml:"input_file"`
			OutputDir string `toml:"output_dir"`
		} `toml:"input"`
	}
	if _, err := toml.Decode(config, &cfg); err != nil {
		return "", ""
	}
	return cfg.Input.InputFile, cfg.Input.OutputDir
}

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
	// AutoImport asks for the finished job's results to be imported as a run by itself, once, when it completes (VGT and ROHSA
	// jobs only). AutoImportTried is set when that attempt was claimed, so it never runs twice and a failure is not retried.
	AutoImport      bool `json:"autoImport"`
	AutoImportTried bool `json:"-"`
	// Usage is what the scheduler reported the job used when it ended (ADR 0045), or nil while it has not ended or when
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
	// RerunMode is how a draft made with "run again with..." relates to its parent run: strict, best_effort or adapted
	// (ADR 0054). ExpectCommit is the parent's revision a strict draft was rendered to insist on.
	RerunMode    string `json:"rerunMode,omitempty"`
	ExpectCommit string `json:"expectCommit,omitempty"`
	// InputRerun is set when the draft runs another run's configuration on a different input cube; BaseConfigText then already
	// names the new input (see inputrerun.go).
	InputRerun *InputRerun `json:"inputRerun,omitempty"`
	// HashInput: the script hashes every byte of the input file before the program starts (set for a configuration run on
	// another cube, and kept by sweeps drafted from one, so their script is what their settings render).
	HashInput bool `json:"hashInput,omitempty"`
}

// VGTRequest returns the stored request, or false for a job that has none (not VGT, or made before it was recorded).
func (j Job) VGTRequest() (VGTRequest, bool) { return j.configRequest("vgt") }

// ROHSARequest is the same record for a ROHSA job: the config text it started from (before the settings and the output
// folder were applied), the settings it changed and the resources it asked for. A ROHSA job drafted before it kept its
// settings (it ran a config by path) has none.
func (j Job) ROHSARequest() (VGTRequest, bool) { return j.configRequest("rohsa") }

// ConfigRequest is the stored request of a VGT or ROHSA job, or false for a job that has none.
func (j Job) ConfigRequest() (VGTRequest, bool) { return j.configRequestAny() }

// configRequestAny is the stored request of a VGT or ROHSA job.
func (j Job) configRequestAny() (VGTRequest, bool) {
	if r, ok := j.configRequest("vgt"); ok {
		return r, true
	}
	return j.configRequest("rohsa")
}

// RerunMode is how the job repeats its parent run (strict, best_effort or adapted), or "" for a job that repeats none.
func (j Job) RerunMode() string {
	r, _ := j.configRequestAny()
	return r.RerunMode
}

func (j Job) configRequest(template string) (VGTRequest, bool) {
	var r VGTRequest
	if j.Template != template || j.Request == "" || json.Unmarshal([]byte(j.Request), &r) != nil {
		return VGTRequest{}, false
	}
	return r, true
}

// Lineage ties a run to the job that produced it.
type Lineage struct {
	JobID       int64
	ParentRunID *int64
	// ConfigHash is the configuration hash of the job's request ("" when it kept none); InputRerun is set for a job drafted
	// with "run this configuration on another cube".
	ConfigHash string
	InputRerun *InputRerun
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
	// ExpectCommit makes the job stop before the program starts unless the code folder is at this revision with no changed
	// tracked file (a strict rerun, ADR 0054).
	ExpectCommit string
	// HashInput makes the job hash every byte of its input file before the program starts (a configuration run on another
	// cube; the run then records input.full_sha256 as the job's own observation).
	HashInput bool
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
	b.WriteString(EnvPreamble(envSpecFor("vgt", p.RepoDir, p.Config, p.ExpectCommit, p.HashInput)))
	b.WriteString("srun --ntasks=1 julia --project=. --threads=\"$threads\" main.jl \"$cfg\"\ndate --iso-8601=seconds\n")
	return b.String(), nil
}

// ROHSASettings are the fields of the ROHSA job template.
type ROHSASettings struct {
	Name       string
	RepoDir    string // the ROHSA-LMC checkout on the host (github.com/ISM-NU/ROHSA-LMC), which holds the ROHSA sources and rohsa/cli/preprocess.jl
	ConfigPath string // the config on the host the settings started from (recorded; the job runs Config)
	Config     string // the full ROHSA config text, written into the job directory by the script
	CPUs       int
	Mem        string
	Time       string
	Partition  string
	Account    string
	// ExpectCommit and HashInput: as for VGTSettings.
	ExpectCommit string
	HashInput    bool
}

// RenderROHSA renders the batch script for one ROHSA run, following the pipeline's own ROHSA cluster script: write the
// config (text embedded here, as the VGT template does, so the script read is the config that runs), build the OpenMP
// Fortran source when it is newer than the binary, then run the preprocessing command on that config with one OpenMP
// thread per allocated CPU. The results go where the config's output_dir says, which Vesqira sets per job. They are not
// imported automatically: a ROHSA fit is read from local files only (ADR 0046).
func RenderROHSA(p ROHSASettings) (string, error) {
	switch {
	case !nameRe.MatchString(p.Name):
		return "", fmt.Errorf("%w: the job name may use letters, digits, . _ - (up to 40)", ErrInvalid)
	case !ValidPath(p.RepoDir), p.ConfigPath != "" && !ValidPath(p.ConfigPath):
		return "", fmt.Errorf("%w: the folder and the config must be absolute paths without spaces or quotes", ErrInvalid)
	case len(p.Config) == 0 || len(p.Config) > MaxScript/2 || strings.Contains(p.Config, configEnd) || strings.ContainsRune(p.Config, 0):
		return "", fmt.Errorf("%w: the config text is empty, too large or malformed", ErrInvalid)
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
	b.WriteString("jobdir=\"$SLURM_SUBMIT_DIR\"\nconfig=\"$jobdir/config.toml\"\n")
	fmt.Fprintf(&b, "cat > \"$config\" <<'%s'\n%s\n%s\n\n", configEnd, strings.TrimRight(p.Config, "\n"), configEnd)
	fmt.Fprintf(&b, "cd %s\n", p.RepoDir)
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
	// after the module and build lines above: the environment recorded is the one the program starts in
	b.WriteString(EnvPreamble(envSpecFor("rohsa", p.RepoDir, p.Config, p.ExpectCommit, p.HashInput)))
	b.WriteString("srun --ntasks=1 julia --project=. rohsa/cli/preprocess.jl --config \"$config\"\ndate --iso-8601=seconds\n")
	return b.String(), nil
}

// ---- editing a config.toml: only a small, typed set of keys ----

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
	if err == nil {
		err = schema.Scope(db, "jobs")
	}
	if err == nil {
		err = schema.AddColumn(db, "jobs", "request_ref", "TEXT NOT NULL DEFAULT ''")
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

type execer interface {
	Exec(string, ...any) (sql.Result, error)
}

func insertDraft(db execer, j Job) (int64, error) {
	if len(j.Script) == 0 || len(j.Script) > MaxScript || !strings.HasPrefix(j.Script, "#!") {
		return 0, fmt.Errorf("%w: the script must start with #! and be at most %d bytes", ErrInvalid, MaxScript)
	}
	if !nameRe.MatchString(j.Name) {
		return 0, fmt.Errorf("%w: bad job name", ErrInvalid)
	}
	res, err := db.Exec(`INSERT INTO jobs(host_id,name,template,script,script_hash,state,detail,slurm_id,work_dir,output_dir,created_at,params,request,parent_run_id,note,auto_import) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.HostID, j.Name, j.Template, j.Script, Hash(j.Script), StateDraft, "", "", NewWorkDir(), j.OutputDir, time.Now().Unix(), j.Params, j.Request, j.ParentRunID, j.Note, j.AutoImport)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) CreateDraft(j Job) (Job, error) {
	id, err := insertDraft(s.db, j)
	if err != nil {
		return Job{}, err
	}
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

// BeginSubmit moves a draft to "submitting" before sbatch runs. Only one caller can win, so a double click or a retry cannot start
// a second submission; and only for scriptHash, so an edit landing between reading the job and this update loses.
func (s *Store) BeginSubmit(id int64, scriptHash string) (bool, error) {
	res, err := s.db.Exec(`UPDATE jobs SET state=?, detail='submitting: waiting for sbatch to answer' WHERE id=? AND state=? AND script_hash=?`, StateSubmitting, id, StateDraft, scriptHash)
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
// VGT or ROHSA job with an output folder that has not become a run, and nobody claimed it before. One caller can win, once, for the
// life of the catalog, so a restart or a second refresh never imports twice and a failed import is not retried.
func (s *Store) ClaimAutoImport(id int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE jobs SET auto_import_tried=1 WHERE id=? AND auto_import=1 AND auto_import_tried=0 AND template IN ('vgt','rohsa') AND output_dir<>'' AND run_id IS NULL AND state=?`, id, StateCompleted)
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
	rows, err := s.db.Query(`SELECT run_id, id, parent_run_id, template, request FROM jobs WHERE run_id IS NOT NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Lineage{}
	for rows.Next() {
		var run, id int64
		var parent sql.NullInt64
		var template, request string
		if err := rows.Scan(&run, &id, &parent, &template, &request); err != nil {
			return nil, err
		}
		l := Lineage{JobID: id}
		if (template == "vgt" || template == "rohsa") && request != "" {
			if req, ok := (Job{Template: template, Request: request}).ConfigRequest(); ok {
				l.ConfigHash, _ = ConfigHash(template, req)
				l.InputRerun = req.InputRerun
			}
		}
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
	// AxialSkyFrame is the celestial frame a sky-frame map states its angles in when that is not its grid's ("icrs" or "galactic").
	AxialSkyFrame string
	// Masks to apply (statistics and moment maps, ADR 0064): a JSON list of {path, type, policy[, weight]}, each path a mask file on this
	// host. Corrections are the researcher's WCS and unit corrections for the input files (ADR 0061), a JSON object {path: {hash, bunit,
	// wcs}} keyed by Input, Reference or Weights. Both land in the script inside single quotes: RenderAnalysis checks their shape and
	// refuses a single quote, so nothing in them can end the quote.
	Masks, Corrections string
	// Masks carried into the result of a cutout, slab, align or convolve (ADR 0055, ADR 0064 addendum): Carry is a JSON list of {path, type,
	// out} (a mask file on this host, its type, and where its carried copy is written) and MissingOut is where the result's own no-value
	// mask goes. Every out is a fixed name chosen by the API in OutDir; RenderAnalysis refuses any other folder or name.
	Carry, MissingOut string
}

// maskSpecKeys are the only keys a mask spec in a job script may carry: no name, rule or meaning (free text stays in the catalog).
var maskSpecKeys = map[string]bool{"path": true, "type": true, "policy": true, "weight": true}

// inertQuoted reports whether a JSON text can stand inside single quotes in the script: valid JSON, one line, no single quote.
func inertQuoted(text string) bool {
	return json.Valid([]byte(text)) && !strings.ContainsAny(text, "'\n\r\x00")
}

// checkJobMasks validates the Masks of an analysis job.
func checkJobMasks(p AnalysisSettings) error {
	if p.Masks == "" {
		return nil
	}
	if p.Op != "stats" && p.Op != "moment" {
		return fmt.Errorf("%w: masks are applied by statistics and moment maps", ErrInvalid)
	}
	var specs []map[string]any
	if !inertQuoted(p.Masks) || json.Unmarshal([]byte(p.Masks), &specs) != nil || len(specs) == 0 || len(specs) > 16 {
		return fmt.Errorf("%w: bad masks", ErrInvalid)
	}
	for _, sp := range specs {
		for k := range sp {
			if !maskSpecKeys[k] {
				return fmt.Errorf("%w: bad masks", ErrInvalid)
			}
		}
		path, _ := sp["path"].(string)
		typ, _ := sp["type"].(string)
		pol, _ := sp["policy"].(string)
		if !ValidPath(path) || !wordRe.MatchString(typ) || (pol != "exclude" && pol != "keep" && pol != "weight") {
			return fmt.Errorf("%w: bad masks", ErrInvalid)
		}
		if w, ok := sp["weight"]; ok {
			if f, isNum := w.(float64); !isNum || f < 0 || f > 1 {
				return fmt.Errorf("%w: bad masks", ErrInvalid)
			}
		}
	}
	return nil
}

// carryNameRe is the only shape of a file a job writes a mask to: the names the API makes (mask-carried-<dataset>-<type>-<hex>.fits and
// mask-missing-<dataset>-missing-<hex>.fits), always directly in the job's own output folder.
var carryNameRe = regexp.MustCompile(`^mask-(carried|missing)-[0-9]{1,18}-[a-z_]{1,32}-[0-9a-f]{8}\.fits$`)

// CarryOutOK reports whether out is a mask file the job may write: directly in dir, with a name of the fixed shape and of the given kind.
func CarryOutOK(out, dir, kind string) bool {
	if !ValidPath(out) || path.Dir(out) != dir {
		return false
	}
	b := path.Base(out)
	return carryNameRe.MatchString(b) && strings.HasPrefix(b, "mask-"+kind+"-")
}

// checkJobCarry validates the Carry and MissingOut of an analysis job.
func checkJobCarry(p AnalysisSettings) error {
	if p.Carry == "" && p.MissingOut == "" {
		return nil
	}
	if p.Op != "cutout" && p.Op != "slab" && p.Op != "align" && p.Op != "convolve" {
		return fmt.Errorf("%w: masks are carried by a cutout, a slab, an alignment and a convolution", ErrInvalid)
	}
	if p.MissingOut != "" && !CarryOutOK(p.MissingOut, p.OutDir, "missing") {
		return fmt.Errorf("%w: bad no-value mask file", ErrInvalid)
	}
	if p.Carry == "" {
		return nil
	}
	var specs []map[string]any
	if !inertQuoted(p.Carry) || json.Unmarshal([]byte(p.Carry), &specs) != nil || len(specs) == 0 || len(specs) > 16 {
		return fmt.Errorf("%w: bad masks to carry", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, sp := range specs {
		for k := range sp {
			if k != "path" && k != "type" && k != "out" {
				return fmt.Errorf("%w: bad masks to carry", ErrInvalid)
			}
		}
		src, _ := sp["path"].(string)
		typ, _ := sp["type"].(string)
		out, _ := sp["out"].(string)
		if !ValidPath(src) || !wordRe.MatchString(typ) || typ == "missing" || !CarryOutOK(out, p.OutDir, "carried") || seen[out] || out == p.MissingOut {
			return fmt.Errorf("%w: bad masks to carry", ErrInvalid)
		}
		seen[out] = true
	}
	return nil
}

// checkJobCorrections validates the Corrections of an analysis job: an object keyed by the job's own input files.
func checkJobCorrections(p AnalysisSettings) error {
	if p.Corrections == "" {
		return nil
	}
	var m map[string]map[string]any
	if !inertQuoted(p.Corrections) || json.Unmarshal([]byte(p.Corrections), &m) != nil || len(m) == 0 {
		return fmt.Errorf("%w: bad corrections", ErrInvalid)
	}
	for k, st := range m {
		if k == "" || (k != p.Input && k != p.Reference && k != p.Weights) {
			return fmt.Errorf("%w: corrections name a file the job does not read", ErrInvalid)
		}
		if h, _ := st["hash"].(string); !hexRe.MatchString(h) {
			return fmt.Errorf("%w: bad corrections", ErrInvalid)
		}
	}
	return nil
}

var (
	wordRe = regexp.MustCompile(`^[a-z_]{1,32}$`)
	hexRe  = regexp.MustCompile(`^[0-9a-f]{16,64}$`)
)

var (
	pyRe       = regexp.MustCompile(`^[A-Za-z0-9_./~-]{1,200}$`)
	scriptDirR = regexp.MustCompile(`^\$HOME/\.cache/vesqira/[0-9a-f]{16}$`)
	analysisOp = map[string]bool{"axial_diff": true, "scalar_diff": true, "stats": true, "moment": true, "cutout": true, "slab": true, "align": true, "convolve": true}
	argKinds   = map[string]*regexp.Regexp{
		"order": regexp.MustCompile(`^[012]$`), "chan-start": intRe, "chan-end": intRe, "x0": intRe, "x1": intRe, "y0": intRe, "y1": intRe,
		"threshold": numRe, "target-bmaj": numRe, "target-bmin": numRe, "target-bpa": numRe, "source-bmaj": numRe, "source-bmin": numRe, "source-bpa": numRe,
		"source-beam-basis": regexp.MustCompile(`^(vgt_approximate|user)$`), "method": regexp.MustCompile(`^(interp|nearest|exact)$`),
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
	case p.AxialUnit != "" && p.AxialUnit != "deg" && p.AxialUnit != "rad", p.AxialFrame != "" && p.AxialFrame != "pixel" && p.AxialFrame != "sky", p.AxialSkyFrame != "" && p.AxialSkyFrame != "icrs" && p.AxialSkyFrame != "galactic",
		p.AxialSkyFrame != "" && p.AxialFrame != "sky",
		p.Weights != "" && !ValidPath(p.Weights), p.AxialUnit == "" && (p.AxialFrame != "" || p.Weights != ""):
		return "", fmt.Errorf("%w: bad orientation declaration", ErrInvalid)
	}
	if err := checkJobMasks(p); err != nil {
		return "", err
	}
	if err := checkJobCorrections(p); err != nil {
		return "", err
	}
	if err := checkJobCarry(p); err != nil {
		return "", err
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
		if p.AxialSkyFrame != "" {
			fmt.Fprintf(&args, " --axial-sky-frame %s", p.AxialSkyFrame)
		}
		if p.Weights != "" {
			fmt.Fprintf(&args, " --weights %s", p.Weights)
		}
		args.WriteString(" --rel-out \"$out/result.reliability.fits\"")
	}
	if p.Masks != "" {
		fmt.Fprintf(&args, " --masks '%s'", p.Masks) // checked above: valid JSON, no single quote, no newline
	}
	if p.Corrections != "" {
		fmt.Fprintf(&args, " --corrections '%s'", p.Corrections)
	}
	if p.Carry != "" {
		fmt.Fprintf(&args, " --carry-masks '%s'", p.Carry)
	}
	if p.MissingOut != "" {
		fmt.Fprintf(&args, " --missing-out %s", p.MissingOut)
	}
	fmt.Fprintf(&b, "%s -P \"%s/analysis.py\" %s --input %s%s%s --out \"$out/result.fits\" > \"$out/result.json\"\n", p.Python, p.ScriptDir, p.Op, p.Input, ref, args.String())
	return b.String(), nil
}
