package server

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

const starlight = "#Star\tRA(J2000)\tDEC(J2000)\tP(%)\tsP(%)\tPA(deg)\tsPA(deg)\n" +
	"67-2\t4 47 07.65\t-67 06 23.31\t1.17\t0.08\t25\t4\t\t\t\n" +
	"69-7\t4 49 15.83\t-69 27 27.92\t1.30\t0.09\t38\t7\n" +
	"67-5\t4 50 17.23\t-67 39 29.93\t0.79\t0.16\t61\t6\t\n" +
	"69-16\t4 52 03.57\t-69 26 33.76\t1.12\t0.08\t41\t\n"

func goodDeclaration() map[string]any {
	return map[string]any{"lon": "RA(J2000)", "lat": "DEC(J2000)", "format": "sexagesimal_hours_degrees", "frame": "fk5", "equinox": "J2000",
		"angle": map[string]any{"column": "PA(deg)", "unit": "deg", "frame": "sky", "quantity": "starlight_polarization", "sigmaColumn": "sPA(deg)"}, "values": []string{"Star"}}
}

func catalogServer(t *testing.T) (url, root, repo string) {
	t.Helper()
	repo, _ = filepath.Abs("../../../..")
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
	pw := &worker.Persistent{Command: []string{"python3", "-P", filepath.Join(repo, "services/scientific-worker/plane_worker.py")}, Timeout: 60 * time.Second}
	t.Cleanup(pw.Close)
	ts, root, _ := newPlaneServerWith(t, pw, pw)
	return ts.URL, root, repo
}

func registerCatalogFile(t *testing.T, url, path string) catalog.Dataset {
	t.Helper()
	st, b := callJSON(t, "POST", url+"/api/catalogs", map[string]string{"path": path})
	if st != 201 {
		t.Fatalf("register %s: %d %s", path, st, b)
	}
	var d catalog.Dataset
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestACatalogIsRegisteredUnderTheSourceRootsAndIsOfKindCatalog(t *testing.T) {
	url, root, _ := catalogServer(t)
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "o.csv"), []byte("a,b\n1,2\n"), 0o644)
	os.Symlink(filepath.Join(other, "o.csv"), filepath.Join(root, "link.csv"))
	for _, p := range []string{filepath.Join(other, "o.csv"), filepath.Join(root, "link.csv"), "relative.csv", filepath.Join(root, "..", "o.csv")} {
		for _, route := range []string{"/api/catalogs", "/api/datasets"} {
			if st, b := callJSON(t, "POST", url+route, map[string]string{"path": p}); st != 403 {
				t.Errorf("%s %q: %d %s, want 403", route, p, st, b)
			}
		}
	}
	f := filepath.Join(root, "stars.dat")
	os.WriteFile(f, []byte(starlight), 0o644)
	// the ordinary registration route sends a table to the catalog reader, and the dataset says what it is
	st, b := callJSON(t, "POST", url+"/api/datasets", map[string]string{"path": f})
	if st != 201 {
		t.Fatalf("%d %s", st, b)
	}
	var d catalog.Dataset
	json.Unmarshal(b, &d)
	h, ok := catalog.ParseCatalogHeader(d.Header)
	if d.Kind != "catalog" || !ok || h.Rows != 4 || len(h.Columns) != 7 || h.Format != "tsv" || d.CatalogDeclaration != nil {
		t.Fatalf("%+v %s", d, d.Header)
	}
	if h.Columns[1].Name != "RA(J2000)" || h.Columns[1].Dtype != "string" || h.Columns[1].Unit != nil {
		t.Fatalf("columns %+v", h.Columns)
	}
	// a file that is not a table is refused in a controlled way
	bin := filepath.Join(root, "bin.csv")
	os.WriteFile(bin, []byte("a,b\n1,\x00\x01\n"), 0o644)
	if st, b := callJSON(t, "POST", url+"/api/catalogs", map[string]string{"path": bin}); st != 422 || !strings.Contains(string(b), "binary") {
		t.Fatalf("%d %s", st, b)
	}
}

