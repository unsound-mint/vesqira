package discover

import (
	"path"
	"regexp"
	"strings"
	"time"
)

// SkipNames are folders that are never walked, in any project.
var SkipNames = []string{".git", ".venv", "venv", "node_modules", "__pycache__", ".cache", ".tox", ".mypy_cache", ".ipynb_checkpoints", ".vesqira"}

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

// Run records, in the order they are tried.
type recordKind string

const (
	recVGT        recordKind = "vgt"        // run_metadata.toml
	recProvenance recordKind = "provenance" // provenance.json
	recRunJSON    recordKind = "run-json"   // run.json
	recParams     recordKind = "params"     // params.yaml|params.json with metrics.json
	recMLflow     recordKind = "mlflow"     // meta.yaml with a run_id
	recManual     recordKind = "manual"     // marked by the researcher
)

// detectRecord finds the run record among the names in a folder. params.* counts only next to metrics.json.
func detectRecord(names map[string]bool) (file string, kind recordKind, ok bool) {
	switch {
	case names["run_metadata.toml"]:
		return "run_metadata.toml", recVGT, true
	case names["provenance.json"]:
		return "provenance.json", recProvenance, true
	case names["run.json"]:
		return "run.json", recRunJSON, true
	case names["params.yaml"] && names["metrics.json"]:
		return "params.yaml", recParams, true
	case names["params.json"] && names["metrics.json"]:
		return "params.json", recParams, true
	case names["meta.yaml"]:
		return "meta.yaml", recMLflow, true // confirmed by its content (a run_id) when read
	}
	return "", "", false
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
