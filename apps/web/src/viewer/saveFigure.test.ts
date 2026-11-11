import { describe, expect, it, vi } from "vitest";
import type { Dataset } from "../api/catalog";
import type { FigureRequest } from "../api/boards";
import type { Plane, View } from "../api/planes";
import { newScene, DEFAULT_DISPLAY, sceneReducer } from "../scene/scene";
import type { Mark } from "../scene/overlays";
import type { Viewer } from "./useViewer";

const sent: FigureRequest[] = [];
const drawn: Mark[][] = [];
vi.mock("../api/boards", () => ({ createFigure: vi.fn(async (r: FigureRequest) => { sent.push(r); return { id: 1 }; }) }));
vi.mock("./figure", async (orig) => ({
  ...(await orig<typeof import("./figure")>()),
  composeImageFigure: vi.fn(async (o: { layers: Mark[][] }) => { drawn.push(o.layers.flat()); return new Blob(["png"]); }),
  composeSpectrumFigure: vi.fn(async () => new Blob(["png"])),
  blobToBase64: async () => "cG5n",
}));
const { saveViewFigure } = await import("./saveFigure");

const dataset = { id: 3, name: "cube.fits", nature: "" } as Dataset;
const plane: Plane = {
  head: { channel: 4, channels: 8, width: 10, height: 10, spectral_value: 8000, spectral_unit: "m/s", preview: null, stats: { finite_count: 100, nonfinite_count: 0, min: 0, max: 9, p01: 1, p99: 8, overflow_count: 0 } } as Plane["head"],
  data: new Float32Array(100),
};
const view = { channels: 8, bunit: "K", bunit_status: "header", spectral: null, flags: { declared: false, channels: [] } } as unknown as View;

function viewer(): Viewer {
  let scene = newScene(3, DEFAULT_DISPLAY, false, true);
  scene = sceneReducer(scene, { type: "setChannel", channel: 4 });
  scene = sceneReducer(scene, { type: "setDisplay", display: { colormap: "inferno", range: "custom", limits: [2, 6] } });
  scene = sceneReducer(scene, { type: "updateOverlay", id: "regions", patch: { active: { shape: "rect", x0: 1, y0: 2, x1: 3, y1: 4 }, saved: [{ id: 9, shape: { shape: "circle", cx: 5, cy: 5, r: 2 } }] } });
  scene = sceneReducer(scene, { type: "setView", view: { zoom: 4, centre: { x: 2, y: 3 } } });
  const d = scene.base.display;
  return {
    scene, view, plane, interp: null, reliability: null, spectrum: null, canvas: { current: { width: 30, height: 20 } as HTMLCanvasElement },
    colormap: d.colormap, stretch: d.stretch, range: d.range, customLimits: [2, 6], region: { shape: "rect", x0: 1, y0: 2, x1: 3, y1: 4 }, showLines: true,
  } as unknown as Viewer;
}

describe("saving a figure", () => {
  it("stores the scene the picture was drawn from, consistent with the view it reports, and draws its overlays from that scene", async () => {
    await saveViewFigure(viewer(), dataset, { title: "Peak" });
    const r = sent.at(-1)!;
    expect(r.view).toMatchObject({ kind: "plane", channel: 4, spectralValue: 8000, spectralUnit: "m/s", colormap: "inferno", range: "custom", limits: [2, 6], limitsUnit: "K", region: { shape: "rect", x0: 1, y0: 2, x1: 3, y1: 4 } });
    expect(r.scene).toEqual({
      schema: "scene/1",
      base: { datasetId: 3, channel: 4, spectral: { value: 8000, unit: "m/s" }, display: { mapping: "scalar", colormap: "inferno", stretch: "linear", range: "custom", limits: [2, 6], limitsUnit: "K" } },
      overlays: [{ id: "regions", kind: "regions", datasetId: 3, visible: true, active: { shape: "rect", x0: 1, y0: 2, x1: 3, y1: 4 }, saved: [] }],
      view: { zoom: 1 },
      // how the base raster was drawn, so "draw again" can reproduce it (jsdom has no theme tokens: the fallback colours)
      render: { canvas: { width: 30, height: 20 }, nodata: [[27, 34, 44], [85, 98, 117]], figureWidth: 720 },
    });
    expect(drawn.at(-1)).toEqual([{ type: "rect", role: "region", x: 1, y: 5, w: 3, h: 3 }]); // only the working region, as the scene says
  });

  it("keeps the scene but not the render record when the canvas is too large to draw again", async () => {
    const v = viewer();
    await saveViewFigure({ ...v, canvas: { current: { width: 12000, height: 12000 } as HTMLCanvasElement } } as Viewer, dataset, { title: "Huge" });
    const r = sent.at(-1)!;
    expect(r.scene).toBeDefined();
    expect(r.scene).not.toHaveProperty("render");
  });

  it("still draws the working region when the scene cannot be stored (custom range without valid limits)", async () => {
    const v = viewer();
    const scene = { ...v.scene!, base: { ...v.scene!.base, display: { ...v.scene!.base.display, limits: [6, 2] } } };
    await saveViewFigure({ ...v, scene, customLimits: [6, 2] } as Viewer, dataset, { title: "Odd" });
    expect(sent.at(-1)!.scene).toBeUndefined();
    expect(drawn.at(-1)).toEqual([{ type: "rect", role: "region", x: 1, y: 5, w: 3, h: 3 }]);
  });

  it("stores no scene with a spectrum figure", async () => {
    const v = { ...viewer(), spectrum: { head: {}, axis: new Float64Array(), mean: new Float64Array(), count: new Uint32Array() } } as unknown as Viewer;
    await saveViewFigure(v, dataset, { kind: "spectrum" });
    expect(sent.at(-1)!.scene).toBeUndefined();
  });
});
