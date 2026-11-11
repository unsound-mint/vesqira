package hosts

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Transfers between this computer and a host (ADR 0051). The bytes travel through the system rsync over the same ssh
// configuration every other host command uses; Vesqira never opens a connection of its own and never builds a shell
// command out of a caller's text. Everything caller-supplied reaches a process as one argv element (rsync, with
// --protect-args so the remote side does not parse it either) or as a positional argument of a fixed sh script, single-quoted
// by quote and used only inside double quotes.

// PartialDir is where rsync keeps an unfinished file, in the folder of the destination. A half-copied cube is never under its
// final name, so it cannot be registered, opened or mistaken for the finished file; a retry resumes from it.
const PartialDir = ".vesqira-partial"

const (
	minLocalRsync  = "3.1.0" // --info=progress2
	minRemoteRsync = "3.0.0" // --protect-args, --no-inc-recursive
	statTimeout    = 60 * time.Second
	hashTimeout    = 6 * time.Hour // 11 GB on a loaded login node
	// IOTimeout is rsync's own --timeout, in seconds. It is long on purpose: resuming a multi-GB partial file or comparing
	// checksums moves no bytes for minutes, and rsync would call that a stall. A dead link is found much sooner by ssh itself
	// (ServerAliveInterval 15 x 2), which ends the attempt; this only catches a connection that stays open and says nothing.
	IOTimeout = 600
)

// ErrTransferRefused is matched with errors.Is for a refusal that retrying cannot cure.
var ErrTransferRefused = errors.New("transfer refused")

// TransferRefused is a request the host's checks or Vesqira's validation refused; Reason says why, in words for the owner.
type TransferRefused struct{ Reason string }

func (e *TransferRefused) Error() string        { return e.Reason }
func (e *TransferRefused) Is(target error) bool { return target == ErrTransferRefused }

func refusedf(format string, a ...any) error { return &TransferRefused{fmt.Sprintf(format, a...)} }

// ---- names and paths ----

// globChars are expanded by rsync itself on the remote side even under --protect-args (checked against rsync 3.5: a source
// named a*.fits matched its sibling a1.fits), so a remote source containing one is refused rather than transferred wrongly.
const globChars = "*?["

// CheckRemoteSource validates a path on a host that is to be read.
func (h Host) CheckRemoteSource(p string) error {
	if err := h.checkRemote(p); err != nil {
		return err
	}
	if strings.ContainsAny(p, globChars) {
		return refusedf("the path contains * ? or [, which rsync would expand on the host as a pattern; rename the file or pick it another way")
	}
	return nil
}

// CheckRemoteDest validates a folder on a host that is to be written into.
func (h Host) CheckRemoteDest(p string) error { return h.checkRemote(p) }

func (h Host) checkRemote(p string) error {
	switch {
	case len(p) > 500:
		return refusedf("the path is too long")
	case unsafeText(p):
		return refusedf("the path contains a NUL, newline, carriage return or backslash, which cannot be passed safely through the host's login shell")
	case !h.InRoots(p):
		return refusedf("the path is not inside a source root of the host")
	}
	return nil
}

// RootOf is the first source root that lexically holds p ("" if none). The host script confirms on the resolved paths.
func (h Host) RootOf(p string) string {
	for _, r := range h.Roots {
		if p == r || strings.HasPrefix(p, strings.TrimRight(r, "/")+"/") {
			return r
		}
	}
	return ""
}

// CheckName validates one file or folder name for a destination.
func CheckName(n string) error {
	switch {
	case n == "" || n == "." || n == ".." || strings.Contains(n, "/") || len(n) > 200:
		return refusedf("the name must be one plain file or folder name")
	case unsafeText(n):
		return refusedf("the name contains a NUL, newline, carriage return or backslash")
	case strings.HasPrefix(n, PartialDir):
		return refusedf("the name is reserved for unfinished transfers")
	}
	return nil
}

// ---- probing rsync ----

// RsyncInfo is what an rsync binary says about itself.
type RsyncInfo struct {
	Path    string `json:"path"`
	Version string `json:"version"` // "3.5.2"
}

var rsyncVersionRe = regexp.MustCompile(`^rsync\s+version\s+(\d+)\.(\d+)(?:\.(\d+))?`)

