// Package sourceroot authorizes local paths against configured source roots.
package sourceroot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotAllowed is the single refusal for anything that is not a regular file inside a source root.
var ErrNotAllowed = errors.New("path is not an existing file inside the configured source roots")

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
		return "", ErrNotAllowed
	}
	// Every refusal returns the same error, so a caller cannot probe which paths exist outside the roots.
	real, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", ErrNotAllowed
	}
	for _, root := range r {
		rel, err := filepath.Rel(root, real)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		fi, err := os.Stat(real)
		if err != nil || !fi.Mode().IsRegular() {
			return "", ErrNotAllowed
		}
		return real, nil
	}
	return "", ErrNotAllowed
}

// ResolveDir is Resolve for a directory: the canonical path of a directory that lies inside (or is) one of the roots.
func (r Roots) ResolveDir(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) || !filepath.IsAbs(p) {
		return "", ErrNotAllowed
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", ErrNotAllowed
	}
	if !r.contains(real) {
		return "", ErrNotAllowed
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", ErrNotAllowed
	}
	return real, nil
}

// contains reports whether a canonical path is a root or lies below one.
func (r Roots) contains(real string) bool {
	for _, root := range r {
		rel, err := filepath.Rel(root, real)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

// ResolveDirOK reports whether a canonical directory is already a root or lies below one.
func (r Roots) ResolveDirOK(real string) bool { return r.contains(real) }
