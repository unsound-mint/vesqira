package hosts

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestValidateVPNArgv(t *testing.T) {
	ok := [][]string{
		{"gpclient", "connect", "vpn.example.edu"},
		{"/usr/bin/gpclient", "connect", "vpn.example.edu"},
		{"openconnect", "--protocol=gp", "--user=me", "vpn.example.edu"},
		{"pkexec", "/usr/bin/gpclient", "connect", "a;b c|d$e"},
		{"nmcli", "connection", "up", ""}, // an empty later part is a legal (if odd) argument
	}
	for _, a := range ok {
		if err := ValidateVPNArgv(a); err != nil {
			t.Errorf("%q: %v", a, err)
		}
	}
	long := strings.Repeat("a", MaxVPNArgLen+1)
	var many []string
	for i := 0; i <= MaxVPNArgs; i++ {
		many = append(many, "x")
	}
	var wide []string // every part within its limit, together over the total
	for i := 0; i < MaxVPNArgs; i++ {
		wide = append(wide, strings.Repeat("b", MaxVPNArgLen-1))
	}
	wide[0] = "prog"
	bad := map[string][]string{
		"nil": nil, "empty": {}, "empty program": {""}, "oversized part": {"x", long}, "too many": many, "too long in all": wide,
		"relative": {"./x"}, "directory": {"bin/x"}, "dash": {"-x"}, "dotdot": {"/usr/../bin/x"}, "trailing slash": {"/usr/bin/"},
		"root": {"/"}, "shell text": {"gpclient connect"}, "semicolon": {"a;b"}, "tilde": {"~/bin/x"}, "NUL": {"x", "a\x00"},
		"newline": {"x", "a\nb"}, "invalid utf8": {"x", "\xff\xfe"},
	}
	for name, a := range bad {
		if err := ValidateVPNArgv(a); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
}

func openStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVPNCommandsAreStoredVerbatim(t *testing.T) {
	s := openStore(t)
	argv := []string{"gpclient", "connect", "  spaced; $(x) `y` \"q\" 'r' \\ ", "vpn.example.edu"}
	h, err := s.Create(Host{Name: "c", Dest: "c", Roots: []string{"/d"}, VPNCommand: argv, VPNDisconnectCommand: []string{"gpclient", "disconnect"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.VPNCommand, "\x00") != strings.Join(argv, "\x00") || len(got.VPNDisconnectCommand) != 2 {
		t.Fatalf("%q", got.VPNCommand)
	}
	if _, err := s.Create(Host{Name: "d", Dest: "d", Roots: []string{"/d"}, VPNCommand: []string{"a b"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an invalid command was stored: %v", err)
	}
	if h, err = s.SetVPN(h.ID, nil, nil); err != nil || len(h.VPNCommand) != 0 || len(h.VPNDisconnectCommand) != 0 {
		t.Fatalf("%v %+v", err, h)
	}
	if _, err := s.SetVPN(999, nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestRunVPNBoundsAndCleansOutput(t *testing.T) {
	// a program that writes escape sequences, a progress line and far more than the bound
	p := filepath.Join(t.TempDir(), "noisy")
	writeTestScript(t, p, `printf '\033[31mred\033[0m \033]0;title\007ok\n'; printf 'a\rb\rfinal\n'; yes x | head -c 100000`)
	res := RunVPN(context.Background(), []string{p}, 10*time.Second)
	if res.ExitCode != 0 || !res.OutputTruncated || len(res.Output) > MaxVPNOutput {
		t.Fatalf("exit %d truncated %v len %d", res.ExitCode, res.OutputTruncated, len(res.Output))
	}
	if !strings.HasPrefix(res.Output, "red ok\nfinal\n") || strings.ContainsAny(res.Output, "\x1b\x07\r") {
		t.Fatalf("%q", res.Output[:40])
	}
}

func TestCleanTerminalText(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b[1;32mgreen\x1b[0m":                   "green",
		"\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\": "link",
		"a\x00b\x07c":                              "abc",
		"keep\ttab\nline":                          "keep\ttab\nline",
		"10%\r50%\r100%\n":                         "100%\n",
		"\xffbad":                                  "�bad",
		"\x1b[?25l\x1b[2Jtext":                     "text",
	} {
		if got := CleanTerminalText(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestRunVPNReportsAProgramThatCannotStart(t *testing.T) {
	res := RunVPN(context.Background(), []string{"definitely-not-installed-xyz"}, time.Second)
	if res.ExitCode != -1 || res.StartError == "" {
		t.Fatalf("%+v", res)
	}
	res = RunVPN(context.Background(), []string{"x y"}, time.Second) // invalid argv is never run
	if res.StartError == "" {
		t.Fatalf("%+v", res)
	}
}

func writeTestScript(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