func parseRsyncVersion(line string) (v [3]int, ok bool) {
	m := rsyncVersionRe.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return v, false
	}
	for i := 0; i < 3; i++ {
		v[i], _ = strconv.Atoi(m[i+1]) // an absent patch number is 0
	}
	return v, true
}

func versionAtLeast(v [3]int, min string) bool {
	var want [3]int
	for i, p := range strings.Split(min, ".") {
		want[i], _ = strconv.Atoi(p)
	}
	for i := range v {
		if v[i] != want[i] {
			return v[i] > want[i]
		}
	}
	return true
}

func (m *Manager) rsyncBin() string {
	if m.Rsync != "" {
		return m.Rsync
	}
	return "rsync"
}

// LocalRsync checks this computer's rsync: present, GNU rsync, new enough for --info=progress2.
func (m *Manager) LocalRsync(ctx context.Context) (RsyncInfo, error) {
	bin, err := exec.LookPath(m.rsyncBin())
	if err != nil {
		return RsyncInfo{}, refusedf("rsync is not installed on this computer (transfers use rsync; install it with your package manager)")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return RsyncInfo{}, refusedf("rsync on this computer did not answer --version")
	}
	line, _, _ := strings.Cut(string(out), "\n")
	v, ok := parseRsyncVersion(line)
	if !ok {
		return RsyncInfo{}, refusedf("the rsync on this computer is not GNU rsync (%q); transfers need rsync %s or newer", clip(line, 60), minLocalRsync)
	}
	if !versionAtLeast(v, minLocalRsync) {
		return RsyncInfo{}, refusedf("rsync %d.%d.%d on this computer is older than %s, which transfers need for progress reporting", v[0], v[1], v[2], minLocalRsync)
	}
	return RsyncInfo{Path: bin, Version: fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])}, nil
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// probeScript prints where the host's rsync is, its version line, and whether a sha256 tool exists. Under a login shell,
// so a module-provided rsync is found the way the other host commands find their tools.
var probeScript = oneLine(`
p=$(command -v rsync 2>/dev/null); printf '%s\n' "vesqira-probe: path $p";
if [ -n "$p" ]; then printf '%s\n' "vesqira-probe: version $(rsync --version 2>/dev/null | head -n 1)"; fi;
if command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1; then printf '%s\n' "vesqira-probe: sha yes"; else printf '%s\n' "vesqira-probe: sha no"; fi;
`)

// HostRsync is what a host offers for transfers.
type HostRsync struct {
	RsyncInfo
	HasSHA bool `json:"hasSha256"`
}

var safeRsyncPath = regexp.MustCompile(`^/[A-Za-z0-9_./+-]{1,200}$`)

// HostRsyncProbe asks the host about rsync and a sha256 tool in one ssh call. A host without rsync (or with one older than
// 3.0, or one that is not GNU rsync) is refused with the reason: Vesqira does not fall back to scp or sftp, because neither
// can resume a cut file without trusting the partial bytes, and a resume that cannot be checked is worse than none.
func (m *Manager) HostRsyncProbe(ctx context.Context, h Host) (HostRsync, error) {
	out, err := m.RunShell(ctx, h, "sh -c "+quote(probeScript), nil, statTimeout)
	if err != nil {
		return HostRsync{}, err
	}
	var res HostRsync
	var version string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimRight(l, "\r")
		switch rest, ok := strings.CutPrefix(l, "vesqira-probe: "); {
		case !ok:
		case strings.HasPrefix(rest, "path "):
			res.Path = strings.TrimSpace(strings.TrimPrefix(rest, "path "))
		case strings.HasPrefix(rest, "version "):
			version = strings.TrimPrefix(rest, "version ")
		case rest == "sha yes":
			res.HasSHA = true
		}
	}
	if res.Path == "" {
		return HostRsync{}, refusedf("rsync is not installed on %s (transfers use rsync; there is no scp or sftp fallback because neither can resume a cut file safely)", h.Name)
	}
	v, ok := parseRsyncVersion(version)
	if !ok {
		return HostRsync{}, refusedf("the rsync on %s is not GNU rsync (%q); transfers need rsync %s or newer", h.Name, clip(version, 60), minRemoteRsync)
	}
	if !versionAtLeast(v, minRemoteRsync) {
		return HostRsync{}, refusedf("rsync %d.%d.%d on %s is older than %s, which transfers need to pass names without the shell", v[0], v[1], v[2], h.Name, minRemoteRsync)
	}
	res.Version = fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
	return res, nil
}

