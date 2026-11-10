package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const scripts = "../../../scientific-worker/"

func card(s string) string { return fmt.Sprintf("%-80s", s) }

// fitsBytes is a primary header of the given cards, END and padding, followed by nothing.
func fitsBytes(cards ...string) []byte {
	var b strings.Builder
	for _, c := range cards {
		b.WriteString(card(c))
	}
	b.WriteString(card("END"))
	for b.Len()%2880 != 0 {
		b.WriteByte(' ')
	}
	return []byte(b.String())
}

func requireAstropy(t *testing.T) {
	t.Helper()
	if exec.Command("python3", "-c", "import astropy").Run() != nil {
		if os.Getenv("VESQIRA_REQUIRE_WORKER") != "" {
			t.Fatal("python3 with astropy is required")
		}
		t.Skip("python3 with astropy not available")
	}
}

// oneShot is the previous registration path: one `fits_header.py <path>` process per file.
func oneShot(path string) (out []byte, errText string) {
	cmd := exec.Command("python3", "-P", scripts+"fits_header.py", path)
	b, _ := cmd.Output()
	var probe struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &probe) != nil {
		// an exception the script does not catch (astropy's VerifyError on an unparsable card): it printed a traceback
		// and no JSON, which the API reported as "worker returned no valid result". The persistent worker answers
		// "cannot read file" for the same file, the one intended behavioural difference, asserted below.
		return nil, "cannot read file: <uncaught>"
	}
	if !probe.OK {
		if probe.Error != "" {
			return nil, "cannot read file: " + probe.Error
		}
		return nil, "cannot read file"
	}
	return bytes.TrimSpace(b), ""
}

// Every generated fixture plus hostile and odd files must register to the same stored JSON, byte for byte, and fail
// with the same message, whether the header is read by a process per file or by the persistent worker.
func TestPersistentHeaderEqualsOneShotScript(t *testing.T) {
	requireAstropy(t)
	dir := t.TempDir()
	gen := exec.Command("python3", "generate.py", dir)
	gen.Dir = "../../../../fixtures"
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("fixtures: %v\n%s", err, out)
	}
	write := func(name string, b []byte) { // after the generated ones: the directory listing below sees all
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	comp := exec.Command("python3", "-c", `
import sys, numpy as np
from astropy.io import fits
fits.HDUList([fits.PrimaryHDU(), fits.CompImageHDU(np.arange(64, dtype="f4").reshape(8, 8))]).writeto(sys.argv[1], overwrite=True)`, filepath.Join(dir, "compressed.fits"))
	if out, err := comp.CombinedOutput(); err != nil {
		t.Fatalf("compressed fixture: %v\n%s", err, out)
	}
	write("not_fits.fits", []byte("this is not a FITS file\n"))
	write("empty.fits", nil)
	write("short.fits", []byte(card("SIMPLE  =                    T")))
	write("no_end.fits", []byte(strings.Repeat(card("COMMENT filler"), 36))) // one block, no END
	var huge []string
	huge = append(huge, "SIMPLE  =                    T", "BITPIX  =                  -32", "NAXIS   =                    0")
	for i := 0; i < 36*65; i++ {
		huge = append(huge, "COMMENT padding to exceed the header size limit")
	}
	write("oversized_header.fits", fitsBytes(huge...))
	write("naxis9.fits", fitsBytes("SIMPLE  =                    T", "BITPIX  =                  -32", "NAXIS   =                    9"))
	write("absurd_size.fits", fitsBytes("SIMPLE  =                    T", "BITPIX  =                  -32", "NAXIS   =                    3",
		"NAXIS1  =           1000000000", "NAXIS2  =           1000000000", "NAXIS3  =           1000000000"))
	write("not_primary.fits", fitsBytes("XTENSION= 'IMAGE   '", "BITPIX  =                  -32", "NAXIS   =                    0"))
	write("control_chars.fits", fitsBytes("SIMPLE  =                    T", "BITPIX  =                  -32", "NAXIS   =                    0",
		"BUNIT   = 'K\x01\x1b[31mé '", "SPECSYS = 'LSRK    '"))
	write("nan_value.fits", fitsBytes("SIMPLE  =                    T", "BITPIX  =                  -32", "NAXIS   =                    1",
		"NAXIS1  =                    4", "CRVAL1  =                  NaN"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := &Persistent{Command: []string{"python3", "-P", scripts + "plane_worker.py"}, Timeout: 30 * time.Second}
	defer p.Close()
	checked, failures := 0, 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".fits") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		wantOut, wantErr := oneShot(path)
		got, err := p.Header(context.Background(), path)
		switch {
		case wantErr == "cannot read file: <uncaught>":
			failures++
			if err == nil || err.Error() != "cannot read file" {
				t.Errorf("%s: error %v, want the generic \"cannot read file\"", e.Name(), err)
			}
		case wantErr != "":
			failures++
			if err == nil || err.Error() != wantErr {
				t.Errorf("%s: error %v, previously %q", e.Name(), err, wantErr)
			}
		case err != nil:
			t.Errorf("%s: %v, previously registered", e.Name(), err)
		case !bytes.Equal(got, wantOut):
			t.Errorf("%s differs\nnow:    %s\nbefore: %s", e.Name(), got, wantOut)
		}
		checked++
	}
	if checked < 20 || failures < 6 {
		t.Fatalf("only %d files checked and %d failures compared: the fixture set shrank", checked, failures)
	}
	if p.Starts.Load() != 1 {
		t.Fatalf("%d worker processes for %d files", p.Starts.Load(), checked)
	}
}
