package hosts

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
)

// Editing a researcher's own text file on a host (ADR 0029, host files). This is the generalisation of the write path of
// ADR 0039 to an arbitrary existing file: no Vesqira marker (the file is the researcher's), and in its place optimistic
// concurrency on the sha256 the file had when it was opened. The same rules hold: values are single-quoted arguments used
// only inside double quotes, the script is one line with no single quote, backslash or exclamation mark (csh and fish),
// nothing is written unless a staged copy has the expected byte count and sha256, links are never followed, and the
// existing file's mode is kept.

// MaxEditBytes bounds a file the editor opens or saves on a host.
const MaxEditBytes = 2 << 20

// editOutputCap is how much of a read the local side keeps: the file, the framing lines and a login banner.
const editOutputCap = MaxEditBytes + 4096

// EditFile is a file on a host as it was read or written. Mtime is nanoseconds since the epoch as a decimal string, from
// the host's whole seconds (portable stat); it is informational, the sha256 is what a save is checked against.
type EditFile struct {
	Content []byte
	Size    int64
	Mtime   string
	Sha256  string
}

// ErrEditPath is matched with errors.Is when a path cannot be edited on the host at all (outside its roots, not a plain
// name, characters a login shell would misread); nothing was sent.
var ErrEditPath = errors.New("edit on host")

// editTarget turns an absolute file path into the declared root that holds it (the longest one), the folder below that
// root and the file name, which is how the scripts walk it: the root is resolved with pwd -P on the host and every
// component below it is checked, so a link or ".." can never lead out of the root.
func editTarget(h Host, p string) (root, rel, name string, err error) {
	bad := func(why string) error { return fmt.Errorf("%w: %s", ErrEditPath, why) }
	switch {
	case unsafeText(p):
		return "", "", "", bad("the path contains a NUL, newline, carriage return or backslash")
	case len(p) > 2000:
		return "", "", "", bad("the path is too long")
	case !h.InRoots(p):
		return "", "", "", bad("the file is not inside a source root of the host")
	}
	for _, r := range h.Roots {
		tr := strings.TrimRight(r, "/")
		if strings.HasPrefix(p, tr+"/") && len(r) > len(root) {
			root = r
		}
	}
	if root == "" {
		return "", "", "", bad("the file is not inside a source root of the host") // p is a root itself, not a file below one
	}
	tr := strings.TrimRight(root, "/")
	dir, name := path.Split(p)
	rel = strings.Trim(strings.TrimPrefix(strings.TrimRight(dir, "/"), tr), "/")
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return "", "", "", bad("the file name is not one plain name")
	}
	return root, rel, name, nil
}

// editWalk resolves the target without creating anything and refuses (printing the reason) a link or a file where a
// folder should be, a root escape, a missing file, a file that is not a plain readable file. It sets $t and $real.
const editWalk = `
p=$rootp;
if [ -n "$rel" ]; then
  oldifs=$IFS; IFS=/; set -f;
  for c in $rel; do
    [ -z "$c" ] && continue;
    p="$p/$c";
    if [ -L "$p" ]; then refuse link; fi;
    if [ -e "$p" ]; then [ -d "$p" ] || refuse notdir; else refuse absent; fi;
  done;
  IFS=$oldifs; set +f;
fi;
real=$(cd -- "$p" 2>/dev/null && pwd -P) || refuse root;
case "$real" in "$rootp"|"$rootp"/*) ;; *) refuse escape;; esac;
t="$real/$name";
if [ -L "$t" ]; then refuse link; fi;
[ -e "$t" ] || refuse absent;
[ -f "$t" ] || refuse notfile;
[ -r "$t" ] || refuse unreadable;
mt() { stat -c %Y "$t" 2>/dev/null || stat -f %m "$t" 2>/dev/null || echo 0; };
emit() {
  n=$(wc -c < "$t" | tr -d " ");
  [ "$n" -le "$cap" ] || refuse toolarge;
  s=$(sha < "$t");
  echo "vesqira-edit: $1";
  cat -- "$t";
  echo;
  echo "vesqira-edit: end $s $n $(mt)";
};
`

