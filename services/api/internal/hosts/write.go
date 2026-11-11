package hosts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// MaxWrite bounds one file written on a host. Both files Vesqira writes there (project.toml, a run record) are small.
const MaxWrite = 1 << 20

// WriteSpec is one file Vesqira writes on a host: Name inside Root/Rel, never anywhere else.
//
// Root must be one of the host's source roots (or inside one): the project folder, or the root that holds a job's output
// folder. Rel is a relative folder under Root (created when missing, one component at a time, refusing links), "" or "."
// for Root itself. Marker is a text every file Vesqira writes carries and Data must contain: an existing file at the
// target is replaced only if it contains Marker, so a file the researcher or another program made is never overwritten.
type WriteSpec struct {
	Root, Rel, Name, Marker string
	Data                    []byte
}

// WriteRefused is a write the host refused by its own checks (nothing was changed): Reason is one of the words below.
type WriteRefused struct{ Reason string }

func (e *WriteRefused) Error() string {
	switch e.Reason {
	case "root":
		return "the folder is not reachable on the host"
	case "link":
		return "a folder or file on the way is a symbolic link; Vesqira does not write through links"
	case "notdir":
		return "a name on the way is a file, not a folder"
	case "escape":
		return "the folder resolves outside the folder Vesqira may write in"
	case "notfile":
		return "the target exists and is not a plain file"
	case "foreign":
		return "a file with that name already exists there and was not written by Vesqira"
	default:
		return "the host could not write the file (" + e.Reason + ")"
	}
}

// ErrForeign is matched with errors.Is when the target exists and was not written by Vesqira.
var ErrForeign = errors.New("foreign file")

func (e *WriteRefused) Is(target error) bool { return target == ErrForeign && e.Reason == "foreign" }

// validate checks everything that can be checked before a round trip. The host repeats the checks that need its
// filesystem (links, resolved location, the marker of an existing file).
func (w WriteSpec) validate(h Host) error {
	bad := func(why string) error { return fmt.Errorf("write on host: %s", why) }
	switch {
	case !h.InRoots(w.Root):
		return bad("the folder is not inside a source root of the host")
	case strings.ContainsRune(w.Root, 0) || strings.ContainsRune(w.Rel, 0) || strings.ContainsRune(w.Name, 0) || strings.ContainsRune(w.Marker, 0):
		return bad("NUL in a name")
	case w.Name == "" || w.Name == "." || w.Name == ".." || strings.Contains(w.Name, "/") || len(w.Name) > 200:
		return bad("the file name is not one plain name")
	case w.Marker == "" || strings.ContainsAny(w.Marker, "\n\r") || len(w.Marker) > 200:
		return bad("the marker must be one short line")
	case !bytes.Contains(w.Data, []byte(w.Marker)):
		return bad("the content does not carry the marker, so Vesqira could not recognise it later")
	case len(w.Data) > MaxWrite:
		return bad("the file is too large")
	}
	if w.Rel != "" && w.Rel != "." {
		if path.IsAbs(w.Rel) {
			return bad("the folder must be relative")
		}
		for _, c := range strings.Split(w.Rel, "/") {
			if c == ".." || c == "" || c == "." {
				return bad("the folder must be a clean relative path")
			}
		}
	}
	return nil
}

// writeScript runs on the host under sh with four arguments: root, rel, name, marker. Every caller-supplied value arrives
// as a positional argument and is only ever used inside double quotes, so no name is parsed as shell syntax. Refusals
// print "vesqira-write: refused:<reason>" and change nothing; success prints "vesqira-write: ok". The data is on stdin.
//
// The checks, in order: the root resolves (pwd -P); each component of rel is created one at a time, refusing a link or a
// file; the resolved folder is still inside the resolved root; the target is not a link, and if it exists it is a plain
// file whose first MiB contains the marker. The content is written to a temporary file in the same folder and renamed
// into place (atomic on one filesystem). The window between the marker check and the rename is not closed: another
// process of the same account could create a file there in between. Both folders are the researcher's own and Vesqira
// holds no lock on them, so that is accepted and said in ADR 0039.
const writeScript = `root=$1; rel=$2; name=$3; marker=$4
umask 022
refuse() { echo "vesqira-write: refused:$1"; exit 0; }
rootp=$(cd -- "$root" 2>/dev/null && pwd -P) || refuse root
p=$rootp
if [ -n "$rel" ] && [ "$rel" != . ]; then
  oldifs=$IFS; IFS=/; set -f
  for c in $rel; do
    [ -z "$c" ] && continue
    p="$p/$c"
    [ -L "$p" ] && refuse link
    if [ -e "$p" ]; then [ -d "$p" ] || refuse notdir
    else mkdir -- "$p" 2>/dev/null || refuse mkdir; fi
  done
  IFS=$oldifs; set +f
fi
real=$(cd -- "$p" 2>/dev/null && pwd -P) || refuse root
case "$real" in "$rootp"|"$rootp"/*) ;; *) refuse escape;; esac
t="$real/$name"
[ -L "$t" ] && refuse link
if [ -e "$t" ]; then
  [ -f "$t" ] || refuse notfile
  head -c 1048576 -- "$t" 2>/dev/null | grep -qF -- "$marker" || refuse foreign
fi
tmp=$(mktemp "$real/.vesqira-write.XXXXXX" 2>/dev/null) || refuse mktemp
if cat > "$tmp" && chmod 644 "$tmp" && mv -f -- "$tmp" "$t"; then echo "vesqira-write: ok"; else rm -f -- "$tmp"; refuse io; fi
`

// WriteFile writes one file on a host through the same ssh path every other remote command uses (RunShell: the user's
// own ssh configuration and agent, no new credential). It returns *WriteRefused when the host's checks refused.
func (m *Manager) WriteFile(ctx context.Context, h Host, w WriteSpec) error {
	if err := w.validate(h); err != nil {
		return err
	}
	inner := "sh -c " + quote(writeScript) + " vesqira-write " + quote(w.Root) + " " + quote(w.Rel) + " " + quote(w.Name) + " " + quote(w.Marker)
	out, err := m.RunShell(ctx, h, inner, w.Data, 45*time.Second)
	if err != nil {
		return err
	}
	// a login shell may print a banner first: the answer is the last line
	lines := strings.Split(strings.TrimRight(string(out), "\r\n"), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	switch {
	case last == "vesqira-write: ok":
		return nil
	case strings.HasPrefix(last, "vesqira-write: refused:"):
		return &WriteRefused{Reason: strings.TrimPrefix(last, "vesqira-write: refused:")}
	}
	return errors.New("the host answered unexpectedly to a write")
}
