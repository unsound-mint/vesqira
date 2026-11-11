package hosts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
)

// MaxWrite bounds one file written on a host. Both files Vesqira writes there (project.toml, a run record) are small.
const MaxWrite = 1 << 20

// MaxRead bounds an existing file read before it is replaced. It is below the 256 KiB a remote command may answer with,
// so the read can never be cut short silently; a larger existing file is refused as "toolarge" and left alone.
const MaxRead = 200000

// ExpectAbsent is WriteSpec.Expect for "the file must not exist when the write happens".
const ExpectAbsent = "-"

// SHA256Hex is the digest WriteSpec.Expect carries.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// WriteSpec is one file Vesqira writes on a host: Name inside Root/Rel, never anywhere else.
//
// Root must be one of the host's source roots (or inside one): the project folder, or the root that holds a job's output
// folder. Rel is a relative folder under Root, "" or "." for Root itself. With Create the missing components of Rel are
// made (one at a time, refusing links) after every other check has passed; without it a missing folder is refused as
// "absent", so a folder somebody else is expected to create (a job's output folder) is never made by Vesqira.
//
// Marker is a text every file Vesqira writes carries and Data must contain. Recognise decides whether an existing file
// at the target is one Vesqira wrote (nil: its first line starts with Marker); only such a file is replaced, so a file the
// researcher or another program made is never overwritten. Data must itself be recognised.
//
// Expect pins what is being replaced. Empty: WriteFile reads the target right now. A caller that read the file earlier and
// built the new content from it passes SHA256Hex of exactly the bytes it read (or ExpectAbsent), and the host refuses
// with "changed" if the file is different at the moment of the write.
type WriteSpec struct {
	Root, Rel, Name, Marker string
	Create                  bool
	Data                    []byte
	Recognise               func(existing []byte) bool
	Expect                  string
}

// WriteRefused is a write the host refused by its own checks (nothing was changed): Reason is one of the words below.
type WriteRefused struct{ Reason string }

func (e *WriteRefused) Error() string {
	switch e.Reason {
	case "root":
		return "the folder is not reachable on the host"
	case "absent":
		return "the folder does not exist on the host (yet)"
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
	case "toolarge":
		return "a file with that name already exists there and is too large to be checked, so it is left as it is"
	case "changed":
		return "the file on the host changed since Vesqira read it, so it was not replaced"
	case "readonly":
		return "the existing file is read-only, so it is left as it is"
	case "truncated":
		return "the content did not arrive intact (the connection was cut?), so nothing was changed"
	case "nosha256":
		return "the host has no sha256sum or shasum, so a write cannot be verified and is not done"
	default:
		return "the host could not write the file (" + e.Reason + ")"
	}
}

// ErrForeign is matched with errors.Is when the target exists and was not written by Vesqira.
var ErrForeign = errors.New("foreign file")

func (e *WriteRefused) Is(target error) bool { return target == ErrForeign && e.Reason == "foreign" }

func firstLineHasPrefix(data []byte, marker string) bool {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	return bytes.HasPrefix(line, []byte(marker))
}

func (w WriteSpec) recognises(b []byte) bool {
	if w.Recognise != nil {
		return w.Recognise(b)
	}
	return firstLineHasPrefix(b, w.Marker)
}

// unsafeText is a character the remote command line cannot carry safely through every login shell: NUL, newline and
// carriage return (csh and fish treat them specially) and the backslash (fish reads it inside single quotes).
func unsafeText(s string) bool { return strings.ContainsAny(s, "\x00\n\r\\") }

