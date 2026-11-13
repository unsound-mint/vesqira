package jobs

import (
	"fmt"
	"strings"
)

// Environment capture (ADR 0054). A job script gets a short preamble that observes where the job runs and writes
// vesqira-env.json. The preamble only reads: it never commits, stashes, fetches, checks out or otherwise changes the code
// folder (git runs with --no-optional-locks, so even the index is not refreshed), and it can never fail the job.
//
// The same collector, in "probe" mode, is what a strict rerun runs on the host before submitting, to compare the code folder
// as it is now with the run being repeated.

// envCollector defines ve_collect. The caller defines ve_emit KEY STATUS VALUE first. Every reason is plain words in the
// character set the reader accepts (runs.ParseEnv); every value is cleaned to that set and cut at 200 characters, so the
// output is bounded and cannot carry markup, quotes or a separator.
//
// Python is not observed: no template runs it, so the fact is reported unresolved with that reason rather than recording an
// interpreter the job never used.
const envCollector = `ve_clean() { tr -cd 'A-Za-z0-9 ._:/+=,@%()-' | head -c 200; }
ve_ok() { ve_emit "$1" verified "$(printf '%s' "$2" | ve_clean)"; }
ve_no() { ve_emit "$1" unresolved "$(printf '%s' "$2" | ve_clean)"; }
ve_no_all() { r="$1"; shift; for k in "$@"; do ve_no "$k" "$r"; done; }
ve_t() { if command -v timeout >/dev/null 2>&1; then timeout 30 "$@"; else "$@"; fi; }
ve_sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum | cut -c1-64; elif command -v shasum >/dev/null 2>&1; then shasum -a 256 | cut -c1-64; fi; }
ve_code() {
  if ! command -v git >/dev/null 2>&1; then ve_no_all "git is not installed on this node" code.commit code.dirty code.untracked_files code.diff_sha256; return; fi
  if ! c=$(ve_t git --no-optional-locks -C "$1" rev-parse --verify HEAD 2>/dev/null) || [ -z "$c" ]; then
    ve_no_all "the code folder is not a repository that git can read" code.commit code.dirty code.untracked_files code.diff_sha256; return
  fi
  ve_ok code.commit "$c"
  if s=$(ve_t git --no-optional-locks -C "$1" status --porcelain --untracked-files=no 2>/dev/null); then
    if [ -z "$s" ]; then ve_ok code.dirty no; ve_ok code.diff_sha256 none
    else
      ve_ok code.dirty yes
      if d=$(set -o pipefail; ve_t git --no-optional-locks -C "$1" diff --no-ext-diff --no-textconv HEAD 2>/dev/null | ve_sha) && [ -n "$d" ]; then ve_ok code.diff_sha256 "$d"; else ve_no code.diff_sha256 "git diff did not finish"; fi
    fi
  else ve_no code.dirty "git status failed"; ve_no code.diff_sha256 "git status failed"; fi
  if u=$(ve_t git --no-optional-locks -C "$1" status --porcelain --untracked-files=normal 2>/dev/null); then ve_ok code.untracked_files "$(printf '%s\n' "$u" | grep -c '^??')"; else ve_no code.untracked_files "git status failed"; fi
}
ve_manifest() {
  if [ -f "$1/Manifest.toml" ]; then
    h=$(ve_sha < "$1/Manifest.toml")
    if [ -n "$h" ]; then ve_ok julia.manifest_sha256 "$h"; else ve_no julia.manifest_sha256 "no sha256 tool on this node"; fi
  else ve_no julia.manifest_sha256 "no Manifest.toml in the code folder"; fi
}
ve_machine() {
  if command -v julia >/dev/null 2>&1; then
    v=$(ve_t julia --version 2>/dev/null | head -n 1)
    if [ -n "$v" ]; then ve_ok julia.version "$v"; else ve_no julia.version "julia did not report a version"; fi
  else ve_no julia.version "julia is not on PATH at this point of the script"; fi
  ve_no python.version "the job template does not run Python"
  if type module >/dev/null 2>&1; then
    l=$(module list 2>&1 | tr '\n' ' ')
    [ -n "$l" ] || l="module list printed nothing"
    ve_ok modules.list "$l"
    h=$(printf '%s' "$l" | ve_sha)
    if [ -n "$h" ]; then ve_ok modules.sha256 "$h"; else ve_no modules.sha256 "no sha256 tool on this node"; fi
  else ve_no_all "there is no module command in this shell" modules.list modules.sha256; fi
  n=$(hostname 2>/dev/null)
  if [ -n "$n" ]; then ve_ok node.name "$n"; else ve_no node.name "hostname gave nothing"; fi
  m=$(grep -m1 -i '^model name' /proc/cpuinfo 2>/dev/null | sed 's/^[^:]*: *//')
  [ -n "$m" ] || m=$(lscpu 2>/dev/null | grep -m1 -i 'model name' | sed 's/^[^:]*: *//')
  if [ -n "$m" ]; then ve_ok cpu.model "$m"; else ve_no cpu.model "this node does not report a CPU model"; fi
}
ve_input() {
  case "$1" in
    /*) ;;
    *) ve_no_all "the config names no input file as a plain absolute path" input.size_bytes input.mtime_epoch; return ;;
  esac
  if [ ! -f "$1" ]; then ve_no_all "the input file is not readable on this node" input.size_bytes input.mtime_epoch; return; fi
  z=$(stat -c %s "$1" 2>/dev/null || stat -f %z "$1" 2>/dev/null)
  t=$(stat -c %Y "$1" 2>/dev/null || stat -f %m "$1" 2>/dev/null)
  if [ -n "$z" ]; then ve_ok input.size_bytes "$z"; else ve_no input.size_bytes "stat gave no size"; fi
  if [ -n "$t" ]; then ve_ok input.mtime_epoch "$t"; else ve_no input.mtime_epoch "stat gave no modification time"; fi
}
ve_input_full() {
  case "$1" in
    /*) ;;
    *) ve_no input.full_sha256 "the config names no input file as a plain absolute path"; return ;;
  esac
  if [ ! -f "$1" ]; then ve_no input.full_sha256 "the input file is not readable on this node"; return; fi
  z0=$(stat -c %s "$1" 2>/dev/null || stat -f %z "$1" 2>/dev/null); t0=$(stat -c %Y "$1" 2>/dev/null || stat -f %m "$1" 2>/dev/null)
  h=$(ve_sha < "$1")
  z1=$(stat -c %s "$1" 2>/dev/null || stat -f %z "$1" 2>/dev/null); t1=$(stat -c %Y "$1" 2>/dev/null || stat -f %m "$1" 2>/dev/null)
  if [ -z "$h" ]; then ve_no input.full_sha256 "no sha256 tool on this node, or the file could not be read"
  elif [ -z "$z0" ] || [ "$z0" != "$z1" ] || [ "$t0" != "$t1" ]; then ve_no input.full_sha256 "the input file changed while it was being read, or its size and time could not be read"
  else ve_ok input.full_sha256 "$h"; fi
}
ve_collect() {
  if [ -z "$1" ] || [ ! -d "$1" ]; then
    ve_no_all "the code folder is not readable on this node" code.commit code.dirty code.untracked_files code.diff_sha256 julia.manifest_sha256
  else ve_code "$1"; ve_manifest "$1"; fi
  ve_input "$2"
  if [ "$3" != probe ] && [ "$4" = full ]; then ve_input_full "$2"; fi
  if [ "$3" != probe ]; then ve_machine; fi
}
`

