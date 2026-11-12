package main

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
)

// ADR 0050 section 1: what the group server refuses to start with.
func TestGroupServerRefusesUnsafeStarts(t *testing.T) {
	ok := groupStart{origin: "https://Lab.Example.edu", addr: "127.0.0.1:8787"}
	if o, err := ok.check(); err != nil || o != "https://lab.example.edu" {
		t.Fatalf("a good start: %q %v", o, err)
	}
	nfs := func(string) (bool, string) { return true, "NFS" }
	for name, g := range map[string]groupStart{
		"no origin":            {addr: "127.0.0.1:8787"},
		"http origin":          {origin: "http://lab.example.edu"},
		"http with dev flag":   {origin: "http://lab.example.edu", insecureDev: true},
		"origin with a path":   {origin: "https://lab.example.edu/vesqira"},
		"editor":               {origin: "https://lab.example.edu", editor: true},
		"terminal":             {origin: "https://lab.example.edu", terminal: true},
		"vpn":                  {origin: "https://lab.example.edu", vpn: true},
		"other local users":    {origin: "https://lab.example.edu", allowOthers: true},
		"review on the same":   {origin: "https://lab.example.edu", addr: "0.0.0.0:8787", reviewAddr: "127.0.0.1:8787"},
		"network file system":  {origin: "https://lab.example.edu", onNetworkFS: nfs},
		"not an http(s) thing": {origin: "ftp://lab.example.edu"},
	} {
		if _, err := g.check(); err == nil {
			t.Errorf("%s: started", name)
		}
	}
	if o, err := (groupStart{origin: "http://localhost:5173", insecureDev: true}).check(); err != nil || o != "http://localhost:5173" {
		t.Fatalf("--insecure-dev localhost: %q %v", o, err)
	}
}

// The bootstrap command prints a link whose secret is in the fragment, and the link creates one admin, once.
func TestAdminBootstrapPrintsASingleUseLink(t *testing.T) {
	db := filepath.Join(t.TempDir(), "c.db")
	var out, errb bytes.Buffer
	if code := runAdmin([]string{"bootstrap", "--db", db, "--public-origin", "https://lab.example.edu"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	m := regexp.MustCompile(`https://lab\.example\.edu/#/setup/([A-Za-z0-9_-]{43})`).FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("no link in %q", out.String())
	}
	st, err := catalog.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	acc, _ := accounts.Open(st.DB())
	acc.Params = accounts.Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1}
	if _, err := acc.CreateAdminFromSetup(m[1], "Ada", "ada", "a long passphrase here"); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.CreateAdminFromSetup(m[1], "Bob", "bob", "a long passphrase here"); err == nil {
		t.Fatal("the link worked twice")
	}
	out.Reset()
	if code := runAdmin([]string{"reset-second-factor", "--db", db, "--username", "ada"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "ada") {
		t.Fatalf("reset: %d %s %s", code, out.String(), errb.String())
	}
	if code := runAdmin([]string{"reset-second-factor", "--db", db, "--username", "nobody"}, &out, &errb); code != 1 {
		t.Fatalf("unknown account: %d", code)
	}
	if code := runAdmin([]string{"bootstrap"}, &out, &errb); code != 2 {
		t.Fatalf("no --db: %d", code)
	}
}
