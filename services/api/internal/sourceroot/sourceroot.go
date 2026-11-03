// Package sourceroot authorizes local paths against configured source roots.
package sourceroot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrOutsideRoots = errors.New("path is outside the configured source roots")

// Roots is a set of canonical (symlink-resolved, absolute) directories.
type Roots []string

// New canonicalizes each root. A root that does not exist or is not a directory is an error.
func New(dirs []string) (Roots, error) {
	var out Roots
	for _, d := range dirs {
		abs, err := filepath.Abs(d)
		if err != nil {
			return nil, err
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("source root %q: %w", d, err)
		}
		fi, err := os.Stat(real)
		if err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("source root %q is not a directory", d)
		}
		out = append(out, real)
	}
	return out, nil
}

// Resolve returns the canonical path of a regular file that lies inside one of the roots.
// Symlinks are resolved first, so a link that points outside a root is rejected; containment is
// decided with filepath.Rel, never a string prefix (/data must not authorize /data2).
func (r Roots) Resolve(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) || !filepath.IsAbs(p) {
		return "", errors.New("path must be absolute")
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", errors.New("file not found")
	}
	for _, root := range r {
		rel, err := filepath.Rel(root, real)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		fi, err := os.Stat(real)
		if err != nil {
			return "", errors.New("file not found")
		}
		if !fi.Mode().IsRegular() {
			return "", errors.New("not a regular file")
		}
		return real, nil
	}
	return "", ErrOutsideRoots
}