func TestADeclarationIsValidatedStoredAndTheSkyReadFromIt(t *testing.T) {
	url, root, _ := catalogServer(t)
	f := filepath.Join(root, "stars.dat")
	os.WriteFile(f, []byte(starlight), 0o644)
	d := registerCatalogFile(t, url, f)
	base := fmt.Sprintf("%s/api/datasets/%d/catalog", url, d.ID)

	// positions are refused, naming what is missing, until the person declares the columns
	if st, b := callJSON(t, "GET", base+"/positions", nil); st != 409 || !strings.Contains(string(b), "no columns are declared") {
		t.Fatalf("%d %s", st, b)
	}
	// the report carries a proposal (labelled) and no declaration
	st, b := callJSON(t, "GET", base+"/report", nil)
	var rep struct {
		Rows     int `json:"rows"`
		Proposal struct {
			Basis string `json:"basis"`
			Lon   string `json:"lon"`
			Frame string `json:"frame"`
		} `json:"proposal"`
		Declaration *catalog.CatalogDeclaration `json:"declaration"`
		Parse       *json.RawMessage            `json:"parse"`
		Preview     struct {
			Rows [][]any `json:"rows"`
		} `json:"preview"`
	}
	if st != 200 || json.Unmarshal(b, &rep) != nil || rep.Rows != 4 || rep.Proposal.Basis != "proposed from the column names" || rep.Proposal.Lon != "RA(J2000)" || rep.Proposal.Frame != "fk5" || rep.Declaration != nil || rep.Parse != nil && string(*rep.Parse) != "null" || len(rep.Preview.Rows) != 4 {
		t.Fatalf("%d %s", st, b)
	}

	// an incomplete declaration is a 400 with every reason; nothing is stored
	bad := goodDeclaration()
	bad["frame"], bad["equinox"] = nil, nil
	bad["format"] = "furlongs"
	st, b = callJSON(t, "PUT", base+"/declaration", bad)
	if st != 400 || !strings.Contains(string(b), "choose the celestial frame") || !strings.Contains(string(b), "say how the positions are written") {
		t.Fatalf("%d %s", st, b)
	}
	if st, _ := callJSON(t, "PUT", base+"/declaration", map[string]any{"lon": "x", "surprise": 1}); st != 400 {
		t.Fatalf("unknown fields must be refused: %d", st)
	}
	if got := getDataset(t, url, d.ID); got.CatalogDeclaration != nil {
		t.Fatal("a refused declaration was stored")
	}

	st, b = callJSON(t, "PUT", base+"/declaration", goodDeclaration())
	if st != 200 {
		t.Fatalf("%d %s", st, b)
	}
	var nd catalog.Dataset
	json.Unmarshal(b, &nd)
	if c := nd.CatalogDeclaration; c == nil || c.Frame != "fk5" || c.Stale || c.DeclaredBy != "user" || *c.Angle.Quantity != "starlight_polarization" {
		t.Fatalf("%s", b)
	}

	// the parse report is now filled in
	_, b = callJSON(t, "GET", base+"/report", nil)
	var rep2 struct {
		Parse struct{ Rows, Placed, Unreadable, Missing int } `json:"parse"`
	}
	json.Unmarshal(b, &rep2)
	if rep2.Parse.Rows != 4 || rep2.Parse.Placed != 4 || rep2.Parse.Unreadable != 0 {
		t.Fatalf("%s", b)
	}

	// positions in the declared frame, against the arithmetic done here
	type positions struct {
		Frame, Equinox string
		Lon, Lat       []*float64
		Angle, Sigma   []*float64
		Row            []int
		AngleAxial     bool
	}
	_, b = callJSON(t, "GET", base+"/positions?limit=2", nil)
	var p positions
	json.Unmarshal(b, &p)
	wantLon := (4 + 47.0/60 + 7.65/3600) * 15
	wantLat := -(67 + 6.0/60 + 23.31/3600)
	if p.Frame != "fk5" || len(p.Lon) != 2 || math.Abs(*p.Lon[0]-wantLon) > 1e-9 || math.Abs(*p.Lat[0]-wantLat) > 1e-9 || *p.Angle[1] != 38 || *p.Sigma[0] != 4 || !p.AngleAxial || p.Row[1] != 2 {
		t.Fatalf("%s", b)
	}
	_, b = callJSON(t, "GET", base+"/positions?frame=galactic", nil)
	var g positions
	json.Unmarshal(b, &g)
	if g.Frame != "galactic" || len(g.Lon) != 4 || *g.Lon[0] < 270 || *g.Lon[0] > 290 || *g.Lat[0] > -30 || *g.Lat[0] < -45 {
		t.Fatalf("LMC in Galactic should be near l=280, b=-33: %s", b)
	}
	if st, _ := callJSON(t, "GET", base+"/positions?frame=fk5", nil); st == 200 { // fk5 needs an equinox: not assumed
		t.Fatal("a transform to FK5 with no equinox was accepted")
	}
	if st, _ := callJSON(t, "GET", base+"/positions?frame=galactic&limit=0", nil); st != 400 {
		t.Fatalf("limit 0: %d", st)
	}

	// rows, paged; each row says its line in the file
	_, b = callJSON(t, "GET", base+"/rows?offset=1&limit=2", nil)
	var rows struct {
		Total int
		Rows  [][]any
		Lines []int
	}
	json.Unmarshal(b, &rows)
	if rows.Total != 4 || len(rows.Rows) != 2 || rows.Rows[0][0] != "69-7" || rows.Lines[0] != 3 {
		t.Fatalf("%s", b)
	}
	if st, _ := callJSON(t, "GET", base+"/rows?limit=501", nil); st != 400 {
		t.Fatalf("limit 501: %d", st)
	}

	// the file changes on disk: reading it is refused until it is registered again, and then the declaration is stale
	os.WriteFile(f, []byte(starlight+"70-1\t5 00 00.00\t-70 00 00.0\t1\t1\t10\t1\n"), 0o644)
	if st, b := callJSON(t, "GET", base+"/rows", nil); st != 409 || !strings.Contains(string(b), "changed since it was registered") {
		t.Fatalf("%d %s", st, b)
	}
	d2 := registerCatalogFile(t, url, f)
	if d2.ID != d.ID || d2.CatalogDeclaration == nil || !d2.CatalogDeclaration.Stale {
		t.Fatalf("%+v", d2.CatalogDeclaration)
	}
	if st, b := callJSON(t, "GET", base+"/positions", nil); st != 409 || !strings.Contains(string(b), "changed since you declared") {
		t.Fatalf("%d %s", st, b)
	}
	// confirming it again makes it current, and the sixth row now exists
	if st, b := callJSON(t, "PUT", base+"/declaration", goodDeclaration()); st != 200 || strings.Contains(string(b), `"stale":true`) {
		t.Fatalf("%d %s", st, b)
	}
	_, b = callJSON(t, "GET", base+"/positions", nil)
	json.Unmarshal(b, &p)
	if len(p.Lon) != 5 {
		t.Fatalf("%s", b)
	}
	// withdrawing returns to "declared nothing"
	st, b = callJSON(t, "DELETE", base+"/declaration", nil)
	var wd catalog.Dataset
	json.Unmarshal(b, &wd)
	if st != 200 || wd.CatalogDeclaration != nil {
		t.Fatalf("%d %s", st, b)
	}
}

