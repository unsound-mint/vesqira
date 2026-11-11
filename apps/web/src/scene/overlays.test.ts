import { describe, expect, it } from "vitest";
import type { Plane } from "../api/planes";
import type { Dataset } from "../api/catalog";
import fixture from "../../../../packages/contracts/fixtures/scenes.json";
import { figureScene, sceneStatable } from "./figureScene";
import { sceneLayers, sceneMarks } from "./overlays";
import { newScene, DEFAULT_DISPLAY, sceneReducer } from "./scene";
import type { Scene } from "./scene";
import { placement, visibleWindow, dragCentre } from "./view";

const W = 8;
const H = 6;
const plane = (over: Partial<Plane["head"]> = {}): Plane => ({
  head: { channel: 0, channels: 1, width: W, height: H, spectral_value: null, spectral_unit: null, preview: null, stats: { finite_count: 48, nonfinite_count: 0, min: 0, max: 10, p01: 1, p99: 9, overflow_count: 0 }, ...over } as Plane["head"],
  data: new Float32Array(W * H).fill(30),
});
const interp = { kind: "axial_orientation", unit: "deg", reliabilityId: null } as unknown as Dataset["interpretation"];

function orientationScene(): Scene {
  let s = newScene(1, DEFAULT_DISPLAY, true, true);
  s = sceneReducer(s, { type: "updateOverlay", id: "regions", patch: { active: { shape: "rect", x0: 1, y0: 1, x1: 2, y1: 2 }, saved: [{ id: 4, shape: { shape: "circle", cx: 5, cy: 3, r: 1 } }] } });
  return s;
}

describe("the overlay registry", () => {
  it("draws the visible overlays in scene order, the first at the bottom", () => {
    const s = orientationScene();
    const ctx = { plane: plane(), scale: 16, interp, reliability: null, draft: { shape: "circle" as const, cx: 0, cy: 0, r: 1 } };
    expect(sceneLayers(s, ctx).map((l) => l.kind)).toEqual(["orientation", "regions"]);
    const marks = sceneMarks(s, ctx);
    const firstShape = marks.findIndex((m) => m.type !== "line");
    expect(marks.slice(0, firstShape).every((m) => m.type === "line")).toBe(true);
    expect(marks.slice(firstShape).map((m) => m.role)).toEqual(["saved", "region", "draft"]); // saved, then the working region, then the drag
    const moved = sceneReducer(s, { type: "moveOverlay", id: "regions", to: 0 });
    expect(sceneLayers(moved, ctx).map((l) => l.kind)).toEqual(["regions", "orientation"]);
    const hidden = sceneReducer(s, { type: "updateOverlay", id: "orientation", patch: { visible: false } });
    expect(sceneLayers(hidden, ctx).map((l) => l.kind)).toEqual(["regions"]);
  });

  it("draws nothing for an overlay whose geometry has not arrived (a catalog included)", () => {
    let s = orientationScene();
    s = sceneReducer(s, { type: "addOverlay", overlay: { id: "beam", kind: "beam", datasetId: 1, visible: true } });
    s = sceneReducer(s, { type: "addOverlay", overlay: { id: "cat", kind: "catalog", datasetId: 3, visible: true } });
    s = sceneReducer(s, { type: "updateOverlay", id: "orientation", patch: { datasetId: 2 } });
    const layers = sceneLayers(s, { plane: plane(), scale: 16, interp, reliability: null, geometry: { beam: { key: "k", status: "loading" } } });
    expect(layers.map((l) => l.kind)).toEqual(["orientation", "regions", "beam", "catalog"]);
    expect(layers[0]!.marks).toEqual([]);
    expect(layers[2]!.marks).toEqual([]);
    expect(layers[3]!.marks).toEqual([]);
    expect(sceneLayers(orientationScene(), { plane: plane(), scale: 16, interp: null, reliability: null })[0]!.marks).toEqual([]); // not declared axial: no lines
  });

  it("samples lines by the scene's step: about 16 output pixels apart on screen, or the step a figure recorded", () => {
    const lines = (s: Scene, scale: number) => sceneMarks(s, { plane: plane(), scale, interp, reliability: null }).filter((m) => m.type === "line").length;
    expect(lines(orientationScene(), 16)).toBe(W * H); // a 16 px cell: every pixel
    expect(lines(orientationScene(), 4)).toBe(2); // step 4 on an 8 x 6 grid: x 2 and 6, y 2
    const fixed = sceneReducer(orientationScene(), { type: "updateOverlay", id: "orientation", patch: { sampling: { mode: "step", step: 3 } } });
    expect(lines(fixed, 16)).toBe(3 * 2);
  });
});

