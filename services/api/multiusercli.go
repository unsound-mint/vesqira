package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/backup"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/fullschema"
	"github.com/unsound-mint/vesqira/services/api/internal/schema"
)

// multiuserCommand is `vesqira-api multiuser import ...` (ADR 0050 section 7, slice 4): bring a local catalog's export into a new
// workspace of a group server.
//
//	multiuser import ARCHIVE --db PATH --workspace NAME --owner USERNAME [--map FROM=TO]... [--data-dir DIR] [--dry-run]
//
// The archive is checked against its manifest (backup.Restore, into a temporary folder; the archive itself is only read), the owner
// must exist and be enabled, the workspace name must be new, and every --map must go from an absolute folder to a canonical folder
// on this server, which becomes one of the workspace's data roots. The rows are written by importInto (multiuserimport.go) in one
// transaction: all or nothing. --dry-run runs the same code and rolls it back, so its list is what the write does.
//
// Exit codes: 0 done, 1 failed or refused, 2 wrong usage.
func multiuserCommand(args []string) bool {
	if len(args) == 0 || args[0] != "multiuser" {
		return false
	}
	os.Exit(runMultiuser(args[1:], os.Stdout, os.Stderr))
	return true
}

type pathMaps []string

func (m *pathMaps) String() string     { return strings.Join(*m, ",") }
func (m *pathMaps) Set(v string) error { *m = append(*m, v); return nil }

type pathMap struct{ from, to string }

// parseMaps checks every FROM=TO: FROM an absolute folder of the computer the export came from, TO an existing folder of this
// server given canonically (no link anywhere in it, so what is declared is what is read, as for any data root).
func parseMaps(raw []string) ([]pathMap, error) {
	var out []pathMap
	for _, r := range raw {
		from, to, ok := strings.Cut(r, "=")
		if !ok || !filepath.IsAbs(from) || !filepath.IsAbs(to) {
			return nil, fmt.Errorf("--map %q: give FROM=TO, both absolute folders", r)
		}
		from, to = filepath.Clean(from), filepath.Clean(to)
		real, err := filepath.EvalSymlinks(to)
		if err != nil {
			return nil, fmt.Errorf("--map %q: %s does not exist on this server", r, to)
		}
		if real != to {
			return nil, fmt.Errorf("--map %q: give %s canonically (it resolves to %s); a data root has no link in it", r, to, real)
		}
		if fi, err := os.Stat(to); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("--map %q: %s is not a folder", r, to)
		}
		if to == "/" {
			return nil, fmt.Errorf("--map %q: the whole disk is never a data root", r)
		}
		out = append(out, pathMap{from, to})
	}
	// the longest FROM wins, so /data/lab=/srv/a and /data=/srv/b both work
	sort.Slice(out, func(i, j int) bool { return len(out[i].from) > len(out[j].from) })
	return out, nil
}

// mapPath rewrites p through the first map whose FROM is p or a folder holding it; "" when none does. Only the explicit maps
// rewrite a path: nothing is guessed from a name.
func mapPath(maps []pathMap, p string) string {
	p = filepath.Clean(p)
	for _, m := range maps {
		if p == m.from {
			return m.to
		}
		if rel, ok := strings.CutPrefix(p, m.from+string(filepath.Separator)); ok && !strings.HasPrefix(rel, "..") {
			return filepath.Join(m.to, rel)
		}
	}
	return ""
}

// serverOwnFolder refuses a map target that holds or lies inside the server's own folders (the catalog's, and the data directory
// with its managed copies, derived results and previews): a member could read them through the file routes. It is the rule the
// admin's "declare a data folder" route applies (internal/server checkDataRoot).
func serverOwnFolder(to string, own ...string) string {
	within := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
	}
	for _, o := range own {
		real, err := filepath.EvalSymlinks(o)
		if err != nil {
			continue
		}
		if within(to, real) || within(real, to) {
			return fmt.Sprintf("%s holds or lies inside the server's own data (%s): map to a folder of research data instead", to, real)
		}
	}
	return ""
}

