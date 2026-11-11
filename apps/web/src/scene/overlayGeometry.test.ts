import { describe, expect, it } from "vitest";
import type { Plane } from "../api/planes";
import type { Dataset } from "../api/catalog";
import { parseGeometry } from "../api/overlays";
import type { OverlayGeometry, OverlayGeometryHead } from "../api/overlays";
import tokens from "../../../../design/tokens.css?raw";
import { orientationLines } from "../viewer/glyphs";
import { overlayBasis } from "./basis";
import { figureScene, overlaysPending } from "./figureScene";
import { geometryKey, spacingFor, toPicture } from "./geometry";
import type { GeometryMap } from "./geometry";
import { draftToOverlay, emptyDraft, overlayToDraft } from "./overlayForm";
import { sceneLayers, sceneMarks } from "./overlays";
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
const placement = { method: "placed by WCS geometry (astropy): never resampled onto the base grid; converted from FK5 (J2000) to the base's ICRS frame", from_frame: "FK5 (J2000)", to_frame: "ICRS", same_frame: false, same_dataset: false };

/** A geometry frame as the API sends it: uint32 header length, JSON padded to 8 bytes, little-endian float32. */
function frame(head: Record<string, unknown>, nums: number[]): ArrayBuffer {
  let json = new TextEncoder().encode(JSON.stringify(head)); // bytes, not characters: labels carry ″ and °
  const pad = (8 - ((4 + json.length) % 8)) % 8;
  json = new Uint8Array([...json, ...new Array<number>(pad).fill(32)]);
  const buf = new ArrayBuffer(4 + json.length + 4 * nums.length);
  const dv = new DataView(buf);
  dv.setUint32(0, json.length, true);
  new Uint8Array(buf, 4, json.length).set(json);
  nums.forEach((v, i) => dv.setFloat32(4 + json.length + 4 * i, v, true));
  return buf;
}

const ready = (g: OverlayGeometry): GeometryMap[string] => ({ key: "k", status: "ready", g });
const contoursHead = (extra: Partial<OverlayGeometryHead> = {}) =>
  ({ kind: "contours", dtype: "<f4", coords: "base pixels", placement, levels: [-0.51, 0.51, 0.85, 1.7], unit: "K", unit_status: "declared", sigma: { value: 0.17, method: "MAD" }, channel: 89, spectral_value: 5200, spectral_unit: "m/s", simplified: null, paths: 2, vertices: 4, ...extra }) as OverlayGeometryHead;

function baseScene(): Scene {
  return newScene(1, DEFAULT_DISPLAY, false, true);
}
const co: SceneOverlay = { id: "co", kind: "contours", datasetId: 2, visible: true, channel: 89, levels: { mode: "sigma", multiples: [-3, 3, 5, 10] }, unit: "K", color: "amber", width: 1.5 };

describe("overlay geometry frames", () => {
  it("parse paths and segments, and refuse a frame of the wrong size", () => {
    const g = parseGeometry(frame(contoursHead(), [0, 2, 3, 2, 1, 2, 3, 4, 5, 6, 7, 8]));
    expect(g.kind).toBe("contours");
    if (g.kind === "orientation") throw new Error("kind");
    expect(g.paths.map((p) => [p.group, Array.from(p.xy)])).toEqual([[0, [1, 2, 3, 4]], [3, [5, 6, 7, 8]]]);
    const o = parseGeometry(frame({ kind: "orientation", dtype: "<f4", coords: "", placement, segments: 1 }, [1, 2, 90, 0.5]));
    expect(o.kind === "orientation" && Array.from(o.segments)).toEqual([1, 2, 90, 0.5]);
    expect(() => parseGeometry(frame(contoursHead(), [0, 2, 3, 2, 1, 2]))).toThrow(/wrong size/);
    expect(() => parseGeometry(frame({ kind: "orientation", dtype: "<f4", coords: "", placement, segments: 2 }, [1, 2, 90, 0.5]))).toThrow(/wrong size/);
  });
});

describe("base pixels to picture coordinates", () => {
  it("puts a base pixel where the base map's own lines put its centre (the half-pixel convention)", () => {
    expect(toPicture(0, 0, H)).toEqual([0.5, H - 0.5]);
    expect(toPicture(W - 1, H - 1, H)).toEqual([W - 0.5, 0.5]);
    // the same-dataset fast path centres a line on pixel (x, y) at (x + 0.5, h - 1 - y + 0.5)
    const data = new Float32Array(W * H).fill(30);
    const g = orientationLines(data, W, H, 1, { stride: 7 })[0]!;
    const [cx, cy] = toPicture(3, 3, H); // stride 7 samples from pixel 3
    expect([(g.x1 + g.x2) / 2, (g.y1 + g.y2) / 2]).toEqual([cx, cy]);
  });
});

