package projects

import (
	"encoding/json"
	"path"
	"sort"

	"github.com/unsound-mint/vesqira/services/api/internal/discover"
)

func parentOf(p string) string {
	d := path.Dir(p)
	if d == "." {
		return ""
	}
	return d
}

// Rows turns a scan into the entries the tree is browsed from. Run entries come first so a run folder is never
// replaced by a plain folder entry of the same path.
func Rows(r *discover.Result) []Row {
	type key struct{ g, parent, name string }
	have := map[key]bool{}
	var out []Row
	add := func(g, kind, p, format string, v any) {
		b, _ := json.Marshal(v)
		k := key{g, parentOf(p), path.Base(p)}
		if have[k] {
			return
		}
		have[k] = true
		out = append(out, Row{Group: g, Parent: k.parent, Name: k.name, Kind: kind, Path: p, Format: format, Data: b})
	}
	for _, run := range r.Runs {
		outs := run.Outputs
		run.Outputs = nil // outputs are entries of their own
		add("runs", "run", run.Path, "", run)
		for _, o := range outs {
			add("runs", "output", o.Path, o.Format, o)
		}
	}
	for _, f := range r.Data {
		add("data", "data", f.Path, f.Format, f)
	}
	for _, f := range r.Configs {
		add("configs", "config", f.Path, "", f)
	}
	for _, f := range r.Jobs {
		add("jobs", "job", f.Path, "", f)
	}
	// folders above the entries, with how many entries each lists
	count := map[key]int{}
	for _, row := range out {
		count[key{row.Group, row.Parent, ""}]++
	}
	dirs := map[key]bool{}
	for _, row := range out {
		for p := row.Parent; p != ""; p = parentOf(p) {
			k := key{row.Group, parentOf(p), path.Base(p)}
			if dirs[k] || have[k] {
				continue
			}
			dirs[k] = true
		}
	}
	var keys []key
	for k := range dirs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].g != keys[j].g {
			return keys[i].g < keys[j].g
		}
		if keys[i].parent != keys[j].parent {
			return keys[i].parent < keys[j].parent
		}
		return keys[i].name < keys[j].name
	})
	for _, k := range keys { // a folder is itself an entry of its parent
		count[key{k.g, k.parent, ""}]++
	}
	for _, k := range keys {
		p := k.name
		if k.parent != "" {
			p = k.parent + "/" + k.name
		}
		b, _ := json.Marshal(map[string]int{"count": count[key{k.g, p, ""}]})
		out = append(out, Row{Group: k.g, Parent: k.parent, Name: k.name, Kind: "dir", Path: p, Data: b})
	}
	return out
}