func getDataset(t *testing.T, url string, id int64) catalog.Dataset {
	t.Helper()
	st, b := callJSON(t, "GET", fmt.Sprintf("%s/api/datasets/%d", url, id), nil)
	var d catalog.Dataset
	if st != 200 || json.Unmarshal(b, &d) != nil {
		t.Fatalf("%d %s", st, b)
	}
	return d
}

func TestUnreadableRowsAreCountedAndReportedWithTheirLineThroughTheAPI(t *testing.T) {
	url, root, _ := catalogServer(t)
	rows := []string{"name\tra\tdec"}
	for i := 0; i < 14; i++ {
		rows = append(rows, fmt.Sprintf("s%d\t%d 00 00\t-30 00 00", i, 4+i%10))
	}
	rows = append(rows, "s14\t25:61:00\t-30 00 00", "s15\t4 00 00\t-30 00 00", "s16\t\t-30 00 00", "short")
	f := filepath.Join(root, "bad.tsv")
	os.WriteFile(f, []byte(strings.Join(rows, "\n")+"\n"), 0o644)
	d := registerCatalogFile(t, url, f)
	base := fmt.Sprintf("%s/api/datasets/%d/catalog", url, d.ID)
	if st, b := callJSON(t, "PUT", base+"/declaration", map[string]any{"lon": "ra", "lat": "dec", "format": "sexagesimal_hours_degrees", "frame": "icrs"}); st != 200 {
		t.Fatalf("%d %s", st, b)
	}
	_, b := callJSON(t, "GET", base+"/report", nil)
	var rep struct {
		Rows int
		Read struct {
			SkippedTotal int `json:"skippedTotal"`
			Skipped      []struct {
				Line   int
				Reason string
			}
		}
		Parse struct {
			Rows, Placed, Unreadable, Missing int
			Samples                           []struct {
				Line          int
				Column, Value string
				Reason        string
			}
		}
	}
	json.Unmarshal(b, &rep)
	if rep.Rows != 17 || rep.Read.SkippedTotal != 1 || rep.Read.Skipped[0].Line != 19 || rep.Parse.Placed != 15 || rep.Parse.Unreadable != 1 || rep.Parse.Missing != 1 {
		t.Fatalf("%s", b)
	}
	if s := rep.Parse.Samples[0]; s.Line != 16 || s.Column != "ra" || s.Value != "25:61:00" || !strings.Contains(s.Reason, "not a valid angle") {
		t.Fatalf("%+v", s)
	}
}