describe("renderers of overlays placed by the worker", () => {
  it("contours: picture polylines in the overlay's style, negative levels dashed", () => {
    const g = parseGeometry(frame(contoursHead(), [0, 2, 1, 2, 10, 20, 11, 21, 12, 22, 13, 23]));
    const s = sceneReducer(baseScene(), { type: "addOverlay", overlay: co });
    const marks = sceneMarks(s, { plane, scale: 4, interp: null, reliability: null, geometry: { co: ready(g) } }).filter((m) => m.type === "path");
    expect(marks).toHaveLength(2);
    const [neg, pos] = marks as Extract<Mark, { type: "path" }>[];
    expect(neg!.points).toEqual([10.5, H - 20.5, 11.5, H - 21.5]);
    expect(neg!.stroke).toEqual({ color: "amber", width: 1.5, dashed: true });
    expect(pos!.stroke.dashed).toBe(false);
    expect(neg!.role).toBe("contour");
  });

  it("orientation of another map: base-pixel north is drawn straight up, length from the spacing and the reliability", () => {
    const head = { kind: "orientation", dtype: "<f4", coords: "", placement, segments: 3, step: 2, spacing_px: 5, reliability: { p99: 1 } } as OverlayGeometryHead;
    const g = parseGeometry(frame(head, [10, 10, 90, 1, 20, 10, 0, 0.5, 30, 10, 45, Number.NaN]));
    const ov: SceneOverlay = { id: "vgt", kind: "orientation", datasetId: 2, visible: true, sampling: { mode: "auto", gapPx: 16 }, length: "reliability", opacity: "reliability" };
    const s = sceneReducer(baseScene(), { type: "addOverlay", overlay: ov });
    const lines = sceneMarks(s, { plane, scale: 4, interp: null, reliability: null, geometry: { vgt: ready(g) } }) as Extract<Mark, { type: "line" }>[];
    expect(lines).toHaveLength(3);
    const [north, east, unknown] = lines;
    expect(north!.x1).toBeCloseTo(north!.x2, 9);
    expect(Math.abs(north!.y2 - north!.y1)).toBeCloseTo(0.8 * 5, 6); // full length: weight 1
    expect(north!.y2).toBeLessThan(north!.y1); // up in the picture
    expect(north!.opacity).toBe(1);
    expect(Math.abs(east!.x2 - east!.x1)).toBeCloseTo(0.8 * 5 * 0.5, 6);
    expect(east!.y1).toBeCloseTo(east!.y2, 9);
    expect(unknown!.opacity).toBe(0.2); // reliability unknown: faint and short, never dropped silently or drawn as certain
    expect(Math.hypot(unknown!.x2 - unknown!.x1, unknown!.y2 - unknown!.y1)).toBeCloseTo(0.8 * 5 * 0.2, 6);
    expect(north!.stroke?.color).toBe("white");
  });

  it("beam: a closed, filled ellipse with its size beside it; grid: lines and labels", () => {
    const beamHead = { kind: "beam", dtype: "<f4", coords: "", placement, paths: 1, vertices: 3, corner: "bottom-left", beam: { bmaj_arcsec: 108, bmin_arcsec: 72, bpa_deg: 45, source: "BMAJ, BMIN and BPA of the header" } } as OverlayGeometryHead;
    const b = parseGeometry(frame(beamHead, [0, 3, 2, 2, 4, 2, 3, 4]));
    const gridHead = { kind: "grid", dtype: "<f4", coords: "", placement, paths: 1, vertices: 2, frame: "ICRS", labels: [{ text: "10h00m", x: 5, y: 0, axis: "lon" }, { text: "+02°", x: 0, y: 7, axis: "lat" }] } as OverlayGeometryHead;
    const gr = parseGeometry(frame(gridHead, [0, 2, 5, 0, 5, 29]));
    let s = sceneReducer(baseScene(), { type: "addOverlay", overlay: { id: "beam", kind: "beam", datasetId: 1, visible: true } });
    s = sceneReducer(s, { type: "addOverlay", overlay: { id: "grid", kind: "grid", datasetId: 1, visible: true, frame: "icrs", dash: "dashed" } });
    const layers = sceneLayers(s, { plane, scale: 4, interp: null, reliability: null, geometry: { beam: ready(b), grid: ready(gr) } });
    const beam = layers.find((l) => l.id === "beam")!.marks;
    expect(beam[0]).toMatchObject({ type: "path", closed: true, fill: 0.35 });
    expect(beam[1]).toMatchObject({ type: "text", text: "108″ × 72″, PA 45°", baseline: "bottom" });
    const grid = layers.find((l) => l.id === "grid")!.marks;
    expect(grid[0]).toMatchObject({ type: "path", stroke: { dashed: true }, points: [5.5, H - 0.5, 5.5, H - 29.5] });
    expect(grid.slice(1)).toMatchObject([{ type: "text", text: "10h00m", anchor: "start", baseline: "bottom" }, { type: "text", text: "+02°", anchor: "start", baseline: "middle" }]);
  });
});

