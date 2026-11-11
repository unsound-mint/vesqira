import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Dataset } from "../api/catalog";
import type { Figure, FigureRequest } from "../api/boards";
import type { OverlayGeometry, OverlayGeometryHead } from "../api/overlays";
import type { Plane, View } from "../api/planes";
import { figureScene } from "../scene/figureScene";
import type { GeometryMap } from "../scene/geometry";
import { DEFAULT_DISPLAY, newScene, sceneReducer } from "../scene/scene";
import type { Scene, SceneOverlay } from "../scene/scene";
import { ApiError } from "../api/http";

const PNG = "data:image/png;base64,iVBORw0KGgo=";
const W = 8, H = 6;
const stats = { finite_count: 48, nonfinite_count: 0, min: 0, max: 10, p01: 1, p99: 9, overflow_count: 0 };
const plane: Plane = {
  head: { channel: 3, channels: 8, width: W, height: H, spectral_value: 8000, spectral_unit: "m/s", preview: null, stats } as unknown as Plane["head"],
  data: new Float32Array(W * H).map((_, i) => i % 11),
};
const view = { channels: 8, bunit: "K", bunit_status: "header", spectral: null, flags: { declared: false, channels: [] } } as unknown as View;
const base = { id: 1, name: "cube.fits", nature: "", interpretation: null } as unknown as Dataset;
const other = { id: 2, name: "co.fits", nature: "", interpretation: null } as unknown as Dataset;

const calls = { geometry: [] as { id: number; overlay: SceneOverlay; spacing: number | null }[], created: [] as FigureRequest[], composed: [] as { layers: unknown[]; captions: string[]; width?: number; source: { width: number; height: number } }[] };
vi.mock("../api/catalog", async (orig) => ({ ...(await orig<typeof import("../api/catalog")>()), listDatasets: vi.fn(async () => [base, other]) }));
vi.mock("../api/planes", async (orig) => ({ ...(await orig<typeof import("../api/planes")>()), getView: vi.fn(async () => view), getPlane: vi.fn(async () => plane) }));
vi.mock("../api/boards", async (orig) => ({ ...(await orig<typeof import("../api/boards")>()), createFigure: vi.fn(async (r: FigureRequest) => { calls.created.push(r); return { id: 99 }; }) }));
const geometryFor = vi.fn();
vi.mock("../api/overlays", async (orig) => ({
  ...(await orig<typeof import("../api/overlays")>()),
  getOverlayGeometry: vi.fn(async (id: number, overlay: SceneOverlay, spacing: number | null) => { calls.geometry.push({ id, overlay, spacing }); return geometryFor(overlay); }),
}));
vi.mock("./figure", async (orig) => ({
  ...(await orig<typeof import("./figure")>()),
  composeImageFigure: vi.fn(async (o: { layers: unknown[]; captions: string[]; width?: number; source: { width: number; height: number } }) => { calls.composed.push(o); return new Blob(["png"]); }),
  blobToBase64: async () => "cG5n",
}));
// jsdom has no 2D canvas: a stub that records what is put on it, enough for the base raster
const painted: { w: number; h: number; px: Uint8ClampedArray }[] = [];
class FakeImageData { constructor(public data: Uint8ClampedArray, public width: number, public height: number) {} }
Object.assign(globalThis, { ImageData: FakeImageData });
HTMLCanvasElement.prototype.getContext = function (this: HTMLCanvasElement) { return { putImageData: (d: FakeImageData) => painted.push({ w: d.width, h: d.height, px: d.data }), measureText: (t: string) => ({ width: t.length * 6 }) } as unknown as CanvasRenderingContext2D; } as never;
HTMLCanvasElement.prototype.toDataURL = () => PNG;

const { redrawSceneFigure, baseRaster, downloadName } = await import("./redraw");

