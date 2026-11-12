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

	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/backup"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/schema"
)

// multiuserCommand is `vesqira-api multiuser import ...` (ADR 0050 section 7, slice 4): bring a local catalog's export into a new
// workspace of a group server.
//
//	multiuser import ARCHIVE --db PATH --workspace NAME --owner USERNAME [--map FROM=TO]... --dry-run
//
// What is built is the verification and the plan, which a dry run prints in full: the archive is checked against its manifest
// (backup.Restore, into a temporary folder; the archive itself is only read), the owner must exist, the workspace name must be new,
// and every --map must go from an absolute folder to a canonical folder on this server, which would become the workspace's data
// root. Each dataset is listed with what would happen to it: rewritten through a map, or kept as a record of a file that is not on
// the server (no --map covers it, or it lives on a host, whose ssh alias is never imported). Writing the rows is not built yet:
// without --dry-run the command says so and writes nothing.
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

func runMultiuser(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: vesqira-api multiuser import ARCHIVE.tar.gz --db PATH --workspace NAME --owner USERNAME [--map FROM=TO]... --dry-run")
		return 2
	}
	if len(args) < 2 || args[0] != "import" {
		return usage()
	}
	archive := args[1]
	fs := flag.NewFlagSet("multiuser import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := fs.String("db", "", "the group server's catalog")
	name := fs.String("workspace", "", "the new workspace's name")
	owner := fs.String("owner", "", "the username of the account that becomes its Owner and the author of every imported row")
	dry := fs.Bool("dry-run", false, "check and list everything, write nothing")
	var maps pathMaps
	fs.Var(&maps, "map", "FROM=TO: rewrite paths under FROM (the exporting computer) to TO (this server); repeatable")
	if err := fs.Parse(args[2:]); err != nil || fs.NArg() != 0 || *db == "" || strings.TrimSpace(*name) == "" || *owner == "" {
		return usage()
	}
	pm, err := parseMaps(maps)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
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
	all, err := acc.AllWorkspaces()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, w := range all {
		if strings.EqualFold(w.Name, strings.TrimSpace(*name)) {
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
	tmp, err := os.MkdirTemp(filepath.Dir(*db), ".vesqira-import-*") // beside the catalog: an archive can be large, and /tmp is often memory
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
	src, err := catalog.Open(filepath.Join(tmp, "vesqira.db")) // the temporary copy is brought to this schema, never the archive
	if err != nil {
		fmt.Fprintln(stderr, "the exported catalog cannot be read:", err)
		return 1
	}
	defer src.Close()

	fmt.Fprintf(stdout, "Import of %s into a new workspace %q, owned by %s (%s); every imported row would be created by that account.\n", filepath.Base(archive), strings.TrimSpace(*name), o.Name, o.Username)
	fmt.Fprintf(stdout, "The archive matches its manifest: %d files, schema %d, exported %s.\n\n", len(m.Files), m.SchemaVersion, m.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"))
	if len(pm) > 0 {
		fmt.Fprintln(stdout, "Data roots the workspace would get (the --map targets):")
		for _, x := range pm {
			fmt.Fprintf(stdout, "  %s  (from %s)\n", x.to, x.from)
		}
		fmt.Fprintln(stdout)
	}
	hosts := map[int64]string{}
	if rows, err := src.DB().Query(`SELECT id, name FROM hosts`); err == nil {
		for rows.Next() {
			var id int64
			var n string
			if rows.Scan(&id, &n) == nil {
				hosts[id] = n
			}
		}
		rows.Close()
	}
	rows, err := src.DB().Query(`SELECT d.name, l.host_id, l.path FROM datasets d JOIN dataset_locations l ON l.dataset_id=d.id AND l.is_primary=1 WHERE d.workspace_id=1 ORDER BY d.id`)
	if err != nil {
		fmt.Fprintln(stderr, "the exported catalog cannot be read:", err)
		return 1
	}
	var mapped, missing, onHosts int
	fmt.Fprintln(stdout, "Datasets:")
	for rows.Next() {
		var dn, p string
		var host int64
		if err := rows.Scan(&dn, &host, &p); err != nil {
			rows.Close()
			fmt.Fprintln(stderr, err)
			return 1
		}
		switch to := mapPath(pm, p); {
		case host != 0:
			onHosts++
			fmt.Fprintf(stdout, "  %s: on host %q; kept as a record of a file not on this server (hosts are not imported)\n", dn, hosts[host])
		case to != "":
			mapped++
			state := "present"
			if _, err := os.Stat(to); err != nil {
				state = "not found there: it would be shown as missing"
			}
			fmt.Fprintf(stdout, "  %s: %s -> %s (%s)\n", dn, p, to, state)
		default:
			missing++
			fmt.Fprintf(stdout, "  %s: %s; no --map covers it, so it would be kept as a record of a file not on this server, never guessed\n", dn, p)
		}
	}
	rows.Close()
	fmt.Fprintf(stdout, "  %d rewritten through a map, %d not covered, %d on hosts.\n\n", mapped, missing, onHosts)
	fmt.Fprintln(stdout, "Rows:")
	for _, t := range []struct{ table, what string }{
		{"figures", "figures"}, {"boards", "boards"}, {"snapshots", "review snapshots (links to an old review address must be reissued)"},
		{"reports", "reports"}, {"runs", "runs (kept as read-only records naming their old host as text)"}, {"masks", "masks"},
		{"saved_regions", "regions"}, {"subsets", "subsets"}, {"jobs", "jobs (read-only records)"},
	} {
		if n, ok := count(src.DB(), t.table); ok {
			fmt.Fprintf(stdout, "  %d %s\n", n, t.what)
		}
	}
	fmt.Fprintln(stdout, "\nNever imported: hosts (their destinations are ssh aliases of the exporting computer), the publisher key (not in an export), tokens and keys.")
	if *dry {
		fmt.Fprintln(stdout, "Dry run: nothing was written.")
		return 0
	}
	fmt.Fprintln(stderr, "Writing the rows into a new workspace is not built yet (ADR 0050 slice 4: the dry run and the checks are). Nothing was written; the archive and this catalog are unchanged.")
	return 1
}

func count(db *sql.DB, table string) (int64, bool) {
	var n int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		return 0, false
	}
	return n, true
}
