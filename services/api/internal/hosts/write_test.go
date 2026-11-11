package hosts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSSHWith stands in for the ssh client: it drops the ssh options and the destination and runs the remote command on
// this machine (plain sh, no profile). The remote umask, the number of stdin bytes that arrive (-1: all) and the remote
// PATH (empty: unchanged) can be set.
func fakeSSHWith(t *testing.T, umask string, stdinCut int, remotePath string) string {
	t.Helper()
	feed := "cat"
	if stdinCut >= 0 {
		feed = fmt.Sprintf("head -c %d", stdinCut)
	}
	pathSet := ""
	if remotePath != "" {
		pathSet = "PATH=" + remotePath + " "
	}
	script := fmt.Sprintf(`#!/bin/sh
while [ $# -gt 0 ]; do case "$1" in -o) shift 2;; -T) shift;; *) break;; esac; done
shift
cmd=$(printf '%%s' "$*" | sed -e 's#bash -lc#bash -c#')
umask %s
%s | %s sh -c "$cmd"
`, umask, feed, pathSet)
	p := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func fakeSSH(t *testing.T) string { return fakeSSHWith(t, "022", -1, "") }

func envWith(t *testing.T, ssh string) (*Manager, Host, string) {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(base, "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(ssh, nil)
	m.ControlDir = t.TempDir()
	return m, Host{Name: "h", Dest: "login1", Python: "python3", Roots: []string{root}}, root
}

func writeEnv(t *testing.T) (*Manager, Host, string) {
	t.Helper()
	return envWith(t, fakeSSH(t))
}

const mk = "# Written by Vesqira"

func spec(root, rel, name, body string) WriteSpec {
	return WriteSpec{Root: root, Rel: rel, Name: name, Marker: mk, Create: true, Data: []byte(mk + "\n" + body)}
}

func TestWriteFileCreatesAtomicallyAndReplacesItsOwnFile(t *testing.T) {
	m, h, root := writeEnv(t)
	ctx := context.Background()
	if err := m.WriteFile(ctx, h, spec(root, ".vesqira", "project.toml", "one\n")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, ".vesqira", "project.toml"))
	if err != nil || string(got) != mk+"\none\n" {
		t.Fatalf("%q %v", got, err)
	}
	if err := m.WriteFile(ctx, h, spec(root, ".vesqira", "project.toml", "two\n")); err != nil {
		t.Fatalf("a file Vesqira wrote can be replaced: %v", err)
	}
	got, _ = os.ReadFile(filepath.Join(root, ".vesqira", "project.toml"))
	if string(got) != mk+"\ntwo\n" {
		t.Fatalf("%q", got)
	}
	ents, _ := os.ReadDir(filepath.Join(root, ".vesqira"))
	if len(ents) != 1 {
		t.Fatalf("no temporary file may remain: %v", ents)
	}
	if fi, _ := os.Stat(filepath.Join(root, ".vesqira", "project.toml")); fi.Mode().Perm() != 0o644 {
		t.Fatalf("a new file follows the umask (022): %v", fi.Mode())
	}
}

func TestWriteFileNeverClobbersAFileItDidNotWrite(t *testing.T) {
	m, h, root := writeEnv(t)
	ctx := context.Background()
	os.MkdirAll(filepath.Join(root, ".vesqira"), 0o755)
	foreign := filepath.Join(root, ".vesqira", "project.toml")
	os.WriteFile(foreign, []byte("[layout]\nskip = [\"old/**\"]\n"), 0o644)
	err := m.WriteFile(ctx, h, spec(root, ".vesqira", "project.toml", "x\n"))
	var wr *WriteRefused
	if !errors.As(err, &wr) || !errors.Is(err, ErrForeign) {
		t.Fatalf("want a foreign refusal, got %v", err)
	}
	if b, _ := os.ReadFile(foreign); string(b) != "[layout]\nskip = [\"old/**\"]\n" {
		t.Fatalf("the researcher's file changed: %q", b)
	}
	// a directory or a link at the target is not replaced either
	os.Mkdir(filepath.Join(root, ".vesqira", "dir.json"), 0o755)
	if err := m.WriteFile(ctx, h, spec(root, ".vesqira", "dir.json", "x")); !errors.As(err, &wr) || wr.Reason != "notfile" {
		t.Fatalf("%v", err)
	}
	os.Symlink(foreign, filepath.Join(root, ".vesqira", "link.json"))
	if err := m.WriteFile(ctx, h, spec(root, ".vesqira", "link.json", "x")); !errors.As(err, &wr) || wr.Reason != "link" {
		t.Fatalf("%v", err)
	}
}

func TestWriteFileStaysInsideTheFolder(t *testing.T) {
	m, h, root := writeEnv(t)
	ctx := context.Background()
	outside := filepath.Join(filepath.Dir(root), "elsewhere")
	os.MkdirAll(outside, 0o755)
	// before any round trip: outside the roots, dot-dot, absolute rel, a name with a slash
	for name, ws := range map[string]WriteSpec{
		"root outside": spec(outside, "", "a.json", ""),
		"dotdot":       spec(root, "../elsewhere", "a.json", ""),
		"absolute":     spec(root, outside, "a.json", ""),
		"slash name":   spec(root, "", "../a.json", ""),
		"dot name":     spec(root, "", "..", ""),
		"empty name":   spec(root, "", "", ""),
		"no marker":    {Root: root, Name: "a.json", Marker: mk, Data: []byte("x")},
		"newline mark": {Root: root, Name: "a.json", Marker: "a\nb", Data: []byte("a\nb")},
	} {
		if err := m.WriteFile(ctx, h, ws); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// on the host: a link inside the project that points out is refused, nothing is created through it
	os.Symlink(outside, filepath.Join(root, "door"))
	var wr *WriteRefused
	if err := m.WriteFile(ctx, h, spec(root, "door/sub", "a.json", "")); !errors.As(err, &wr) || wr.Reason != "link" {
		t.Fatalf("a link out of the project: %v", err)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("something was written through the link: %v", ents)
	}
	// the root itself reached through a link resolves outside the roots' own text, and is still just that folder
	if err := m.WriteFile(ctx, h, spec(root, "", "a.json", "")); err != nil {
		t.Fatal(err)
	}
	// a file where a folder is needed
	os.WriteFile(filepath.Join(root, "afile"), nil, 0o644)
	if err := m.WriteFile(ctx, h, spec(root, "afile/x", "a.json", "")); !errors.As(err, &wr) || wr.Reason != "notdir" {
		t.Fatalf("%v", err)
	}
	// a root that does not exist on the host
	if err := m.WriteFile(ctx, h, spec(filepath.Join(root, "missing"), "", "a.json", "")); !errors.As(err, &wr) || wr.Reason != "root" {
		t.Fatalf("%v", err)
	}
}

// Names with spaces, quotes, command substitutions and a trailing semicolon reach the host as arguments, never as shell syntax.
func TestWriteFileQuotesHostileNames(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	t.Chdir(base) // a command substitution that ran would create ./PWNED here
	pwn := "PWNED"
	pwnPath := filepath.Join(base, pwn)
	hostileRoot := filepath.Join(base, fmt.Sprintf("my proj its \"q\" $(touch %s) `touch %s` ;touch %s;", pwn, pwn, pwn))
	if err := os.MkdirAll(hostileRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(fakeSSH(t), nil)
	m.ControlDir = t.TempDir()
	h := Host{Name: "h", Dest: "login1", Python: "python3", Roots: []string{hostileRoot}}
	rel := "a b/$(touch " + pwn + ")/its new line/\"x\""
	name := "run $(touch " + pwn + ") \"two\" `three`;&|<>.json"
	if err := m.WriteFile(context.Background(), h, spec(hostileRoot, rel, name, "{}")); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(hostileRoot, filepath.FromSlash(rel), name)
	if b, err := os.ReadFile(want); err != nil || string(b) != mk+"\n{}" {
		t.Fatalf("the file is not where the hostile names say: %v %q", err, b)
	}
	if _, err := os.Stat(pwnPath); err == nil {
		t.Fatal("a name was executed as shell syntax")
	}
	// the same names on a second write replace the file (the marker is found), still without executing anything
	if err := m.WriteFile(context.Background(), h, spec(hostileRoot, rel, name, "{ }")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pwnPath); err == nil {
		t.Fatal("a name was executed as shell syntax")
	}
	// a marker that looks like an option or a pattern is data
	m2 := WriteSpec{Root: hostileRoot, Name: "o.json", Marker: "-e .*", Data: []byte("-e .* y")}
	if err := m.WriteFile(context.Background(), h, m2); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteFile(context.Background(), h, m2); err != nil {
		t.Fatalf("second write with an option-like marker: %v", err)
	}
	if !strings.Contains(string(mustRead(t, filepath.Join(hostileRoot, "o.json"))), "-e .*") {
		t.Fatal("content")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
