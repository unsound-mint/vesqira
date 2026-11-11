package hosts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Outputs as the tools print them (BeeGFS 7, Lustre 2.12, Linux quota-tools, GNU df).
const beegfsOut = `Quota information for storage pool Default (ID: 1):

      user/group     ||           size          ||    chunk files    
     name     |  id  ||    used    |    hard    ||  used   |  hard   
--------------|------||------------|------------||---------|---------
      spectre|  1000||  395.20 GiB|  400.00 GiB||  1234567|unlimited
`

const beegfsTwoPools = `Quota information for storage pool Default (ID: 1):

      user/group     ||           size          ||    chunk files    
     name     |  id  ||    used    |    hard    ||  used   |  hard   
--------------|------||------------|------------||---------|---------
      spectre|  1000||  395.20 GiB|  400.00 GiB||  1234567|unlimited

Quota information for storage pool fast (ID: 2):

      user/group     ||           size          ||    chunk files    
     name     |  id  ||    used    |    hard    ||  used   |  hard   
--------------|------||------------|------------||---------|---------
      spectre|  1000||     0 Byte|  unlimited||        0|unlimited
`

const lfsOut = `Disk quotas for usr spectre (uid 1000):
     Filesystem  kbytes   quota   limit   grace   files   quota   limit   grace
       /scratch 414392320       0 524288000       -   12345       0       0       -
`

const lfsWrapped = `Disk quotas for usr spectre (uid 1000):
     Filesystem  kbytes   quota   limit   grace   files   quota   limit   grace
/lustre/a/very/long/file/system/name
                 1024*  900000 1000000   6days   12345       0       0       -
`

const quotaOut = `Disk quotas for user spectre (uid 1000):
     Filesystem   space   quota   limit   grace   files   quota   limit   grace
      /dev/sda1  2048M*  1024M   5120M   6days     150       0       0
      /dev/sdb1   12G    20G     25G              1500       0       0
`

const dfOut = `Filesystem     1024-blocks      Used Available Capacity Mounted on
/dev/sda2        479152128 301234567 153000000      67% /home
`

func TestBeeGFSQuota(t *testing.T) {
	es, err := ParseBeeGFSQuota(beegfsOut)
	if err != nil || len(es) != 1 {
		t.Fatalf("%v %v", es, err)
	}
	e := es[0]
	if e.Scope != "storage pool Default" || e.UsedBytes != 424342768845 || e.HardBytes == nil || *e.HardBytes != 400<<30 || e.SoftBytes != nil {
		t.Errorf("%+v", e)
	}
	if !strings.Contains(e.Raw, "395.20 GiB") || !strings.Contains(e.Raw, "400.00 GiB") {
		t.Errorf("the raw line keeps the printed units: %q", e.Raw)
	}
	es, err = ParseBeeGFSQuota(beegfsTwoPools)
	if err != nil || len(es) != 2 || es[1].Scope != "storage pool fast" || es[1].UsedBytes != 0 || es[1].HardBytes != nil {
		t.Fatalf("two pools, the second unlimited: %+v %v", es, err)
	}
}

func TestLfsQuota(t *testing.T) {
	es, err := ParseLfsQuota(lfsOut)
	if err != nil || len(es) != 1 {
		t.Fatalf("%v %v", es, err)
	}
	if e := es[0]; e.Scope != "/scratch" || e.UsedBytes != 414392320<<10 || e.SoftBytes != nil || e.HardBytes == nil || *e.HardBytes != 524288000<<10 {
		t.Errorf("kbytes are KiB and 0 means none: %+v", es[0])
	}
	es, err = ParseLfsQuota(lfsWrapped)
	if err != nil || len(es) != 1 || es[0].Scope != "/lustre/a/very/long/file/system/name" || es[0].UsedBytes != 1024<<10 || es[0].SoftBytes == nil || *es[0].SoftBytes != 900000<<10 {
		t.Fatalf("a wrapped file system name and an over-quota star: %+v %v", es, err)
	}
}