// editReadScript runs under sh with four arguments: root, rel, name and the byte cap. It prints
// "vesqira-edit: refused:<reason>", or "vesqira-edit: begin", the file, a newline and "vesqira-edit: end <sha256> <bytes> <mtime>".
var editReadScript = oneLine(`
tag=vesqira-edit; root=$1; rel=$2; name=$3; cap=$4; td=;
` + scriptCommon + editWalk + `
emit begin;
`)

// editWriteScript runs under sh with seven arguments: root, rel, name, length, sha256 of the new content, the sha256 the
// file had when it was opened, and the byte cap. The content is on stdin. It only ever replaces an existing plain file:
// (1) resolve and check the target as the read does (a missing file is "absent", never created); (2) if the file's sha256
// is not the opened one, print "vesqira-edit: conflict", the current file and its end line, and change nothing; (3) stage
// the stdin in a private 0700 folder inside the target's own folder and refuse unless its byte count and sha256 are the
// expected ones; (4) check once more that the target is still a plain file with the opened sha256; (5) copy the old file
// with cp -p (so the mode is kept; portable POSIX, no stat), put the new bytes into the copy, verify them again, and
// rename it over the target. Success prints "vesqira-edit: ok <mtime>". A read-only file is refused. The window between
// step 4 and the rename is not closed (another process of the same account could write the file in between), the same
// limit as ADR 0039 and as the local editor.
var editWriteScript = oneLine(`
tag=vesqira-edit; root=$1; rel=$2; name=$3; want_len=$4; want_sha=$5; prior=$6; cap=$7; td=;
` + scriptCommon + editWalk + `
conflict() { if [ -n "$td" ]; then rm -rf -- "$td"; td=; fi; emit conflict; exit 0; };
[ "$(sha < "$t")" = "$prior" ] || conflict;
[ -w "$t" ] || refuse readonly;
td=$(mktemp -d "$real/.vesqira-edit.XXXXXX" 2>/dev/null) || refuse mktemp;
cat > "$td/in" || refuse io;
got_len=$(wc -c < "$td/in" | tr -d " ");
[ "$got_len" = "$want_len" ] || refuse truncated;
[ "$(sha < "$td/in")" = "$want_sha" ] || refuse truncated;
if [ -L "$t" ]; then refuse link; fi;
[ -f "$t" ] || refuse notfile;
[ "$(sha < "$t")" = "$prior" ] || conflict;
cp -p -- "$t" "$td/out" 2>/dev/null || refuse io;
cat "$td/in" > "$td/out" || refuse io;
[ "$(sha < "$td/out")" = "$want_sha" ] || refuse io;
mv -f -- "$td/out" "$t" || refuse io;
rm -rf -- "$td"; td=;
echo "vesqira-edit: ok $(mt)";
`)

const (
	editBegin    = "vesqira-edit: begin\n"
	editConflict = "vesqira-edit: conflict\n"
	editEnd      = "vesqira-edit: end "
)

// parseEditFile reads the file a script printed between its first begin or conflict line and its last end line, and
// verifies the bytes against the host's own sha256 and byte count, so a cut stream is an error, never a short file.
func parseEditFile(out []byte) (f EditFile, conflict bool, err error) {
	s := string(out)
	b, e := strings.Index(s, editBegin), strings.LastIndex(s, "\n"+editEnd)
	skip := len(editBegin)
	if c := strings.Index(s, editConflict); c >= 0 && (b < 0 || c < b) {
		b, conflict, skip = c, true, len(editConflict)
	}
	fields := strings.Fields(strings.TrimPrefix(lastLine(out), editEnd))
	if b < 0 || e < b+skip || len(fields) != 3 || !strings.HasPrefix(lastLine(out), editEnd) {
		return EditFile{}, false, errors.New("the host answered unexpectedly")
	}
	content := []byte(s[b+skip : e])
	n, nerr := strconv.Atoi(fields[1])
	if nerr != nil || n != len(content) || SHA256Hex(content) != fields[0] {
		return EditFile{}, false, errors.New("the file on the host was not read intact")
	}
	mtime := "0"
	if sec, merr := strconv.ParseInt(fields[2], 10, 64); merr == nil && sec > 0 {
		mtime = strconv.FormatInt(sec*int64(time.Second), 10)
	}
	return EditFile{Content: content, Size: int64(len(content)), Mtime: mtime, Sha256: fields[0]}, conflict, nil
}

