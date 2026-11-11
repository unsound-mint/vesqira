package hosts

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A new file gets the account's default mode (the umask applies); a replacement keeps the mode of the file it replaces.
func TestWriteFileModes(t *testing.T) {
	m, h, root := envWith(t, fakeSSHWith(t, "077", -1, ""))
	ctx := context.Background()
	f := filepath.Join(root, ".vesqira", "project.toml")
	if err := m.WriteFile(ctx, h, spec(root, ".vesqira", "project.toml", "one\n")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o600 {
		t.Fatalf("umask 077 must give 0600, got %v", fi.Mode())
	}
	os.Chmod(f, 0o664) // the researcher shares it with their group
	if err := m.WriteFile(ctx, h, spec(root, ".vesqira", "project.toml", "two\n")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o664 || string(mustRead(t, f)) != mk+"\ntwo\n" {
		t.Fatalf("a replacement keeps the old mode: %v", fi.Mode())
	}
	os.Chmod(f, 0o444)
	err := m.WriteFile(ctx, h, spec(root, ".vesqira", "project.toml", "three\n"))
	var wr *WriteRefused
	if !errors.As(err, &wr) || wr.Reason != "readonly" || string(mustRead(t, f)) != mk+"\ntwo\n" {
		t.Fatalf("a read-only file is left alone: %v", err)
	}
}

// A stream cut short must never install a partial file: the host checks the byte count and the sha256.
func TestWriteFileRefusesATruncatedStream(t *testing.T) {
	m, h, root := writeEnv(t)
	ctx := context.Background()
	f := filepath.Join(root, ".vesqira", "project.toml")
	if err := m.WriteFile(ctx, h, spec(root, ".vesqira", "project.toml", "original\n")); err != nil {
		t.Fatal(err)
	}
	cut := NewManager(fakeSSHWith(t, "022", 12, ""), nil)
	cut.ControlDir = t.TempDir()
	err := cut.WriteFile(ctx, h, spec(root, ".vesqira", "project.toml", "a much longer replacement that is cut\n"))
	var wr *WriteRefused
	if !errors.As(err, &wr) || wr.Reason != "truncated" {
		t.Fatalf("want a truncated refusal, got %v", err)
	}
	if string(mustRead(t, f)) != mk+"\noriginal\n" {
		t.Fatalf("the original changed: %q", mustRead(t, f))
	}
	if ents, _ := os.ReadDir(filepath.Join(root, ".vesqira")); len(ents) != 1 {
		t.Fatalf("no temporary file may remain: %v", ents)
	}
	if ents, _ := os.ReadDir(root); len(ents) != 1 {
		t.Fatalf("no temporary folder may remain: %v", ents)
	}
	// a cut stream into a folder that does not exist yet creates nothing at all
	if err := cut.WriteFile(ctx, h, spec(root, "new/sub", "a.json", "a much longer replacement that is cut\n")); !errors.As(err, &wr) || wr.Reason != "truncated" {
		t.Fatalf("%v", err)
	}
	if _, serr := os.Stat(filepath.Join(root, "new")); serr == nil {
		t.Fatal("a folder was created for a write that was refused")
	}
}

func toolsDir(t *testing.T, tools ...string) string {
	t.Helper()
	bin := t.TempDir()
	for _, tool := range tools {
		src, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("no %s on this machine", tool)
		}
		os.Symlink(src, filepath.Join(bin, tool))
	}
	return bin
}

var baseTools = []string{"sh", "cat", "cut", "tr", "wc", "mktemp", "mkdir", "mv", "cp", "rm", "head", "sed", "bash"}

// With neither sha256sum nor shasum the host refuses before touching anything.
func TestWriteFileNeedsASha256Tool(t *testing.T) {
	m, h, root := envWith(t, fakeSSHWith(t, "022", -1, toolsDir(t, baseTools...)))
	err := m.WriteFile(context.Background(), h, spec(root, "d", "a.json", ""))
	var wr *WriteRefused
	if !errors.As(err, &wr) || wr.Reason != "nosha256" || !strings.Contains(err.Error(), "sha256sum or shasum") {
		t.Fatalf("want a clear no-sha256 refusal, got %v", err)
	}
	if ents, _ := os.ReadDir(root); len(ents) != 0 {
		t.Fatalf("nothing may be created: %v", ents)
	}
}

// shasum alone is enough (macOS and BSD have no sha256sum).
func TestWriteFileFallsBackToShasum(t *testing.T) {
	if _, err := exec.LookPath("shasum"); err != nil {
		t.Skip("no shasum on this machine")
	}
	m, h, root := envWith(t, fakeSSHWith(t, "022", -1, toolsDir(t, append(baseTools, "shasum", "perl", "env")...)))
	for _, body := range []string{"x", "y"} {
		if err := m.WriteFile(context.Background(), h, spec(root, "d", "a.json", body)); err != nil {
			t.Fatal(err)
		}
	}
	if string(mustRead(t, filepath.Join(root, "d", "a.json"))) != mk+"\ny" {
		t.Fatal("content")
	}
}

// The file changed after the caller read it (or appeared after it saw none): refused, nothing changes.
func TestWriteFileRefusesAFileThatChangedSinceItWasRead(t *testing.T) {
	m, h, root := writeEnv(t)
	ctx := context.Background()
	f := filepath.Join(root, ".vesqira", "project.toml")
	if err := m.WriteFile(ctx, h, spec(root, ".vesqira", "project.toml", "one\n")); err != nil {
		t.Fatal(err)
	}
	read, exists, err := m.ReadExisting(ctx, h, root, ".vesqira", "project.toml")
	if err != nil || !exists || string(read) != mk+"\none\n" {
		t.Fatalf("%q %v %v", read, exists, err)
	}
	edited := mk + "\nedited by the researcher meanwhile\n"
	os.WriteFile(f, []byte(edited), 0o644)
	ws := spec(root, ".vesqira", "project.toml", "two\n")
	ws.Expect = SHA256Hex(read)
	var wr *WriteRefused
	if err := m.WriteFile(ctx, h, ws); !errors.As(err, &wr) || wr.Reason != "changed" {
		t.Fatalf("want a changed refusal, got %v", err)
	}
	if string(mustRead(t, f)) != edited {
		t.Fatalf("the edit was lost: %q", mustRead(t, f))
	}
	ws.Expect = ExpectAbsent // the caller saw no file, then one appeared
	if err := m.WriteFile(ctx, h, ws); !errors.As(err, &wr) || wr.Reason != "changed" {
		t.Fatalf("a file that appeared must be refused: %v", err)
	}
	if string(mustRead(t, f)) != edited {
		t.Fatal("changed")
	}
	os.Remove(f) // the caller read content, then the file vanished
	ws.Expect = SHA256Hex(read)
	if err := m.WriteFile(ctx, h, ws); !errors.As(err, &wr) || wr.Reason != "changed" {
		t.Fatalf("a vanished file must be refused: %v", err)
	}
	if _, serr := os.Stat(f); serr == nil {
		t.Fatal("written although it had vanished")
	}
}

// ReadExisting reports absent folders and files, refuses links, and returns the bytes exactly (no trailing newline added).
func TestReadExisting(t *testing.T) {
	m, h, root := writeEnv(t)
	ctx := context.Background()
	if _, ex, err := m.ReadExisting(ctx, h, root, "no/such", "a.json"); ex || err != nil {
		t.Fatalf("%v %v", ex, err)
	}
	os.WriteFile(filepath.Join(root, "bare"), []byte("no newline"), 0o644)
	os.WriteFile(filepath.Join(root, "empty"), nil, 0o644)
	os.WriteFile(filepath.Join(root, "ends"), []byte("a\n\n"), 0o644)
	for name, want := range map[string]string{"bare": "no newline", "empty": "", "ends": "a\n\n"} {
		got, ex, err := m.ReadExisting(ctx, h, root, "", name)
		if err != nil || !ex || string(got) != want {
			t.Fatalf("%s: %q %v %v", name, got, ex, err)
		}
	}
	if _, ex, err := m.ReadExisting(ctx, h, root, "", "missing"); ex || err != nil {
		t.Fatalf("%v %v", ex, err)
	}
	os.WriteFile(filepath.Join(root, "big"), make([]byte, MaxRead+1), 0o644)
	var wr *WriteRefused
	if _, _, err := m.ReadExisting(ctx, h, root, "", "big"); !errors.As(err, &wr) || wr.Reason != "toolarge" {
		t.Fatalf("%v", err)
	}
	os.Symlink(filepath.Join(root, "bare"), filepath.Join(root, "lnk"))
	if _, _, err := m.ReadExisting(ctx, h, root, "", "lnk"); !errors.As(err, &wr) || wr.Reason != "link" {
		t.Fatalf("%v", err)
	}
}

// Without Create, a folder that is missing is refused and not made (a job's output folder belongs to the job).
func TestWriteFileDoesNotCreateWhatItWasNotAskedTo(t *testing.T) {
	m, h, root := writeEnv(t)
	ws := spec(root, "runs/out", "run.json", "{}")
	ws.Create = false
	var wr *WriteRefused
	if err := m.WriteFile(context.Background(), h, ws); !errors.As(err, &wr) || wr.Reason != "absent" {
		t.Fatalf("%v", err)
	}
	if _, serr := os.Stat(filepath.Join(root, "runs")); serr == nil {
		t.Fatal("a folder was created")
	}
	os.MkdirAll(filepath.Join(root, "runs", "out"), 0o755)
	if err := m.WriteFile(context.Background(), h, ws); err != nil {
		t.Fatalf("the folder exists now: %v", err)
	}
}

// A refusal leaves no folder behind: everything is checked before the first mkdir.
func TestWriteFileMakesNoFolderWhenACheckRefuses(t *testing.T) {
	m, h, root := writeEnv(t)
	os.WriteFile(filepath.Join(root, "afile"), nil, 0o644)
	if err := m.WriteFile(context.Background(), h, spec(root, "afile/x/y", "a.json", "")); err == nil {
		t.Fatal("accepted")
	}
	os.MkdirAll(filepath.Join(root, "ok", "deeper"), 0o755)
	os.Symlink(root, filepath.Join(root, "ok", "deeper", "door"))
	if err := m.WriteFile(context.Background(), h, spec(root, "ok/new/deeper/door/x", "a.json", "")); err != nil {
		t.Fatalf("a plain nested create under an existing folder: %v", err)
	}
	if err := m.WriteFile(context.Background(), h, spec(root, "ok/deeper/door/new", "a.json", "")); err == nil {
		t.Fatal("accepted a link")
	}
	if _, serr := os.Stat(filepath.Join(root, "new")); serr == nil {
		t.Fatal("made a folder through a link")
	}
	// a caller that expects an existing file under a folder that is not there is refused before any folder is made
	ws := spec(root, "gone/sub", "a.json", "")
	ws.Expect = SHA256Hex([]byte("old"))
	var wr *WriteRefused
	if err := m.WriteFile(context.Background(), h, ws); !errors.As(err, &wr) || wr.Reason != "changed" {
		t.Fatalf("%v", err)
	}
	if _, serr := os.Stat(filepath.Join(root, "gone")); serr == nil {
		t.Fatal("made a folder for a refused write")
	}
}

// sshd runs the command through the account's login shell. csh rejects a newline inside single quotes and reads "!" and a
// backslash specially; fish reads a backslash inside single quotes. The scripts avoid all of them.
func TestScriptsSurviveAnyLoginShell(t *testing.T) {
	for name, sc := range map[string]string{"write": writeScript, "read": readScript} {
		for _, bad := range []string{"\n", "\r", "'", "\\", "!", "\x00"} {
			if strings.Contains(sc, bad) {
				t.Errorf("%s script contains %q", name, bad)
			}
		}
	}
}

// Characters a login shell would treat specially are refused before anything is sent.
func TestWriteFileRefusesBackslashNulAndNewlineInNames(t *testing.T) {
	m, h, root := writeEnv(t)
	for name, ws := range map[string]WriteSpec{
		"backslash name": spec(root, "", "a\\b.json", ""),
		"backslash rel":  spec(root, "a\\b", "a.json", ""),
		"newline name":   spec(root, "", "a\nb.json", ""),
		"newline rel":    spec(root, "a\nb", "a.json", ""),
		"nul name":       spec(root, "", "a\x00b.json", ""),
		"nul rel":        spec(root, "a\x00b", "a.json", ""),
		"cr name":        spec(root, "", "a\rb.json", ""),
		"backslash root": spec(root+"\\x", "", "a.json", ""),
		"newline root":   spec(root+"\nx", "", "a.json", ""),
		"backslash mark": {Root: root, Name: "a.json", Marker: "a\\b", Data: []byte("a\\b")},
	} {
		if err := m.WriteFile(context.Background(), h, ws); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if ents, _ := os.ReadDir(root); len(ents) != 0 {
		t.Fatalf("something was written: %v", ents)
	}
}

// The marker has to start the first line; a mention further down does not make a file ours.
func TestWriteFileMarkerMustStartTheFirstLine(t *testing.T) {
	m, h, root := writeEnv(t)
	f := filepath.Join(root, "project.toml")
	later := "[layout]\n" + mk + " (copied from somewhere)\n"
	os.WriteFile(f, []byte(later), 0o644)
	if err := m.WriteFile(context.Background(), h, spec(root, "", "project.toml", "x\n")); !errors.Is(err, ErrForeign) {
		t.Fatalf("a marker on a later line must not count: %v", err)
	}
	if string(mustRead(t, f)) != later {
		t.Fatal("changed")
	}
}

// A custom recogniser (the run record is JSON) decides what counts as Vesqira's.
func TestWriteFileUsesTheCallersRecogniser(t *testing.T) {
	m, h, root := writeEnv(t)
	f := filepath.Join(root, "run.json")
	os.WriteFile(f, []byte(`{"note": "vesqira-run-record"}`), 0o644)
	isOurs := func(b []byte) bool { return strings.Contains(string(b), `"record": "vesqira-run-record"`) }
	ws := WriteSpec{Root: root, Name: "run.json", Marker: "vesqira-run-record", Data: []byte(`{"record": "vesqira-run-record"}`), Recognise: isOurs}
	if err := m.WriteFile(context.Background(), h, ws); !errors.Is(err, ErrForeign) {
		t.Fatalf("%v", err)
	}
	os.Remove(f)
	for i := 0; i < 2; i++ {
		if err := m.WriteFile(context.Background(), h, ws); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
}
