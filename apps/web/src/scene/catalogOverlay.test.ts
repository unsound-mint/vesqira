import { describe, expect, it } from "vitest";
import type { Dataset } from "../api/catalog";
import { parseGeometry } from "../api/overlays";
import type { OverlayGeometry, OverlayGeometryHead } from "../api/overlays";
import type { Plane } from "../api/planes";
import { overlayBasis, overlayTitle } from "./basis";
import { figureScene, overlaysPending } from "./figureScene";
import { needsGeometry } from "./geometry";
import type { GeometryMap } from "./geometry";
import { catalogCandidates, draftToOverlay, emptyDraft, numericColumns, overlayToDraft } from "./overlayForm";
import { nearestMarker, sceneLayers, strokeMarks, valueColor } from "./overlays";
import type { Mark } from "./overlays";
import { OVERLAY_COLORS } from "./palette";
import { DEFAULT_DISPLAY, newScene, sceneReducer } from "./scene";
import type { Scene, SceneOverlay } from "./scene";

const W = 40;
const H = 30;
const plane: Plane = {
  head: { channel: 0, channels: 1, width: W, height: H, spectral_value: null, spectral_unit: null, preview: null, stats: { finite_count: W * H, nonfinite_count: 0, min: 0, max: 1, p01: 0, p99: 1, overflow_count: 0 } } as unknown as Plane["head"],
  data: new Float32Array(W * H),
};
const placement = { method: "placed by WCS geometry (astropy): never resampled onto the base grid; converted from Galactic to the base's ICRS frame; catalog frame Galactic (declared by you in the catalog's column declaration); base frame ICRS (declared in the header)", from_frame: "Galactic", to_frame: "ICRS", same_frame: false, same_dataset: false };

/** A geometry frame as the API sends it: uint32 header length, JSON padded to 8 bytes, little-endian float32. */
function frame(head: Record<string, unknown>, nums: number[]): ArrayBuffer {
  let json = new TextEncoder().encode(JSON.stringify(head));
  const pad = (8 - ((4 + json.length) % 8)) % 8;
  json = new Uint8Array([...json, ...new Array<number>(pad).fill(32)]);
  const buf = new ArrayBuffer(4 + json.length + 4 * nums.length);
  const dv = new DataView(buf);
  dv.setUint32(0, json.length, true);
  new Uint8Array(buf, 4, json.length).set(json);
  nums.forEach((v, i) => dv.setFloat32(4 + json.length + 4 * i, v, true));
  return buf;
}

const counts = { rows: 10, no_position: 3, no_position_missing: 1, no_position_unreadable: 2, outside: 2, filtered: 2, drawn: 3 };
const headOf = (extra: Record<string, unknown> = {}) =>
  ({ kind: "catalog", dtype: "<f4", coords: "base pixels", placement, markers: 3, counts, catalog: { format: "tsv", frame: "Galactic", frame_status: "declared", skipped_lines: 1 }, filter: null, vectors: null, color: null, ...extra }) as unknown as OverlayGeometryHead;
// x, y, vector angle, vector length, colour value, row
const ROWS = [10, 5, 90, 8, 1.0, 3, 20, 15, NaN, NaN, NaN, 4, 30, 25, 0, 6, 3.0, 9];
const geo = (head: OverlayGeometryHead = headOf(), nums = ROWS): OverlayGeometry => parseGeometry(frame(head, nums));
const ready = (g: OverlayGeometry): GeometryMap[string] => ({ key: "k", status: "ready", g });
const cat: SceneOverlay = { id: "stars", kind: "catalog", datasetId: 7, visible: true, color: "lime" };
const scene = (o: SceneOverlay = cat): Scene => sceneReducer(newScene(1, DEFAULT_DISPLAY, false, true), { type: "addOverlay", overlay: o });
const names = (id: number) => (id === 7 ? "stars.tsv" : `dataset ${id}`);
type Marker = Extract<Mark, { type: "marker" }>;
const markersOf = (s: Scene, g: GeometryMap) => sceneLayers(s, { plane, scale: 4, interp: null, reliability: null, geometry: g }).flatMap((l) => l.marks);

