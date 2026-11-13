package discover

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// ProfilesDir is where a project keeps profiles of tools Vesqira does not ship (ADR 0068, slice 6): one JSON file per tool.
const ProfilesDir = ".vesqira/profiles"

// MaxProfileFiles bounds how many files of the folder are read; the rest are reported as not read.
const MaxProfileFiles = 16

var profileFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}\.json$`)

// ProfileFile is one entry of the profiles folder: what it holds, or why it was not read.
type ProfileFile struct {
	Rel  string // project-relative, slash-separated
	Data []byte // nil when Why is set
	Why  string
}

// ReadProfileFiles lists the files of a project's profiles folder through the same FS the scanner uses, so a link out of the folder,
// a name that leaves it, a file that is not a regular file and a file over runs.MaxProjectProfileBytes are refused by the same
// rules as every other read. A missing folder is not an error: most projects have none. Each unreadable file is reported with a
// sentence and does not stop the others. Nothing is parsed here.
func ReadProfileFiles(fsys FS) []ProfileFile {
	for _, dir := range []string{".vesqira", ProfilesDir} {
		st, err := fsys.Stat(dir)
		if err != nil {
			return nil
		}
		if st.Link {
			return []ProfileFile{{Rel: dir, Why: dir + " is a link; links are not followed, so no profile was read"}}
		}
		if !st.Dir {
			return nil
		}
	}
	ents, err := fsys.ReadDir(ProfilesDir)
	if err != nil {
		return []ProfileFile{{Rel: ProfilesDir, Why: "the folder cannot be read"}}
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name < ents[j].Name })
	var out []ProfileFile
	for _, e := range ents {
		rel := ProfilesDir + "/" + e.Name
		switch {
		case len(out) >= MaxProfileFiles:
			out = append(out, ProfileFile{Rel: rel, Why: fmt.Sprintf("not read: at most %d profile files are read per project", MaxProfileFiles)})
			continue
		case e.Dir:
			continue // a subfolder is not a profile; nothing in it is read
		case !profileFileName.MatchString(e.Name):
			out = append(out, ProfileFile{Rel: rel, Why: "not read: a profile file is named <tool>.json (letters, digits, '.', '_' and '-')"})
			continue
		}
		st, err := fsys.Stat(rel)
		switch {
		case err != nil:
			out = append(out, ProfileFile{Rel: rel, Why: "cannot be read"})
		case st.Link && (st.LinkOutside || st.LinkDangling):
			out = append(out, ProfileFile{Rel: rel, Why: "a link that leaves the project folder (or leads nowhere) is not followed"})
		case st.Size > runs.MaxProjectProfileBytes:
			out = append(out, ProfileFile{Rel: rel, Why: fmt.Sprintf("larger than %d KiB", runs.MaxProjectProfileBytes>>10)})
		default:
			data, truncated, err := fsys.ReadFile(rel, runs.MaxProjectProfileBytes)
			switch {
			case err != nil:
				out = append(out, ProfileFile{Rel: rel, Why: "cannot be read: " + err.Error()})
			case truncated:
				out = append(out, ProfileFile{Rel: rel, Why: fmt.Sprintf("larger than %d KiB", runs.MaxProjectProfileBytes>>10)})
			default:
				out = append(out, ProfileFile{Rel: rel, Data: data})
			}
		}
	}
	return out
}

// ProfileStatus is what a project's profiles folder gave: the profile each readable file declares, or the sentence that says why a
// file was refused.
type ProfileStatus struct {
	File    string `json:"file"`
	Profile string `json:"profile"` // name@version, empty for a refused file
	Why     string `json:"why,omitempty"`
}

// ProjectRegistry is the program's registry plus every profile file of a project that passes the checks. A file that does not is left
// out and reported; the others still load. Files load in name order, so a name two files both take goes to the first.
func ProjectRegistry(base *runs.Registry, files []ProfileFile) (*runs.Registry, []ProfileStatus) {
	reg := base
	var out []ProfileStatus
	for _, f := range files {
		if f.Why != "" {
			out = append(out, ProfileStatus{File: f.Rel, Why: f.Why})
			continue
		}
		next, p, err := reg.WithProject(f.Data, f.Rel)
		if err != nil {
			out = append(out, ProfileStatus{File: f.Rel, Why: err.Error()})
			continue
		}
		reg = next
		out = append(out, ProfileStatus{File: f.Rel, Profile: p.Label()})
	}
	return reg, out
}