func TestQuotaCommandHumanSizesAndAnEmptyGraceColumn(t *testing.T) {
	es, err := ParseQuotaCmd(quotaOut)
	if err != nil || len(es) != 2 {
		t.Fatalf("%v %v", es, err)
	}
	if e := es[0]; e.Scope != "/dev/sda1" || e.UsedBytes != 2048<<20 || e.SoftBytes == nil || *e.SoftBytes != 1024<<20 || *e.HardBytes != 5120<<20 {
		t.Errorf("%+v", e)
	}
	if e := es[1]; e.UsedBytes != 12<<30 || *e.SoftBytes != 20<<30 || *e.HardBytes != 25<<30 {
		t.Errorf("an empty grace column shifts the later columns, which are not read: %+v", e)
	}
}

func TestQuotaParsersRefuseGarbageAndNeverGuess(t *testing.T) {
	garbage := []string{
		"", "\n\n", "beegfs-ctl: command not found", "quota: Cannot open quotafile /home/aquota.user: Permission denied",
		"Disk quotas for user spectre (uid 1000): none", "Quota information for storage pool Default (ID: 1):\n\nQuota is not enabled\n",
		strings.Repeat("A", 1<<20), strings.Repeat("a b c d e f g\n", 5000), "\x00\x01\x02||\x03|\x04",
		"Filesystem kbytes quota limit grace\n/scratch -5 0 0 -", "/scratch 99999999999999999999 0 0 -", "/dev/x 99999999T 1G 2G",
		"name | id || used | hard ||\n spectre| 1000|| 12 parsecs| 400 GiB|| 1|2",
	}
	for _, g := range garbage {
		if es, err := ParseBeeGFSQuota(g); err == nil {
			t.Errorf("beegfs accepted %.40q: %+v", g, es)
		}
		if es, err := ParseLfsQuota(g); err == nil {
			t.Errorf("lfs accepted %.40q: %+v", g, es)
		}
		if es, err := ParseQuotaCmd(g); err == nil {
			t.Errorf("quota accepted %.40q: %+v", g, es)
		}
		if f, err := ParseDF(g); err == nil {
			t.Errorf("df accepted %.40q: %+v", g, f)
		}
	}
}

func TestDF(t *testing.T) {
	f, err := ParseDF(dfOut)
	if err != nil || f.Filesystem != "/dev/sda2" || f.Mount != "/home" || f.TotalBytes != 479152128<<10 || f.UsedBytes != 301234567<<10 || f.AvailBytes != 153000000<<10 {
		t.Fatalf("%+v %v", f, err)
	}
	f, err = ParseDF("Filesystem 1024-blocks Used Available Capacity Mounted on\n10.0.0.1:/export/my home 100 50 50 50% /mnt/my share\n")
	if err != nil || f.Filesystem != "10.0.0.1:/export/my home" || f.Mount != "/mnt/my share" {
		t.Fatalf("names with spaces: %+v %v", f, err)
	}
}

func TestDU(t *testing.T) {
	u, err := ParseDU("24117248\t/data/run1\n@@rc 0\n")
	if err != nil || u.KiB != 24117248 || u.Bytes != 24117248<<10 || u.Partial {
		t.Fatalf("%+v %v", u, err)
	}
	if u, err := ParseDU("100\t/data/run1\n@@rc 1\n"); err != nil || !u.Partial {
		t.Errorf("unreadable entries make a lower bound, not a clean number: %+v %v", u, err)
	}
	for _, bad := range []string{"", "@@rc 0\n", "abc\t/x\n@@rc 0\n", "99999999999999999999\t/x\n@@rc 0", "-5\t/x\n@@rc 0", strings.Repeat("9", 5000)} {
		if _, err := ParseDU(bad); err == nil {
			t.Errorf("accepted %.30q", bad)
		}
	}
	if _, err := ParseDU("123\t/x\n@@rc 124\n"); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("a timeout must not show a partial size as the size: %v", err)
	}
}

