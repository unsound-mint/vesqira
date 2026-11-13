package schema

import (
	"database/sql"
	"regexp"
	"strings"
)

var componentMap = regexp.MustCompile(`^component_g[0-9]+_`)

// RunOutputsTable is a run's linked outputs (ADR 0068, item 1). role is a generic role (orientation, reliability, support,
// component_maps, model_cube, ...) or an extra a tool profile declares; product is the tool's own name for the output (psi,
// component_g1_mu_kms, cube_model, ...), which is what several outputs of one role are told apart by, so it is the key. The runs
// package creates it for a fresh install and the migration rebuilds an older one, from this one text.
const RunOutputsTable = `CREATE TABLE IF NOT EXISTS run_outputs(
	run_id INTEGER NOT NULL, role TEXT NOT NULL, product TEXT NOT NULL, dataset_id INTEGER NOT NULL, name TEXT NOT NULL, PRIMARY KEY(run_id, product))`

// LegacyRunOutputRole is the generic role of an output recorded before schema 35, from the run's kind and the old role string
// (which becomes the product). It is the rule of that one migration, kept here so it does not follow the live profile file: the
// profile may change, what the rows were before this step may not. An output it cannot place keeps its old name as a declared
// extra, never a guess.
func LegacyRunOutputRole(kind, old string) string {
	switch kind {
	case "vgt":
		if old == "psi" {
			return "orientation"
		}
	case "rohsa":
		switch {
		case old == "cube_model":
			return "model_cube"
		case strings.HasPrefix(old, "component_g") && componentMap.MatchString(old):
			return "component_maps"
		}
	}
	return old
}

// toolProfiles gives every run the profile that recognised it ("legacy" for the runs already there: they were recognised by the
// code that predates profiles, not by a profile with a name and version) and rewrites run_outputs to generic roles, keeping the
// old role as the product. Files on disk are not touched. Either table may be absent (a catalog whose runs store never opened).
func toolProfiles(tx *sql.Tx) error {
	exists := func(name string) (bool, error) {
		var n int
		err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
		return n > 0, err
	}
	if ok, err := exists("runs"); err != nil {
		return err
	} else if ok {
		if err := AddColumn(tx, "runs", "profile", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE runs SET profile='legacy' WHERE profile=''`); err != nil {
			return err
		}
	}
	ok, err := exists("run_outputs")
	if err != nil || !ok {
		return err
	}
	has := false
	rows, err := tx.Query(`SELECT name FROM pragma_table_info('run_outputs')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		has = has || n == "product"
	}
	rows.Close()
	if has {
		return nil
	}
	type row struct {
		run, ds    int64
		kind, role string
		name       string
	}
	var old []row
	orows, err := tx.Query(`SELECT o.run_id, COALESCE(r.kind,''), o.role, o.dataset_id, o.name FROM run_outputs o LEFT JOIN runs r ON r.id=o.run_id`)
	if err != nil {
		return err
	}
	for orows.Next() {
		var x row
		if err := orows.Scan(&x.run, &x.kind, &x.role, &x.ds, &x.name); err != nil {
			orows.Close()
			return err
		}
		old = append(old, x)
	}
	orows.Close()
	if err := orows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE run_outputs`); err != nil {
		return err
	}
	if _, err := tx.Exec(RunOutputsTable); err != nil {
		return err
	}
	for _, x := range old {
		if _, err := tx.Exec(`INSERT INTO run_outputs(run_id,role,product,dataset_id,name) VALUES(?,?,?,?,?)`, x.run, LegacyRunOutputRole(x.kind, x.role), x.role, x.ds, x.name); err != nil {
			return err
		}
	}
	return nil
}