// ---- looking at a path on a host ----

// statScript runs under sh with four arguments: root, dir, name and the mode ("stat" or "hash"). It resolves the root and dir,
// refuses a dir that resolves outside the root, and reports the entry Name inside it (or the dir itself when name is empty):
//
//	vesqira-stat: refused:<reason> | absent | link | other | dir 0 - <mtime> <resolved path> | file <bytes> <sha256 or -> <mtime> <resolved path>
//
// It never creates, changes or deletes anything. A final component that is a symbolic link is reported as "link" and never
// followed. Hashing needs sha256sum or shasum; without either the hash is "-" and the caller says so. The modification time is
// seconds since the epoch, "-" when the host's stat does not say. Paths are printed with printf: a path may hold a backslash
// (the resolved one can, through a link), which dash's echo would interpret.
var statScript = oneLine(`
tag=vesqira-stat; root=$1; dir=$2; name=$3; mode=$4;
refuse() { printf '%s\n' "$tag: refused:$1"; exit 0; };
sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum | cut -d" " -f1; else shasum -a 256 | cut -d" " -f1; fi; };
havesha() { command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1; };
rootp=$(cd -- "$root" 2>/dev/null && pwd -P) || refuse root;
real=$(cd -- "$dir" 2>/dev/null && pwd -P) || refuse absent;
case "$real" in "$rootp"|"$rootp"/*) ;; *) refuse escape;; esac;
if [ -n "$name" ]; then t="$real/$name"; else t=$real; fi;
if [ -L "$t" ]; then printf '%s\n' "$tag: link"; exit 0; fi;
if [ ! -e "$t" ]; then printf '%s\n' "$tag: absent"; exit 0; fi;
mt=$(stat -c %Y -- "$t" 2>/dev/null || stat -f %m -- "$t" 2>/dev/null); case "$mt" in ""|*[!0-9]*) mt=-;; esac;
if [ -d "$t" ]; then printf '%s\n' "$tag: dir 0 - $mt $t"; exit 0; fi;
if [ -f "$t" ]; then
  n=$(wc -c < "$t" | tr -d " "); s=-;
  if [ "$mode" = hash ] && havesha; then s=$(sha < "$t") || refuse io; fi;
  printf '%s\n' "$tag: file $n $s $mt $t"; exit 0;
fi;
printf '%s\n' "$tag: other";
`)

// Entry kinds reported by StatPath.
const (
	KindAbsent = "absent"
	KindFile   = "file"
	KindDir    = "dir"
	KindLink   = "link"
	KindOther  = "other"
)

// RemoteStat is what the host says about one path.
type RemoteStat struct {
	Kind string
	Size int64
	SHA  string // "" unless hashed; "-" is never returned: no tool means ""
	Real string // the resolved path of the entry
	// MTime is the modification time in seconds; 0 when the host did not say.
	MTime int64
}

type statRefusal string

func (r statRefusal) text() string {
	switch r {
	case "root":
		return "the source root is not reachable on the host"
	case "absent":
		return "the folder does not exist on the host"
	case "escape":
		return "the folder resolves outside the source root (a symbolic link on the way?)"
	default:
		return "the host refused to look at the path (" + string(r) + ")"
	}
}

