package hosts

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// rsync killed with SIGKILL gets no chance to move its hidden temporary file into the partial folder. The next attempt adopts
// exactly that file (same destination name), resumes from it, and finishes with the right bytes. Files that merely look similar
// stay where they are.
func TestAKilledRsyncLeavesATempFileThatTheNextAttemptResumesFrom(t *testing.T) {
	needRsync(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	remoteDir, localDir := filepath.Join(base, "remote"), filepath.Join(base, "local")
	os.MkdirAll(remoteDir, 0o755)
	os.MkdirAll(localDir, 0o755)
	const size = 24 << 20
	want := randomFile(t, filepath.Join(remoteDir, "cube.fits"), size)
	m := NewManager(plainSSH(t), nil)
	m.ControlDir = t.TempDir()
	h := Host{Name: "h", Dest: "login1", Roots: []string{remoteDir}}
	spec := RsyncSpec{Direction: Download, Local: filepath.Join(localDir, "cube.fits"), Remote: filepath.Join(remoteDir, "cube.fits"), BwlimitKBps: 8000}
	argv, err := m.RsyncArgv(h, spec)
	if err != nil {
		t.Fatal(err)
	}
	// files that look like temporaries but are not this destination's: they must survive untouched
	decoys := map[string][]byte{
		".other.fits.abcdef":  []byte("another file's temporary"),
		".cube.fits.abc":      []byte("too short a suffix"),
		"cube.fits.abcdef":    []byte("no leading dot"),
		".cube.fits.abcdef.x": []byte("longer"),
		".cube.fitsXabcdef":   []byte("no dot before the suffix"),
		".cubeXfits.abcdef":   []byte("the dot in the name is literal"),
		"unrelated.txt":       []byte("research notes"),
	}
	for n, b := range decoys {
		if err := os.WriteFile(filepath.Join(localDir, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var temp string
	deadline := time.Now().Add(20 * time.Second)
	for temp == "" && time.Now().Before(deadline) {
		es, _ := os.ReadDir(localDir)
		for _, e := range es {
			if strings.HasPrefix(e.Name(), ".cube.fits.") && len(e.Name()) == len(".cube.fits.")+6 {
				if fi, err := os.Stat(filepath.Join(localDir, e.Name())); err == nil && fi.Size() > 2<<20 {
					temp = filepath.Join(localDir, e.Name())
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if temp == "" {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatal("rsync never wrote its temporary file")
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // the whole group: client, receiver and the stand-in's sender
	cmd.Wait()
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(localDir, PartialDir, "cube.fits")); err == nil {
		t.Skip("rsync managed to move its file before the kill; the orphan case did not arise")
	}
	if _, err := os.Stat(temp); err != nil {
		t.Fatalf("the orphaned temporary file is not there: %v", err)
	}
	tmpSize := func() int64 { fi, _ := os.Stat(temp); return fi.Size() }()

	n, err := AdoptOrphanTemp(localDir, "cube.fits")
	if err != nil || n != 1 {
		t.Fatalf("adopted %d, %v", n, err)
	}
	if _, err := os.Stat(temp); err == nil {
		t.Error("the orphan is still under its temporary name")
	}
	part, err := os.Stat(filepath.Join(localDir, PartialDir, "cube.fits"))
	if err != nil || part.Size() != tmpSize {
		t.Fatalf("the orphan did not become the partial file: %v %v", part, err)
	}
	for n, b := range decoys {
		got, err := os.ReadFile(filepath.Join(localDir, n))
		if err != nil || !bytes.Equal(got, b) {
			t.Errorf("%s was touched: %v", n, err)
		}
	}
	// the next attempt resumes from it (the matched bytes are not sent again) and the result is the source, byte for byte
	argv, _ = m.RsyncArgv(h, RsyncSpec{Direction: Download, Local: spec.Local, Remote: spec.Remote})
	res, err := m.RunRsync(context.Background(), argv, nil)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("resume: %v %+v", err, res)
	}
	got, _ := os.ReadFile(filepath.Join(localDir, "cube.fits"))
	if !bytes.Equal(got, want) {
		t.Fatal("the resumed file differs from the source")
	}
	if _, err := os.Stat(filepath.Join(localDir, PartialDir, "cube.fits")); err == nil {
		t.Error("the partial file outlived the transfer")
	}
}

func TestAdoptOrphanTempKeepsABiggerPartialAndRemovesOnlyItsOwnOrphans(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, PartialDir), 0o755)
	put := func(rel string, n int) string {
		p := filepath.Join(dir, rel)
		os.WriteFile(p, bytes.Repeat([]byte("x"), n), 0o644)
		return p
	}
	big := put(filepath.Join(PartialDir, "a.fits"), 100)
	o1 := put(".a.fits.AAAAAA", 40)
	o2 := put(".a.fits.BBBBBB", 60)
	bystander := put(".b.fits.AAAAAA", 10)
	n, err := AdoptOrphanTemp(dir, "a.fits")
	if err != nil || n != 2 {
		t.Fatalf("handled %d, %v", n, err)
	}
	for _, p := range []string{o1, o2} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was left", p)
		}
	}
	if fi, err := os.Stat(big); err != nil || fi.Size() != 100 {
		t.Errorf("the bigger partial file was replaced: %v %v", fi, err)
	}
	if _, err := os.Stat(bystander); err != nil {
		t.Errorf("another name's temporary file was touched")
	}
	// a link with the right name is never followed, moved or removed
	target := put("target", 5)
	link := filepath.Join(dir, ".c.fits.CCCCCC")
	os.Symlink(target, link)
	if n, _ := AdoptOrphanTemp(dir, "c.fits"); n != 0 {
		t.Errorf("handled a link")
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("the link is gone")
	}
}