func TestAmountsAreBoundedAndStrict(t *testing.T) {
	for in, want := range map[string]int64{"1 GiB": 1 << 30, "1.5K": 1536, "0 Byte": 0, "7*": 7} {
		if got, ok := parseAmount(in, 1); !ok || got != want {
			t.Errorf("%q: %d %v", in, got, ok)
		}
	}
	for _, in := range []string{"unlimited", "-", "12 parsecs", "1e9G", "99999999999999 TiB", "..", ""} {
		if _, ok := parseAmount(in, 1); ok {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestStorageCommandIsReadOnlyAndQuotesTheRoots(t *testing.T) {
	cmd := storageCommand("@@vqTEST", []string{"/data/it's here", "/scratch/x"})
	for _, bad := range []string{"sudo", " rm ", "mv ", "rm -", "chmod", "tee"} {
		if strings.Contains(strings.ReplaceAll(cmd, "2>&1", ""), bad) {
			t.Errorf("the command contains %q:\n%s", bad, cmd)
		}
	}
	if !strings.Contains(cmd, `'/data/it'\''s here'`) || !strings.Contains(cmd, "timeout 20") || strings.Count(cmd, "head -c") < 4 {
		t.Errorf("roots must be quoted, every tool timed and bounded:\n%s", cmd)
	}
	// the order the researcher asked for
	i1, i2, i3 := strings.Index(cmd, "beegfs-ctl --getquota"), strings.Index(cmd, "lfs quota"), strings.Index(cmd, "quota -s -w")
	if !(i1 >= 0 && i1 < i2 && i2 < i3) {
		t.Errorf("beegfs-ctl, lfs quota, quota -s -w in that order: %d %d %d", i1, i2, i3)
	}
}

func section(m, header, body string) string { return m + " " + header + "\n" + body + "\n" }

func TestParseStorageUsesTheFirstReadableToolAndFallsBack(t *testing.T) {
	const m = "@@vqT"
	roots := []string{"/home/u", "/scratch/u"}
	df0, df1 := section(m, "df 0", dfOut), section(m, "df 1", "df: /scratch/u: No such file or directory")
	now := time.Now()

	rep := ParseStorage(section(m, "beegfs", beegfsOut)+section(m, "lfs 0", lfsOut)+section(m, "quota", quotaOut)+df0+df1+m+" end\n", m, roots, now)
	if rep.Quota == nil || rep.Quota.Tool != "beegfs-ctl" || len(rep.Quota.Entries) != 1 || rep.QuotaNote != "" {
		t.Fatalf("%+v", rep.Quota)
	}
	if len(rep.Filesystems) != 2 || rep.Filesystems[0].Mount != "/home" || rep.Filesystems[0].Root != "/home/u" || rep.Filesystems[1].Error == "" || rep.Filesystems[1].TotalBytes != 0 {
		t.Errorf("df per root, an unreadable one reported as such: %+v", rep.Filesystems)
	}

	// beegfs-ctl exists but prints something that is not a quota table: lfs answers next
	rep = ParseStorage(section(m, "beegfs", "Error: unable to connect to mgmtd")+section(m, "lfs 1", lfsOut)+df0+df1, m, roots, now)
	if rep.Quota == nil || rep.Quota.Tool != "lfs quota" || rep.Quota.Entries[0].Scope != "/scratch" {
		t.Errorf("%+v", rep.Quota)
	}
	rep = ParseStorage(section(m, "lfs 0", "lfs quota: not a Lustre filesystem")+section(m, "quota", quotaOut)+df0, m, roots[:1], now)
	if rep.Quota == nil || rep.Quota.Tool != "quota" {
		t.Errorf("%+v", rep.Quota)
	}
	// nothing answered: say so, keep the free space
	rep = ParseStorage(section(m, "quota", "quota: Cannot open quotafile")+df0, m, roots[:1], now)
	if rep.Quota != nil || !strings.Contains(rep.QuotaNote, "quota tool is not available") || rep.Filesystems[0].TotalBytes == 0 {
		t.Errorf("%+v", rep)
	}
	// text that imitates a section header without the random marker is only text: it cannot supply another section's data
	forged := section(m, "beegfs", "garbage\n@@vqOTHER df 0\n"+dfOut+"\n"+m+"x df 0\n"+dfOut)
	if rep := ParseStorage(forged, m, roots[:1], now); rep.Quota != nil || rep.Filesystems[0].Error == "" || rep.Filesystems[0].TotalBytes != 0 {
		t.Errorf("a forged section boundary was honoured: %+v", rep)
	}
	if rep := ParseStorage("", m, roots, now); rep.Quota != nil || len(rep.Filesystems) != 2 || rep.Filesystems[0].Error == "" {
		t.Errorf("%+v", rep)
	}
}

// fakeTools makes a directory of stand-in remote tools for the ssh stand-in's PATH.
func fakeTools(t *testing.T, tools map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range tools {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir + ":/usr/bin:/bin"
}

func TestStorageOverSSHStandIn(t *testing.T) {
	big := filepath.Join(t.TempDir(), "big.txt")
	os.WriteFile(big, []byte(strings.Repeat("A", 2<<20)), 0o644)
	remotePath := fakeTools(t, map[string]string{
		"beegfs-ctl": "cat " + big + "\n", // 2 MiB of junk: bounded, then ignored
		"lfs":        "echo 'lfs quota: command not supported' >&2; exit 5\n",
		"quota":      "cat <<'EOF'\n" + quotaOut + "EOF\n",
	})
	m, h, root := envWith(t, fakeSSHWith(t, "022", -1, remotePath))
	rep, err := m.Storage(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Quota == nil || rep.Quota.Tool != "quota" || len(rep.Quota.Entries) != 2 {
		t.Fatalf("junk from the first tool must not stop the next: %+v", rep.Quota)
	}
	if len(rep.Filesystems) != 1 || rep.Filesystems[0].Root != root || rep.Filesystems[0].Error != "" || rep.Filesystems[0].TotalBytes <= 0 {
		t.Errorf("a real df of the root: %+v", rep.Filesystems)
	}

	// no quota tool on the host at all (a bare PATH): the free space still shows and the note says why
	bare := t.TempDir() // a PATH with sh, df, head, id, du and timeout only
	for _, tool := range []string{"sh", "bash", "df", "head", "id", "timeout", "du"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skip(tool + " not available")
		}
		os.Symlink(p, filepath.Join(bare, tool))
	}
	m, h, _ = envWith(t, fakeSSHWith(t, "022", -1, bare))
	rep, err = m.Storage(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Quota != nil || !strings.Contains(rep.QuotaNote, "not available") {
		t.Errorf("%+v", rep)
	}
	if len(rep.Filesystems) != 1 || rep.Filesystems[0].TotalBytes <= 0 {
		t.Errorf("%+v", rep.Filesystems)
	}

	// a host that does not answer is an error, not an empty report
	m, h, _ = envWith(t, "/nonexistent/ssh")
	if _, err := m.Storage(context.Background(), h); err == nil {
		t.Error("an unreachable host reported storage")
	}
}

func TestDiskUsageOverSSHStandIn(t *testing.T) {
	m, h, root := writeEnv(t)
	run := filepath.Join(root, "run1")
	os.MkdirAll(run, 0o755)
	os.WriteFile(filepath.Join(run, "a.bin"), make([]byte, 3<<20), 0o644)
	u, err := m.DiskUsage(context.Background(), h, run)
	if err != nil || u.Path != run || u.KiB < 3<<10 || u.Bytes != u.KiB<<10 || u.Partial || u.MeasuredAt.IsZero() {
		t.Fatalf("%+v %v", u, err)
	}
	// a folder with something the user cannot read gives a lower bound, flagged
	if os.Getuid() != 0 {
		locked := filepath.Join(run, "locked")
		os.MkdirAll(locked, 0o755)
		os.WriteFile(filepath.Join(locked, "x"), make([]byte, 1<<20), 0o644)
		os.Chmod(locked, 0)
		defer os.Chmod(locked, 0o755)
		if u, err := m.DiskUsage(context.Background(), h, run); err != nil || !u.Partial {
			t.Errorf("partial: %+v %v", u, err)
		}
	}
	// outside the roots, relative, and with shell characters: refused before any call
	for _, p := range []string{"/etc", filepath.Join(root, "..", "x"), "run1", root + "/a; rm -rf ~", root + "/$(id)/x", root + "/x\ny"} {
		if _, err := m.DiskUsage(context.Background(), h, p); err == nil {
			t.Errorf("%q was measured", p)
		}
	}
	// a path with a quote in it reaches du as one argument
	odd := filepath.Join(root, "it's a 'run' $HOME")
	os.MkdirAll(odd, 0o755)
	os.WriteFile(filepath.Join(odd, "f"), make([]byte, 1<<20), 0o644)
	if u, err := m.DiskUsage(context.Background(), h, odd); err != nil || u.KiB < 1024 {
		t.Errorf("%+v %v", u, err)
	}
	if _, err := m.DiskUsage(context.Background(), h, filepath.Join(root, "missing")); err == nil {
		t.Error("a missing folder has no size")
	}
}