// StatPath looks at dir/name on the host (name "" means dir itself); with hash it also computes the file's sha256 on the host.
// dir must be inside a root of the host (checked here lexically and on the host after resolving links).
func (m *Manager) StatPath(ctx context.Context, h Host, dir, name string, hash bool) (RemoteStat, error) {
	if err := h.checkRemote(dir); err != nil {
		return RemoteStat{}, err
	}
	if name != "" {
		if err := CheckName(name); err != nil {
			return RemoteStat{}, err
		}
	}
	mode, timeout := "stat", statTimeout
	if hash {
		mode, timeout = "hash", hashTimeout
	}
	inner := "sh -c " + quote(statScript) + " vesqira-stat " + quote(h.RootOf(dir)) + " " + quote(dir) + " " + quote(name) + " " + mode
	out, err := m.RunShell(ctx, h, inner, nil, timeout)
	if err != nil {
		return RemoteStat{}, err
	}
	last := lastLine(out)
	rest, ok := strings.CutPrefix(last, "vesqira-stat: ")
	if !ok {
		return RemoteStat{}, errors.New("the host answered unexpectedly when asked about a path")
	}
	switch {
	case strings.HasPrefix(rest, "refused:"):
		return RemoteStat{}, &TransferRefused{statRefusal(strings.TrimPrefix(rest, "refused:")).text()}
	case rest == KindAbsent || rest == KindLink || rest == KindOther:
		return RemoteStat{Kind: rest}, nil
	case strings.HasPrefix(rest, "dir ") || strings.HasPrefix(rest, "file "):
		f := strings.SplitN(rest, " ", 5)
		if len(f) != 5 {
			break
		}
		n, nerr := strconv.ParseInt(f[1], 10, 64)
		if nerr != nil || n < 0 {
			break
		}
		st := RemoteStat{Kind: f[0], Size: n, Real: f[4]}
		if f[3] != "-" {
			if mt, merr := strconv.ParseInt(f[3], 10, 64); merr == nil && mt > 0 {
				st.MTime = mt
			}
		}
		if f[2] != "-" {
			if len(f[2]) != 64 {
				break
			}
			st.SHA = f[2]
		}
		return st, nil
	}
	return RemoteStat{}, errors.New("the host answered unexpectedly when asked about a path")
}