// validate checks everything that can be checked before a round trip. The host repeats the checks that need its
// filesystem (links, resolved location, the existing file).
func (w WriteSpec) validate(h Host) error {
	bad := func(why string) error { return fmt.Errorf("write on host: %s", why) }
	switch {
	case !h.InRoots(w.Root):
		return bad("the folder is not inside a source root of the host")
	case unsafeText(w.Root) || unsafeText(w.Rel) || unsafeText(w.Name) || unsafeText(w.Marker):
		return bad("a name contains a NUL, newline, carriage return or backslash")
	case w.Name == "" || w.Name == "." || w.Name == ".." || strings.Contains(w.Name, "/") || len(w.Name) > 200:
		return bad("the file name is not one plain name")
	case w.Marker == "" || len(w.Marker) > 200:
		return bad("the marker must be one short line")
	case !bytes.Contains(w.Data, []byte(w.Marker)) || !w.recognises(w.Data):
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

// oneLine joins the lines of a script into a single line. sshd hands the command to the account's login shell, and csh
// refuses a newline inside single quotes, so the scripts below are one line each (every source line ends in ";" or
// opens or closes a block). They also contain no single quote, backslash or exclamation mark, which csh, tcsh and fish
// read specially even inside single quotes; TestScriptsSurviveAnyLoginShell enforces that.
func oneLine(s string) string {
	var parts []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " ")
}

// scriptCommon is shared by the read and write scripts: the refusal reporter (prefix in $tag), the sha256 filter (sha256sum,
// else shasum -a 256, else a refusal before anything is touched) and the resolved root.
const scriptCommon = `
refuse() { if [ -n "$td" ]; then rm -rf -- "$td"; fi; echo "$tag: refused:$1"; exit 0; };
sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum | cut -d" " -f1; else shasum -a 256 | cut -d" " -f1; fi; };
command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1 || refuse nosha256;
rootp=$(cd -- "$root" 2>/dev/null && pwd -P) || refuse root;
[ "$rel" = . ] && rel=;
`

// writeScript runs on the host under sh with seven arguments: root, rel, name, length, sha256 of the content, the sha256 of
// the file it replaces (or "-" for none), and "1" when missing folders may be created. Every caller-supplied value is only
// ever used inside double quotes, so no name is parsed as shell syntax. Refusals print "vesqira-write: refused:<reason>"
// and success "vesqira-write: ok". The content is on stdin.
//
// Order: (1) resolve the root; walk rel without creating anything, refusing a link, a file where a folder should be, and
// (without create) a missing folder; (2) stage the stdin in a private 0700 folder made inside the deepest existing folder
// and refuse unless its byte count and sha256 are the expected ones, so a stream cut short never reaches the target;
// (3) only now create the missing folders; (4) check the target (not a link, a plain file, its sha256 is the expected
// one, writable); (5) rename into place. A new file gets the account's default mode (umask applies). A replacement is a
// copy of the old file made with cp -p, so it keeps the old mode (portable POSIX, no stat); a read-only file is refused.
// The window between the sha256 check and the rename is not closed (another process of the same account could change
// the file in between); both folders are the researcher's own and Vesqira holds no lock there (ADR 0039).
var writeScript = oneLine(`
tag=vesqira-write; root=$1; rel=$2; name=$3; want_len=$4; want_sha=$5; prior=$6; create=$7; td=;
` + scriptCommon + `
walk() {
  p=$rootp; d=$rootp; miss=;
  if [ -n "$rel" ]; then
    oldifs=$IFS; IFS=/; set -f;
    for c in $rel; do
      [ -z "$c" ] && continue;
      p="$p/$c";
      if [ -L "$p" ]; then refuse link; fi;
      if [ -e "$p" ]; then
        [ -d "$p" ] || refuse notdir;
        d=$p;
      else
        [ "$create" = 1 ] || refuse absent;
        miss=1;
        if [ "$1" = make ]; then mkdir -- "$p" 2>/dev/null || refuse mkdir; d=$p; fi;
      fi;
    done;
    IFS=$oldifs; set +f;
  fi;
};
walk check;
if [ -n "$miss" ]; then [ "$prior" = - ] || refuse changed; fi;
td=$(mktemp -d "$d/.vesqira-write.XXXXXX" 2>/dev/null) || refuse mktemp;
cat > "$td/in" || refuse io;
got_len=$(wc -c < "$td/in" | tr -d " ");
[ "$got_len" = "$want_len" ] || refuse truncated;
[ "$(sha < "$td/in")" = "$want_sha" ] || refuse truncated;
walk make;
real=$(cd -- "$p" 2>/dev/null && pwd -P) || refuse root;
case "$real" in "$rootp"|"$rootp"/*) ;; *) refuse escape;; esac;
t="$real/$name";
if [ -L "$t" ]; then refuse link; fi;
if [ -e "$t" ]; then
  [ -f "$t" ] || refuse notfile;
  if [ "$prior" = - ]; then refuse changed; fi;
  [ "$(sha < "$t")" = "$prior" ] || refuse changed;
  [ -w "$t" ] || refuse readonly;
  cp -p -- "$t" "$td/out" 2>/dev/null || refuse io;
  cat "$td/in" > "$td/out" || refuse io;
else
  [ "$prior" = - ] || refuse changed;
  mv -- "$td/in" "$td/out" || refuse io;
fi;
mv -f -- "$td/out" "$t" || refuse io;
rm -rf -- "$td"; td=;
echo "vesqira-write: ok";
`)

// readScript runs under sh with four arguments: root, rel, name and the byte cap. It performs the same path checks as
// writeScript but creates nothing. It prints "vesqira-read: absent" (the folder or the file is not there),
// "vesqira-read: refused:<reason>", or "vesqira-read: begin", the file, a newline, and "vesqira-read: end <sha256> <bytes>".
var readScript = oneLine(`
tag=vesqira-read; root=$1; rel=$2; name=$3; cap=$4; td=;
` + scriptCommon + `
p=$rootp;
if [ -n "$rel" ]; then
  oldifs=$IFS; IFS=/; set -f;
  for c in $rel; do
    [ -z "$c" ] && continue;
    p="$p/$c";
    if [ -L "$p" ]; then refuse link; fi;
    if [ -e "$p" ]; then [ -d "$p" ] || refuse notdir; else echo "vesqira-read: absent"; exit 0; fi;
  done;
  IFS=$oldifs; set +f;
fi;
real=$(cd -- "$p" 2>/dev/null && pwd -P) || refuse root;
case "$real" in "$rootp"|"$rootp"/*) ;; *) refuse escape;; esac;
t="$real/$name";
if [ -L "$t" ]; then refuse link; fi;
if [ -e "$t" ]; then :; else echo "vesqira-read: absent"; exit 0; fi;
[ -f "$t" ] || refuse notfile;
n=$(wc -c < "$t" | tr -d " ");
[ "$n" -le "$cap" ] || refuse toolarge;
s=$(sha < "$t");
echo "vesqira-read: begin";
cat -- "$t";
echo;
echo "vesqira-read: end $s $n";
`)

// lastLine is the answer of a script: a login shell may print a banner first.
func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimRight(string(out), "\r\n"), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// ReadExisting reads the file Name in Root/Rel on a host for a later WriteFile: at most MaxRead bytes, through the same
// path checks as a write (no link, inside the root), creating nothing. exists is false when the folder or the file is
// not there. It returns *WriteRefused when the host's checks refused (including "toolarge"). The bytes are verified
// against the host's own sha256 and byte count, so a cut stream is an error, never a short file.
func (m *Manager) ReadExisting(ctx context.Context, h Host, root, rel, name string) (data []byte, exists bool, err error) {
	if err := (WriteSpec{Root: root, Rel: rel, Name: name, Marker: "x", Data: []byte("x")}).validate(h); err != nil {
		return nil, false, err
	}
	inner := "sh -c " + quote(readScript) + " vesqira-read " + quote(root) + " " + quote(rel) + " " + quote(name) + " " + strconv.Itoa(MaxRead)
	out, err := m.RunShell(ctx, h, inner, nil, 45*time.Second)
	if err != nil {
		return nil, false, err
	}
	const begin, endMark = "vesqira-read: begin\n", "\nvesqira-read: end "
	last := lastLine(out)
	switch {
	case last == "vesqira-read: absent":
		return nil, false, nil
	case strings.HasPrefix(last, "vesqira-read: refused:"):
		return nil, false, &WriteRefused{Reason: strings.TrimPrefix(last, "vesqira-read: refused:")}
	case strings.HasPrefix(last, strings.TrimPrefix(endMark, "\n")):
		s := string(out)
		b, e := strings.Index(s, begin), strings.LastIndex(s, endMark)
		f := strings.Fields(strings.TrimPrefix(last, strings.TrimPrefix(endMark, "\n")))
		if b < 0 || e < b+len(begin) || len(f) != 2 {
			break
		}
		content := []byte(s[b+len(begin) : e])
		if n, nerr := strconv.Atoi(f[1]); nerr != nil || n != len(content) || SHA256Hex(content) != f[0] {
			return nil, false, errors.New("the file on the host was not read intact")
		}
		return content, true, nil
	}
	return nil, false, errors.New("the host answered unexpectedly to a read")
}

// WriteFile writes one file on a host through the same ssh path every other remote command uses (RunShell: the user's
// own ssh configuration and agent, no new credential). It first reads the existing target (unless spec.Expect pins it),
// refuses with ErrForeign unless Recognise accepts it, then writes with the byte count and sha256 of the content and the
// sha256 of what it read, which the host verifies. It returns *WriteRefused when the host's checks refused.
func (m *Manager) WriteFile(ctx context.Context, h Host, w WriteSpec) error {
	if err := w.validate(h); err != nil {
		return err
	}
	expect := w.Expect
	if expect == "" {
		old, exists, err := m.ReadExisting(ctx, h, w.Root, w.Rel, w.Name)
		if err != nil {
			return err
		}
		expect = ExpectAbsent
		if exists {
			if !w.recognises(old) {
				return &WriteRefused{Reason: "foreign"}
			}
			expect = SHA256Hex(old)
		}
	}
	create := "0"
	if w.Create {
		create = "1"
	}
	inner := "sh -c " + quote(writeScript) + " vesqira-write " + quote(w.Root) + " " + quote(w.Rel) + " " + quote(w.Name) + " " +
		strconv.Itoa(len(w.Data)) + " " + SHA256Hex(w.Data) + " " + quote(expect) + " " + create
	out, err := m.RunShell(ctx, h, inner, w.Data, 45*time.Second)
	if err != nil {
		return err
	}
	last := lastLine(out)
	switch {
	case last == "vesqira-write: ok":
		return nil
	case strings.HasPrefix(last, "vesqira-write: refused:"):
		return &WriteRefused{Reason: strings.TrimPrefix(last, "vesqira-write: refused:")}
	}
	return errors.New("the host answered unexpectedly to a write")
}