describe("what changes an overlay's geometry", () => {
  it("is its settings, the base and the spacing, not its colour or visibility", () => {
    const k = geometryKey(1, co, null);
    expect(geometryKey(1, { ...co, color: "cyan", width: 3, dash: "dashed", visible: false } as SceneOverlay, null)).toBe(k);
    expect(geometryKey(1, { ...co, channel: 3 } as SceneOverlay, null)).not.toBe(k);
    expect(geometryKey(2, co, null)).not.toBe(k);
  });

  it("spaces automatic orientation lines in base pixels, in steps of a square root of two", () => {
    const ov: SceneOverlay = { id: "o", kind: "orientation", datasetId: 2, visible: true, sampling: { mode: "auto", gapPx: 16 }, length: "fixed", opacity: "uniform" };
    expect(spacingFor(ov, 4)).toBe(4);
    expect(spacingFor(ov, 4.2)).toBe(spacingFor(ov, 4)); // a small zoom change asks for nothing new
    expect(spacingFor(ov, 2)).toBe(8);
    expect(spacingFor({ ...ov, sampling: { mode: "step", step: 3 } } as SceneOverlay, 4)).toBeNull();
    expect(spacingFor(co, 4)).toBeNull();
  });
});

describe("a figure of overlays from other datasets", () => {
  const datasets = [{ id: 2, name: "co.fits", interpretation: null }, { id: 4, name: "psi.fits", interpretation: { kind: "axial_orientation", unit: "deg", reliabilityId: 5, frame: "pixel", quantity: "b_field" } }] as unknown as Dataset[];
  const vgt: SceneOverlay = { id: "vgt", kind: "orientation", datasetId: 4, visible: true, sampling: { mode: "auto", gapPx: 16 }, length: "fixed", opacity: "reliability" };
  const beam: SceneOverlay = { id: "beam", kind: "beam", datasetId: 2, visible: true };
  const hidden: SceneOverlay = { id: "grid", kind: "grid", datasetId: 1, visible: false, frame: "galactic" };

  it("records the sigma and the step it drew with, leaves out a refused overlay and keeps a hidden one", () => {
    let s = baseScene();
    for (const o of [co, vgt, beam, hidden]) s = sceneReducer(s, { type: "addOverlay", overlay: o });
    const og = parseGeometry(frame({ kind: "orientation", dtype: "<f4", coords: "", placement, segments: 0, step: 6, spacing_px: 4.2, reliability: { p99: 1 } }, []));
    const geometry: GeometryMap = {
      co: ready(parseGeometry(frame(contoursHead({ paths: 0, vertices: 0 }), []))),
      vgt: ready(og),
      beam: { key: "k", status: "refused", reason: "the header of the overlay map has no BMAJ and BMIN" },
    };
    expect(overlaysPending(s, geometry)).toEqual([]);
    expect(overlaysPending(s, { ...geometry, co: { key: "k", status: "loading" } })).toEqual(["co"]);
    const fig = figureScene(s, { plane, axial: false, scale: 4, bunit: "K", interp: null, reliability: null, geometry, datasets });
    const byId = Object.fromEntries(fig.overlays.map((o) => [o.id, o]));
    expect(byId.co).toMatchObject({ levels: { mode: "sigma", multiples: [-3, 3, 5, 10], sigma: 0.17 } });
    expect(byId.vgt).toMatchObject({ sampling: { mode: "step", step: 6 }, opacity: "reliability", reliabilityId: 5 });
    expect(byId.beam).toBeUndefined();
    expect(byId.grid).toEqual(hidden);
  });

  it("states each overlay's basis, or the reason it is not drawn", () => {
    const s = sceneReducer(baseScene(), { type: "addOverlay", overlay: co });
    const g = parseGeometry(frame(contoursHead({ paths: 0, vertices: 0 }), []));
    const text = overlayBasis(co, s, ready(g), (id) => (id === 2 ? "co.fits" : "?"))!;
    expect(text).toContain("co.fits contours of channel 90 (5.2 km/s) at -3, 3, 5, 10 × σ (σ = 0.17 K, 1.4826 × MAD of that channel, signal included)");
    expect(text).toContain("converted from FK5 (J2000) to the base's ICRS frame");
    expect(text).toContain("negative levels dashed");
    expect(overlayBasis(co, s, { key: "k", status: "refused", reason: "the contoured map does not overlap the base map" }, () => "")).toBe("Not drawn: the contoured map does not overlap the base map");
    expect(overlayBasis(co, s, { key: "k", status: "loading" }, () => "")).toBeNull();
  });
});

