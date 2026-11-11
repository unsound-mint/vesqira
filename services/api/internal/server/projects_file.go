package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/discover"
)

// localDirty counts the changed and untracked files of a git work tree on this computer, without trusting the folder:
// no fsmonitor (it would run a program named by the repository's own config), no optional locks (status must not
// rewrite the index), a time limit and a cap on the output. .vesqira/ is not counted, so writing the project file does
// not make a project dirty. Any failure is "unknown", never zero.
func localDirty(root string) func(ctx context.Context) (int, bool) {
	return func(ctx context.Context) (int, bool) {
		bin, err := exec.LookPath("git")
		if err != nil {
			return 0, false
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, "-c", "core.fsmonitor=false", "-C", root, "status", "--porcelain", "--untracked-files=normal", "--", ".", ":(exclude).vesqira")
		cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin", "HOME=" + os.Getenv("HOME"), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}
		var out cappedOut
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			return 0, false
		}
		return bytes.Count(out.b.Bytes(), []byte{'\n'}), true
	}
}

// cappedOut keeps at most 1 MiB of output.
type cappedOut struct{ b bytes.Buffer }

func (l *cappedOut) Write(p []byte) (int, error) {
	if room := (1 << 20) - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}

var errProjectFile = errors.New("project file")

// projectFileDir and Path are where a local project keeps its file.
func projectFilePaths(root string) (dir, file string) {
	dir = filepath.Join(root, ".vesqira")
	return dir, filepath.Join(dir, "project.toml")
}

// writeProjectFile replaces the project file of a local project with data. Neither .vesqira nor the file may be a
// symbolic link (a link could send the write anywhere), the content goes to a temporary file in the same folder and is
// renamed into place, so a reader never sees half a file. Vesqira never runs git here: staging and committing are the
// researcher's.
func writeProjectFile(root string, data []byte) error {
	dir, file := projectFilePaths(root)
	if fi, err := os.Lstat(dir); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("%w: .vesqira is not a plain folder", errProjectFile)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(dir, 0o755); err != nil {
			return err
		}
	} else {
		return err
	}
	if fi, err := os.Lstat(file); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: project.toml is not a plain file", errProjectFile)
	}
	return writeFileAtomic(root, filepath.ToSlash(filepath.Join(filepath.Base(dir), filepath.Base(file))), data, 0o644)
}

// readProjectFile reads the project file through the scanner's file interface (the same for a host).
func readProjectFile(fsys discover.FS) (pf discover.ProjectFile, exists bool, text string, err error) {
	data, truncated, rerr := fsys.ReadFile(discover.ProjectFilePath, discover.MaxProjectFile)
	if rerr != nil {
		return pf, false, "", nil // no file (or none that can be read from inside the folder)
	}
	if truncated {
		return pf, true, "", errors.New("project.toml is too large to read")
	}
	pf, err = discover.ParseProjectFile(data)
	return pf, true, strings.TrimRight(string(data), "\n") + "\n", err
}
