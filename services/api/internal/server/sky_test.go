package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// The sky routes pass the panel's frame and the pixel to the worker and return its answer as it is; the positions themselves
// are tested against astropy in the worker's own tests.
func TestSkyRoutesAskTheWorkerAndValidate(t *testing.T) {
	t.Parallel()
	head := `{"ok":true,"nbytes":0,"footprint":null,"reason":"no complete celestial WCS"}`
	fp := &fakePlanes{reply: worker.Reply{Head: json.RawMessage(head)}}
	base, _, _, _, id := planeServer(t, fp)
	url := func(route, q string) string { return base + "/api/datasets/" + itoa(id) + "/" + route + q }

	resp, body := get(t, url("sky-footprint", "?frame=galactic"))
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"footprint":null`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if fp.calls[0]["op"] != "sky_footprint" || fp.calls[0]["frame"] != "galactic" {
		t.Fatalf("worker got %v", fp.calls[0])
	}
	// a second ask for the same frame is the cache's; another frame is another answer
	get(t, url("sky-footprint", "?frame=galactic"))
	if len(fp.calls) != 1 {
		t.Fatalf("a repeated footprint asked the worker again: %d calls", len(fp.calls))
	}
	get(t, url("sky-footprint", "?frame=icrs"))
	if len(fp.calls) != 2 || fp.calls[1]["frame"] != "icrs" {
		t.Fatalf("frame is part of the key: %v", fp.calls)
	}

	if r, _ := get(t, url("sky-point", "?frame=icrs&x=3&y=4")); r.StatusCode != 200 {
		t.Fatalf("point: %d", r.StatusCode)
	}
	last := fp.calls[len(fp.calls)-1]
	if last["op"] != "sky_point" || last["x"] != 3 || last["y"] != 4 || last["frame"] != "icrs" {
		t.Fatalf("worker got %v", last)
	}
	n := len(fp.calls)
	for _, c := range [][2]string{
		{"sky-footprint", ""}, {"sky-footprint", "?frame=fk5"}, {"sky-footprint", "?frame=ICRS"},
		{"sky-point", "?frame=icrs"}, {"sky-point", "?frame=icrs&x=-1&y=2"}, {"sky-point", "?frame=icrs&x=a&y=2"}, {"sky-point", "?x=1&y=2"},
	} {
		if r, _ := get(t, url(c[0], c[1])); r.StatusCode != 400 {
			t.Errorf("%s%s: %d, want 400", c[0], c[1], r.StatusCode)
		}
	}
	if len(fp.calls) != n {
		t.Fatal("the worker must not run for a malformed request")
	}
	if r, _ := get(t, base+"/api/datasets/9999/sky-footprint?frame=icrs"); r.StatusCode != 404 {
		t.Errorf("unknown id: %d", r.StatusCode)
	}
}
