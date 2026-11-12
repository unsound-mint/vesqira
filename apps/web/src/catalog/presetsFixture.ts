import type { RelOrientPresets } from "../api/analysis";

/** What GET /api/analysis/relative-orientation/presets returns, in the shape of services/scientific-worker/relorient_presets.json. The
    numbers of "standard" are the ones the analysis used before presets existed; "tight" differs so a test can see a field refill. */
const reasons = (v: Record<string, number>) => Object.fromEntries(Object.entries(v).map(([k, value]) => [k, { value, reason: `reason for ${k}` }]));
const STANDARD = { radius_arcmin: 10, min_pixels: 5, snr_min: 3, sigma_floor_deg: 1, boot_samples: 2000, n_shifts_catalog: 3000, n_shifts_maps: 1000,
  min_shift_deg: 1, keep_fraction: 0.9, seed: 20261020, bins: 9, strata_bins: 5 };

export const PRESETS: RelOrientPresets = {
  schema: 1,
  default: "standard",
  presets: [
    { name: "standard", version: 1, title: "Standard", description: "The defaults used when no preset is named.", values: reasons(STANDARD) },
    { name: "tight", version: 2, title: "Tight disk", description: "A smaller disk and a stricter filter.", values: reasons({ ...STANDARD, radius_arcmin: 4, snr_min: 5, bins: 6 }) },
  ],
};
