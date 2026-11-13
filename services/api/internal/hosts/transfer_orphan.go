package hosts

import (
	"os"
	"path/filepath"
	"regexp"
)

// While it receives a file, rsync writes it under a hidden temporary name in the destination folder, ".<name>.<6 random
// characters>", and moves it into PartialDir only when it is told to stop (SIGTERM, a dropped connection). A process that dies
// without that chance (SIGKILL, the kernel's OOM killer, a power cut) leaves the temporary file behind, and the next attempt
// would start the file from zero while the orphan sits there for good.

// AdoptOrphanTemp finds the orphaned temporary files of exactly this destination name in dir and puts the biggest one into
// PartialDir (unless a partial file at least as big is already there), so the next attempt resumes from it. Anything else
// matching the pattern for this name is removed. What may be touched is fixed by the name: regular files (never a link or a
// folder) called exactly ".<name>." plus six letters or digits, directly in dir. No other file is read, moved or deleted. The
// content needs no trust: rsync only uses a partial file as a basis, matching it block by block against the source, and the
// finished file is hashed on both ends afterwards. It reports how many files it moved or removed.
func AdoptOrphanTemp(dir, name string) (handled int, err error) {
	re, err := regexp.Compile(`^\.` + regexp.QuoteMeta(name) + `\.[A-Za-z0-9]{6}$`)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var best string
	var bestSize int64 = -1
	var all []string
	for _, e := range entries {
		if !re.MatchString(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		fi, lerr := os.Lstat(p)
		if lerr != nil || !fi.Mode().IsRegular() {
			continue
		}
		all = append(all, p)
		if fi.Size() > bestSize {
			best, bestSize = p, fi.Size()
		}
	}
	if best == "" {
		return 0, nil
	}
	partial := filepath.Join(dir, PartialDir, name)
	if pi, perr := os.Lstat(partial); perr == nil && (!pi.Mode().IsRegular() || pi.Size() >= bestSize) {
		best = "" // the partial file already there is at least as far along (or is not a file we may replace)
	}
	if best != "" {
		if err := os.MkdirAll(filepath.Join(dir, PartialDir), 0o755); err != nil {
			return 0, err
		}
		if err := os.Rename(best, partial); err != nil {
			return 0, err
		}
		handled++
	}
	for _, p := range all {
		if p == best {
			continue
		}
		if os.Remove(p) == nil {
			handled++
		}
	}
	return handled, nil
}
