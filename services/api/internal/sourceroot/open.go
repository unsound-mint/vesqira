package sourceroot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

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
	fd, err := openNoFollow(real, false)
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

// OpenDir is OpenFile for a directory: the folder named by a resolved path, opened with no link followed, so a listing reads the
// folder that was checked and not whatever a link swapped in since. Entries are then read and examined through the descriptor
// (ReadDir, Readlinkat, Fstatat), never by joining names onto the path again. Every refusal is ErrNotAllowed.
func OpenDir(p string) (*os.File, error) {
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
	fd, err := openNoFollow(real, true)
	if err != nil {
		return nil, ErrNotAllowed
	}
	return os.NewFile(uintptr(fd), real), nil
}

// FileID is what the operating system calls a file: the device and the inode. It is recorded when a file is registered and compared
// when the file is used again, so a different file under the same name is noticed even when its size and time happen to match.
// The zero value means "not recorded".
type FileID struct{ Dev, Ino int64 }

// IDOf is the FileID of a stat result; ok is false when the platform gives none.
func IDOf(fi os.FileInfo) (FileID, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return FileID{}, false
	}
	return FileID{Dev: int64(st.Dev), Ino: int64(st.Ino)}, true
}

// Recorded reports whether the id holds a recorded file (an inode is never 0).
func (f FileID) Recorded() bool { return f.Ino != 0 }

func openNoFollow(real string, dir bool) (int, error) {
	extra := unix.O_NONBLOCK
	if dir {
		extra = unix.O_DIRECTORY
	}
	if useOpenat2 {
		fd, err := unix.Openat2(unix.AT_FDCWD, real, &unix.OpenHow{
			Flags:   uint64(unix.O_RDONLY | unix.O_CLOEXEC | extra),
			Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		})
		if err == nil {
			return fd, nil
		}
		if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EPERM) && !errors.Is(err, unix.E2BIG) {
			return -1, err // ELOOP, ENOENT, ...: a real refusal, not a kernel without openat2
		}
	}
	return walkNoFollow(real, dir)
}

// walkNoFollow opens each component of the absolute, resolved path with O_NOFOLLOW from the directory fd of the one before.
func walkNoFollow(real string, dir bool) (int, error) {
	parts := strings.Split(strings.Trim(real, "/"), "/")
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for i, name := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if i < len(parts)-1 || dir {
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
