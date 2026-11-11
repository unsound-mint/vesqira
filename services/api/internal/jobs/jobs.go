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
	return `squeue -h -j ` + slurmID + ` -o %T 2>/dev/null; echo '---'; sacct -n -P -X -j ` + slurmID + ` -o State,ExitCode,Elapsed,Start,End 2>/dev/null`, nil
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
	if err != nil {
		return nil, err
	}
	return &Store{db}, nil
}

const cols = `id,host_id,name,template,script,script_hash,state,detail,slurm_id,work_dir,output_dir,created_at,submitted_at,finished_at,run_id,params,request,parent_run_id,note,record_owed`

func scan(row interface{ Scan(...any) error }) (Job, error) {
	var j Job
	var c int64
	var sub, fin, run, parent sql.NullInt64
	var owed int
	if err := row.Scan(&j.ID, &j.HostID, &j.Name, &j.Template, &j.Script, &j.ScriptHash, &j.State, &j.Detail, &j.SlurmID, &j.WorkDir, &j.OutputDir, &c, &sub, &fin, &run, &j.Params, &j.Request, &parent, &j.Note, &owed); err != nil {
		return Job{}, err
	}
	j.RecordOwed = owed != 0
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
	res, err := s.db.Exec(`INSERT INTO jobs(host_id,name,template,script,script_hash,state,detail,slurm_id,work_dir,output_dir,created_at,params,request,parent_run_id,note) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.HostID, j.Name, j.Template, j.Script, Hash(j.Script), StateDraft, "", "", NewWorkDir(), j.OutputDir, time.Now().Unix(), j.Params, j.Request, j.ParentRunID, j.Note)
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
