package hosts

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A folder's verifying dry run must list what the real copy skipped silently: a link that points outside the folder and a
// special file. The listing's lines are sorted by what they mean.
func TestParseDifferencesNamesSkippedLinksAndSpecialFiles(t *testing.T) {
	listing := strings.Join([]string{
		"cL+++++++++ unsafe -> /etc/passwd",
		"cS+++++++++ pipe",
		"cD+++++++++ dev",
		">fc.t...... a.fits",
		"*deleting   mine.txt",
		"cd+++++++++ sub/",
	}, "\n")
	d := ParseDifferences(listing)
	if len(d.Links) != 1 || d.Links[0] != "unsafe" || len(d.Specials) != 2 || len(d.Other) != 3 || d.Empty() || d.Count() != 6 {
		t.Fatalf("%+v", d)
	}
	if !ParseDifferences("").Empty() {
		t.Error("an empty listing has differences")
	}
}

// The dry run that verifies a folder is built to see skipped entries, the real copy is built to skip them.
func TestVerifyArgvSeesSkippedEntriesAndCopyArgvSkipsThem(t *testing.T) {
	m := NewManager("ssh", nil)
	h := Host{Name: "h", Dest: "login1", Roots: []string{"/r"}}
	base := RsyncSpec{Direction: Download, Dir: true, Local: "/l/x", Remote: "/r/x"}
	copyArgv, err := m.RsyncArgv(h, base)
	if err != nil {
		t.Fatal(err)
	}
	if indexOf(copyArgv, "--safe-links") < 0 {
		t.Error("the copy must keep dropping links that point outside the folder")
	}
	v := base
	v.VerifyOnly = true
	vArgv, _ := m.RsyncArgv(h, v)
	if indexOf(vArgv, "--safe-links") >= 0 || indexOf(vArgv, "-rltpD") < 0 || indexOf(vArgv, "-n") < 0 {
		t.Errorf("the verifying run must not hide skipped entries: %v", vArgv)
	}
	p := base
	p.Preflight = true
	pArgv, _ := m.RsyncArgv(h, p)
	if indexOf(pArgv, "-n") < 0 || indexOf(pArgv, "--delete") >= 0 || indexOf(pArgv, "--ignore-existing") >= 0 {
		t.Errorf("the preflight is a dry run that deletes nothing: %v", pArgv)
	}
}

