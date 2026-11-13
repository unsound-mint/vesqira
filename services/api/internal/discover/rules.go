package discover

import (
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// SkipNames are folders that are never walked, in any project.
var SkipNames = []string{".git", ".venv", "venv", "node_modules", "site-packages", ".julia", "__pycache__", ".cache", ".tox", ".mypy_cache", ".ipynb_checkpoints", ".vesqira", ".vesqira-partial"}

// ToolEnvNames are folders that hold a tool's own files, never the project's data. ".julia-*" (a depot per project) is
// matched by IsToolEnvName, not listed here. Skipped folders of these names, and folders recognised by IsToolEnvDir, are
// counted together as tool environments in the scan summary.
var ToolEnvNames = []string{".venv", "venv", "node_modules", "site-packages", "__pycache__", ".julia"}

// IsToolEnvName says whether a folder name alone marks a tool environment.
func IsToolEnvName(name string) bool {
	for _, n := range ToolEnvNames {
		if name == n {
			return true
		}
	}
	return strings.HasPrefix(name, ".julia-")
}

// IsToolEnvDir says, from the names a folder holds, whether it is a Python venv (pyvenv.cfg), a conda environment
// (conda-meta/) or a Julia depot (registries/ and packages/). dirs and files are the names of its subfolders and files.
// The host's worker applies the same rule (plane_worker.py, _tool_env).
func IsToolEnvDir(dirs, files map[string]bool) bool {
	return files["pyvenv.cfg"] || dirs["conda-meta"] || (dirs["registries"] && dirs["packages"])
}

// NeverFiles and NeverDirs are name patterns (matched case-insensitively against one name) for things that are never
// read, statted or descended into: a blinding key or anything its owner marked as not to be opened. They are sent to
// a host's worker as they are, so both sides apply the same rule.
var (
	NeverFiles = []string{"*do_not_open*"}
	NeverDirs  = []string{"*do_not_open*", "blind*key*"}
)

func matchAny(patterns []string, name string) bool {
	n := strings.ToLower(name)
	for _, p := range patterns {
		if ok, _ := path.Match(p, n); ok {
			return true
		}
	}
	return false
}

// MatchGlob matches a project.toml skip pattern against a relative path: "dir/**" skips a folder and everything under
// it, "**/name" matches the name at any depth, a pattern without a slash matches a name at any depth, otherwise the
// pattern is matched against the whole relative path.
func MatchGlob(pattern, rel string) bool {
	pattern = strings.TrimSuffix(strings.TrimPrefix(pattern, "./"), "/")
	if pattern == "" {
		return false
	}
	if p, ok := strings.CutSuffix(pattern, "/**"); ok {
		return rel == p || strings.HasPrefix(rel, p+"/")
	}
	if p, ok := strings.CutPrefix(pattern, "**/"); ok {
		pattern = p
		if ok, _ := path.Match(pattern, path.Base(rel)); ok {
			return true
		}
		return false
	}
	if !strings.Contains(pattern, "/") {
		ok, _ := path.Match(pattern, path.Base(rel))
		return ok
	}
	ok, _ := path.Match(pattern, rel)
	return ok
}

// A data format whose file carries its own header. Viewer says whether Vesqira can show it.
type format struct {
	Name   string
	Viewer bool
}

var fitsSuffixes = []string{".fits", ".fit", ".fits.gz", ".fz"}

func fitsName(lower string) bool {
	for _, s := range fitsSuffixes {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	return false
}

// dataFormat says what a file name is, among the self-describing formats. A .zarr is a folder: see isZarrDir.
func dataFormat(name string) (format, bool) {
	l := strings.ToLower(name)
	switch {
	case fitsName(l):
		return format{"FITS", true}, true
	case strings.HasSuffix(l, ".ome.tif") || strings.HasSuffix(l, ".ome.tiff"):
		return format{"OME-TIFF", false}, true
	case strings.HasSuffix(l, ".tif") || strings.HasSuffix(l, ".tiff"):
		return format{"TIFF", false}, true
	case strings.HasSuffix(l, ".czi"):
		return format{"CZI", false}, true
	case strings.HasSuffix(l, ".nd2"):
		return format{"ND2", false}, true
	case strings.HasSuffix(l, ".nd"):
		return format{"ND", false}, true
	case strings.HasSuffix(l, ".h5") || strings.HasSuffix(l, ".hdf5"):
		return format{"HDF5", false}, true
	}
	return format{}, false
}

// LeafDirSuffixes are folders that are one data entry, not walked (a Zarr store is a folder of chunk files).
var LeafDirSuffixes = []string{".zarr"}

func isZarrDir(name string) bool { return strings.HasSuffix(strings.ToLower(name), ".zarr") }

func isArchive(name string) bool {
	l := strings.ToLower(name)
	if fitsName(l) {
		return false // a compressed FITS file is data, not an archive
	}
	for _, s := range []string{".zip", ".7z", ".tar", ".tgz", ".tbz2", ".txz"} {
		if strings.HasSuffix(l, s) {
			return true
		}
	}
	return strings.Contains(l, ".tar.")
}

var jobExt = []string{".slurm", ".sbatch", ".pbs"}

func hasJobExt(name string) bool {
	l := strings.ToLower(name)
	for _, e := range jobExt {
		if strings.HasSuffix(l, e) {
			return true
		}
	}
	return false
}

var jobDirs = map[string]bool{"hpc": true, "slurm": true, "jobs": true, "scripts": true}

func underAny(rel string, names map[string]bool) bool {
	parts := strings.Split(rel, "/")
	for _, p := range parts[:len(parts)-1] {
		if names[strings.ToLower(p)] {
			return true
		}
	}
	return false
}

func isConfigFile(name string) bool {
	l := strings.ToLower(name)
	return strings.HasSuffix(l, ".toml") || strings.HasSuffix(l, ".yaml") || strings.HasSuffix(l, ".yml") || strings.HasSuffix(l, ".json")
}

func underConfigDir(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, p := range parts[:len(parts)-1] {
		l := strings.ToLower(p)
		if l == "configs" || strings.HasPrefix(l, "config") {
			return true
		}
	}
	return false
}

// dataRoles are the folder names of the data/raw|interim|processed|external convention. A role says where a file sits
// in the research workflow; it never says what the values are, what unit they have or what they mean.
var dataRoles = map[string]bool{"raw": true, "interim": true, "processed": true, "external": true}

// roleOf returns the role of a file or folder path under a data/<role> layout, or "".
func roleOf(rel string) string {
	parts := strings.Split(rel, "/")
	for i := 0; i+1 < len(parts); i++ {
		if strings.EqualFold(parts[i], "data") && dataRoles[strings.ToLower(parts[i+1])] {
			return strings.ToLower(parts[i+1])
		}
	}
	return ""
}

// RoleOf is roleOf for callers outside the scanner (a path relative to the project folder, slash-separated).
func RoleOf(rel string) string { return roleOf(rel) }

// RoleIsReadOnly says whether files in a data role are source data Vesqira never writes: raw and external.
func RoleIsReadOnly(role string) bool { return role == "raw" || role == "external" }

// Run records, in the order they are tried.
type recordKind string

const (
	recROHSA      recordKind = "rohsa"      // a profile's run kind that mergeJobRecords knows (ADR 0046)
	recProvenance recordKind = "provenance" // provenance.json
	recRunJSON    recordKind = "run-json"   // run.json
	recParams     recordKind = "params"     // params.yaml|params.json with metrics.json
	recMLflow     recordKind = "mlflow"     // meta.yaml with a run_id
	recManual     recordKind = "manual"     // marked by the researcher
)

// candidate is a file that may be the run record of a folder. A profile's candidate is decided by the record's content: the
// file name alone only nominates it (ADR 0068).
type candidate struct {
	file string
	kind recordKind
	prof *runs.Profile // nil for the records that are not a tool's
}

// detectRecords lists the candidates for a folder's run record in the order they are tried: the tools' profiles, then
// provenance.json, run.json, params.* (only next to metrics.json) and meta.yaml (confirmed by its content, a run_id, when read).
func detectRecords(reg *runs.Registry, names map[string]bool) []candidate {
	var out []candidate
	for _, c := range reg.Candidates(names) {
		out = append(out, candidate{c.File, recordKind(c.Profile.Kind), c.Profile})
	}
	switch {
	case names["provenance.json"]:
		out = append(out, candidate{file: "provenance.json", kind: recProvenance})
	case names["run.json"]:
		out = append(out, candidate{file: "run.json", kind: recRunJSON})
	case names["params.yaml"] && names["metrics.json"]:
		out = append(out, candidate{file: "params.yaml", kind: recParams})
	case names["params.json"] && names["metrics.json"]:
		out = append(out, candidate{file: "params.json", kind: recParams})
	case names["meta.yaml"]:
		out = append(out, candidate{file: "meta.yaml", kind: recMLflow})
	}
	return out
}

var stampSha = regexp.MustCompile(`^(\d{4})-?(\d{2})-?(\d{2})t(\d{2}):?(\d{2}):?(\d{2})z?_([0-9a-f]{7,40})$`)

// fromFolderName reads "<UTC timestamp>_<short sha>". These are guesses from a name: the caller labels them inferred.
func fromFolderName(name string) (startedAt time.Time, commit string, ok bool) {
	m := stampSha.FindStringSubmatch(strings.ToLower(name))
	if m == nil {
		return time.Time{}, "", false
	}
	t, err := time.Parse("20060102T150405", m[1]+m[2]+m[3]+"T"+m[4]+m[5]+m[6])
	if err != nil {
		return time.Time{}, "", false
	}
	return t.UTC(), m[7], true
}

var resultLike = regexp.MustCompile(`(?i)^(results?|outputs?|runs?|experiments?|out)([_ .-].*)?$`)

func looksLikeResults(name string) bool { return resultLike.MatchString(name) }
