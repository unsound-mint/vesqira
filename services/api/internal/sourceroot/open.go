package sourceroot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// useOpenat2 is set false in tests that exercise the component walk on a kernel that has openat2.
var useOpenat2 = true

// afterResolve runs between the resolve and the open; tests swap a component here to make the race deterministic.
var afterResolve func(real string)

// OpenFile opens a local source file for reading without following a symbolic link after the path has been resolved
// (ADR 0050, section 8: the authorise-then-open window). The path is resolved as Resolve does, so an honest link whose target
// lies where the caller allowed (a data folder reached through a link on HPC scratch) keeps working; the resolved path is then
// opened with no link followed, by openat2 (RESOLVE_NO_SYMLINKS | RESOLVE_NO_MAGICLINKS) where the kernel has it and otherwise by
// walking the components from "/" with O_NOFOLLOW. A component swapped for a link between the caller's containment check and
// this open makes the open fail. The result is a regular file (a FIFO or device is refused), already open: read from it, do
// not reopen the name. Every refusal is ErrNotAllowed.
//
// Containment in the source roots is the caller's check (Roots.Resolve); pass its result, or any path whose containment the
// caller has judged on the resolved form.
func OpenFile(p string) (*os.File, error) {
	if p == "" || strings.ContainsRune(p, 0) || !filepath.IsAbs(p) {
		return nil, ErrNotAllowed
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return nil, ErrNotAllowed
	}
	if afterResolve != nil {
		afterResolve(real)
	}
	fd, err := openNoFollow(real)
	if err != nil {
		return nil, ErrNotAllowed
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return nil, ErrNotAllowed
	}
	_ = unix.SetNonblock(fd, false) // opened non-blocking so a FIFO could not hang; a regular file is read the ordinary way
	return os.NewFile(uintptr(fd), real), nil
}

func openNoFollow(real string) (int, error) {
	if useOpenat2 {
		fd, err := unix.Openat2(unix.AT_FDCWD, real, &unix.OpenHow{
			Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK,
			Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		})
		if err == nil {
			return fd, nil
		}
		if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EPERM) && !errors.Is(err, unix.E2BIG) {
			return -1, err // ELOOP, ENOENT, ...: a real refusal, not a kernel without openat2
		}
	}
	return walkNoFollow(real)
}

// walkNoFollow opens each component of the absolute, resolved path with O_NOFOLLOW from the directory fd of the one before.
func walkNoFollow(real string) (int, error) {
	parts := strings.Split(strings.Trim(real, "/"), "/")
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for i, name := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		} else {
			flags |= unix.O_NONBLOCK
		}
		next, err := unix.Openat(fd, name, flags, 0)
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}
