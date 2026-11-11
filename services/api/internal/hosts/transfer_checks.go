package hosts

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Checks that rsync's own output or the host's folders have to answer before a transfer may run or be called verified.

// itemizedNew matches an itemized line of a dry run for an entry the receiver does not have at all (all nine attribute flags
// are "+"): a file, folder, link, device or special file that the copy would create.
var itemizedNew = regexp.MustCompile(`^[<>ch.][fdLDS]\+{9} (.+)$`)

var itemizedEscape = regexp.MustCompile(`\\#([0-7]{3})`)

func unescapeItemized(s string) string {
	return itemizedEscape.ReplaceAllStringFunc(s, func(m string) string {
		var b byte
		fmt.Sscanf(m[2:], "%o", &b)
		return string([]byte{b})
	})
}

// NewEntries reads a dry run's itemized listing and returns the relative paths rsync would create (folders without the
// trailing slash). An entry that exists at the destination although rsync sees it as new is one of a different type there: a
// link or file where the source has a folder, or the reverse. Replacing it means deleting what the owner has.
func NewEntries(listing string) []string {
	var out []string
	for _, l := range strings.Split(listing, "\n") {
		m := itemizedNew.FindStringSubmatch(strings.TrimRight(l, "\r"))
		if m == nil {
			continue
		}
		name := m[1]
		if strings.HasPrefix(l[1:2], "L") { // "name -> target"
			name, _, _ = strings.Cut(name, " -> ")
		}
		name = strings.TrimSuffix(unescapeItemized(name), "/")
		if name == "" || name == "." {
			continue
		}
		out = append(out, name)
	}
	return out
}

// Differences is a verifying dry run's listing, sorted by what the lines mean.
type Differences struct {
	Links    []string // links the source has and the copy lacks: the ones that point outside the folder are dropped by --safe-links
	Specials []string // fifos, sockets and devices, which are never copied
	Other    []string // the remaining lines: content, extra files, anything else
}

func (d Differences) Empty() bool { return len(d.Links)+len(d.Specials)+len(d.Other) == 0 }

// Count is how many entries differ.
func (d Differences) Count() int { return len(d.Links) + len(d.Specials) + len(d.Other) }

// ParseDifferences sorts the lines of the dry run that verifies a folder. The run omits --safe-links and adds -D, so an entry
// the real copy skipped is listed here as one the copy lacks.
func ParseDifferences(listing string) Differences {
	var d Differences
	for _, l := range strings.Split(strings.TrimSpace(listing), "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		if m := itemizedNew.FindStringSubmatch(l); m != nil {
			name := m[1]
			switch l[1] {
			case 'L':
				name, _, _ = strings.Cut(name, " -> ")
				d.Links = append(d.Links, unescapeItemized(name))
				continue
			case 'D', 'S':
				d.Specials = append(d.Specials, unescapeItemized(name))
				continue
			}
		}
		d.Other = append(d.Other, l)
	}
	return d
}

// ---- looking at the destination folder of an upload ----

// existsScript runs under sh with root, dir and name; it reads relative paths, one per line, from stdin and prints those that
// exist (or are links) under dir/name. The same resolution and root check as statScript. Paths are printed with printf.
var existsScript = oneLine(`
tag=vesqira-exists; root=$1; dir=$2; name=$3;
rootp=$(cd -- "$root" 2>/dev/null && pwd -P) || { printf '%s\n' "$tag: refused:root"; exit 0; };
real=$(cd -- "$dir" 2>/dev/null && pwd -P) || { printf '%s\n' "$tag: refused:absent"; exit 0; };
case "$real" in "$rootp"|"$rootp"/*) ;; *) printf '%s\n' "$tag: refused:escape"; exit 0;; esac;
while IFS= read -r p; do
  [ -n "$p" ] || continue;
  if [ -L "$real/$name/$p" ] || [ -e "$real/$name/$p" ]; then printf '%s\n' "$tag: at $p"; fi;
done;
printf '%s\n' "$tag: end";
`)

// MaxExistsPaths bounds how many paths one ExistingAmong call sends.
const MaxExistsPaths = 20000

