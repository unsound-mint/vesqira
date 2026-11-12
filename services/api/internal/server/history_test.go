package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/history"
	"github.com/unsound-mint/vesqira/services/api/internal/lifecycle"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

func sceneFor(id int64, channel int) map[string]any {
	return map[string]any{
		"schema":   "scene/1",
		"base":     map[string]any{"datasetId": id, "channel": channel, "display": map[string]any{"mapping": "scalar", "colormap": "viridis", "stretch": "linear", "range": "percentile"}},
		"overlays": []any{},
		"view":     map[string]any{"zoom": 1},
	}
}

func TestExplorationHistoryRoutes(t *testing.T) {
	t.Parallel()
	cat, err := catalog.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	hs, err := history.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	ps, err := projects.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	d, err := cat.Upsert(workspace.Local, "local-owner", catalog.Dataset{Path: "/data/cube.fits", Name: "cube.fits", SizeBytes: 1, ModifiedAt: time.Now(), Header: json.RawMessage(`{"ok":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&Server{Store: cat, History: hs, Projects: ps}).Handler())
	defer ts.Close()
	url := ts.URL + "/api/history"

	r, b := postJSON(t, url, map[string]any{"projectId": 0, "kind": "opened", "label": "Opened cube.fits", "scene": sceneFor(d.ID, 0)})
	if r.StatusCode != 201 {
		t.Fatalf("add: %d %s", r.StatusCode, b)
	}
	var first struct {
		ID    int64
		Scene json.RawMessage
	}
	json.Unmarshal(b, &first)
	r, b = postJSON(t, url, map[string]any{"projectId": 0, "parentId": first.ID, "kind": "channel", "label": "Channel 11", "scene": sceneFor(d.ID, 10)})
	if r.StatusCode != 201 {
		t.Fatalf("add child: %d %s", r.StatusCode, b)
	}

	// refused: a scene that is not scene/1, one on a dataset that is not here, an unknown project, an unknown field, a bad kind
	for name, in := range map[string]map[string]any{
		"scene":   {"projectId": 0, "kind": "opened", "label": "x", "scene": map[string]any{"schema": "scene/1"}},
		"dataset": {"projectId": 0, "kind": "opened", "label": "x", "scene": sceneFor(999, 0)},
		"project": {"projectId": 77, "kind": "opened", "label": "x", "scene": sceneFor(d.ID, 0)},
		"kind":    {"projectId": 0, "kind": "bogus", "label": "x", "scene": sceneFor(d.ID, 0)},
		"parent":  {"projectId": 0, "parentId": 4242, "kind": "channel", "label": "x", "scene": sceneFor(d.ID, 0)},
		"unknown": {"projectId": 0, "kind": "opened", "label": "x", "scene": sceneFor(d.ID, 0), "extra": 1},
	} {
		if r, b := postJSON(t, url, in); r.StatusCode < 400 {
			t.Errorf("%s accepted: %d %s", name, r.StatusCode, b)
		}
	}

	// the list has no scenes; one step has its scene
	_, lb := get(t, url+"?project=0")
	var list struct {
		Steps []map[string]any
		Keep  int
	}
	json.Unmarshal(lb, &list)
	if len(list.Steps) != 2 || list.Keep != history.Keep || strings.Contains(string(lb), `"scene"`) {
		t.Errorf("list: %s", lb)
	}
	if list.Steps[1]["parentId"] != float64(first.ID) || list.Steps[0]["parentId"] != nil {
		t.Errorf("parents: %s", lb)
	}
	if _, ob := get(t, url+"?project=5"); !strings.Contains(string(ob), "project not found") {
		t.Errorf("history of an unknown project: %s", ob)
	}
	resp, one := get(t, url+"/"+itoa(int(first.ID)))
	if resp.StatusCode != 200 || !strings.Contains(string(one), `"scene":{`) || !strings.Contains(string(one), `"schema":"scene/1"`) {
		t.Errorf("get: %d %s", resp.StatusCode, one)
	}
	if resp, _ := get(t, url+"/9999"); resp.StatusCode != 404 {
		t.Errorf("unknown step: %d", resp.StatusCode)
	}

	// deleting the dataset removes the steps that open it, and the impact says so first
	svc := lifecycle.New(cat.DB())
	im, err := svc.Impact(workspace.Local, lifecycle.KindDataset, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	said := false
	for _, g := range im.Groups {
		said = said || (g.Key == "history" && g.Effect == "removed" && g.Count == 2)
	}
	if !said {
		t.Errorf("impact does not name the history: %+v", im.Groups)
	}
	if _, err := svc.Delete(workspace.Local, lifecycle.KindDataset, d.ID, im.Digest); err != nil {
		t.Fatal(err)
	}
	if steps, _ := hs.List(workspace.Local, 0); len(steps) != 0 {
		t.Errorf("steps of a deleted dataset remain: %v", steps)
	}
}
