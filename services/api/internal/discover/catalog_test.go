package discover

import "testing"

// Catalogs are offered instead of "other discipline" (ADR 0044), but a README that sits beside the tables is not one.
func TestCatalogsAreOfferedAndReadmesAreNot(t *testing.T) {
	root := canonical(t)
	write(t, root, "data/stars/mf_stars_LMC.dat", "#Star\tRA(J2000)\tDEC(J2000)\tP(%)\tsP(%)\tPA(deg)\tsPA(deg)\n67-2\t4 47 07.65\t-67 06 23.31\t1.17\t0.08\t25\t4\t\t\t\n69-7\t4 49 15.83\t-69 27 27.92\t1.30\t0.09\t38\t7\n67-5\t4 50 17.23\t-67 39 29.93\t0.79\t0.16\t61\t6\t\n")
	write(t, root, "data/stars/README_detailed.txt", "This tarball contains 4 files:\n\n - schmidt_stars_LMC.dat --> the polarization data.\nLegend description\nStar: star name.\nRA(J2000): right ascension in hours precessed for 2000.\n")
	write(t, root, "data/stars/fg.txt", "1 69.1968 -66.4503 0.04\n2 73.1145 -69.4266 0.6\n3 73.3225 -65.8601 0.39\n")
	write(t, root, "data/stars/tbl.xcat.txt", "|num|glon|\n|real|real|\n 1 2.0\n")
	write(t, root, "data/sightlines.csv", "name,l,b,N\na,1,2,3\nb,4,5,6\n")
	write(t, root, "data/absorption.ecsv", "# %ECSV 1.0\n# ---\n# datatype:\n# - {name: a, datatype: int64}\na\n1\n")
	write(t, root, "elsewhere/notes.csv", "name,l,b\na,1,2\nb,3,4\n") // a table outside a declared data folder is not offered
	write(t, root, "elsewhere/table.vot", "<?xml version=\"1.0\"?><VOTABLE/>")
	r := scan(t, LocalFS{root}, Options{Catalogs: true, Layout: Layout{Data: []string{"data"}}})
	got := map[string]File{}
	for _, f := range r.Data {
		got[f.Path] = f
	}
	for _, p := range []string{"data/stars/mf_stars_LMC.dat", "data/stars/fg.txt", "data/stars/tbl.xcat.txt", "data/sightlines.csv", "data/absorption.ecsv", "elsewhere/table.vot"} {
		if f, ok := got[p]; !ok || f.Format != "Catalog" || !f.Viewer {
			t.Errorf("%s: %+v, want a catalog with a viewer", p, f)
		}
	}
	if f := got["data/stars/README_detailed.txt"]; f.Format != "other" || f.Viewer {
		t.Errorf("README: %+v, want other, no viewer", f)
	}
	if _, ok := got["elsewhere/notes.csv"]; ok {
		t.Error("a csv outside a declared data folder was offered")
	}
}

func TestLooksTabular(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		want bool
	}{
		"tabs with a comment header": {"#a\tb\tc\n1\t2\t3\n4\t5\t6\n", true},
		"commas":                     {"a,b\n1,2\n3,4\n", true},
		"blanks":                     {"1 2 3\n4 5 6\n", true},
		"ragged trailing tabs":       {"a\tb\tc\n1\t2\t3\t\t\n4\t5\t6\n7\t8\t9\t\n", true},
		"prose":                      {"This tarball contains 4 files:\n\n - a --> b.\nLegend description\nStar: star name.\n", false},
		"one line":                   {"a b c\n", false},
		"one column":                 {"a\nb\nc\n", false},
		"ecsv":                       {"# %ECSV 1.0\n# ---\n", true},
		"ipac":                       {"\\keyword = x\n|a|b|\n", true},
		"mostly ragged":              {"a b c\nd e\nf\ng h i j\nk\n", false},
	} {
		if got := looksTabular([]byte(tc.text), false); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
	if !looksTabular([]byte("a b c\nd e f\ng h"), true) {
		t.Error("the cut last line must not count")
	}
}

// A project on another host is scanned with catalogs off: they are read from this computer only.
func TestNoCatalogsWhenTheScanIsNotLocal(t *testing.T) {
	root := canonical(t)
	write(t, root, "data/a.csv", "a,b\n1,2\n3,4\n")
	write(t, root, "data/b.ecsv", "# %ECSV 1.0\n")
	r := scan(t, LocalFS{root}, Options{Layout: Layout{Data: []string{"data"}}})
	for _, f := range r.Data {
		if f.Format == "Catalog" {
			t.Errorf("%s offered as a catalog", f.Path)
		}
	}
}