// ExistingAmong asks the host which of the relative paths exist (as anything, a link included) under dir/name. A path with a
// newline, carriage return, NUL or backslash cannot be passed and is refused. Nothing is changed on the host.
func (m *Manager) ExistingAmong(ctx context.Context, h Host, dir, name string, rels []string) ([]string, error) {
	if err := h.checkRemote(dir); err != nil {
		return nil, err
	}
	if err := CheckName(name); err != nil {
		return nil, err
	}
	if len(rels) > MaxExistsPaths {
		return nil, refusedf("the folder holds more than %d new entries to check against the host's folder", MaxExistsPaths)
	}
	for _, r := range rels {
		if unsafeText(r) || strings.HasPrefix(r, "/") {
			return nil, refusedf("an entry name contains a newline, carriage return, NUL or backslash, which cannot be checked on the host")
		}
	}
	inner := "sh -c " + quote(existsScript) + " vesqira-exists " + quote(h.RootOf(dir)) + " " + quote(dir) + " " + quote(name)
	out, err := m.RunShell(ctx, h, inner, []byte(strings.Join(rels, "\n")+"\n"), statTimeout)
	if err != nil {
		return nil, err
	}
	var found []string
	ended := false
	for _, l := range strings.Split(string(out), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimRight(l, "\r"), "vesqira-exists: ")
		switch {
		case !ok:
		case strings.HasPrefix(rest, "refused:"):
			return nil, &TransferRefused{statRefusal(strings.TrimPrefix(rest, "refused:")).text()}
		case rest == "end":
			ended = true
		case strings.HasPrefix(rest, "at "):
			found = append(found, strings.TrimPrefix(rest, "at "))
		}
	}
	if !ended {
		return nil, errors.New("the host answered unexpectedly when asked about a folder")
	}
	return found, nil
}

// targetScript runs under sh with root, dir and name before an upload. rsync older than 3.4 can follow a symbolic link that
// someone planted in the destination (CVE-2024-12747 and the symlink races fixed in 3.4), and Vesqira supports hosts that
// have 3.0 to 3.3. So before every upload the host confirms that the folder resolves inside the root, that the partial folder
// is not a link, that the name is not a link, and that the existing folder at the name holds no link to a folder and no link
// called like the partial folder. The window between this check and rsync is not closed (only rsync 3.4 does that).
var targetScript = oneLine(`
tag=vesqira-target; root=$1; dir=$2; name=$3;
refuse() { printf '%s\n' "$tag: refused:$1"; exit 0; };
rootp=$(cd -- "$root" 2>/dev/null && pwd -P) || refuse root;
real=$(cd -- "$dir" 2>/dev/null && pwd -P) || refuse absent;
case "$real" in "$rootp"|"$rootp"/*) ;; *) refuse escape;; esac;
if [ -L "$real/.vesqira-partial" ]; then refuse partiallink; fi;
t="$real/$name";
if [ -L "$t" ]; then refuse namelink; fi;
if [ -d "$t" ]; then
  bad=$(find "$t" -type l \( -name .vesqira-partial -o -exec test -d {} \; \) -print 2>/dev/null | head -n 1);
  if [ -n "$bad" ]; then printf '%s\n' "$tag: refused:linkinside ${bad#"$t"/}"; exit 0; fi;
fi;
printf '%s\n' "$tag: ok";
`)

// CheckUploadTarget refuses an upload into dir/name when the host's folders hold links that an older rsync could follow. It
// changes nothing on the host.
func (m *Manager) CheckUploadTarget(ctx context.Context, h Host, dir, name string) error {
	if err := h.checkRemote(dir); err != nil {
		return err
	}
	if err := CheckName(name); err != nil {
		return err
	}
	inner := "sh -c " + quote(targetScript) + " vesqira-target " + quote(h.RootOf(dir)) + " " + quote(dir) + " " + quote(name)
	out, err := m.RunShell(ctx, h, inner, nil, statTimeout)
	if err != nil {
		return err
	}
	rest, ok := strings.CutPrefix(lastLine(out), "vesqira-target: ")
	switch {
	case !ok:
		return errors.New("the host answered unexpectedly when asked about the destination")
	case rest == "ok":
		return nil
	}
	code, arg, _ := strings.Cut(strings.TrimPrefix(rest, "refused:"), " ")
	switch code {
	case "partiallink":
		return refusedf("%s in the destination folder on %s is a symbolic link; an upload there could be written somewhere else. Remove the link on the host and try again.", PartialDir, h.Name)
	case "namelink":
		return refusedf("the destination name on %s is a symbolic link; Vesqira does not write through links", h.Name)
	case "linkinside":
		return refusedf("the existing folder at the destination on %s holds a symbolic link (%s); rsync older than 3.4 can be made to write through such a link. Remove or move it on the host, or upload to a new name.", h.Name, clip(arg, 120))
	}
	return &TransferRefused{statRefusal(code).text()}
}