describe("catalog geometry frames", () => {
  it("parse six numbers per drawn row, and refuse a frame of the wrong size", () => {
    const g = geo();
    expect(g.kind === "catalog" && g.markers.length).toBe(18);
    expect(() => geo(headOf(), ROWS.slice(0, 17))).toThrow(/wrong size/);
    expect(() => geo(headOf({ markers: 4 }))).toThrow(/wrong size/);
  });
});

describe("the catalog renderer", () => {
  it("draws one ring per row at its base pixel, in picture coordinates, ringed in the overlay colour", () => {
    const plain = ROWS.map((v, i) => (i % 6 === 2 || i % 6 === 3 ? NaN : v)); // the worker sends no angle or length when vectors are off
    const marks = markersOf(scene(), { stars: ready(geo(headOf(), plain)) });
    const dots = marks.filter((m): m is Marker => m.type === "marker");
    expect(dots.map((m) => [m.row, m.x, m.y])).toEqual([[3, 10.5, H - 0.5 - 5], [4, 20.5, H - 0.5 - 15], [9, 30.5, H - 0.5 - 25]]);
    expect(dots.every((m) => m.ring === "lime" && m.layer === "stars" && m.fill === "ink")).toBe(true);
    expect(marks.some((m) => m.type === "line")).toBe(false); // vectors are off: no segments
  });

  it("draws a headless segment through the marker, as long as the worker said and in the base pixel direction, for rows that have one", () => {
    const g = geo(headOf({ vectors: { quantity: "starlight_polarization", drawn: 2, no_angle: 1, no_length: 0, length: { method: "fixed" } } }));
    const lines = markersOf(scene({ ...cat, vectors: true }), { stars: ready(g) }).filter((m): m is Extract<Mark, { type: "line" }> => m.type === "line");
    expect(lines).toHaveLength(2); // the row with no angle has none
    const [up, right] = lines;
    // 90 degrees counter-clockwise from +x points up the picture (y decreases); length 8 base pixels, centred on the marker
    expect(up!.x1).toBeCloseTo(10.5, 6);
    expect(up!.x2).toBeCloseTo(10.5, 6);
    expect(up!.y1 - up!.y2).toBeCloseTo(8, 5);
    expect((up!.y1 + up!.y2) / 2).toBeCloseTo(H - 0.5 - 5, 6);
    expect(right!.y1).toBeCloseTo(right!.y2, 6);
    expect(right!.x2 - right!.x1).toBeCloseTo(6, 5);
  });

  it("colours by the column's value between the stated ends, and draws a row with no value hollow", () => {
    const head = headOf({ color: { column: "P(%)", unit: "%", min: 1, max: 3, unknown: 1, scale: "linear" } });
    const dots = markersOf(scene({ ...cat, colorColumn: "P(%)" }), { stars: ready(geo(head)) }).filter((m): m is Marker => m.type === "marker");
    expect(dots.map((m) => m.fill)).toEqual([valueColor(1, 1, 3), "none", valueColor(3, 1, 3)]);
    expect(valueColor(1, 1, 3)).not.toBe(valueColor(3, 1, 3));
    expect(valueColor(5, 1, 3)).toBe(valueColor(3, 1, 3)); // clipped to the ends, not extrapolated
    expect(valueColor(2, 2, 2)).toMatch(/^#[0-9a-f]{6}$/); // a constant column still has a colour
  });

  it("finds the marker under a click: nearest within the radius in screen pixels, none beyond it", () => {
    const marks = markersOf(scene(), { stars: ready(geo()) });
    const cell = 4; // screen pixels per image pixel
    expect(nearestMarker(marks, 10.5 + 1 / cell, H - 5.5, cell)?.row).toBe(3); // one screen pixel away
    expect(nearestMarker(marks, 20.5, H - 15.5 + 6 / cell, cell)?.row).toBe(4);
    expect(nearestMarker(marks, 15, 5, cell)).toBeNull();
    expect(nearestMarker(marks, 10.5 + 20 / cell, H - 5.5, cell)).toBeNull(); // 20 screen pixels away
    expect(nearestMarker([], 1, 1, cell)).toBeNull();
  });

  it("strokes rings, filled or hollow, on a figure canvas with a dark halo", () => {
    const calls: string[] = [];
    const ctx = new Proxy({} as Record<string, unknown>, {
      get: (t, k: string) => (k in t ? t[k] : (...a: unknown[]) => { calls.push(`${k}(${a.map((v) => (typeof v === "number" ? Math.round(v) : v)).join(",")})`); }),
      set: (t, k: string, v) => { t[k] = v; if (k === "fillStyle" || k === "strokeStyle") calls.push(`${k}=${String(v)}`); return true; },
    }) as unknown as CanvasRenderingContext2D;
    const dots: Mark[] = [
      { type: "marker", role: "catalog", layer: "s", row: 1, x: 5, y: 5, r: 4, ring: "lime", ringWidth: 1.5, fill: "ink" },
      { type: "marker", role: "catalog", layer: "s", row: 2, x: 9, y: 5, r: 4, ring: "lime", ringWidth: 1.5, fill: "none" },
    ];
    strokeMarks(ctx, dots, 2, 2, "#fff");
    expect(calls.filter((c) => c.startsWith("arc(")).length).toBe(4); // a halo and a ring each
    expect(calls.filter((c) => c === "fill()")).toHaveLength(1); // only the filled one
    expect(calls).toContain(`fillStyle=${OVERLAY_COLORS.lime}`);
  });
});

describe("what a catalog overlay says about itself", () => {
  it("counts every row once and points to the parse report for rows it could not place", () => {
    const head = headOf({
      filter: { rule: "P(%) / sP(%) >= 3", column: "P(%)", sigma_column: "sP(%)", min: 3, kept: 3, removed: 1, unknown: 1, note: "a row whose value or uncertainty is missing or unreadable does not pass" },
      vectors: { quantity: "starlight_polarization", drawn: 2, no_angle: 1, no_length: 0, length: { method: "every vector is 6% of the base's shorter side long" } },
      color: { column: "P(%)", unit: "%", min: 1, max: 3, unknown: 1, scale: "linear between the smallest and largest value drawn" },
      conversion: "angles east of north in Galactic, declared as starlight polarization, parallel to B; rotated into ICRS; then into the base's pixel frame with its WCS",
    });
    const o: SceneOverlay = { ...cat, vectors: true, colorColumn: "P(%)", filter: { column: "P(%)", sigmaColumn: "sP(%)", min: 3 } } as SceneOverlay;
    const text = overlayBasis(o, scene(o), ready(geo(head)), names)!;
    for (const part of ["3 of 10 rows drawn as markers", "2 orientation vectors (headless)", "starlight polarization, parallel to B", "1 row with no angle", "coloured by P(%): 1 to 3 %", "1 row with no value, drawn hollow",
      "row filter P(%) / sP(%) >= 3: 3 kept, 1 below, 1 unknown", "2 rows on the map not drawn", "2 rows outside the base map, counted and not drawn", "3 rows not drawn", "1 line of the file was not rows: see the catalog's parse report",
      "catalog frame Galactic (declared by you", "base frame ICRS (declared in the header)"]) {
      expect(text, part).toContain(part);
    }
    expect(text.startsWith("stars.tsv:")).toBe(true);
    expect(overlayTitle(o, scene(o), names)).toBe("Catalog of stars.tsv");
    expect(overlayBasis(cat, scene(), { key: "k", status: "refused", reason: "no columns are declared yet" }, names)).toBe("Not drawn: no columns are declared yet");
    expect(overlayBasis(cat, scene(), { key: "k", status: "loading" }, names)).toBeNull();
  });

  it("is asked of the worker, waited for by a figure, and kept in the figure's scene only when it was drawn", () => {
    const s = scene();
    expect(needsGeometry(cat, s)).toBe(true);
    expect(overlaysPending(s, {})).toEqual(["stars"]);
    const ctx = { plane, axial: false, scale: 4, bunit: null, interp: null, reliability: null };
    const drawn = figureScene(s, { ...ctx, geometry: { stars: ready(geo()) } });
    expect(drawn.overlays.map((o) => o.id)).toEqual(["regions", "stars"]);
    expect(figureScene(s, { ...ctx, geometry: { stars: { key: "k", status: "refused", reason: "no" } } }).overlays.map((o) => o.id)).toEqual(["regions"]);
    const hidden = sceneReducer(s, { type: "updateOverlay", id: "stars", patch: { visible: false } });
    expect(figureScene(hidden, { ...ctx, geometry: {} }).overlays.map((o) => o.id)).toEqual(["regions"]);
  });
});

describe("the catalog overlay form", () => {
  const col = (name: string, dtype = "float", scalar = true, unit: string | null = null) => ({ name, dtype, scalar, unit });
  const decl = (over: Record<string, unknown> = {}) => ({ lon: "RA", lat: "DEC", format: "degrees", frame: "icrs", equinox: null, angle: { column: "PA", unit: "deg", frame: "sky", quantity: "starlight_polarization", sigmaColumn: null }, values: [], declaredBy: "user", declaredAt: "2026-11-11T00:00:00Z", stale: false, ...over });
  const catalogDs = (id: number, over: Record<string, unknown> = {}) =>
    ({ id, name: `c${id}.tsv`, kind: "catalog", header: { kind: "catalog", format: "tsv", rows: 4, columns: [col("RA"), col("DEC"), col("PA"), col("P(%)", "float", true, "%"), col("sP(%)"), col("Star", "string"), col("arr", "other", false)] }, catalogDeclaration: decl(), interpretation: null, unitDeclaration: null, ...over }) as unknown as Dataset;
  const datasets = [catalogDs(7), catalogDs(8, { catalogDeclaration: null }), catalogDs(9, { catalogDeclaration: decl({ stale: true }) }), catalogDs(10, { catalogDeclaration: decl({ angle: { column: "PA", unit: "deg", frame: "sky", quantity: null, sigmaColumn: null } }) })];
  const draft = (over: Record<string, unknown> = {}) => ({ ...emptyDraft("catalog", 7), ...over });
  const make = (over: Record<string, unknown> = {}, id = 7) => draftToOverlay(draft({ datasetId: id, ...over }) as ReturnType<typeof emptyDraft>, "catalog-1", true, datasets, 1);

  it("offers only catalogs whose declaration is in force, and only numeric one-value columns", () => {
    expect(catalogCandidates(datasets).map((d) => d.id)).toEqual([7, 10]);
    expect(numericColumns(datasets[0] as never).map((c) => c.name)).toEqual(["RA", "DEC", "PA", "P(%)", "sP(%)"]);
  });

  it("builds an overlay with vectors, a length column and scale, a colour column and a filter, and takes them back for editing", () => {
    const o = make({ vectors: true, lengthColumn: "P(%)", lengthScale: "4", colorColumn: "sP(%)", filterColumn: "P(%)", filterSigma: "sP(%)", filterMin: "3", width: "2", dash: "dashed" });
    expect(o).toEqual({ kind: "catalog", id: "catalog-1", datasetId: 7, visible: true, color: "lime", width: 2, dash: "dashed", vectors: true, lengthColumn: "P(%)", lengthScale: 4, colorColumn: "sP(%)", filter: { column: "P(%)", sigmaColumn: "sP(%)", min: 3 } });
    expect(overlayToDraft(o as SceneOverlay)).toMatchObject({ kind: "catalog", datasetId: 7, vectors: true, lengthColumn: "P(%)", lengthScale: "4", colorColumn: "sP(%)", filterColumn: "P(%)", filterSigma: "sP(%)", filterMin: "3", dash: "dashed" });
    expect(make({})).toEqual({ kind: "catalog", id: "catalog-1", datasetId: 7, visible: true, color: "lime" }); // markers only: no vector or filter keys
  });

  it("refuses what the server would, next to the field", () => {
    expect(make({}, 8)).toMatch(/Choose a point catalog/); // no declaration
    expect(make({}, 9)).toMatch(/Choose a point catalog/); // out of date
    expect(make({ vectors: true }, 10)).toMatch(/Declare what the angle column PA is/);
    expect(make({ lengthColumn: "P(%)" })).toMatch(/needs vectors/);
    expect(make({ vectors: true, lengthColumn: "Star" })).toMatch(/not a numeric column/);
    expect(make({ colorColumn: "arr" })).toMatch(/not a numeric column/);
    expect(make({ vectors: true, lengthColumn: "P(%)", lengthScale: "0" })).toMatch(/positive number/);
    expect(make({ lengthScale: "3" })).toMatch(/needs vectors|positive number/);
    expect(make({ filterColumn: "P(%)", filterMin: "" })).toMatch(/minimum is a number/);
    expect(make({ filterSigma: "sP(%)" })).toMatch(/Choose the column/);
  });
});
