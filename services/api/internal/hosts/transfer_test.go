package hosts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func needRsync(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync is not installed")
	}
}

// DropSSH is a fake ssh that runs the remote command on this machine, and on the first rsync server it starts it kills its own
// process (the transport) once the destination holds at least dropAt bytes under watch: a network drop in the middle of a file.
func dropSSH(t *testing.T, watch string, dropAt int) string {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
while [ $# -gt 0 ]; do case "$1" in -o) shift 2;; -T) shift;; *) break;; esac; done
shift
cmd=$(printf '%%s' "$*" | sed -e 's#bash -lc#bash -c#')
case "$cmd" in
*"rsync --server"*)
  if [ ! -e %q ]; then
    : > %q
    me=$$
    ( while :; do s=$(cat %q/.[!.]* %q/.vesqira-partial/* 2>/dev/null | wc -c); [ "$s" -ge %d ] && break; sleep 0.01; done; kill -KILL $me ) &
  fi;;
esac
# The rsync server must BE this process: dash (Ubuntu's /bin/sh) forks for "sh -c", so killing $me would orphan the server and
# the copy would finish under the final name instead of being cut.
case "$cmd" in *"rsync --server"*) eval "exec $cmd";; *) exec sh -c "$cmd";; esac
`, filepath.Join(dir, "dropped"), filepath.Join(dir, "dropped"), watch, watch, dropAt)
	p := filepath.Join(dir, "ssh")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// plainSSH runs the remote command in place of ssh with no pipe in between (fakeSSH's cat would outlive rsync and hold its
// stderr open), and with the remote PATH unchanged.
func plainSSH(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ssh")
	script := `#!/bin/sh
while [ $# -gt 0 ]; do case "$1" in -o) shift 2;; -T) shift;; *) break;; esac; done
shift
cmd=$(printf '%s' "$*" | sed -e 's#bash -lc#bash -c#')
exec sh -c "$cmd"
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func randomFile(t *testing.T, p string, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	rand.New(rand.NewSource(7)).Read(b)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVersionParsing(t *testing.T) {
	for line, want := range map[string]bool{
		"rsync  version 3.5.2-g83d74e46  protocol version 33": true,
		"rsync  version 3.1.0  protocol version 31":           true,
		"rsync  version 2.6.9  protocol version 29":           true,
		"openrsync: protocol version 29":                      false,
		"":                                                    false,
	} {
		if _, ok := parseRsyncVersion(line); ok != want {
			t.Errorf("%q: parsed=%v", line, ok)
		}
	}
	v, _ := parseRsyncVersion("rsync  version 3.0.9  protocol version 30")
	if !versionAtLeast(v, "3.0.0") || versionAtLeast(v, "3.1.0") {
		t.Errorf("3.0.9 compared wrongly")
	}
	v, _ = parseRsyncVersion("rsync  version 2.6.9  protocol version 29")
	if versionAtLeast(v, "3.0.0") {
		t.Errorf("2.6.9 accepted")
	}
}

func TestParseProgressReadsBothRsyncGenerations(t *testing.T) {
	for _, c := range []struct {
		line string
		want Progress
		ok   bool
	}{
		{"\r       10518528B  26% 10241994B/s    0:00:02  ", Progress{10518528, 26, 10241994, 2}, true},
		{"  40000000B 100% 10441138B/s    0:00:03 (xfr#1, to-chk=0/1)", Progress{40000000, 100, 10441138, 3}, true},
		{"  1,234,567  12%    1.50MB/s    1:02:03", Progress{1234567, 12, 1572864, 3723}, true},
		{"     32,768   0%    0.00kB/s    0:00:00", Progress{32768, 0, 0, 0}, true},
		{"rsync: connection unexpectedly closed", Progress{}, false},
		{">f+++++++++ big.bin", Progress{}, false},
	} {
		got, ok := ParseProgress(c.line)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%q: got %+v %v, want %+v %v", c.line, got, ok, c.want, c.ok)
		}
	}
}

func TestExitCodesClassifyNetworkLossFromRefusal(t *testing.T) {
	for code, want := range map[int]bool{12: true, 16: true, 20: true, 30: true, 255: true, 10: true, -1: true, 0: false, 1: false, 11: false, 23: false, 24: false, 3: false} {
		if RsyncExitRetryable(code) != want {
			t.Errorf("exit %d: retryable=%v", code, !want)
		}
	}
}

// Hostile names are single argv elements after "--", the remote one behind "host:", and nothing is shell text.
func TestRsyncArgvKeepsHostileNamesWhole(t *testing.T) {
	m := NewManager("ssh", nil)
	m.ControlDir = "/tmp/ctl"
	h := Host{Name: "h", Dest: "login1", Roots: []string{"/data"}}
	names := []string{"a b.fits", "it's.fits", `say "hi".fits`, "$(touch PWNED).fits", "x;rm -rf ~.fits", "`id`.fits", "-rf.fits", "tab\there.fits", "a:b.fits"}
	for _, n := range names {
		remote, local := "/data/"+n, "/home/u/data/"+n
		for _, dir := range []string{Download, Upload} {
			argv, err := m.RsyncArgv(h, RsyncSpec{Direction: dir, Local: local, Remote: remote, Compress: true, IgnoreExisting: true})
			if err != nil {
				t.Fatalf("%q: %v", n, err)
			}
			i := indexOf(argv, "--")
			if i < 0 || len(argv) != i+3 {
				t.Fatalf("%q: argv %q", n, argv)
			}
			a, b := argv[i+1], argv[i+2]
			if dir == Upload {
				a, b = b, a
			}
			if a != "login1:"+remote || b != local {
				t.Errorf("%s %q: got %q and %q", dir, n, a, b)
			}
			for _, x := range argv[:i] {
				if strings.Contains(x, n) && n != "a:b.fits" {
					t.Errorf("a name leaked into an option: %q", x)
				}
			}
		}
	}
	// the options that matter are all there, and rsync is told to keep unfinished files out of the final name
	argv, _ := m.RsyncArgv(h, RsyncSpec{Direction: Download, Local: "/l/f", Remote: "/data/f", BwlimitKBps: 500})
	for _, want := range []string{"--protect-args", "--partial-dir=" + PartialDir, "--timeout=600", "--info=progress2", "--bwlimit=500", "--safe-links"} {
		if indexOf(argv, want) < 0 {
			t.Errorf("missing %s in %q", want, argv)
		}
	}
	if indexOf(argv, "-z") >= 0 || indexOf(argv, "--ignore-existing") >= 0 {
		t.Errorf("-z or --ignore-existing without being asked: %q", argv)
	}
	for _, bad := range []string{"-a", "--inplace", "--append", "--append-verify", "--remove-source-files", "--delete"} {
		if indexOf(argv, bad) >= 0 {
			t.Errorf("a transfer must never use %s: %q", bad, argv)
		}
	}
	// a folder copies its contents into a folder, both ends with a slash
	argv, _ = m.RsyncArgv(h, RsyncSpec{Direction: Download, Dir: true, Local: "/l/d", Remote: "/data/d"})
	if i := indexOf(argv, "--"); argv[i+1] != "login1:/data/d/" || argv[i+2] != "/l/d/" {
		t.Errorf("folder paths: %q", argv[i:])
	}
	// the verification run changes nothing and does not delete: it reports
	argv, _ = m.RsyncArgv(h, RsyncSpec{Direction: Download, Dir: true, Local: "/l/d", Remote: "/data/d", VerifyOnly: true})
	if indexOf(argv, "-n") < 0 || indexOf(argv, "--checksum") < 0 || indexOf(argv, "--info=progress2") >= 0 {
		t.Errorf("verification argv: %q", argv)
	}
}

func indexOf(a []string, s string) int {
	for i, x := range a {
		if x == s {
			return i
		}
	}
	return -1
}

func TestRsyncArgvRefusesWhatCannotBePassedSafely(t *testing.T) {
	m := NewManager("ssh", nil)
	h := Host{Name: "h", Dest: "login1", Roots: []string{"/data"}}
	for _, p := range []string{"/data/a\nb", "/data/a\rb", "/data/a\x00b", `/data/a\b`, "relative", "/data/" + strings.Repeat("x", 600)} {
		if _, err := m.RsyncArgv(h, RsyncSpec{Direction: Download, Local: "/l/f", Remote: p}); err == nil && len(p) <= 500 {
			t.Errorf("%q accepted", p)
		}
		if err := h.CheckRemoteSource(p); err == nil {
			t.Errorf("CheckRemoteSource accepted %q", p)
		}
	}
	for _, p := range []string{"/data/a*.fits", "/data/x[1].fits", "/data/q?.fits"} {
		if err := h.CheckRemoteSource(p); !errors.Is(err, ErrTransferRefused) {
			t.Errorf("a glob in a remote source must be refused: %q (%v)", p, err)
		}
		if err := h.CheckRemoteDest(path2(p)); err != nil {
			t.Errorf("a glob character in a destination folder is only a name: %v", err)
		}
	}
	if err := h.CheckRemoteSource("/etc/passwd"); err == nil {
		t.Errorf("a path outside the roots was accepted")
	}
	if _, err := m.RsyncArgv(Host{Dest: "-oProxyCommand=x"}, RsyncSpec{Direction: Download, Local: "/l/f", Remote: "/data/f"}); err == nil {
		t.Errorf("an option-like ssh destination was accepted")
	}
	if _, err := m.RsyncArgv(h, RsyncSpec{Direction: Download, Local: "/l/f", Remote: "/data/f", BwlimitKBps: -1}); err == nil {
		t.Errorf("a negative limit was accepted")
	}
	for _, n := range []string{"", ".", "..", "a/b", "a\nb", ".vesqira-partial", ".vesqira-partial-x"} {
		if CheckName(n) == nil {
			t.Errorf("name %q accepted", n)
		}
	}
	if err := CheckName("it's a $(x).fits"); err != nil {
		t.Errorf("an ordinary hostile-looking name must be fine: %v", err)
	}
	// a ControlPath that needs both kinds of quote cannot be given to rsync -e
	m.ControlDir = `/tmp/it's "x"`
	if _, err := m.RsyncArgv(h, RsyncSpec{Direction: Download, Local: "/l/f", Remote: "/data/f"}); err == nil {
		t.Errorf("an unquotable ssh command was accepted")
	}
	m.ControlDir = "/tmp/a dir"
	argv, err := m.RsyncArgv(h, RsyncSpec{Direction: Download, Local: "/l/f", Remote: "/data/f"})
	if err != nil || !strings.Contains(argv[indexOf(argv, "-e")+1], "'ControlPath=/tmp/a dir/%C'") {
		t.Errorf("a control path with a space must be quoted for rsync -e: %v %q", err, argv)
	}
}

func path2(p string) string { return filepath.Dir(p) + "/" + "x" }

// Files whose names would break a shell travel intact in both directions and no command in them runs.
func TestTransferMovesHostileNamesWithoutRunningThem(t *testing.T) {
	needRsync(t)
	m, h, root := envWith(t, plainSSH(t))
	local := t.TempDir()
	hostile := []string{"a b.fits", "it's.fits", `say "hi".fits`, "$(touch PWNED).fits", "x;touch PWNED;.fits", "`touch PWNED`.fits", "-rf.fits", "a:b.fits"}
	ctx := context.Background()
	for i, name := range hostile {
		want := randomFile(t, filepath.Join(root, name), 5000+i)
		argv, err := m.RsyncArgv(h, RsyncSpec{Direction: Download, Local: filepath.Join(local, name), Remote: filepath.Join(root, name)})
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		res, err := m.RunRsync(ctx, argv, nil)
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("download %q: %v %+v", name, err, res)
		}
		got, _ := os.ReadFile(filepath.Join(local, name))
		if !bytes.Equal(got, want) {
			t.Fatalf("download %q: bytes differ", name)
		}
		up := name + ".up"
		argv, _ = m.RsyncArgv(h, RsyncSpec{Direction: Upload, Local: filepath.Join(local, name), Remote: filepath.Join(root, up)})
		if res, err := m.RunRsync(ctx, argv, nil); err != nil || res.ExitCode != 0 {
			t.Fatalf("upload %q: %v %+v", name, err, res)
		}
		got, _ = os.ReadFile(filepath.Join(root, up))
		if !bytes.Equal(got, want) {
			t.Fatalf("upload %q: bytes differ", name)
		}
	}
	for _, d := range []string{local, root, "."} {
		if _, err := os.Stat(filepath.Join(d, "PWNED")); err == nil {
			t.Fatalf("a command inside a file name ran (found PWNED in %s)", d)
		}
	}
	if es, _ := os.ReadDir(local); len(es) != len(hostile) { // no leftover partial folder after a clean finish
		for _, e := range es {
			t.Logf("left: %s", e.Name())
		}
		t.Errorf("destination has %d entries, want %d", len(es), len(hostile))
	}
}

// A connection that drops in the middle of a file leaves the bytes so far in the partial folder, never under the final
// name, and the next run sends only the rest. Both directions.
func TestResumeAfterTheConnectionDropsMidFile(t *testing.T) {
	needRsync(t)
	for _, dir := range []string{Download, Upload} {
		t.Run(dir, func(t *testing.T) {
			base, _ := filepath.EvalSymlinks(t.TempDir())
			remoteDir, localDir := filepath.Join(base, "remote"), filepath.Join(base, "local")
			os.MkdirAll(remoteDir, 0o755)
			os.MkdirAll(localDir, 0o755)
			const size = 24 << 20
			dst := localDir
			src := filepath.Join(remoteDir, "cube.fits")
			if dir == Upload {
				dst, src = remoteDir, filepath.Join(localDir, "cube.fits")
			}
			want := randomFile(t, src, size)
			m := NewManager(dropSSH(t, dst, 3<<20), nil)
			m.ControlDir = t.TempDir()
			h := Host{Name: "h", Dest: "login1", Roots: []string{remoteDir}}
			spec := RsyncSpec{Direction: dir, Local: filepath.Join(localDir, "cube.fits"), Remote: filepath.Join(remoteDir, "cube.fits"), BwlimitKBps: 8000}
			if dir == Upload {
				spec.Local = src
			}
			argv, err := m.RsyncArgv(h, spec)
			if err != nil {
				t.Fatal(err)
			}
			res, err := m.RunRsync(context.Background(), argv, nil)
			if err != nil {
				t.Fatal(err)
			}
			if res.ExitCode == 0 || !RsyncExitRetryable(res.ExitCode) {
				t.Fatalf("the drop was not seen as a lost connection: %+v", res)
			}
			final := filepath.Join(dst, "cube.fits")
			if st, err := os.Stat(final); err == nil {
				t.Fatalf("a half file sits under the final name (%d bytes)", st.Size())
			}
			part, err := os.Stat(filepath.Join(dst, PartialDir, "cube.fits"))
			if err != nil || part.Size() < 1<<20 || part.Size() >= size {
				t.Fatalf("expected a partial file inside the cut: %v %v", part, err)
			}
			// the second run (the fake ssh no longer drops) resumes: the bytes already there are matched, not sent again
			argv, _ = m.RsyncArgv(h, RsyncSpec{Direction: dir, Local: spec.Local, Remote: spec.Remote})
			var last Progress
			res, err = m.RunRsync(context.Background(), argv, func(p Progress) { last = p })
			if err != nil || res.ExitCode != 0 {
				t.Fatalf("resume: %v %+v", err, res)
			}
			got, _ := os.ReadFile(final)
			if !bytes.Equal(got, want) {
				t.Fatalf("the resumed file differs from the source")
			}
			if last.Bytes != size {
				t.Errorf("progress ended at %d, want %d", last.Bytes, size)
			}
			if _, err := os.Stat(filepath.Join(dst, PartialDir, "cube.fits")); err == nil {
				t.Errorf("the partial file was not cleaned up after completion")
			}
		})
	}
}

func TestStatPathSeesFilesFoldersLinksAndRefusesEscapes(t *testing.T) {
	m, h, root := envWith(t, fakeSSH(t))
	ctx := context.Background()
	want := randomFile(t, filepath.Join(root, "f.fits"), 1234)
	os.MkdirAll(filepath.Join(root, "d"), 0o755)
	os.Symlink("f.fits", filepath.Join(root, "link.fits"))
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "out"))
	os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o644)

	st, err := m.StatPath(ctx, h, root, "f.fits", true)
	sum, _, _ := HashFile(ctx, filepath.Join(root, "f.fits"))
	if err != nil || st.Kind != KindFile || st.Size != int64(len(want)) || st.SHA != sum || st.Real != filepath.Join(root, "f.fits") {
		t.Fatalf("file: %+v %v", st, err)
	}
	if st, _ := m.StatPath(ctx, h, root, "f.fits", false); st.SHA != "" {
		t.Errorf("stat without hash returned a hash")
	}
	for name, kind := range map[string]string{"d": KindDir, "nothing": KindAbsent, "link.fits": KindLink} {
		if st, err := m.StatPath(ctx, h, root, name, false); err != nil || st.Kind != kind {
			t.Errorf("%s: %+v %v", name, st, err)
		}
	}
	if st, err := m.StatPath(ctx, h, root, "", false); err != nil || st.Kind != KindDir || st.Real != root {
		t.Errorf("the folder itself: %+v %v", st, err)
	}
	// a folder that resolves outside the root through a link is refused, whatever the lexical path says
	if _, err := m.StatPath(ctx, h, filepath.Join(root, "out"), "secret", false); !errors.Is(err, ErrTransferRefused) {
		t.Errorf("a link out of the root was followed: %v", err)
	}
	if _, err := m.StatPath(ctx, h, filepath.Join(root, "missing"), "x", false); !errors.Is(err, ErrTransferRefused) {
		t.Errorf("a missing folder: %v", err)
	}
	if _, err := m.StatPath(ctx, h, "/etc", "passwd", false); !errors.Is(err, ErrTransferRefused) {
		t.Errorf("a path outside the roots: %v", err)
	}
	if _, err := m.StatPath(ctx, h, root, "../x", false); err == nil {
		t.Errorf("a name with .. was accepted")
	}
	// hostile folder and file names go through quote and double quotes only
	nasty := "it's $(touch " + filepath.Join(root, "PWNED") + ") `x`;"
	os.MkdirAll(filepath.Join(root, nasty), 0o755)
	randomFile(t, filepath.Join(root, nasty, "a b'c"), 10)
	if st, err := m.StatPath(ctx, h, filepath.Join(root, nasty), "a b'c", true); err != nil || st.Kind != KindFile || st.Size != 10 {
		t.Errorf("hostile names: %+v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(root, "PWNED")); err == nil {
		t.Fatalf("a command inside a name ran")
	}
}

func TestStatPathWithoutAHashToolSaysSoInsteadOfInventingOne(t *testing.T) {
	m, h, root := envWith(t, fakeSSH(t))
	randomFile(t, filepath.Join(root, "f"), 10)
	// a PATH with sh and its friends but no sha256sum or shasum
	bin := t.TempDir()
	for _, tool := range []string{"sh", "bash", "cat", "cut", "wc", "tr", "head", "sed", "dirname"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		os.Symlink(p, filepath.Join(bin, tool))
	}
	m.Ssh = fakeSSHWith(t, "022", -1, bin)
	st, err := m.StatPath(context.Background(), h, root, "f", true)
	if err != nil || st.Kind != KindFile || st.SHA != "" || st.Size != 10 {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestHostRsyncProbe(t *testing.T) {
	needRsync(t)
	m, h, _ := envWith(t, fakeSSH(t))
	info, err := m.HostRsyncProbe(context.Background(), h)
	if err != nil || info.Version == "" || !info.HasSHA || info.Path == "" {
		t.Fatalf("%+v %v", info, err)
	}
	if li, err := m.LocalRsync(context.Background()); err != nil || li.Version == "" {
		t.Fatalf("%+v %v", li, err)
	}
	// an old rsync, and one that is not GNU rsync, are refused with the reason
	for line, want := range map[string]string{"rsync  version 2.6.9  protocol version 29": "older than 3.0.0", "openrsync: protocol version 29": "not GNU rsync"} {
		bin := t.TempDir()
		os.WriteFile(filepath.Join(bin, "rsync"), []byte("#!/bin/sh\necho '"+line+"'\n"), 0o755)
		m.Ssh = fakeSSHWith(t, "022", -1, bin+":/usr/bin:/bin")
		if _, err := m.HostRsyncProbe(context.Background(), h); err == nil || !errors.Is(err, ErrTransferRefused) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", line, err)
		}
	}
}

// A folder is compared by rsync's own checksums and every difference, including a file that should not be there, is listed.
func TestVerifyOnlyListsDifferences(t *testing.T) {
	needRsync(t)
	m, h, root := envWith(t, plainSSH(t))
	local := t.TempDir()
	os.MkdirAll(filepath.Join(root, "run", "sub"), 0o755)
	randomFile(t, filepath.Join(root, "run", "a.fits"), 3000)
	randomFile(t, filepath.Join(root, "run", "sub", "b.fits"), 3000)
	ctx := context.Background()
	spec := RsyncSpec{Direction: Download, Dir: true, Local: filepath.Join(local, "run"), Remote: filepath.Join(root, "run")}
	argv, _ := m.RsyncArgv(h, spec)
	if res, err := m.RunRsync(ctx, argv, nil); err != nil || res.ExitCode != 0 {
		t.Fatalf("%v %+v", err, res)
	}
	spec.VerifyOnly = true
	argv, _ = m.RsyncArgv(h, spec)
	res, err := m.RunRsync(ctx, argv, nil)
	if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Listing) != "" {
		t.Fatalf("identical folders must list nothing: %v %+v", err, res)
	}
	// same size, same mtime, different bytes: only a content comparison sees it
	st, _ := os.Stat(filepath.Join(local, "run", "a.fits"))
	data, _ := os.ReadFile(filepath.Join(local, "run", "a.fits"))
	data[10] ^= 0xff
	os.Chmod(filepath.Join(local, "run", "a.fits"), 0o644)
	os.WriteFile(filepath.Join(local, "run", "a.fits"), data, 0o644)
	os.Chtimes(filepath.Join(local, "run", "a.fits"), st.ModTime(), st.ModTime())
	os.WriteFile(filepath.Join(local, "run", "extra.txt"), []byte("x"), 0o644)
	res, _ = m.RunRsync(ctx, argv, nil)
	if !strings.Contains(res.Listing, "a.fits") || !strings.Contains(res.Listing, "extra.txt") {
		t.Fatalf("differences not listed: %q", res.Listing)
	}
	if _, err := os.Stat(filepath.Join(local, "run", "extra.txt")); err != nil {
		t.Errorf("the verification run deleted a file")
	}
	got, _ := os.ReadFile(filepath.Join(local, "run", "a.fits"))
	if !bytes.Equal(got, data) {
		t.Errorf("the verification run changed a file")
	}
}

func TestRunRsyncTermKeepsThePartialAndReportsASignal(t *testing.T) {
	needRsync(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	remoteDir, localDir := filepath.Join(base, "r"), filepath.Join(base, "l")
	os.MkdirAll(remoteDir, 0o755)
	os.MkdirAll(localDir, 0o755)
	randomFile(t, filepath.Join(remoteDir, "c.fits"), 24<<20)
	m := NewManager(plainSSH(t), nil)
	m.ControlDir = t.TempDir()
	h := Host{Name: "h", Dest: "login1", Roots: []string{remoteDir}}
	argv, _ := m.RsyncArgv(h, RsyncSpec{Direction: Download, Local: filepath.Join(localDir, "c.fits"), Remote: filepath.Join(remoteDir, "c.fits"), BwlimitKBps: 6000})
	ctx, cancel := context.WithCancel(context.Background())
	res, err := m.RunRsync(ctx, argv, func(p Progress) {
		if p.Bytes > 2<<20 {
			cancel()
		}
	})
	if err != nil || res.ExitCode == 0 {
		t.Fatalf("%v %+v", err, res)
	}
	if _, err := os.Stat(filepath.Join(localDir, "c.fits")); err == nil {
		t.Errorf("a cancelled transfer left a file under the final name")
	}
	if st, err := os.Stat(filepath.Join(localDir, PartialDir, "c.fits")); err != nil || st.Size() == 0 {
		t.Errorf("SIGTERM must keep the partial file: %v %v", st, err)
	}
}
