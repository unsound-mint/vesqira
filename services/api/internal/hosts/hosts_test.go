package hosts

import (
	"strings"
	"testing"
)

func TestInRootsIsLexicalAndStrict(t *testing.T) {
	h := Host{Roots: []string{"/data", "/mnt/cubes/"}}
	for p, want := range map[string]bool{
		"/data": true, "/data/a.fits": true, "/data2/a.fits": false, "/data/../etc/passwd": false, "/data/./a": false,
		"/mnt/cubes/x/y.fits": true, "relative": false, "": false, "/": false, "//data/a": false,
	} {
		if got := h.InRoots(p); got != want {
			t.Errorf("InRoots(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestQuoteSurvivesAShell(t *testing.T) {
	if got := quote(`a'b"c $x`); got != `'a'\''b"c $x'` {
		t.Errorf("%s", got)
	}
}

func TestValidateRefusesWhatCouldReachTheSSHCommandLine(t *testing.T) {
	ok := Host{Name: "n", Dest: "cluster-login", Python: "python3", Roots: []string{"/shared/home/u/cubes"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Host{
		{Name: "n", Dest: "-oProxyCommand=x", Python: "python3", Roots: ok.Roots},
		{Name: "n", Dest: "a;b", Python: "python3", Roots: ok.Roots},
		{Name: "n", Dest: "a", Python: "$(id)", Roots: ok.Roots},
		{Name: "n", Dest: "a", Python: "-c", Roots: ok.Roots},
		{Name: "n", Dest: "a", Python: "python3", Roots: []string{"/a/../b"}},
		{Name: strings.Repeat("x", 61), Dest: "a", Python: "python3", Roots: ok.Roots},
		{Name: "n", Dest: "a", Python: "python3", Roots: []string{"/"}},
	}
	for i, h := range bad {
		if h.Validate() == nil {
			t.Errorf("case %d accepted: %+v", i, h)
		}
	}
}
