package main

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/backup"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/fullschema"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// ADR 0050 section 7, slice 4: `multiuser import` checks the archive against its manifest and lists what would happen to every
// dataset; only explicit --map prefixes rewrite a path; refusals say why; it never changes the archive, and since writing the rows
// is not built, it writes nothing without --dry-run either.
func TestMultiuserImportDryRunPlansAndRefuses(t *testing.T) {
	// the exporting computer: one dataset under a mapped folder, one outside every map
	src := t.TempDir()
	lc, err := catalog.Open(filepath.Join(src, "vesqira.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fullschema.Open(lc.DB(), src); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/home/denis/data/lmc/cube.fits", "/scratch/other.fits"} {
		if _, err := lc.Upsert(workspace.Local, "local-owner", catalog.Dataset{Path: p, Name: filepath.Base(p), Header: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	lc.Close()
	archive := filepath.Join(t.TempDir(), "export.tar.gz")
	out, _ := os.Create(archive)
	if _, err := backup.Export(workspace.Local, filepath.Join(src, "vesqira.db"), src, out); err != nil {
		t.Fatal(err)
	}
	out.Close()
	before := fileSum(t, archive)

	// the group server: an account to own the workspace, and a canonical folder to map to
	dbPath := filepath.Join(t.TempDir(), "server.db")
	sc, _ := catalog.Open(dbPath)
	acc, _ := accounts.Open(sc.DB())
	acc.Params = accounts.Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1}
	tok, _, _ := acc.CreateSetupToken()
	if _, err := acc.CreateAdminFromSetup(tok, "Ada", "ada", "a long passphrase here"); err != nil {
		t.Fatal(err)
	}
	sc.Close()
	target, _ := filepath.EvalSymlinks(t.TempDir())
	_ = os.MkdirAll(filepath.Join(target, "lmc"), 0o755)
	_ = os.WriteFile(filepath.Join(target, "lmc", "cube.fits"), []byte("x"), 0o644)

	run := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := runMultiuser(append([]string{"import", archive, "--db", dbPath}, args...), &o, &e)
		return code, o.String(), e.String()
	}
	code, plan, errs := run("--workspace", "Denis's data", "--owner", "ada", "--map", "/home/denis/data="+target, "--dry-run")
	if code != 0 {
		t.Fatalf("dry run: %d %s", code, errs)
	}
	for _, want := range []string{"matches its manifest", "cube.fits: /home/denis/data/lmc/cube.fits -> " + filepath.Join(target, "lmc", "cube.fits") + " (present)",
		"other.fits: /scratch/other.fits; no --map covers it", "1 rewritten through a map, 1 not covered", "Dry run: nothing was written."} {
		if !strings.Contains(plan, want) {
			t.Errorf("the plan lacks %q:\n%s", want, plan)
		}
	}
	for name, args := range map[string][]string{
		"an unknown owner":        {"--workspace", "W", "--owner", "nobody", "--dry-run"},
		"an existing workspace":   {"--workspace", "main workspace", "--owner", "ada", "--dry-run"},
		"a relative map":          {"--workspace", "W", "--owner", "ada", "--map", "data=" + target, "--dry-run"},
		"a missing map target":    {"--workspace", "W", "--owner", "ada", "--map", "/a=/no/such/folder", "--dry-run"},
		"writing, not yet built":  {"--workspace", "W", "--owner", "ada", "--map", "/home/denis/data=" + target},
		"no workspace name given": {"--owner", "ada"},
	} {
		if code, _, e := run(args...); code == 0 || e == "" {
			t.Errorf("%s: exit %d, %q", name, code, e)
		}
	}
	if link := filepath.Join(t.TempDir(), "link"); os.Symlink(target, link) == nil {
		if code, _, e := run("--workspace", "W", "--owner", "ada", "--map", "/a="+link, "--dry-run"); code != 2 || !strings.Contains(e, "canonically") {
			t.Errorf("a map target through a link: %d %s", code, e)
		}
	}
	if fileSum(t, archive) != before {
		t.Error("the archive changed")
	}
	sc, _ = catalog.Open(dbPath)
	defer sc.Close()
	acc, _ = accounts.Open(sc.DB())
	if ws, _ := acc.AllWorkspaces(); len(ws) != 1 {
		t.Errorf("the server gained a workspace: %+v", ws)
	}
	if mapPath([]pathMap{{"/data", "/srv/b"}}, "/database/x") != "" || mapPath([]pathMap{{"/data", "/srv/b"}}, "/data/x") != "/srv/b/x" {
		t.Error("a map rewrites only paths inside its folder")
	}
}

func fileSum(t *testing.T, p string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}
