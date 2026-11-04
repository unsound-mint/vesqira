package hosts

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `
# comment
Host *
    ServerAliveInterval 30
    User everyone

Host irgetas irgetas-login
    HostName login.example.org
    User denis
    IdentityFile ~/.ssh/id_secret
    ProxyCommand ssh -W %h:%p bastion

host=lowercase-form
  hostname 10.0.0.7

Host !blocked wild* ?q -bad ok.name
Host "quoted"
    HostName shouldnotattach

Match host irgetas exec "true"
    HostName never.example.org

Host later
    HostName "later.example.org"
    User bad value with spaces
`

func names(hs []SSHHost) string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Alias)
	}
	return strings.Join(out, ",")
}

func TestSSHConfigOffersPlainAliasesAndOnlyWhatIsPlain(t *testing.T) {
	hs := ParseSSHConfig(strings.NewReader(sample), nil)
	if got := names(hs); got != "irgetas,irgetas-login,later,lowercase-form,ok.name" {
		t.Fatalf("aliases: %s", got)
	}
	by := map[string]SSHHost{}
	for _, h := range hs {
		by[h.Alias] = h
	}
	if by["irgetas"].HostName != "login.example.org" || by["irgetas"].User != "denis" || by["irgetas-login"].User != "denis" {
		t.Errorf("irgetas: %+v %+v", by["irgetas"], by["irgetas-login"])
	}
	if by["lowercase-form"].HostName != "10.0.0.7" {
		t.Errorf("Host=alias and case-insensitive keywords: %+v", by["lowercase-form"])
	}
	if by["later"].HostName != "later.example.org" || by["later"].User != "" {
		t.Errorf("a value with spaces must not be kept: %+v", by["later"])
	}
	if by["irgetas"].HostName == "never.example.org" {
		t.Error("a Match block changed a host")
	}
	for _, h := range hs {
		if strings.ContainsAny(h.Alias, "*?!\"") || strings.HasPrefix(h.Alias, "-") {
			t.Errorf("unsafe alias offered: %q", h.Alias)
		}
	}
}

func TestLoadSSHConfigFollowsIncludesInsideTheDirectoryOnly(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("Host outsider\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	write("config", "Include conf.d/*\nInclude ../x\nInclude "+outside+"\nHost main\n")
	write("conf.d/a", "Host from-include\n  User u\n")
	hs, found := LoadSSHConfig(filepath.Join(dir, "config"))
	if !found || names(hs) != "from-include,main" {
		t.Fatalf("found=%v %s", found, names(hs))
	}
	if hs2, found2 := LoadSSHConfig(filepath.Join(dir, "missing")); found2 || len(hs2) != 0 {
		t.Errorf("a missing file offers nothing: %v %v", found2, hs2)
	}
	var _ io.Reader = strings.NewReader("")
}

func TestAnAbsurdlyLargeConfigIsCapped(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxSSHHosts+50; i++ {
		b.WriteString("Host h")
		b.WriteString(strings.Repeat("a", 1+i%5))
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(strings.Repeat("b", i/26))
		b.WriteString("\n")
	}
	if n := len(ParseSSHConfig(strings.NewReader(b.String()), nil)); n > maxSSHHosts {
		t.Errorf("%d hosts", n)
	}
}