// rsync's dry run, without --delete, shows an entry of another type at the destination as new; NewEntries returns those names.
func TestNewEntriesOfARealDryRunIncludeATypeClash(t *testing.T) {
	needRsync(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	src, dst := filepath.Join(base, "src"), filepath.Join(base, "dst")
	os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	os.MkdirAll(filepath.Join(dst, "elsewhere"), 0o755)
	os.WriteFile(filepath.Join(src, "sub", "a b"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(src, "f"), []byte("x"), 0o644)
	os.Symlink("../elsewhere", filepath.Join(dst, "sub"))
	out, err := exec.Command("rsync", "-n", "-rltp", "--itemize-changes", src+"/", dst+"/").Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(NewEntries(string(out)), ",")
	if got != "f,sub,sub/a b" {
		t.Fatalf("%q from %q", got, out)
	}
	if es := NewEntries(`>f+++++++++ with\#012newline`); len(es) != 1 || es[0] != "with\nnewline" {
		t.Errorf("escapes: %q", es)
	}
}

func TestExistingAmongAsksTheHostAndRefusesUnsafeNames(t *testing.T) {
	m, h, root := envWith(t, fakeSSH(t))
	ctx := context.Background()
	os.MkdirAll(filepath.Join(root, "run", "d"), 0o755)
	os.Symlink("d", filepath.Join(root, "run", "lnk"))
	os.WriteFile(filepath.Join(root, "run", "f"), []byte("x"), 0o644)
	got, err := m.ExistingAmong(ctx, h, root, "run", []string{"f", "lnk", "d", "nope", "d/missing"})
	if err != nil || strings.Join(got, ",") != "f,lnk,d" {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := m.ExistingAmong(ctx, h, root, "run", nil); err != nil || len(got) != 0 {
		t.Errorf("no paths: %v %v", got, err)
	}
	if _, err := m.ExistingAmong(ctx, h, root, "run", []string{"a\nb"}); !errors.Is(err, ErrTransferRefused) {
		t.Errorf("a newline in a name: %v", err)
	}
	if _, err := m.ExistingAmong(ctx, h, "/etc", "run", []string{"f"}); !errors.Is(err, ErrTransferRefused) {
		t.Errorf("a folder outside the roots: %v", err)
	}
}

// Before an upload the host's folders are checked for links that an rsync older than 3.4 could be made to write through.
func TestCheckUploadTargetRefusesLinksAnOlderRsyncCouldFollow(t *testing.T) {
	m, h, root := envWith(t, fakeSSH(t))
	ctx := context.Background()
	outside := t.TempDir()
	if err := m.CheckUploadTarget(ctx, h, root, "new"); err != nil {
		t.Fatalf("an absent name: %v", err)
	}
	os.MkdirAll(filepath.Join(root, "run", "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "run", "sub", "f"), []byte("x"), 0o644)
	os.Symlink("f", filepath.Join(root, "run", "sub", "filelink")) // a link to a file is not followed into anything
	if err := m.CheckUploadTarget(ctx, h, root, "run"); err != nil {
		t.Fatalf("an ordinary folder: %v", err)
	}
	os.Symlink(outside, filepath.Join(root, "run", "sub", "out"))
	err := m.CheckUploadTarget(ctx, h, root, "run")
	if !errors.Is(err, ErrTransferRefused) || !strings.Contains(err.Error(), "out") || !strings.Contains(err.Error(), "3.4") {
		t.Fatalf("a link to a folder inside the target: %v", err)
	}
	os.Remove(filepath.Join(root, "run", "sub", "out"))
	os.Symlink(outside, filepath.Join(root, "run", "sub", PartialDir)) // a link named like the partial folder, deeper down
	if err := m.CheckUploadTarget(ctx, h, root, "run"); !errors.Is(err, ErrTransferRefused) {
		t.Fatalf("a nested partial-folder link: %v", err)
	}
	os.Remove(filepath.Join(root, "run", "sub", PartialDir))
	os.Symlink(outside, filepath.Join(root, PartialDir))
	if err := m.CheckUploadTarget(ctx, h, root, "new"); !errors.Is(err, ErrTransferRefused) || !strings.Contains(err.Error(), PartialDir) {
		t.Fatalf("a partial folder that is a link: %v", err)
	}
	os.Remove(filepath.Join(root, PartialDir))
	os.Symlink(outside, filepath.Join(root, "named"))
	if err := m.CheckUploadTarget(ctx, h, root, "named"); !errors.Is(err, ErrTransferRefused) {
		t.Fatalf("a name that is a link: %v", err)
	}
	if err := m.CheckUploadTarget(ctx, h, filepath.Join(root, "missing"), "x"); !errors.Is(err, ErrTransferRefused) {
		t.Fatalf("a missing folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "f")); err == nil {
		t.Fatal("the check wrote through a link")
	}
}

// The resolved path of a folder can hold a backslash (a link may lead to a folder with one); the host's reply must give it back
// as it is. dash's echo turns a backslash sequence into a character, so the script prints with printf; the test runs the script
// under dash when this machine has it and the reply is checked either way.
func TestStatReplyKeepsABackslashInTheResolvedPath(t *testing.T) {
	m, h, root := envWith(t, fakeSSH(t))
	ctx := context.Background()
	real := filepath.Join(filepath.Dir(root), `we\ird`)
	os.MkdirAll(real, 0o755)
	os.WriteFile(filepath.Join(real, "f"), []byte("abc"), 0o644)
	link := filepath.Join(filepath.Dir(root), "linkroot")
	os.Symlink(real, link)
	h.Roots = []string{link}
	st, err := m.StatPath(ctx, h, link, "f", true)
	if err != nil || st.Kind != KindFile || st.Real != filepath.Join(real, "f") {
		t.Fatalf("%+v %v", st, err)
	}
	if st, err := m.StatPath(ctx, h, link, "", false); err != nil || st.Kind != KindDir || st.Real != real {
		t.Fatalf("the folder: %+v %v", st, err)
	}
	// no echo of a variable that can hold a path, in any of the scripts
	for name, script := range map[string]string{"stat": statScript, "probe": probeScript, "exists": existsScript, "target": targetScript} {
		if regexp.MustCompile(`\becho\b`).MatchString(script) {
			t.Errorf("the %s script uses echo, which interprets backslashes under dash", name)
		}
	}
	if dash, err := exec.LookPath("dash"); err == nil {
		out, err := exec.Command(dash, "-c", statScript, "vesqira-stat", link, link, "f", "stat").Output()
		if err != nil || !strings.Contains(string(out), filepath.Join(real, "f")) {
			t.Fatalf("under dash: %q %v", out, err)
		}
	}
}

// A fake rsync that records its pid and waits; the test process starts it through RunRsync from a child process that is then
// killed, and the fake rsync must receive SIGTERM from the kernel.
func TestRunRsyncChildDiesWithItsParent(t *testing.T) {
	if os.Getenv("VESQIRA_PDEATHSIG_HELPER") != "" {
		m := NewManager("ssh", nil)
		_, _ = m.RunRsync(context.Background(), []string{os.Getenv("VESQIRA_PDEATHSIG_HELPER")}, nil)
		return
	}
	if runtime.GOOS != "linux" {
		t.Skip("the parent-death signal is Linux only")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	fake := filepath.Join(dir, "rsync")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho $$ > "+pidFile+"\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	parent := exec.Command(os.Args[0], "-test.run=^TestRunRsyncChildDiesWithItsParent$")
	parent.Env = append(os.Environ(), "VESQIRA_PDEATHSIG_HELPER="+fake)
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer parent.Process.Kill()
	var pid int
	for i := 0; i < 300 && pid == 0; i++ {
		if b, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the fake rsync did not start")
	}
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = parent.Wait()
	for i := 0; i < 200; i++ {
		if err := syscall.Kill(pid, 0); err != nil {
			return // gone
		}
		if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil && strings.Contains(string(b), ") Z ") {
			return // dead, not yet reaped by whoever adopted it
		}
		time.Sleep(25 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Fatal("rsync outlived the process that started it")
}