// EnvSpec is what the preamble is rendered for. Every path must pass ValidPath; one that does not is left out and the
// fact it would have supplied is reported unresolved.
type EnvSpec struct {
	RepoDir   string // the code checkout the job runs from
	OutputDir string // the job's output folder; the file is copied there if the folder exists
	InputFile string // the input file named in the job's config, if it is a plain absolute path
	// ExpectCommit, for a strict rerun, is the commit of the run being repeated: the job stops before the science starts if
	// the code folder is at another commit or has changed tracked files.
	ExpectCommit string
	// HashInput asks the job to read every byte of the input file and record its SHA-256 (input.full_sha256), once, before the
	// program starts. It costs one pass over the file on the compute node, so it is on only for a run that needs the input's
	// identity as the job saw it (a configuration run on another cube). Never in probe mode.
	HashInput bool
	// NoJulia leaves out the Julia probes (the Manifest.toml hash and the julia version) of a tool whose profile declares none; the
	// facts are then reported unresolved with that reason, never recorded for an interpreter the job does not use.
	NoJulia bool
}

// collectorFor is the collector text for a spec: the full one, or without the Julia probes.
func collectorFor(noJulia bool) string {
	if !noJulia {
		return envCollector
	}
	c := strings.Replace(envCollector, "  else ve_code \"$1\"; ve_manifest \"$1\"; fi\n", "  else ve_code \"$1\"; ve_no julia.manifest_sha256 \"this tool's profile declares no Julia probe\"; fi\n", 1)
	const julia = "  if command -v julia >/dev/null 2>&1; then\n    v=$(ve_t julia --version 2>/dev/null | head -n 1)\n    if [ -n \"$v\" ]; then ve_ok julia.version \"$v\"; else ve_no julia.version \"julia did not report a version\"; fi\n  else ve_no julia.version \"julia is not on PATH at this point of the script\"; fi\n"
	return strings.Replace(c, julia, "  ve_no julia.version \"this tool's profile declares no Julia probe\"\n", 1)
}

