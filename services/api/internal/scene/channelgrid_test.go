package scene

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const goodGrid = `{"schema":"channel-grid/1","datasetId":3,"first":10,"last":40,"stride":3,"average":2,"columns":4,"panels":11,
 "spectral":{"unit":"km/s","convention":"radio"},
 "display":{"colormap":"viridis","stretch":"asinh","limits":[0.5,8.25],"limitsUnit":"K","source":"percentile 1 to 99 of the pooled panels 1, 3, 5"},
 "preview":{"step":2,"panelWidth":300,"panelHeight":200}}`

func TestAChannelGridIsReadStrictlyAndReMarshalled(t *testing.T) {
	g, err := ParseChannelGrid([]byte(goodGrid))
	if err != nil {
		t.Fatal(err)
	}
	if g.Panels != 11 || g.Display.Limits != [2]float64{0.5, 8.25} || g.Spectral.Convention != "radio" || g.Preview.Step != 2 {
		t.Fatalf("%+v", g)
	}
	out, err := g.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseChannelGrid(out)
	if err != nil {
		t.Fatalf("what is stored must read back: %v", err)
	}
	if a, _ := again.Marshal(); string(a) != string(out) {
		t.Errorf("not stable:\n%s\n%s", out, a)
	}
	var back map[string]any
	if json.Unmarshal(out, &back) != nil || back["schema"] != ChannelGridVersion {
		t.Errorf("%s", out)
	}
}

func TestAChannelGridIsRefusedWhenItBreaksTheCapsOrIsIncomplete(t *testing.T) {
	cases := map[string]func(string) string{
		"another schema":           func(s string) string { return strings.Replace(s, "channel-grid/1", "channel-grid/2", 1) },
		"an unknown key":           func(s string) string { return strings.Replace(s, `"columns":4`, `"columns":4,"extra":1`, 1) },
		"a range backwards":        func(s string) string { return strings.Replace(s, `"first":10,"last":40`, `"first":41,"last":40`, 1) },
		"average above the stride": func(s string) string { return strings.Replace(s, `"average":2`, `"average":4`, 1) },
		"more than 64 panels": func(s string) string {
			return strings.Replace(strings.Replace(s, `"stride":3`, `"stride":1`, 1), `"last":40`, `"last":100`, 1)
		},
		"a panel count that disagrees": func(s string) string { return strings.Replace(s, `"panels":11`, `"panels":12`, 1) },
		"no limits":                    func(s string) string { return strings.Replace(s, `"limits":[0.5,8.25],`, ``, 1) },
		"limits upside down":           func(s string) string { return strings.Replace(s, `[0.5,8.25]`, `[9,8.25]`, 1) },
		"a null limit":                 func(s string) string { return strings.Replace(s, `[0.5,8.25]`, `[null,8.25]`, 1) },
		"no source for the limits": func(s string) string {
			return strings.Replace(s, `"source":"percentile 1 to 99 of the pooled panels 1, 3, 5"`, `"source":""`, 1)
		},
		"an unknown colormap":    func(s string) string { return strings.Replace(s, `"viridis"`, `"rainbow"`, 1) },
		"twilight (angles only)": func(s string) string { return strings.Replace(s, `"viridis"`, `"twilight"`, 1) },
		"an unknown convention":  func(s string) string { return strings.Replace(s, `"radio"`, `"galilean"`, 1) },
		"a unit with markup":     func(s string) string { return strings.Replace(s, `"km/s"`, `"<b>"`, 1) },
		"no columns":             func(s string) string { return strings.Replace(s, `"columns":4,`, ``, 1) },
		"nine columns":           func(s string) string { return strings.Replace(s, `"columns":4`, `"columns":9`, 1) },
		"a preview step of zero": func(s string) string { return strings.Replace(s, `"step":2`, `"step":0`, 1) },
		"panels too large to send": func(s string) string {
			return strings.Replace(s, `"panelWidth":300,"panelHeight":200`, `"panelWidth":3000,"panelHeight":3000`, 1)
		},
		"too many pixels together": func(s string) string {
			s = strings.Replace(s, `"panelWidth":300,"panelHeight":200`, `"panelWidth":1024,"panelHeight":1024`, 1)
			s = strings.Replace(s, `"stride":3`, `"stride":2`, 1)
			s = strings.Replace(s, `"last":40`, `"last":42`, 1)
			return strings.Replace(s, `"panels":11`, `"panels":17`, 1)
		},
		"no dataset":       func(s string) string { return strings.Replace(s, `"datasetId":3,`, ``, 1) },
		"a stride of zero": func(s string) string { return strings.Replace(s, `"stride":3`, `"stride":0`, 1) },
	}
	for name, mutate := range cases {
		in := mutate(goodGrid)
		if in == goodGrid {
			t.Errorf("%s: the case changed nothing", name)
			continue
		}
		if _, err := ParseChannelGrid([]byte(in)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ParseChannelGrid([]byte(goodGrid + "{}")); err == nil {
		t.Error("trailing data accepted")
	}
}

func TestGridPanelsCountsFromTheFirstChannel(t *testing.T) {
	for _, c := range []struct{ f, l, s, want int }{{0, 0, 1, 1}, {0, 7, 3, 3}, {2, 20, 5, 4}, {5, 4, 1, 0}, {0, 5, 0, 0}} {
		if got := GridPanels(c.f, c.l, c.s); got != c.want {
			t.Errorf("%+v: %d", c, got)
		}
	}
}
