package jobs

import (
	"errors"
	"testing"
)

// The input path is read the way the tool reads it: a by-name template (ROHSA) finds the key in any table outside its guard table,
// and a config naming it twice is refused, so a standing approval never checks one path while the tool reads another.
func TestInputPathOnceReadsTheKeyByNameAcrossTables(t *testing.T) {
	rohsa := ShippedTemplate("rohsa")
	vgt := ShippedTemplate("vgt")
	for _, c := range []struct {
		name, text, want string
		refused          bool
	}{
		{"in the input table", "[input]\ninput = \"/a/x.fits\"\n[rohsa]\nn_gauss = [4]\n", "/a/x.fits", false},
		{"in another table", "[data]\ninput = \"/a/y.fits\"\n[rohsa]\nn_gauss = [4]\n", "/a/y.fits", false},
		{"in two tables", "[input]\ninput = \"/a/x.fits\"\n[data]\ninput = \"/b/y.fits\"\n", "", true},
		{"in a nested table too", "[input]\ninput = \"/a/x.fits\"\n[data.more]\ninput = \"/b/y.fits\"\n", "", true},
		{"only in the guard table", "[input]\ninput = \"/a/x.fits\"\n[auto_config]\nenabled = false\ninput = \"/b/y.fits\"\n", "/a/x.fits", false},
		{"nowhere", "[rohsa]\nn_gauss = [4]\n", "", false},
	} {
		got, err := InputPathOnce(rohsa, c.text)
		if c.refused != errors.Is(err, ErrInvalid) || got != c.want {
			t.Errorf("%s: %q, %v", c.name, got, err)
		}
		if p := InputPathFor(rohsa, c.text); p != c.want {
			t.Errorf("%s: InputPathFor %q", c.name, p)
		}
	}
	// a template that reads one table keeps reading only that table
	if got, err := InputPathOnce(vgt, "[input]\ninput_file = \"/a/x.fits\"\n[other]\ninput_file = \"/b/y.fits\"\n"); err != nil || got != "/a/x.fits" {
		t.Errorf("vgt: %q, %v", got, err)
	}
}