describe("the overlay form", () => {
  const ds = (id: number, over: Record<string, unknown> = {}) => ({ id, name: `d${id}.fits`, header: { bunit: "K", axes: [{ length: 32 }, { length: 24 }, { length: 64 }] }, interpretation: null, unitDeclaration: null, ...over }) as unknown as Dataset;
  const datasets = [ds(1), ds(2), ds(3, { header: { bunit: null, axes: [{ length: 8 }, { length: 8 }] } }), ds(4, { interpretation: { kind: "axial_orientation", unit: "deg", reliabilityId: null, frame: "pixel", quantity: "b_field" } })];

  it("builds contours in the file's unit, and says what is wrong in a sentence", () => {
    const d = { ...emptyDraft("contours", 2), channel: "41" };
    expect(draftToOverlay(d, "contours-1", true, datasets, 1)).toEqual({ kind: "contours", id: "contours-1", datasetId: 2, visible: true, color: "amber", channel: 40, levels: { mode: "sigma", multiples: [3, 5, 10] }, unit: "K" });
    expect(draftToOverlay({ ...d, datasetId: 3, channel: "1", levelMode: "list", values: "1 2 4" }, "c", true, datasets, 1)).toMatchObject({ levels: { mode: "list", values: [1, 2, 4] } }); // no unit: none stated
    expect(draftToOverlay({ ...d, datasetId: 3, channel: "1", levelMode: "list", values: "1 2 4" }, "c", true, datasets, 1)).not.toHaveProperty("unit");
    expect(draftToOverlay({ ...d, channel: "65" }, "c", true, datasets, 1)).toMatch(/1 to 64/);
    expect(draftToOverlay({ ...d, values: "5, 3" }, "c", true, datasets, 1)).toMatch(/larger than the last/);
    expect(draftToOverlay({ ...d, levelMode: "linear", step: "0" }, "c", true, datasets, 1)).toMatch(/not zero/);
    expect(draftToOverlay({ ...d, levelMode: "geometric", factor: "1" }, "c", true, datasets, 1)).toMatch(/factor above 1/);
    expect(draftToOverlay({ ...emptyDraft("orientation", 4), length: "reliability" }, "o", true, datasets, 1)).toMatch(/reliability map/);
  });

  it("edits an overlay without losing a setting", () => {
    for (const o of [{ ...co, channel: 40 }, { id: "o", kind: "orientation", datasetId: 4, visible: true, sampling: { mode: "step", step: 3 }, length: "fixed", opacity: "uniform", color: "cyan" }, { id: "b", kind: "beam", datasetId: 2, visible: true, corner: "top-right", color: "white" }, { id: "g", kind: "grid", datasetId: 1, visible: false, frame: "galactic", color: "lime", dash: "dashed", width: 0.5 }] as SceneOverlay[]) {
      expect(draftToOverlay(overlayToDraft(o)!, o.id, o.visible, datasets, 1)).toEqual(o);
    }
  });
});

describe("the overlay palette", () => {
  it("is the --overlay-* tokens, so the screen and a figure draw the same colours", () => {
    for (const [name, hex] of Object.entries(OVERLAY_COLORS)) expect(tokens).toContain(`--overlay-${name}:${hex};`);
  });
});