func (m *Manager) runEdit(ctx context.Context, h Host, script, tag string, stdin []byte, args ...string) ([]byte, error) {
	inner := "sh -c " + quote(script) + " " + tag
	for _, a := range args {
		inner += " " + quote(a)
	}
	return m.runShellCap(ctx, h, inner, stdin, 60*time.Second, editOutputCap)
}

func refusedReason(out []byte) (string, bool) {
	last := lastLine(out)
	const pre = "vesqira-edit: refused:"
	if strings.HasPrefix(last, pre) {
		return strings.TrimPrefix(last, pre), true
	}
	return "", false
}

// ReadText reads the existing plain file p (absolute, clean, inside a declared root of the host) for the editor: at most
// MaxEditBytes, creating nothing, refusing links, a path that leaves the root and anything that is not a regular file.
// It returns *WriteRefused when the host's checks refused ("absent", "link", "notfile", "toolarge", ...). The text rules
// (extension, UTF-8, FITS) are the caller's.
func (m *Manager) ReadText(ctx context.Context, h Host, p string) (EditFile, error) {
	root, rel, name, err := editTarget(h, p)
	if err != nil {
		return EditFile{}, err
	}
	out, err := m.runEdit(ctx, h, editReadScript, "vesqira-edit", nil, root, rel, name, strconv.Itoa(MaxEditBytes))
	if err != nil {
		return EditFile{}, err
	}
	if why, ok := refusedReason(out); ok {
		return EditFile{}, &WriteRefused{Reason: why}
	}
	f, conflict, err := parseEditFile(out)
	if err != nil || conflict {
		return EditFile{}, errors.New("the host answered unexpectedly to a read")
	}
	return f, nil
}

// ReplaceText replaces the existing file p with data when its sha256 is still prior (what the editor opened). Never
// creates a file. When the file differs it changes nothing and returns the current file as conflict. It returns
// *WriteRefused when the host refused. On success saved carries the new sha256, size and mtime.
func (m *Manager) ReplaceText(ctx context.Context, h Host, p string, data []byte, prior string) (saved EditFile, conflict *EditFile, err error) {
	root, rel, name, err := editTarget(h, p)
	if err != nil {
		return EditFile{}, nil, err
	}
	switch {
	case len(data) > MaxEditBytes:
		return EditFile{}, nil, errors.New("edit on host: the file is too large")
	case len(prior) != 64 || strings.Trim(prior, "0123456789abcdef") != "":
		return EditFile{}, nil, errors.New("edit on host: the sha256 of the opened file is required")
	}
	sum := SHA256Hex(data)
	out, err := m.runEdit(ctx, h, editWriteScript, "vesqira-edit", data, root, rel, name, strconv.Itoa(len(data)), sum, prior, strconv.Itoa(MaxEditBytes))
	if err != nil {
		return EditFile{}, nil, err
	}
	if why, ok := refusedReason(out); ok {
		return EditFile{}, nil, &WriteRefused{Reason: why}
	}
	last := lastLine(out)
	if strings.HasPrefix(last, "vesqira-edit: ok ") {
		mtime := "0"
		if sec, merr := strconv.ParseInt(strings.TrimPrefix(last, "vesqira-edit: ok "), 10, 64); merr == nil && sec > 0 {
			mtime = strconv.FormatInt(sec*int64(time.Second), 10)
		}
		return EditFile{Content: data, Size: int64(len(data)), Mtime: mtime, Sha256: sum}, nil, nil
	}
	if f, isConflict, perr := parseEditFile(out); perr == nil && isConflict {
		return EditFile{}, &f, nil
	}
	return EditFile{}, nil, errors.New("the host answered unexpectedly to a save")
}
