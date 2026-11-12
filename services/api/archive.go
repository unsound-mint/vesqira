package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/backup"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// archiveCommand handles `export` and `restore`, which work on the data directory and need no server. It reports
// whether args named one of them.
func archiveCommand(args []string) bool {
	if len(args) == 0 || (args[0] != "export" && args[0] != "restore") {
		return false
	}
	fs := flag.NewFlagSet(args[0], flag.ExitOnError)
	data := fs.String("data-dir", "", "data directory (the catalog, figures, derived and managed files)")
	file := fs.String("file", "", "archive to write (export) or read (restore)")
	_ = fs.Parse(args[1:])
	if *data == "" || *file == "" {
		fmt.Fprintln(os.Stderr, "usage: vesqira-api", args[0], "--data-dir DIR --file ARCHIVE.tar.gz")
		os.Exit(2)
	}
	dir, err := filepath.Abs(*data)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if args[0] == "export" {
		tmp := *file + ".part"
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		// local-only feature: never mounted on the group server (ADR 0050 owner answer 3)
		m, err := backup.Export(workspace.Local, filepath.Join(dir, "vesqira.db"), dir, out)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(tmp)
			fmt.Fprintln(os.Stderr, "export failed:", err)
			os.Exit(1)
		}
		if err := os.Rename(tmp, *file); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("Wrote %s: %d files, schema %d, %d external files referenced but not included.\n", *file, len(m.Files), m.SchemaVersion, len(m.References))
		return true
	}
	in, err := os.Open(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer in.Close()
	m, err := backup.Restore(in, dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "restore failed:", err)
		os.Exit(1)
	}
	fmt.Printf("Restored %d files into %s (archive made %s, schema %d).\n", len(m.Files), dir, m.CreatedAt.Format(time.RFC3339), m.SchemaVersion)
	if len(m.References) > 0 {
		fmt.Printf("%d datasets refer to files that are not in the archive; they work only where the same paths exist:\n", len(m.References))
		for i, r := range m.References {
			if i == 5 {
				fmt.Printf("  ... and %d more (see manifest.json in the archive)\n", len(m.References)-5)
				break
			}
			fmt.Printf("  %s\n", strings.TrimSpace(r.Path))
		}
	}
	return true
}
