// Package fullschema opens every store main.go opens on the catalog file, so a test sees the whole schema a running
// install has. Only tests import it. Add a store here when main.go gains one (main's TestFullSchemaOpensEveryStoreMainOpens
// fails otherwise).
package fullschema

import (
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/history"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/masks"
	"github.com/unsound-mint/vesqira/services/api/internal/notify"
	"github.com/unsound-mint/vesqira/services/api/internal/offline"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/subsets"
	"github.com/unsound-mint/vesqira/services/api/internal/sweeps"
	"github.com/unsound-mint/vesqira/services/api/internal/transfers"
)

// Open creates the tables of every store on db (the catalog's own are created by catalog.Open); dir is the data directory
// for the stores that keep files.
func Open(db *sql.DB, dir string) error {
	for _, s := range []struct {
		name string
		open func() error
	}{
		{"boards", func() error { _, e := boards.Open(db, dir); return e }},
		{"runs", func() error { _, e := runs.Open(db); return e }},
		{"hosts", func() error { _, e := hosts.Open(db); return e }},
		{"jobs", func() error { _, e := jobs.Open(db); return e }},
		{"sweeps", func() error { _, e := sweeps.Open(db); return e }},
		{"audit", func() error { _, e := audit.Open(db); return e }},
		{"accounts", func() error { _, e := accounts.Open(db); return e }},
		{"notify", func() error { _, e := notify.Open(db); return e }},
		{"regions", func() error { _, e := regions.Open(db); return e }},
		{"derived", func() error { _, e := derived.Open(db); return e }},
		{"masks", func() error { _, e := masks.Open(db); return e }},
		{"reports", func() error { _, e := reports.Open(db, dir); return e }},
		{"projects", func() error { _, e := projects.Open(db); return e }},
		{"history", func() error { _, e := history.Open(db); return e }},
		{"transfers", func() error { _, e := transfers.Open(db); return e }},
		{"subsets", func() error { _, e := subsets.Open(db); return e }},
		{"offline", func() error { _, e := offline.Open(db, filepath.Join(dir, "offline")); return e }},
	} {
		if err := s.open(); err != nil {
			return fmt.Errorf("opening %s: %w", s.name, err)
		}
	}
	return nil
}