func runMultiuser(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("multiuser import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	usage := func() int {
		fmt.Fprintln(stderr, "usage: vesqira-api multiuser import ARCHIVE.tar.gz --db PATH --workspace NAME --owner USERNAME [--map FROM=TO]... [--data-dir DIR] [--dry-run]")
		fmt.Fprintln(stderr, "Writes the export's rows into a new workspace in one transaction (all or nothing). --dry-run lists exactly what that would do and writes nothing.")
		fs.PrintDefaults()
		return 2
	}
	fs.Usage = func() {}
	db := fs.String("db", "", "the group server's catalog")
	dataDir := fs.String("data-dir", "", "the server's data directory, where figure previews go (default: next to the catalog, as for the server)")
	name := fs.String("workspace", "", "the new workspace's name")
	owner := fs.String("owner", "", "the username of the account that becomes its Owner and the author of every imported row")
	dry := fs.Bool("dry-run", false, "list what the import would do and write nothing")
	var maps pathMaps
	fs.Var(&maps, "map", "FROM=TO: rewrite paths under FROM (the exporting computer) to TO (this server), which becomes a data root of the workspace; repeatable")
	if len(args) < 1 || args[0] != "import" {
		return usage()
	}
	if len(args) < 2 || strings.HasPrefix(args[1], "-") {
		_ = fs.Parse(args[1:])
		return usage()
	}
	archive := args[1]
	if err := fs.Parse(args[2:]); err != nil {
		return usage()
	}
	wsName := strings.TrimSpace(*name)
	if fs.NArg() != 0 || *db == "" || wsName == "" || *owner == "" {
		return usage()
	}
	if len([]rune(wsName)) > 100 {
		fmt.Fprintln(stderr, "a workspace name is at most 100 characters")
		return 2
	}
	pm, err := parseMaps(maps)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	dbPath := strings.TrimPrefix(*db, "file:")
	if *dataDir == "" {
		*dataDir = filepath.Dir(dbPath)
	}
	for _, x := range pm {
		if msg := serverOwnFolder(x.to, *dataDir, filepath.Dir(dbPath)); msg != "" {
			fmt.Fprintf(stderr, "--map %s=%s: %s\n", x.from, x.to, msg)
			return 2
		}
	}

	store, err := catalog.Open(*db)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer store.Close()
	acc, err := accounts.Open(store.DB())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	o, err := acc.ByUsername(*owner)
	if errors.Is(err, accounts.ErrNotFound) {
		fmt.Fprintf(stderr, "no account has the username %q: create it first (an invitation, or admin bootstrap)\n", *owner)
		return 1
	} else if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if o.Disabled {
		fmt.Fprintf(stderr, "the account %q is disabled: an import needs an enabled account to own the workspace\n", *owner)
		return 1
	}
	all, err := acc.AllWorkspaces()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, w := range all {
		if strings.EqualFold(w.Name, wsName) {
			fmt.Fprintf(stderr, "a workspace named %q exists already: an import always goes into a new workspace\n", w.Name)
			return 1
		}
	}

	in, err := os.Open(archive)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer in.Close()
	tmp, err := os.MkdirTemp(filepath.Dir(dbPath), ".vesqira-import-*") // beside the catalog: an archive can be large, and /tmp is often memory
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer os.RemoveAll(tmp)
	m, err := backup.Restore(in, tmp) // checks every file against the manifest; writes only into tmp
	if err != nil {
		fmt.Fprintln(stderr, "the archive was refused:", err)
		return 1
	}
	if m.SchemaVersion > schema.Version {
		fmt.Fprintf(stderr, "the archive is from a newer Vesqira (schema %d; this server knows %d): upgrade the server first\n", m.SchemaVersion, schema.Version)
		return 1
	}
	// the temporary copy is brought to this schema through the normal migrations and the stores' own upgrades; the archive never is
	src, err := catalog.Open(filepath.Join(tmp, "vesqira.db"))
	if err != nil {
		fmt.Fprintln(stderr, "the exported catalog cannot be read:", err)
		return 1
	}
	defer src.Close()
	if err := fullschema.Open(src.DB(), tmp); err != nil {
		fmt.Fprintln(stderr, "the exported catalog cannot be read:", err)
		return 1
	}
	var accts, wss int
	_ = src.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM accounts), (SELECT COUNT(*) FROM workspaces WHERE id<>1)`).Scan(&accts, &wss)
	if accts > 0 || wss > 0 {
		fmt.Fprintln(stderr, "this export comes from a group server (it holds accounts or several workspaces): only a local catalog is imported")
		return 1
	}
	if !*dry { // the server's own tables, as the server opens them
		if err := fullschema.Open(store.DB(), *dataDir); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	sp := &importSpec{src: src.DB(), srcDir: tmp, dstDir: *dataDir, owner: o.ID, name: wsName, maps: pm, archive: filepath.Base(archive)}
	tx, err := store.DB().Begin()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	rep, err := importInto(tx, sp, time.Now().Unix())
	if err != nil {
		_ = tx.Rollback()
		fmt.Fprintln(stderr, err)
		return 1
	}
	head := func(verb string) {
		fmt.Fprintf(stdout, "Import of %s into a new workspace %q, owned by %s (%s); every imported row %s created by that account.\n", filepath.Base(archive), wsName, o.Name, o.Username, verb)
		fmt.Fprintf(stdout, "The archive matches its manifest: %d files, schema %d, exported %s.\n\n", len(m.Files), m.SchemaVersion, m.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"))
	}
	if *dry {
		_ = tx.Rollback()
		fmt.Fprintf(stdout, "DRY RUN: nothing below is written.\n\n")
		head("would be")
		printPlan(stdout, rep, pm)
		fmt.Fprintln(stdout, "\nDry run only: nothing was written; the archive and this catalog are unchanged. Run again without --dry-run to write it.")
		return 0
	}
	created, err := copyPreviews(rep, *dataDir)
	if err != nil {
		_ = tx.Rollback()
		removeAll(created)
		fmt.Fprintln(stderr, "figure previews could not be copied; nothing was imported:", err)
		return 1
	}
	if err := tx.Commit(); err != nil {
		removeAll(created)
		fmt.Fprintln(stderr, "the import failed; nothing was imported:", err)
		return 1
	}
	head("was")
	printPlan(stdout, rep, pm)
	fmt.Fprintf(stdout, "\nImported into workspace %q (id %d). The archive was only read. Review links from the old computer work only if the review address is the same: reissue them otherwise. The publisher key is entered again on the publish server.\n", wsName, rep.workspace)
	return 0
}

func count(db *sql.DB, table string) (int64, bool) {
	var n int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		return 0, false
	}
	return n, true
}