// ---- hashing and room on this computer ----

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// HashFile is the sha256 and size of a local regular file. It stops when ctx ends.
func HashFile(ctx context.Context, p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, ctxReader{ctx, f})
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// FreeBytes is the space available to this user on the file system holding dir.
func FreeBytes(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// ---- the rsync command line ----

// Directions of a transfer.
const (
	Download = "download" // host -> this computer
	Upload   = "upload"   // this computer -> host
)

// RsyncSpec is one rsync run. Local is an absolute path on this computer, Remote an absolute path on the host. For a file,
// both name the file itself (the destination is the full final path, which is how a kept-both name is chosen); for a folder
// both name the folder, and its contents go into the destination folder.
type RsyncSpec struct {
	Direction      string
	Dir            bool
	Local, Remote  string
	BwlimitKBps    int  // 0: unlimited
	Compress       bool // -z
	IgnoreExisting bool // never overwrite a file that is there now (every policy but an explicit replace)
	Checksum       bool // compare contents, not size and time (a retry after a mismatch)
	VerifyOnly     bool // dry run comparing contents both ways, listing every difference; changes nothing
	Preflight      bool // dry run listing what the copy would create, to find entries it would have to delete; changes nothing
	HostRsyncPath  string
}

// rshWord quotes one word of the -e command the way rsync reads it (it splits on spaces and honours ' and ").
func rshWord(s string) (string, error) {
	if !strings.ContainsAny(s, " \t'\"") {
		return s, nil
	}
	switch {
	case !strings.Contains(s, "'"):
		return "'" + s + "'", nil
	case !strings.Contains(s, `"`):
		return `"` + s + `"`, nil
	}
	return "", refusedf("the ssh command or its control path contains both kinds of quote, which rsync cannot be given")
}

// rsh is the ssh command rsync runs: the same options as every host command (batch mode, keep-alives, the multiplexing
// socket), without the destination, which rsync appends from the host:path argument.
func (m *Manager) rsh(h Host) (string, error) {
	args := append([]string{m.Ssh}, m.sshArgs(h)...)
	args = args[:len(args)-1] // sshArgs ends with the destination
	words := make([]string, len(args))
	for i, a := range args {
		w, err := rshWord(a)
		if err != nil {
			return "", err
		}
		words[i] = w
	}
	return strings.Join(words, " "), nil
}

func relDirSlash(p string) string { return strings.TrimRight(p, "/") + "/" }

// RsyncArgv builds the argument vector (argv[0] is the rsync binary). Nothing is joined into shell text: every path is one
// element after "--", and the remote side receives its arguments through rsync's own protected channel.
//
// The flags, and why:
//
//	-rltp            recurse, keep links as links, times and permissions. Not owner, group or devices (-a): they mean
//	                 nothing without root and a special file does not belong in a research folder.
//	--safe-links     a link inside a copied folder that points outside the copied tree is dropped, not copied. The
//	                 verifying dry run leaves it out (and adds -D) so that every entry the copy skipped shows up as a difference.
//	--protect-args   the remote path is not parsed by the host's shell.
//	--partial-dir    an unfinished file lives in PartialDir, never under its final name; a retry resumes from it.
//	--timeout        an attempt whose connection stays open but silent for ten minutes ends and is retried.
//	--info=progress2 one running total, which is what the queue shows.
//	--no-i-r         the whole file list first, so a folder's total is known.
//	-z               on by default (the owner's habit); off for data that does not compress.
//	--ignore-existing a file that appeared at the destination after the conflict check is never overwritten.
func (m *Manager) RsyncArgv(h Host, s RsyncSpec) ([]string, error) {
	if s.Direction != Download && s.Direction != Upload {
		return nil, refusedf("unknown direction %q", s.Direction)
	}
	if !path.IsAbs(s.Local) || !path.IsAbs(s.Remote) || unsafeText(s.Local) || unsafeText(s.Remote) {
		return nil, refusedf("transfers take absolute paths without NUL, newline, carriage return or backslash")
	}
	if !ValidDest(h.Dest) {
		return nil, refusedf("the host's ssh destination is not acceptable")
	}
	if s.Direction == Download && strings.ContainsAny(s.Remote, globChars) {
		return nil, refusedf("the remote source contains * ? or [")
	}
	if s.BwlimitKBps < 0 || s.BwlimitKBps > 10_000_000 {
		return nil, refusedf("the bandwidth limit must be between 0 and 10000000 KiB/s")
	}
	rsh, err := m.rsh(h)
	if err != nil {
		return nil, err
	}
	argv := []string{m.rsyncBin(), "-rltp", "--safe-links", "--protect-args", "--partial-dir=" + PartialDir,
		"--timeout=" + strconv.Itoa(IOTimeout), "--no-i-r", "-e", rsh}
	if s.VerifyOnly {
		// Without --safe-links and with -D (devices and special files), what the copy dropped silently is a difference: a link
		// that points outside the folder, a fifo or a socket. They are never copied, so the folder cannot be called verified.
		argv = []string{m.rsyncBin(), "-rltpD", "--protect-args", "--partial-dir=" + PartialDir,
			"--timeout=" + strconv.Itoa(IOTimeout), "--no-i-r", "-e", rsh}
	}
	if s.HostRsyncPath != "" && s.HostRsyncPath != "rsync" && safeRsyncPath.MatchString(s.HostRsyncPath) {
		argv = append(argv, "--rsync-path="+s.HostRsyncPath)
	}
	switch {
	case s.Preflight:
		argv = append(argv, "-n", "--itemize-changes", "-8")
	case s.VerifyOnly:
		argv = append(argv, "-n", "--checksum", "--delete", "--itemize-changes")
	default:
		argv = append(argv, "--info=progress2", "--no-h")
		if s.Checksum {
			argv = append(argv, "--checksum")
		}
		if s.IgnoreExisting {
			argv = append(argv, "--ignore-existing")
		}
	}
	if s.Compress {
		argv = append(argv, "-z")
	}
	if s.BwlimitKBps > 0 {
		argv = append(argv, "--bwlimit="+strconv.Itoa(s.BwlimitKBps))
	}
	local, remote := s.Local, h.Dest+":"+s.Remote
	if s.Dir {
		local, remote = relDirSlash(local), relDirSlash(s.Remote)
		remote = h.Dest + ":" + remote
	}
	if s.Direction == Download {
		argv = append(argv, "--", remote, local)
	} else {
		argv = append(argv, "--", local, remote)
	}
	return argv, nil
}

// ---- running it ----

// Progress is one reading of rsync's --info=progress2 line. EtaSec is -1 when rsync gave none.
type Progress struct {
	Bytes   int64
	Percent int
	RateBps int64
	EtaSec  int64
}

var progressRe = regexp.MustCompile(`^\s*([\d,]+)B?\s+(\d{1,3})%\s+([\d.,]+)\s?([kKMG]?)B/s\s+(\d+):(\d{2}):(\d{2})`)

// ParseProgress reads one progress line ("  40000000B 100% 10441138B/s 0:00:03 (xfr#1, to-chk=0/1)"). Older rsync prints no B
// after the byte count and kB/s, MB/s for the rate; thousands separators are dropped. It reports false for any other line.
func ParseProgress(line string) (Progress, bool) {
	m := progressRe.FindStringSubmatch(line)
	if m == nil {
		return Progress{}, false
	}
	b, err := strconv.ParseInt(strings.ReplaceAll(m[1], ",", ""), 10, 64)
	if err != nil {
		return Progress{}, false
	}
	pct, _ := strconv.Atoi(m[2])
	rate, err := strconv.ParseFloat(strings.ReplaceAll(m[3], ",", ""), 64)
	if err != nil {
		return Progress{}, false
	}
	switch m[4] {
	case "k", "K":
		rate *= 1 << 10
	case "M":
		rate *= 1 << 20
	case "G":
		rate *= 1 << 30
	}
	hh, _ := strconv.Atoi(m[5])
	mm, _ := strconv.Atoi(m[6])
	ss, _ := strconv.Atoi(m[7])
	return Progress{Bytes: b, Percent: pct, RateBps: int64(rate), EtaSec: int64(hh*3600 + mm*60 + ss)}, true
}

// RsyncResult is how a run ended. ExitCode is -1 when the process was ended by a signal.
type RsyncResult struct {
	ExitCode int
	Stderr   string // the last part of what rsync said, control characters removed
	Listing  string // non-progress lines of stdout (the itemized differences of a verification run), capped
}

// RsyncExitRetryable says whether an exit code is a lost or stalled connection (worth another attempt that resumes) rather
// than a refusal. 16 ("sibling process terminated abnormally") is how rsync 3.4 reports the other end vanishing mid-copy, which
// is what a dropped VPN or a killed ssh looks like there; older versions report 12 or 255 for the same event. 20 is rsync's exit for a SIGTERM or SIGINT it received (something outside Vesqira stopped it, for example the
// host's login node killing it; a cancel by the owner is recognised before this is asked). 23 and 24 (a file could not be read,
// or vanished) and 11 (a full disk) are not retried: they repeat.
func RsyncExitRetryable(code int) bool {
	switch code {
	case -1, 5, 10, 12, 14, 16, 20, 30, 35, 255:
		return true
	}
	return false
}

func splitProgress(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

type tail struct {
	b   []byte
	max int
}

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func printable(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' || r == 0x7f {
			return ' '
		}
		return r
	}, s))
}