const head = (sigma: number): OverlayGeometryHead => ({ kind: "contours", dtype: "<f4", coords: "base pixels", placement: { method: "placed by WCS", from_frame: "ICRS", to_frame: "ICRS", same_frame: true, same_dataset: false }, levels: [0.3, 0.5], unit: "K", unit_status: "header", sigma: { value: sigma, method: "MAD" }, channel: 2, spectral_value: null, spectral_unit: null, simplified: null, paths: 1, vertices: 3 }) as unknown as OverlayGeometryHead;
const contours = (sigma: number): OverlayGeometry => ({ kind: "contours", head: head(sigma), paths: [{ group: 0, xy: new Float32Array([1, 1, 4, 1, 4, 4]) }] });
const co: SceneOverlay = { id: "co", kind: "contours", datasetId: 2, visible: true, channel: 2, levels: { mode: "sigma", multiples: [3, 5], sigma: 0.1 }, unit: "K", color: "amber" };
const render = { canvas: { width: 16, height: 12 }, nodata: [[27, 34, 44], [85, 98, 117]], figureWidth: 720 };

/** The scene a save would have stored: the live scene projected onto the picture, plus how the raster was drawn. */
function storedScene(): Scene {
  let live = sceneReducer(newScene(1, DEFAULT_DISPLAY, false, true), { type: "setChannel", channel: 3 });
  live = sceneReducer(live, { type: "setDisplay", display: { colormap: "inferno", range: "custom", limits: [2, 6] } });
  live = sceneReducer(live, { type: "updateOverlay", id: "regions", patch: { active: { shape: "rect", x0: 1, y0: 1, x1: 3, y1: 2 }, saved: [{ id: 5, shape: { shape: "circle", cx: 4, cy: 3, r: 1 } }] } });
  live = sceneReducer(live, { type: "addOverlay", overlay: co });
  const geometry: GeometryMap = { co: { key: "k", status: "ready", g: contours(0.1) } };
  return { ...figureScene(live, { plane, axial: false, scale: 85, bunit: "K", interp: null, reliability: null, geometry }), render };
}
const figure = (scene: Scene = storedScene()): Figure => ({ id: 7, datasetId: 1, title: "Peak", caption: "The ridge.", scene, provenance: { source: { name: "cube.fits" }, statements: [], view: {} } } as unknown as Figure);

beforeEach(() => {
  calls.geometry.length = 0;
  calls.created.length = 0;
  calls.composed.length = 0;
  painted.length = 0;
  geometryFor.mockReset();
  geometryFor.mockImplementation(async () => contours(0.1));
});

describe("a figure scene projected again", () => {
  it("is the same scene: projecting what a save stored changes nothing (so a redrawing of unchanged data stores what was stored)", () => {
    const s = storedScene();
    const { render: _r, ...bare } = s;
    const again = figureScene(s, { plane, axial: false, scale: 85, bunit: "K", interp: null, reliability: null, geometry: { co: { key: "k", status: "ready", g: contours(0.1) } } });
    expect(again).toEqual(bare);
  });
});

