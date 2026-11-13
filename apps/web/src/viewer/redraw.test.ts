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
const gridCalls = { plan: [] as unknown[], panels: [] as unknown[], composed: [] as { width?: number; columns: number; aspect: number; scale: { lo: number; hi: number }; unit: string | null; captions: string[]; panels: { label: string }[]; title: string }[] };
const gridPlan = vi.fn();
vi.mock("../api/channelmaps", async (orig) => ({
  ...(await orig<typeof import("../api/channelmaps")>()),
  getChannelGrid: vi.fn(async (_id: number, g: unknown) => { gridCalls.plan.push(g); return gridPlan(); }),
  getPanel: vi.fn(async (_id: number, p: unknown, step: number) => { gridCalls.panels.push([p, step]); return { head: { width: 4, height: 3 } as Plane["head"], data: new Float32Array(12).fill(1), slab: null }; }),
}));
vi.mock("./channelGrid", async (orig) => ({
  ...(await orig<typeof import("./channelGrid")>()),
  composeGridFigure: vi.fn(async (o: (typeof gridCalls.composed)[number]) => { gridCalls.composed.push(o); return new Blob(["png"]); }),
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

const { redrawSceneFigure, redrawGridFigure, baseRaster, downloadName } = await import("./redraw");
const { listDatasets } = await import("../api/catalog");

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
  gridCalls.plan.length = 0;
  gridCalls.panels.length = 0;
  gridCalls.composed.length = 0;
  vi.mocked(listDatasets).mockResolvedValue([base, other]);
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

describe("orientation lines and the reliability map they were drawn with", () => {
  const axialBase = { ...base, interpretation: { kind: "axial_orientation", unit: "deg", reliabilityId: 5, frame: "pixel", declaredBy: "user" } } as unknown as Dataset;
  const lines: SceneOverlay = { id: "orientation", kind: "orientation", datasetId: 1, visible: true, sampling: { mode: "step", step: 2 }, length: "fixed", opacity: "reliability" };
  function axialStored(): Scene {
    const live = sceneReducer(sceneReducer(newScene(1, DEFAULT_DISPLAY, true, true), { type: "setChannel", channel: 3 }), { type: "updateOverlay", id: "orientation", patch: { opacity: "reliability" } });
    return { ...figureScene(live, { plane, axial: true, scale: 85, bunit: null, interp: axialBase.interpretation, reliability: plane }), render };
  }
  it("are drawn again with the map the figure named, which the scene says", async () => {
    vi.mocked(listDatasets).mockResolvedValue([axialBase, other]);
    const s = axialStored();
    expect(s.overlays[0]).toMatchObject({ opacity: "reliability", reliabilityId: 5 });
    await expect(redrawSceneFigure(figure(s), { refresh: false })).resolves.toBeDefined();
  });
  it("stop, naming the dataset, when the declaration now names another reliability map", async () => {
    vi.mocked(listDatasets).mockResolvedValue([{ ...axialBase, interpretation: { ...axialBase.interpretation!, reliabilityId: 6 } } as Dataset, other]);
    await expect(redrawSceneFigure(figure(axialStored()), { refresh: false })).rejects.toThrow(/reliability map declared for cube\.fits is not the one the figure used any more/);
  });
  it("stop when the map was un-declared, and when it cannot be read, instead of quietly drawing uniform lines", async () => {
    const s = axialStored();
    const none = { ...axialBase, interpretation: { ...axialBase.interpretation!, reliabilityId: null } } as Dataset;
    vi.mocked(listDatasets).mockResolvedValue([none, other]);
    // the recorded map is gone from the declaration: the first check names it
    await expect(redrawSceneFigure(figure(s), { refresh: false })).rejects.toThrow(/not the one the figure used any more/);
    // a figure that used the length from reliability (no recorded map) and a declaration that has none now
    const lengthOnly = { ...s, overlays: [{ ...lines, opacity: "uniform", length: "reliability" } as SceneOverlay] };
    await expect(redrawSceneFigure(figure(lengthOnly as Scene), { refresh: false })).rejects.toThrow(/declares no reliability map now/);
    // and one whose map cannot be read
    vi.mocked(listDatasets).mockResolvedValue([axialBase, other]);
    const { getPlane } = await import("../api/planes");
    vi.mocked(getPlane).mockImplementation(async (id: number) => { if (id === 5) throw new ApiError("file not found", [], 404); return plane; });
    await expect(redrawSceneFigure(figure(s), { refresh: false })).rejects.toThrow(/reliability map .* cannot be read, and the figure's orientation lines were drawn with it: file not found/);
    vi.mocked(getPlane).mockImplementation(async () => plane);
  });
});

describe("drawing a channel-map grid again", () => {
  const long = "1st to 99th percentile of the finite pixels pooled over panels " + "1, 3, 5, 7, 9, 11, 13, 15, 17, 19, 21, 23, 25, 27, 29 ".repeat(5) + "(evenly spaced, at the preview resolution); one scale for every panel";
  const spec = {
    schema: "channel-grid/1", datasetId: 1, first: 0, last: 20, stride: 10, average: 1, columns: 2, panels: 3,
    display: { colormap: "inferno", stretch: "asinh", limits: [0.5, 8.25], limitsUnit: "K", source: long.slice(0, 190) },
    preview: { step: 1, panelWidth: 4, panelHeight: 3 },
    render: { figureWidth: 640, overlaysNotDrawn: 1, scaleSource: long },
  };
  const planned = {
    channels: 64, width: 4, height: 3, panel_width: 4, panel_height: 3, step: 1, panel_count: 3, pixels_per_panel: 12, pixels_total: 36,
    caps: { panels: 64, pixels_per_panel: 1048576, pixels_total: 16777216 },
    panels: [0, 10, 20].map((c, i) => ({ index: i, first: c, last: c, channels: 1, mean: false, spectral: null, flagged: null })),
    preview: null, averaging: null, scale: { lo: 0.5, hi: 8.25, source: "custom limits", sampled_panels: [] },
    spectral: { axis: "channel", ctype: null, unit: null, unit_status: null, native_unit: null, convention: null, convention_source: null, specsys: null, rest: null, basis: null, refused: "the file has no spectral axis", reference_frame_note: "x" },
    nan_meaning: "no value",
  };
  const gridFigure = (): Figure => ({ id: 11, datasetId: 1, title: "Channel maps", caption: "", scene: spec, provenance: {} }) as unknown as Figure;

  it("keeps the recorded scale, its whole sentence, the columns and the width, and saves a linked revision with the same description", async () => {
    gridPlan.mockResolvedValue(planned);
    const r = await redrawGridFigure(gridFigure());
    expect(gridCalls.plan[0]).toMatchObject({ first: 0, last: 20, stride: 10, average: 1, unit: "", limits: [0.5, 8.25] }); // the recorded shared scale is asked for, never a new one
    expect(gridCalls.panels).toHaveLength(3);
    const c = gridCalls.composed.at(-1)!;
    expect(c).toMatchObject({ width: 640, columns: 2, scale: { lo: 0.5, hi: 8.25 }, unit: "K", title: "Channel maps" });
    expect(c.aspect).toBeCloseTo(4 / 3);
    expect(c.panels.map((p) => p.label)).toEqual(["Channel 1", "Channel 11", "Channel 21"]);
    expect(c.captions.join("\n")).toContain(long); // the whole sentence, not the 190 characters the description keeps
    expect(c.captions.join("\n")).toContain("The map's 1 overlay is not drawn on the panels");
    expect(r.svg).toBeNull();
    expect(r.notes).toEqual([]);
    await r.save();
    const sent = calls.created.at(-1)!;
    expect(sent).toMatchObject({ datasetId: 1, title: "Channel maps", redrawnFrom: 11 });
    expect(sent.channelGrid).toMatchObject({ schema: "channel-grid/1", first: 0, last: 20, columns: 2, display: { limits: [0.5, 8.25] }, render: { figureWidth: 640, overlaysNotDrawn: 1, scaleSource: long } });
  });

  it("says when the panels are now read at another preview step, and refuses a figure that does not record how it was drawn", async () => {
    gridPlan.mockResolvedValue({ ...planned, step: 2 });
    expect((await redrawGridFigure(gridFigure())).notes[0]).toContain("preview step 2; the figure recorded 1");
    await expect(redrawGridFigure({ ...gridFigure(), scene: { ...spec, render: undefined } } as unknown as Figure)).rejects.toThrow(/does not record how it was drawn/);
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
