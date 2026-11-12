package runs

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
)

func viewsStore(t *testing.T) *Store {
	t.Helper()
	cat, err := catalog.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	st, err := Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSavedViewsKeepWordsAndReplaceByName(t *testing.T) {
	st := viewsStore(t)
	a, err := st.SaveView("  LMC sweep ", ViewSpec{Query: "block_size:16..64 am>0.3", Columns: []string{"status", "p:block_size", "m:am", "p:block_size"}, Sort: &ViewSort{Column: "m:am", Dir: -1}, Plot: &ViewPlot{Y: "am", X: "block_size", XKind: "param", By: "gradient_method"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "LMC sweep" || len(a.Spec.Columns) != 3 {
		t.Fatalf("name %q columns %v: the name is trimmed and a repeated column dropped", a.Name, a.Spec.Columns)
	}
	// the same name in another case is the same view
	b, err := st.SaveView("lmc SWEEP", ViewSpec{Query: "qc:passed", Columns: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != a.ID {
		t.Fatalf("saving under the same name made a second view (%d, %d)", a.ID, b.ID)
	}
	if _, err := st.SaveView("other", ViewSpec{Columns: []string{"status"}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Views()
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Name != "lmc SWEEP" || got[0].Spec.Query != "qc:passed" || got[0].Spec.Plot != nil || got[0].Spec.Sort != nil {
		t.Fatalf("the replacement did not replace: %+v", got[0])
	}
	if err := st.DeleteView(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteView(a.ID); !errors.Is(err, ErrViewNotFound) {
		t.Fatalf("deleting twice: %v", err)
	}
}

func TestSavedViewsRefuseWhatTheyCannotKeep(t *testing.T) {
	st := viewsStore(t)
	long := strings.Repeat("x", 2001)
	for name, c := range map[string]struct {
		name string
		sp   ViewSpec
	}{
		"no name":      {"  ", ViewSpec{}},
		"name lines":   {"a\nb", ViewSpec{}},
		"long query":   {"v", ViewSpec{Query: long}},
		"query lines":  {"v", ViewSpec{Query: "a\nb"}},
		"empty column": {"v", ViewSpec{Columns: []string{""}}},
		"bad sort dir": {"v", ViewSpec{Sort: &ViewSort{Column: "status", Dir: 2}}},
		"bad x kind":   {"v", ViewSpec{Plot: &ViewPlot{XKind: "file"}}},
	} {
		if _, err := st.SaveView(c.name, c.sp); err == nil || !strings.HasPrefix(err.Error(), "invalid: ") {
			t.Errorf("%s: %v", name, err)
		}
	}
	for i := 0; i < MaxViews; i++ {
		if _, err := st.SaveView(fmt.Sprintf("v%d", i), ViewSpec{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.SaveView("one too many", ViewSpec{}); err == nil {
		t.Error("a view past the cap was kept")
	}
	if _, err := st.SaveView("v3", ViewSpec{Query: "replaced"}); err != nil {
		t.Errorf("replacing at the cap must still work: %v", err)
	}
}