func plainPath(p string) string {
	if ValidPath(p) {
		return p
	}
	return ""
}

// EnvPreamble is the text inserted into a job script before the program starts. It expects $jobdir to be the job's own
// directory (SLURM_SUBMIT_DIR). The record is written there first and copied next to the outputs (also on exit, because the
// pipeline may create its output folder only after it starts): that copy is what Vesqira reads.
func EnvPreamble(e EnvSpec) string {
	var b strings.Builder
	b.WriteString("\n# --- Vesqira: record the environment this job runs in (ADR 0054). Read-only commands; nothing in the code folder is\n")
	b.WriteString("# written, and a failure here never fails the job. The record is vesqira-env.json next to the outputs. ---\n")
	fmt.Fprintf(&b, "ve_out=\"%s\"\nve_file=\"$jobdir/vesqira-env.json\"\n", plainPath(e.OutputDir))
	b.WriteString("ve_copy() { [ -n \"$ve_out\" ] && [ -d \"$ve_out\" ] && [ ! -L \"$ve_out\" ] && [ ! -L \"$ve_out/vesqira-env.json\" ] && [ -f \"$ve_file\" ] && cp -f \"$ve_file\" \"$ve_out/vesqira-env.json\" 2>/dev/null; return 0; }\n")
	b.WriteString("(\n  set +e +u\n  ve_emit() { printf '%s|%s|%s\\n' \"$1\" \"$2\" \"$3\"; }\n")
	b.WriteString(collectorFor(e.NoJulia))
	hash := ""
	if e.HashInput {
		hash = " full"
	}
	fmt.Fprintf(&b, "  ve_collect \"%s\" \"%s\" job%s\n", plainPath(e.RepoDir), plainPath(e.InputFile), hash)
	b.WriteString(") > \"$jobdir/vesqira-env.lines\" 2>/dev/null || true\n")
	b.WriteString("{\n  printf '{\\n  \"record\": \"vesqira-env\",\\n  \"version\": 1,\\n  \"slurm_job_id\": \"%s\",\\n  \"facts\": {' \"$(printf '%s' \"${SLURM_JOB_ID:-}\" | tr -cd '0-9')\"\n")
	b.WriteString("  awk -F'|' 'NF>=3 { v=$3; for (i=4;i<=NF;i++) v=v \"|\" $i; printf \"%s\\n    \\\"%s\\\": {\\\"status\\\": \\\"%s\\\", \\\"%s\\\": \\\"%s\\\"}\", (n++ ? \",\" : \"\"), $1, $2, ($2==\"verified\" ? \"value\" : \"reason\"), v }' \"$jobdir/vesqira-env.lines\"\n")
	b.WriteString("  printf '\\n  }\\n}\\n'\n} > \"$ve_file\" 2>/dev/null || true\nve_copy\ntrap ve_copy EXIT\n")
	if c := e.ExpectCommit; c != "" {
		repo := plainPath(e.RepoDir)
		b.WriteString("# Strict rerun: stop here, before the science starts, if the code is not the code of the run this repeats.\n")
		fmt.Fprintf(&b, "ve_now=\"$(git --no-optional-locks -C \"%s\" rev-parse --verify HEAD 2>/dev/null || true)\"\n", repo)
		fmt.Fprintf(&b, "if [ \"$ve_now\" != \"%s\" ]; then echo \"strict rerun stopped: the code folder is at ${ve_now:-an unknown commit}, not at %s\" >&2; exit 3; fi\n", c, c)
		fmt.Fprintf(&b, "if [ -n \"$(git --no-optional-locks -C \"%s\" status --porcelain --untracked-files=no 2>/dev/null)\" ]; then echo \"strict rerun stopped: tracked files in the code folder have changed\" >&2; exit 3; fi\n", repo)
	}
	b.WriteString("# --- end of the environment record ---\n")
	return b.String()
}

// EnvProbe is the command and the script (on stdin) that read the code folder, the Manifest.toml and the input file the way
// the preamble does, without a job. The folder and file must pass ValidPath.
func EnvProbe(repoDir, inputFile string) (cmd, script string, err error) {
	if !ValidPath(repoDir) {
		return "", "", fmt.Errorf("%w: the code folder must be an absolute path without spaces or quotes", ErrInvalid)
	}
	in := "-"
	if inputFile != "" {
		if !ValidPath(inputFile) {
			return "", "", fmt.Errorf("%w: the input file must be an absolute path without spaces or quotes", ErrInvalid)
		}
		in = inputFile
	}
	return "bash -s -- " + repoDir + " " + in,
		"ve_emit() { printf '%s|%s|%s\\n' \"$1\" \"$2\" \"$3\"; }\n" + envCollector + "ve_collect \"$1\" \"$2\" probe\n", nil
}
