package schema

import "testing"

// Schema 35 gives every run the "legacy" profile and rewrites run_outputs to generic roles, keeping the old role as the product,
// without losing a row or a dataset link. A run of another kind keeps its role name.
func TestToolProfilesMigrationKeepsEveryOutputAndItsOldName(t *testing.T) {
	db := open(t, t.TempDir()+"/c.db")
	for _, q := range []string{
		`CREATE TABLE runs(id INTEGER PRIMARY KEY, kind TEXT NOT NULL)`,
		`CREATE TABLE run_outputs(run_id INTEGER NOT NULL, role TEXT NOT NULL, dataset_id INTEGER NOT NULL, name TEXT NOT NULL, PRIMARY KEY(run_id, role))`,
		`INSERT INTO runs VALUES (1,'vgt'),(2,'rohsa'),(3,'mlflow')`,
		`INSERT INTO run_outputs VALUES (1,'psi',10,'psi.fits'),(1,'reliability',11,'rel.fits'),(1,'fit_rbar2',12,'f.fits'),
			(2,'component_g1_mu_kms',20,'a.fits'),(2,'component_g2_amp_k',21,'b.fits'),(2,'cube_model',22,'m.fits'),(2,'model_nhi_cm2',23,'n.fits'),(2,'cube_other',24,'o.fits'),
			(3,'psi',30,'p.fits')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := toolProfiles(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	want := map[int64][2]string{ // dataset -> role, product
		10: {"orientation", "psi"}, 11: {"reliability", "reliability"}, 12: {"fit_rbar2", "fit_rbar2"},
		20: {"component_maps", "component_g1_mu_kms"}, 21: {"component_maps", "component_g2_amp_k"}, 22: {"model_cube", "cube_model"},
		23: {"model_nhi_cm2", "model_nhi_cm2"}, 24: {"cube_other", "cube_other"}, 30: {"psi", "psi"},
	}
	rows, err := db.Query(`SELECT dataset_id, role, product FROM run_outputs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var ds int64
		var role, product string
		if err := rows.Scan(&ds, &role, &product); err != nil {
			t.Fatal(err)
		}
		if w := want[ds]; w != [2]string{role, product} {
			t.Errorf("dataset %d: %s/%s, want %v", ds, role, product, w)
		}
		n++
	}
	if n != len(want) {
		t.Fatalf("%d outputs after the migration, want %d", n, len(want))
	}
	var legacy int
	if err := db.QueryRow(`SELECT COUNT(*) FROM runs WHERE profile='legacy'`).Scan(&legacy); err != nil || legacy != 3 {
		t.Fatalf("legacy runs: %d %v", legacy, err)
	}
	// running it again changes nothing (the table already has product)
	tx, _ = db.Begin()
	if err := toolProfiles(tx); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
}

func TestToolProfilesMigrationToleratesCatalogsWithoutRunTables(t *testing.T) {
	db := open(t, t.TempDir()+"/c.db")
	tx, _ := db.Begin()
	if err := toolProfiles(tx); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
}