describe("a figure's scene", () => {
  it("describes the picture: the drawn channel, twilight for an axial map, the line step used, the working region only, the whole plane", () => {
    const live = sceneReducer(sceneReducer(orientationScene(), { type: "setView", view: { zoom: 3, centre: { x: 2, y: 2 } } }), { type: "setChannel", channel: 0 });
    const s = figureScene(live, { plane: plane(), axial: true, scale: 85, bunit: null, interp, reliability: null });
    expect(s.base.display).toEqual({ mapping: "axial", colormap: "twilight", stretch: "linear", range: "minmax" });
    expect(s.overlays[0]).toMatchObject({ kind: "orientation", sampling: { mode: "step", step: 1 }, opacity: "uniform" }); // no reliability map drawn: say so
    expect(s.overlays[0]).not.toHaveProperty("reliabilityId");
    expect(s.overlays[1]).toMatchObject({ kind: "regions", saved: [], active: { shape: "rect", x0: 1, y0: 1, x1: 2, y1: 2 } });
    expect(s.view).toEqual({ zoom: 1 });
    // the same shape the server is tested to accept (packages/contracts/fixtures/scenes.json)
    const noRegion = sceneReducer(live, { type: "updateOverlay", id: "regions", patch: { active: null } });
    const withRel = { ...interp!, reliabilityId: 2 };
    const rel = plane();
    expect(figureScene(noRegion, { plane: plane(), axial: true, scale: 6, bunit: null, interp: withRel, reliability: rel })).toEqual(fixture.valid[1]!.scene); // 16 px apart at 6 px per pixel: step 3; opacity from map 2
    const otherGrid = { ...rel, head: { ...rel.head, width: 4 } };
    expect(figureScene(noRegion, { plane: plane(), axial: true, scale: 6, bunit: null, interp: withRel, reliability: otherGrid }).overlays[0]).toMatchObject({ opacity: "uniform" });
  });

  it("keeps how the pane was linked, so a figure says how its panes followed each other", () => {
    const link = { id: "compare", name: "H I and CO", basis: "wcs" as const, facets: { cursor: true, channel: true, view: false }, datasets: [1, 2] };
    const live = sceneReducer(newScene(1, DEFAULT_DISPLAY, false, true), { type: "setLink", link });
    expect(figureScene(live, { plane: plane(), axial: false, scale: 1, bunit: "K", interp: null, reliability: null }).link).toEqual(link);
    expect("link" in figureScene(newScene(1, DEFAULT_DISPLAY, false, true), { plane: plane(), axial: false, scale: 1, bunit: "K", interp: null, reliability: null })).toBe(false);
  });

  it("records custom limits with their unit, leaves computed limits to the data, and gives up on limits it cannot state", () => {
    const scalar = newScene(1, DEFAULT_DISPLAY, false, true);
    const custom = sceneReducer(scalar, { type: "setDisplay", display: { range: "custom", limits: [2, 5], scaleGroup: "compare" } });
    const p = plane({ channel: 2, channels: 4, spectral_value: 1.42e9, spectral_unit: "Hz" });
    expect(figureScene(custom, { plane: p, axial: false, scale: 1, bunit: "K", interp: null, reliability: null }).base).toEqual({
      datasetId: 1, channel: 2, spectral: { value: 1.42e9, unit: "Hz" },
      display: { mapping: "scalar", colormap: "viridis", stretch: "linear", range: "custom", limits: [2, 5], limitsUnit: "K", scaleGroup: "compare" },
    });
    const remembered = sceneReducer(custom, { type: "setDisplay", display: { range: "percentile" } });
    expect(figureScene(remembered, { plane: p, axial: false, scale: 1, bunit: "K", interp: null, reliability: null }).base.display).toEqual({ mapping: "scalar", colormap: "viridis", stretch: "linear", range: "percentile", scaleGroup: "compare" });
    const broken = sceneReducer(scalar, { type: "setDisplay", display: { range: "custom", limits: [5, 2] } });
    const b = figureScene(broken, { plane: p, axial: false, scale: 1, bunit: "K", interp: null, reliability: null });
    expect(sceneStatable(b)).toBe(false); // still drawn from, but not stored
    expect(sceneStatable(figureScene(custom, { plane: p, axial: false, scale: 1, bunit: "K", interp: null, reliability: null }))).toBe(true);
  });
});

describe("the view", () => {
  const image = { w: 64, h: 48 };
  const view = { zoom: 2, centre: { x: 30, y: 20 } };

  it("shows the same image window on any pane: the window of width/zoom by height/zoom around the centre, whole and as large as fits", () => {
    for (const pane of [{ w: 400, h: 300 }, { w: 1280, h: 800 }, { w: 390, h: 844 }, { w: 1000, h: 200 }]) {
      const win = visibleWindow(view, pane, image);
      const p = placement(view, pane, image);
      const half = { x: image.w / view.zoom / 2, y: image.h / view.zoom / 2 };
      const eps = 1e-9;
      // (the centre is at the pane's centre unless that would push the picture past its edge; the window is shown whole either way)
      expect(win.x0).toBeLessThanOrEqual(30 - half.x + eps);
      expect(win.x1).toBeGreaterThanOrEqual(30 + half.x - eps);
      expect(win.y0).toBeLessThanOrEqual(20 - half.y + eps);
      expect(win.y1).toBeGreaterThanOrEqual(20 + half.y - eps);
      const fills = Math.abs((2 * half.x) * p.scale - pane.w) < 1e-6 || Math.abs((2 * half.y) * p.scale - pane.h) < 1e-6;
      expect(fills, `${pane.w}x${pane.h}`).toBe(true);
    }
  });

  it("fitted shows the whole image centred, and a drag moves the centre by the dragged distance in image pixels", () => {
    const pane = { w: 640, h: 480 };
    expect(placement({ zoom: 1 }, pane, image)).toEqual({ width: 640, height: 480, scale: 10, offset: { x: 0, y: 0 } });
    const p = placement(view, pane, image); // scale 20
    const c = dragCentre(p.offset, 40, -20, view, pane, image)!;
    expect(c).toEqual({ x: 28, y: 19 }); // right by 2 image pixels shows what is left; up on screen shows what is lower
    expect(placement({ zoom: 2, centre: c }, pane, image).offset).toEqual({ x: p.offset.x + 40, y: p.offset.y - 20 });
  });
});
