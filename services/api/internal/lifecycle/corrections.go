package lifecycle

import (
	"strconv"
)

// CorrectionImpact is what depends on a dataset's coordinates or unit, before a WCS or unit correction is saved or reverted
// (ADR 0061). Each group says whether it is re-read with the new values (live) or keeps the values it was made with (pinned).
// Pinned evidence is never changed: a saved figure keeps its picture and records the correction state it was drawn under (Draw
// again announces a change); a derived result keeps its values and records the corrections its inputs had.
type CorrectionImpact struct {
	Groups []Group  `json:"groups"`
	Live   []string `json:"live"`  // what is read again with the new values, in plain words
	Notes  []string `json:"notes"` // what does not take corrections at all
}

// CorrectionImpact lists the dependents of dataset id for a change of its WCS (wcs) and/or its unit (unit).
func (s *Service) CorrectionImpact(id int64, wcs, unit bool) (CorrectionImpact, error) {
	im := &Impact{}
	b := &builder{im: im, ids: map[string][]int64{}}
	q := s.db
	out := CorrectionImpact{Groups: []Group{}, Live: []string{}}
	figs, revs, err := figuresOf(q, id)
	if err != nil {
		return out, err
	}
	b.add("figures", "pinned", figs, func(n int) string {
		return plural(n, "saved figure was", "saved figures were") + " drawn from this dataset. Each keeps its picture and records the correction state it was drawn under; Draw again says the corrections changed before it draws."
	})
	// figures that draw this dataset as an overlay (contours, orientation lines, a beam) on another base
	pat := `%"datasetId":` + strconv.FormatInt(id, 10) + `,%`
	ov, err := items(q, "figures", `SELECT id,title FROM figures WHERE dataset_id<>? AND scene LIKE ?`, id, pat)
	if err != nil {
		return out, err
	}
	b.add("overlay_figures", "pinned", ov, func(n int) string {
		return plural(n, "saved figure draws", "saved figures draw") + " this dataset as an overlay. They keep their picture; Draw again says the corrections changed."
	})
	allFigs := append(append([]Item{}, figs...), ov...)
	bs, err := boardsHolding(q, idsOf(allFigs))
	if err != nil {
		return out, err
	}
	b.add("boards", "pinned", bs, func(n int) string {
		return plural(n, "board holds", "boards hold") + " those figures. Nothing on them changes."
	})
	sn, err := snapshotsPinning(q, revs)
	if err != nil {
		return out, err
	}
	b.add("snapshots", "pinned", sn, func(n int) string {
		return plural(n, "published review shows", "published reviews show") + " those figures from a frozen copy. A reviewer keeps seeing what was published."
	})
	der, err := items(q, "derived", `SELECT d.dataset_id, COALESCE(ds.name, 'dataset '||d.dataset_id) FROM derived d, json_each(d.sources) j
		LEFT JOIN datasets ds ON ds.id=d.dataset_id WHERE j.value=? AND d.dataset_id<>? AND d.op NOT LIKE 'relorient%'`, id, id)
	if err != nil {
		return out, err
	}
	b.add("derived", "pinned", der, func(n int) string {
		return plural(n, "derived result was", "derived results were") + " made from this dataset (moment maps, cutouts, alignments, differences). They keep their values and the header they were written with; their record says which corrections were applied (none, for results made before corrections existed). Make them again to use the new values."
	})
	ro, err := items(q, "derived", `SELECT d.dataset_id, COALESCE(ds.name, 'result '||d.dataset_id) FROM derived d, json_each(d.sources) j
		LEFT JOIN datasets ds ON ds.id=d.dataset_id WHERE j.value=? AND d.op LIKE 'relorient%'`, id)
	if err != nil {
		return out, err
	}
	b.add("relorient", "pinned", ro, func(n int) string {
		return plural(n, "relative-orientation result uses", "relative-orientation results use") + " this dataset. They keep their angles and statistics as computed; run them again to use the new values."
	})
	if wcs {
		cat, err := items(q, "figures", `SELECT id,title FROM figures WHERE dataset_id=? AND scene LIKE '%"kind":"catalog"%'`, id)
		if err != nil {
			return out, err
		}
		b.add("catalog_overlays", "pinned", cat, func(n int) string {
			return plural(n, "saved figure places", "saved figures place") + " catalog sources on this map by its WCS. The saved picture keeps the old positions; a live view places them with the corrected WCS."
		})
		rm, err := items(q, "masks", `SELECT id, type||' mask '||name FROM masks WHERE dataset_id=? AND method='regions'`, id)
		if err != nil {
			return out, err
		}
		b.add("region_masks", "pinned", rm, func(n int) string {
			return plural(n, "mask was", "masks were") + " made from regions. Their pixels do not move (regions are in pixels); the WCS cards copied into each mask file are the old ones."
		})
		regs, err := items(q, "saved_regions", `SELECT id,name FROM saved_regions WHERE dataset_id=?`, id)
		if err != nil {
			return out, err
		}
		b.add("regions", "live", regs, func(n int) string {
			return plural(n, "saved region stays", "saved regions stay") + " on the same pixels; its sky position and any width in arcsec are read with the corrected WCS."
		})
	}
	if unit {
		th, err := items(q, "masks", `SELECT id, type||' mask '||name FROM masks WHERE dataset_id=? AND type IN ('low_snr','saturated')`, id)
		if err != nil {
			return out, err
		}
		b.add("threshold_masks", "pinned", th, func(n int) string {
			return plural(n, "mask has", "masks have") + " a threshold stated in the unit in force when it was made. The mask keeps its pixels and its rule; make it again if the threshold should be read in the new unit."
		})
	}
	steps, err := items(q, "exploration_steps", `SELECT id, label FROM exploration_steps WHERE dataset_id=?`, id)
	if err != nil {
		return out, err
	}
	b.add("history", "live", steps, func(n int) string {
		return plural(n, "step of the exploration history reopens", "steps of the exploration history reopen") + " this dataset; each is read with the corrections in force when it is reopened."
	})
	out.Groups = im.Groups
	if out.Groups == nil {
		out.Groups = []Group{}
	}
	if wcs {
		out.Live = append(out.Live, "Open views, linked views, coordinate readouts, region spectra, channel maps, position-velocity cuts and overlays drawn from now on use the corrected WCS.")
	}
	if unit {
		out.Live = append(out.Live, "Open views, readouts, spectra, conversions and figure limits from now on use the corrected unit and say it is a correction.")
	}
	out.Notes = []string{"Analyses run as jobs on a host read the file's own header and do not take corrections; Vesqira refuses to draft one for a dataset with corrections in force."}
	return out, nil
}