// RunRsync runs argv without a shell. onProgress is called for each progress line. When ctx ends, rsync gets SIGTERM, which
// keeps the unfinished file in PartialDir (SIGKILL would leave a hidden temporary file in the destination folder instead);
// it is killed only if it has not exited 20 seconds later.
func (m *Manager) RunRsync(ctx context.Context, argv []string, onProgress func(Progress)) (RsyncResult, error) {
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return RsyncResult{}, refusedf("rsync is not installed on this computer")
	}
	cmd := exec.CommandContext(ctx, bin, argv[1:]...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 20 * time.Second
	dieWithParent(cmd)
	// An explicit environment: nothing from the caller's RSYNC_* or SSH_* variables, and C numbers (no locale separators).
	cmd.Env = append([]string{"PATH=/usr/bin:/bin:/usr/local/bin", "HOME=" + os.Getenv("HOME"), "LC_ALL=C"}, m.env()...)
	stderr := &tail{max: 4096}
	cmd.Stderr = stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return RsyncResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return RsyncResult{}, err
	}
	var listing strings.Builder
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	sc.Split(splitProgress)
	for sc.Scan() {
		line := sc.Text()
		if p, ok := ParseProgress(line); ok {
			if onProgress != nil {
				onProgress(p)
			}
			continue
		}
		if strings.TrimSpace(line) != "" && listing.Len() < 64<<10 {
			listing.WriteString(line + "\n")
		}
	}
	_, _ = io.Copy(io.Discard, out)
	werr := cmd.Wait()
	res := RsyncResult{Stderr: printable(string(stderr.b)), Listing: listing.String()}
	var ee *exec.ExitError
	switch {
	case werr == nil:
	case errors.As(werr, &ee):
		res.ExitCode = ee.ExitCode()
	default:
		return res, werr
	}
	return res, nil
}
