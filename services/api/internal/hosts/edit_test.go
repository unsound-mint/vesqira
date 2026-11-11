package hosts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func putFile(t *testing.T, p, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func reason(err error) string {
	var wr *WriteRefused
	if errors.As(err, &wr) {
		return wr.Reason
	}
	return ""
}

func TestEditRoundTripKeepsTheModeAndLeavesNothingBehind(t *testing.T) {
	m, h, root := envWith(t, fakeSSHWith(t, "077", -1, ""))
	ctx := context.Background()
	p := filepath.Join(root, "cfg", "pipeline.toml")
	putFile(t, p, "ker = 8\n", 0o664)
	f, err := m.ReadText(ctx, h, p)
	if err != nil || string(f.Content) != "ker = 8\n" || f.Sha256 != SHA256Hex(f.Content) || f.Size != 8 || f.Mtime == "" || f.Mtime == "0" {
		t.Fatalf("%+v %v", f, err)
	}
	saved, conflict, err := m.ReplaceText(ctx, h, p, []byte("ker = 16\n# no trailing newline"), f.Sha256)
	if err != nil || conflict != nil {
		t.Fatalf("%v %v", err, conflict)
	}
	if string(mustRead(t, p)) != "ker = 16\n# no trailing newline" || saved.Sha256 != SHA256Hex(mustRead(t, p)) {
		t.Fatalf("%q %+v", mustRead(t, p), saved)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o664 { // umask 077 on the host must not narrow the researcher's mode
		t.Fatalf("mode %v", fi.Mode())
	}
	if ents, _ := os.ReadDir(filepath.Dir(p)); len(ents) != 1 {
		t.Fatalf("a temporary file or folder remained: %v", ents)
	}
	// the new version is the next base
	if _, c, err := m.ReplaceText(ctx, h, p, []byte("x\n"), saved.Sha256); err != nil || c != nil {
		t.Fatalf("%v %v", err, c)
	}
}

func TestEditRoundTripOfAnEmptyFileAndOneWithoutATrailingNewline(t *testing.T) {
	m, h, root := writeEnv(t)
	for name, body := range map[string]string{"empty.txt": "", "nonl.txt": "abc", "nl.txt": "abc\n", "blank.txt": "\n\n", "crlf.txt": "a\r\nb\r\n"} {
		p := filepath.Join(root, name)
		putFile(t, p, body, 0o644)
		f, err := m.ReadText(context.Background(), h, p)
		if err != nil || string(f.Content) != body {
			t.Fatalf("%s: %q %v", name, f.Content, err)
		}
	}
}

func TestEditStaleShaIsAConflictCarryingTheCurrentFile(t *testing.T) {
	m, h, root := writeEnv(t)
	ctx := context.Background()
	p := filepath.Join(root, "job.sh")
	putFile(t, p, "one\n", 0o755)
	f, _ := m.ReadText(ctx, h, p)
	putFile(t, p, "changed on the host\n", 0o755)
	saved, conflict, err := m.ReplaceText(ctx, h, p, []byte("mine\n"), f.Sha256)
	if err != nil || conflict == nil || saved.Sha256 != "" {
		t.Fatalf("%+v %v %v", saved, conflict, err)
	}
	if string(conflict.Content) != "changed on the host\n" || conflict.Sha256 != SHA256Hex(conflict.Content) || conflict.Mtime == "" {
		t.Fatalf("%+v", conflict)
	}
	if string(mustRead(t, p)) != "changed on the host\n" {
		t.Fatal("a conflicting save wrote")
	}
	if ents, _ := os.ReadDir(root); len(ents) != 1 {
		t.Fatalf("leftovers: %v", ents)
	}
	// keeping mine = resending with the base the conflict returned
	if _, c, err := m.ReplaceText(ctx, h, p, []byte("mine\n"), conflict.Sha256); err != nil || c != nil || string(mustRead(t, p)) != "mine\n" {
		t.Fatalf("%v %v", err, c)
	}
}

func TestEditNeverCreatesAFileOrAFolder(t *testing.T) {
	m, h, root := writeEnv(t)
	ctx := context.Background()
	sum := SHA256Hex([]byte("x"))
	for _, p := range []string{filepath.Join(root, "new.toml"), filepath.Join(root, "no", "such", "new.toml")} {
		if _, _, err := m.ReplaceText(ctx, h, p, []byte("x"), sum); reason(err) != "absent" {
			t.Fatalf("%s: %v", p, err)
		}
		if _, err := m.ReadText(ctx, h, p); reason(err) != "absent" {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if ents, _ := os.ReadDir(root); len(ents) != 0 {
		t.Fatalf("something was created: %v", ents)
	}
	// a file that vanished between open and save is not recreated
	p := filepath.Join(root, "a.toml")
	putFile(t, p, "a", 0o644)
	f, _ := m.ReadText(ctx, h, p)
	os.Remove(p)
	if _, _, err := m.ReplaceText(ctx, h, p, []byte("b"), f.Sha256); reason(err) != "absent" {
		t.Fatalf("%v", err)
	}
	if _, serr := os.Stat(p); serr == nil {
		t.Fatal("recreated")
	}
}

func TestEditRefusesLinksEscapesAndNonRegularFiles(t *testing.T) {
	m, h, root := writeEnv(t)
	ctx := context.Background()
	outside := filepath.Join(filepath.Dir(root), "elsewhere")
	putFile(t, filepath.Join(outside, "secret.toml"), "s = 1\n", 0o644)
	putFile(t, filepath.Join(root, "real.toml"), "r = 1\n", 0o644)
	os.Symlink(filepath.Join(outside, "secret.toml"), filepath.Join(root, "leak.toml")) // file link out of the root
	os.Symlink(filepath.Join(root, "real.toml"), filepath.Join(root, "alias.toml"))     // file link inside the root
	os.Symlink(outside, filepath.Join(root, "door"))                                    // folder link out of the root
	os.MkdirAll(filepath.Join(root, "adir.toml"), 0o755)
	if mkfifo, err := exec.LookPath("mkfifo"); err == nil {
		exec.Command(mkfifo, filepath.Join(root, "pipe.log")).Run()
	}
	want := map[string]string{"leak.toml": "link", "alias.toml": "link", "door/secret.toml": "link", "adir.toml": "notfile"}
	if _, err := os.Lstat(filepath.Join(root, "pipe.log")); err == nil {
		want["pipe.log"] = "notfile"
	}
	sum := SHA256Hex([]byte("s = 1\n"))
	for rel, why := range want {
		p := filepath.Join(root, rel)
		if _, err := m.ReadText(ctx, h, p); reason(err) != why {
			t.Errorf("read %s: want %s, got %v", rel, why, err)
		}
		if _, _, err := m.ReplaceText(ctx, h, p, []byte("pwned"), sum); reason(err) != why {
			t.Errorf("save %s: want %s, got %v", rel, why, err)
		}
	}
	if string(mustRead(t, filepath.Join(outside, "secret.toml"))) != "s = 1\n" || string(mustRead(t, filepath.Join(root, "real.toml"))) != "r = 1\n" {
		t.Fatal("a refused save wrote through a link")
	}
	// ".." and paths outside every root never reach the host
	for _, p := range []string{root + "/../elsewhere/secret.toml", filepath.Join(outside, "secret.toml"), root, root + "/", root + "//real.toml", "real.toml"} {
		if _, err := m.ReadText(ctx, h, p); err == nil || reason(err) != "" {
			t.Errorf("%q: want a local refusal, got %v", p, err)
		}
	}
	// a link made after the open: the target became a symlink
	p := filepath.Join(root, "real.toml")
	f, _ := m.ReadText(ctx, h, p)
	os.Remove(p)
	os.Symlink(filepath.Join(outside, "secret.toml"), p)
	if _, _, err := m.ReplaceText(ctx, h, p, []byte("x"), f.Sha256); reason(err) != "link" {
		t.Fatalf("%v", err)
	}
	if string(mustRead(t, filepath.Join(outside, "secret.toml"))) != "s = 1\n" {
		t.Fatal("wrote through a link made after the open")
	}
}

func TestEditNestedRootsUseTheLongestRoot(t *testing.T) {
	m, h, root := writeEnv(t)
	inner := filepath.Join(root, "inner")
	h.Roots = []string{root, inner + "/"}
	p := filepath.Join(inner, "d", "a.md")
	putFile(t, p, "hi", 0o644)
	if f, err := m.ReadText(context.Background(), h, p); err != nil || string(f.Content) != "hi" {
		t.Fatalf("%v", err)
	}
}

func TestEditOverTheLimitAndExactlyAtIt(t *testing.T) {
	m, h, root := writeEnv(t)
	ctx := context.Background()
	exact := strings.Repeat("0123456789abcdef\n", MaxEditBytes/17)
	exact += strings.Repeat("x", MaxEditBytes-len(exact))
	p := filepath.Join(root, "big.log")
	putFile(t, p, exact, 0o644)
	f, err := m.ReadText(ctx, h, p)
	if err != nil || len(f.Content) != MaxEditBytes || string(f.Content) != exact {
		t.Fatalf("a file of exactly the limit must come back whole: %d %v", len(f.Content), err)
	}
	// and be saved whole
	if _, c, err := m.ReplaceText(ctx, h, p, []byte(exact[:len(exact)-1]+"y"), f.Sha256); err != nil || c != nil {
		t.Fatalf("%v %v", err, c)
	}
	putFile(t, p, exact+"z", 0o644)
	if _, err := m.ReadText(ctx, h, p); reason(err) != "toolarge" {
		t.Fatalf("%v", err)
	}
	if _, _, err := m.ReplaceText(ctx, h, p, []byte("x"), f.Sha256); reason(err) != "toolarge" { // a conflict whose current file is too large to show
		t.Fatalf("%v", err)
	}
	if _, _, err := m.ReplaceText(ctx, h, p, []byte(exact+"z"), f.Sha256); err == nil {
		t.Fatal("content over the limit must be refused before it is sent")
	}
}

func TestEditRefusesReadOnlyFilesAndATruncatedStream(t *testing.T) {
	m, h, root := writeEnv(t)
	ctx := context.Background()
	p := filepath.Join(root, "ro.toml")
	putFile(t, p, "a\n", 0o444)
	f, _ := m.ReadText(ctx, h, p)
	if _, _, err := m.ReplaceText(ctx, h, p, []byte("b\n"), f.Sha256); reason(err) != "readonly" {
		t.Fatalf("%v", err)
	}
	cut, ch, croot := envWith(t, fakeSSHWith(t, "022", 3, ""))
	q := filepath.Join(croot, "a.toml")
	putFile(t, q, "old\n", 0o644)
	g, _ := cut.ReadText(ctx, ch, q)
	if _, _, err := cut.ReplaceText(ctx, ch, q, []byte("a much longer replacement\n"), g.Sha256); reason(err) != "truncated" {
		t.Fatalf("%v", err)
	}
	if string(mustRead(t, q)) != "old\n" {
		t.Fatal("a cut stream reached the file")
	}
	if ents, _ := os.ReadDir(croot); len(ents) != 1 {
		t.Fatalf("leftovers: %v", ents)
	}
}

func TestEditNeedsASha256Tool(t *testing.T) {
	m, h, root := envWith(t, fakeSSHWith(t, "022", -1, toolsDir(t, baseTools...)))
	p := filepath.Join(root, "a.toml")
	putFile(t, p, "a", 0o644)
	if _, err := m.ReadText(context.Background(), h, p); reason(err) != "nosha256" {
		t.Fatalf("%v", err)
	}
	if _, _, err := m.ReplaceText(context.Background(), h, p, []byte("b"), SHA256Hex([]byte("a"))); reason(err) != "nosha256" {
		t.Fatalf("%v", err)
	}
	if string(mustRead(t, p)) != "a" {
		t.Fatal("changed")
	}
}

func TestEditRefusesBackslashNulNewlineAndCRInPathsBeforeSending(t *testing.T) {
	m, h, root := writeEnv(t)
	for _, bad := range []string{"a\\b.toml", "a\nb.toml", "a\rb.toml", "a\x00b.toml", "d\\/a.toml"} {
		p := root + "/" + bad
		if _, err := m.ReadText(context.Background(), h, p); err == nil || reason(err) != "" {
			t.Errorf("read %q: %v", bad, err)
		}
		if _, _, err := m.ReplaceText(context.Background(), h, p, []byte("x"), SHA256Hex([]byte("x"))); err == nil || reason(err) != "" {
			t.Errorf("save %q: %v", bad, err)
		}
	}
}

// Names with spaces, quotes, $(), backticks and shell operators are data at every level: root, folder and file.
func TestEditQuotesHostileNames(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	t.Chdir(base) // a command substitution that ran would create ./PWNED here
	pwn := "PWNED"
	root := filepath.Join(base, fmt.Sprintf("my proj it's \"q\" $(touch %s) `touch %s` ;touch %s;", pwn, pwn, pwn))
	rel := "a b/$(touch " + pwn + ")/it's \"x\" `touch " + pwn + "`"
	name := "run $(touch " + pwn + ") 'one' \"two\" `three`;&|<>*?[x] -rf.json"
	p := filepath.Join(root, rel, name)
	putFile(t, p, "{}\n", 0o640)
	m := NewManager(fakeSSH(t), nil)
	m.ControlDir = t.TempDir()
	h := Host{Name: "h", Dest: "login1", Python: "python3", Roots: []string{root}}
	ctx := context.Background()
	f, err := m.ReadText(ctx, h, p)
	if err != nil || string(f.Content) != "{}\n" {
		t.Fatalf("%q %v", f.Content, err)
	}
	if _, c, err := m.ReplaceText(ctx, h, p, []byte("{ }\n"), f.Sha256); err != nil || c != nil || string(mustRead(t, p)) != "{ }\n" {
		t.Fatalf("%v %v", err, c)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if _, serr := os.Stat(filepath.Join(base, pwn)); serr == nil {
		t.Fatal("a name was executed as shell syntax")
	}
}

// Both scripts are one line a csh or fish login shell hands to sh unchanged.
func TestEditScriptsSurviveAnyLoginShell(t *testing.T) {
	for name, sc := range map[string]string{"edit read": editReadScript, "edit write": editWriteScript} {
		for _, bad := range []string{"\n", "\r", "'", "\\", "!", "\x00"} {
			if strings.Contains(sc, bad) {
				t.Errorf("%s script contains %q", name, bad)
			}
		}
	}
	if !bytes.Contains([]byte(editWriteScript), []byte("cp -p")) {
		t.Error("the mode of the existing file is kept with cp -p")
	}
}
