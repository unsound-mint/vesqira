import type { RohsaSigmav } from "../api/rohsa";

/** A small diagram: three cells (one with contributors and zero weight), km/s axes, two centre bins and three width bins. */
export const diagram = {
  runId: 7, cropName: "region_a.fits", weight: "integrated", weight_unit: "K km/s", weight_how: "amp x sigma x sqrt(2 pi) x dv. Assumes nothing else.",
  axes: { centre: { unit: "km/s", name: "radio velocity" }, sigma: { unit: "km/s" } },
  v_edges: [0, 5, 10], sigma_edges: [0, 2, 4, 6], binning: "defaults", policy: "p",
  cells: [[0, 0, 3, 12.5], [1, 1, 2, 1250], [2, 1, 1, 0]],
  components: 10, invalid_sigma: 1, outside: 3, in_diagram: 6, weight_total: 1262.5, weight_outside: 0, observed: 6,
  dat: { name: "x.dat", size_bytes: 100, mtime_ns: 5 }, unit_source: "file", width: 3, height: 2,
} as unknown as RohsaSigmav;