describe("drawing a scene figure again", () => {
  it("draws the recorded scene at the recorded canvas size and width, asks the worker for the same overlay with the recorded sigma, and saves a linked revision with the same scene", async () => {
    const r = await redrawSceneFigure(figure(), { refresh: false });
    // the base raster: the CPU renderer at the recorded size, with the recorded no-value colours
    expect(painted.at(-1)).toMatchObject({ w: 16, h: 12 });
    expect(calls.composed.at(-1)).toMatchObject({ width: 720, source: { width: 16, height: 12 } });
    // the overlay request is the stored overlay, sigma included, at no spacing (a step was recorded or there is none)
    expect(calls.geometry).toEqual([{ id: 1, overlay: co, spacing: null }]);
    expect(calls.composed.at(-1)!.layers).toHaveLength(2); // the contours, then the working region, as scene order says
    expect(calls.composed.at(-1)!.captions.join("\n")).toContain("co.fits contours of channel 3 at 3, 5 × σ (σ = 0.1 K");
    expect(r.notes).toEqual([]);
    const saved = await r.save();
    expect(saved).toMatchObject({ id: 99 });
    const sent = calls.created.at(-1)!;
    expect(sent).toMatchObject({ datasetId: 1, title: "Peak", caption: "The ridge.", redrawnFrom: 7 });
    expect(sent.scene).toEqual(storedScene());
    expect(sent.view).toMatchObject({ kind: "plane", channel: 3, colormap: "inferno", range: "custom", limits: [2, 6], limitsUnit: "K", spectralValue: 8000, spectralUnit: "m/s", region: { shape: "rect", x0: 1, y0: 1, x1: 3, y1: 2 } });
  });

  it("exports an SVG whose raster is the base plane and whose vector layers are the same marks as the picture", async () => {
    const r = await redrawSceneFigure(figure(), { refresh: false });
    const doc = new DOMParser().parseFromString(r.svg!(), "image/svg+xml");
    expect(doc.querySelector("parsererror")).toBeNull();
    expect(doc.querySelector("image")?.getAttributeNS("http://www.w3.org/1999/xlink", "href")).toBe(PNG);
    expect(doc.querySelectorAll('[data-role="contour"]')).toHaveLength(1);
    expect(doc.querySelectorAll('[data-role="region"]')).toHaveLength(1);
    expect(doc.querySelector("metadata")?.textContent).toContain('"name":"cube.fits"');
    expect(r.svg!()).not.toMatch(/datasetId|"id":/);
  });

  it("measures the sigma again only once the owner agreed to draw from data that is not proven unchanged, and says what changed", async () => {
    geometryFor.mockImplementation(async (ov: SceneOverlay) => contours(ov.kind === "contours" && ov.levels.mode === "sigma" && ov.levels.sigma === undefined ? 0.25 : 0.1));
    const r = await redrawSceneFigure(figure(), { refresh: true });
    const asked = calls.geometry[0]!.overlay;
    expect(asked.kind === "contours" && asked.levels).toEqual({ mode: "sigma", multiples: [3, 5] }); // no recorded sigma: the worker measures
    expect(r.notes).toHaveLength(1);
    expect(r.notes[0]).toContain("σ) is now 0.25, the figure recorded 0.1");
    await r.save();
    const sent = calls.created.at(-1)!.scene!;
    const c = sent.overlays.find((o) => o.id === "co")!;
    expect(c.kind === "contours" && c.levels).toEqual({ mode: "sigma", multiples: [3, 5], sigma: 0.25 }); // the new figure states the sigma it drew with
  });

  it("stops, naming the layer, when an overlay the figure drew is refused now; it never leaves a layer out", async () => {
    geometryFor.mockRejectedValue(new ApiError("the recorded noise level does not match the data", [], 409));
    await expect(redrawSceneFigure(figure(), { refresh: false })).rejects.toThrow(/co\.fits cannot be drawn again: the recorded noise level does not match the data/);
    expect(calls.created).toEqual([]);
  });

  it("refuses a figure that does not record how it was drawn", async () => {
    const s = { ...storedScene(), render: undefined } as Scene;
    await expect(redrawSceneFigure(figure(s), { refresh: false })).rejects.toThrow(/does not record how it was drawn/);
  });
});

describe("the base raster", () => {
  it("is the viewer's CPU renderer at the recorded size: nearest-neighbour, the recorded hatching for a pixel with no value", () => {
    const p: Plane = { head: plane.head, data: new Float32Array(W * H).fill(5) };
    p.data[0] = NaN; // row 0 is the bottom row: it lands in the picture's last row
    baseRaster(p, storedScene(), null, { canvas: { width: 16, height: 12 }, nodata: [[1, 2, 3], [4, 5, 6]], figureWidth: 720 });
    const img = painted.at(-1)!;
    expect(img.w * img.h * 4).toBe(img.px.length);
    // the no-value pixel fills output pixels x 0-1, y 10-11 (row 0 is the bottom): three-pixel stripes of the two recorded colours
    const at = (x: number, y: number) => Array.from(img.px.slice((y * 16 + x) * 4, (y * 16 + x) * 4 + 3));
    expect(at(0, 11)).toEqual([4, 5, 6]);
    expect(at(1, 11)).toEqual([1, 2, 3]);
    expect(at(8, 0)).not.toEqual([1, 2, 3]); // a pixel with a value takes a colour of the scale
  });
});

describe("file names for downloads", () => {
  it("keep ASCII letters, digits, dash and underscore only", () => {
    expect(downloadName("Peak: ridge / ‘x’", "svg")).toBe("Peak-ridge-x.svg");
    expect(downloadName("   ", "svg")).toBe("figure.svg");
  });
});
