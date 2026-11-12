package lifecycle

import "sort"

// MaskSource is a registered file whose only role is being the source of an imported mask (ADR 0055, ADR 0058): the mask rows
// say it (method `import`, the id of the file in the rule), and nothing else in the catalog uses it.
type MaskSource struct {
	DatasetID int64
	Targets   []Item // the datasets it is the mask file of
}

// useQueries each select the ids of datasets a researcher put to a use of their own. A dataset in any of them is not "only a mask
// source", whatever else it is: it is shown in the explorer. Tables a catalog does not have are skipped, as in datasetImpact.
var useQueries = []struct{ table, query string }{
	{"figures", `SELECT dataset_id FROM figures WHERE dataset_id IS NOT NULL`},                         // a figure (and so a board) is made from it
	{"saved_regions", `SELECT dataset_id FROM saved_regions`},                                          // regions drawn on it
	{"exploration_steps", `SELECT dataset_id FROM exploration_steps`},                                  // opened in the viewer
	{"run_outputs", `SELECT dataset_id FROM run_outputs`},                                              // an output of a run
	{"derived", `SELECT dataset_id FROM derived`},                                                      // itself a result
	{"derived", `SELECT CAST(j.value AS INTEGER) FROM derived d, json_each(d.sources) j`},              // an input of an analysis
	{"interpretations", `SELECT dataset_id FROM interpretations`},                                      // an orientation declared for it
	{"interpretations", `SELECT reliability_id FROM interpretations WHERE reliability_id IS NOT NULL`}, // the reliability map of another
	{"masks", `SELECT dataset_id FROM masks`},                                                          // it has masks of its own
	{"offline_entries", `SELECT dataset_id FROM offline_entries`},                                      // kept for offline use
}

// MaskSources lists the registered files that only serve as the file of an imported mask, with the datasets they are the mask
// file of. It is derived from stored rows on every call: forgetting the mask, or using the file for anything else, ends it.
// Nothing is deleted or unregistered; a caller decides how to show these.
func (s *Service) MaskSources() ([]MaskSource, error) {
	if !has(s.db, "masks") {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT CAST(json_extract(m.rule,'$.sourceDatasetId') AS INTEGER), m.dataset_id, t.name
		FROM masks m JOIN datasets t ON t.id=m.dataset_id JOIN datasets src ON src.id=CAST(json_extract(m.rule,'$.sourceDatasetId') AS INTEGER)
		WHERE m.method='import' AND json_extract(m.rule,'$.sourceDatasetId') IS NOT NULL AND src.id<>m.dataset_id ORDER BY m.id`)
	if err != nil {
		return nil, err
	}
	targets := map[int64][]Item{}
	for rows.Next() {
		var src int64
		var it Item
		if err := rows.Scan(&src, &it.ID, &it.Label); err != nil {
			rows.Close()
			return nil, err
		}
		dup := false
		for _, x := range targets[src] {
			dup = dup || x.ID == it.ID
		}
		if !dup {
			targets[src] = append(targets[src], it)
		}
	}
	err = rows.Err()
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	if err != nil || len(targets) == 0 {
		return nil, err
	}
	for _, u := range useQueries {
		if !has(s.db, u.table) {
			continue
		}
		r, err := s.db.Query(u.query)
		if err != nil {
			return nil, err
		}
		for r.Next() {
			var id int64
			if err := r.Scan(&id); err != nil {
				r.Close()
				return nil, err
			}
			delete(targets, id)
		}
		err = r.Err()
		if cerr := r.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
	}
	out := make([]MaskSource, 0, len(targets))
	for id, t := range targets {
		out = append(out, MaskSource{DatasetID: id, Targets: t})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DatasetID < out[j].DatasetID })
	return out, nil
}