func TestAPointCatalogIsRefusedWhereAPixelGridIsNeededAndAFitsFileIsRefusedWhereACatalogIsNeeded(t *testing.T) {
	url, root, repo := catalogServer(t)
	f := filepath.Join(root, "stars.dat")
	os.WriteFile(f, []byte(starlight), 0o644)
	d := registerCatalogFile(t, url, f)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/view", nil}, {"GET", "/plane?channel=0", nil}, {"GET", "/coord?x=0&y=0&channel=0", nil}, {"GET", "/spectrum?shape=circle&cx=1&cy=1&r=1", nil},
		{"GET", "/axial", nil},
		{"POST", "/interpretation", map[string]any{"kind": "axial_orientation", "unit": "deg"}}, {"POST", "/unit", map[string]any{"unit": "Jy/beam"}},
	} {
		if st, b := callJSON(t, c.method, fmt.Sprintf("%s/api/datasets/%d%s", url, d.ID, c.path), c.body); st != 409 {
			t.Errorf("%s %s: %d %s, want 409", c.method, c.path, st, b)
		}
	}
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	st, b := callJSON(t, "POST", url+"/api/datasets", map[string]string{"path": filepath.Join(root, "hi_cube.fits")})
	var cube catalog.Dataset
	json.Unmarshal(b, &cube)
	if st != 201 || cube.Kind != "fits" {
		t.Fatalf("%d %s", st, b)
	}
	for _, c := range []struct{ method, path string }{{"GET", "/catalog/rows"}, {"GET", "/catalog/report"}, {"GET", "/catalog/positions"}, {"PUT", "/catalog/declaration"}, {"DELETE", "/catalog/declaration"}} {
		var body any
		if c.method == "PUT" {
			body = goodDeclaration()
		}
		if st, b := callJSON(t, c.method, fmt.Sprintf("%s/api/datasets/%d%s", url, cube.ID, c.path), body); st != 409 {
			t.Errorf("%s %s on a cube: %d %s, want 409", c.method, c.path, st, b)
		}
	}
	// the cube still opens
	if st, b := callJSON(t, "GET", fmt.Sprintf("%s/api/datasets/%d/view", url, cube.ID), nil); st != 200 {
		t.Fatalf("%d %s", st, b)
	}
}

func TestAFitsTableIsRegisteredAsACatalogAndAnImageStaysAnImage(t *testing.T) {
	url, root, repo := catalogServer(t)
	mk := exec.Command("python3", "-I", "-c", "import sys\nfrom astropy.io import fits\nfrom astropy.table import Table\nt=Table({'ra':[10.5,11.5],'dec':[-30.0,-31.0],'pa':[10.0,20.0]})\nt['pa'].unit='deg'\nfits.HDUList([fits.PrimaryHDU(), fits.table_to_hdu(t)]).writeto(sys.argv[1])", filepath.Join(root, "tab.fits"))
	if out, err := mk.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	st, b := callJSON(t, "POST", url+"/api/datasets", map[string]string{"path": filepath.Join(root, "tab.fits")})
	var d catalog.Dataset
	json.Unmarshal(b, &d)
	h, ok := catalog.ParseCatalogHeader(d.Header)
	if st != 201 || d.Kind != "catalog" || !ok || h.Format != "fits" || h.Rows != 2 || h.Columns[2].Unit == nil || *h.Columns[2].Unit != "deg" {
		t.Fatalf("%d %s", st, b)
	}
	gen := exec.Command("python3", filepath.Join(repo, "fixtures/generate.py"), root)
	gen.Dir = filepath.Join(repo, "fixtures")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	_, b = callJSON(t, "POST", url+"/api/datasets", map[string]string{"path": filepath.Join(root, "hi_cube.fits")})
	var cube catalog.Dataset
	json.Unmarshal(b, &cube)
	if cube.Kind != "fits" {
		t.Fatalf("%s", b)
	}
}

func TestDeletingACatalogForgetsItsDeclaration(t *testing.T) {
	url, root, _ := catalogServer(t)
	f := filepath.Join(root, "stars.dat")
	os.WriteFile(f, []byte(starlight), 0o644)
	d := registerCatalogFile(t, url, f)
	base := fmt.Sprintf("%s/api/datasets/%d", url, d.ID)
	if st, b := callJSON(t, "PUT", base+"/catalog/declaration", goodDeclaration()); st != 200 {
		t.Fatalf("%d %s", st, b)
	}
	_, b := callJSON(t, "GET", base+"/impact", nil)
	if !strings.Contains(string(b), "declared catalog columns") {
		t.Fatalf("the impact preview must name the declaration: %s", b)
	}
	var pre struct {
		Digest string `json:"digest"`
	}
	json.Unmarshal(b, &pre)
	if pre.Digest == "" {
		t.Fatalf("no digest in %s", b)
	}
	if st, b := callJSON(t, "POST", base+"/delete", map[string]any{"impactDigest": pre.Digest}); st != 200 {
		t.Fatalf("delete: %d %s", st, b)
	}
	if st, _ := callJSON(t, "GET", base, nil); st != 404 {
		t.Fatalf("the catalog is still there: %d", st)
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatal("deleting a registered catalog must never remove the file")
	}
}
