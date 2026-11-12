import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Dataset } from "../api/catalog";
import type { Figure, FigureRequest } from "../api/boards";
import type { Plane, View } from "../api/planes";
import { DEFAULT_DISPLAY, newScene } from "../scene/scene";
import type { Scene } from "../scene/scene";
import { compassShapes, figureCompassOf } from "../ui/compassGeometry";
import type { CompassFacts } from "../ui/compassGeometry";
import { compassLayout, drawCompass } from "./figure";
import type { ImageFigure } from "./figure";
import { figureSvg } from "./figureSvg";
import type { Viewer } from "./useViewer";

const PNG = "data:image/png;base64,iVBORw0KGgo=";
const sent: FigureRequest[] = [];
const composed: { captions: string[]; compass?: unknown }[] = [];
vi.mock("../api/boards", async (orig) => ({ ...(await orig<typeof import("../api/boards")>()), createFigure: vi.fn(async (r: FigureRequest) => { sent.push(r); return { id: 1 }; }) }));
vi.mock("./figure", async (orig) => ({
  ...(await orig<typeof import("./figure")>()),
  composeImageFigure: vi.fn(async (o: (typeof composed)[number]) => { composed.push(o); return new Blob(["png"]); }),
  blobToBase64: async () => "cG5n",
}));
const { saveViewFigure } = await import("./saveFigure");

const measure = (t: string) => t.length * 6;
const lut = new Uint8ClampedArray(256 * 3);
const base: ImageFigure = { planeW: 40, planeH: 30, layers: [], legend: { kind: "scalar", lut, limits: [0, 1], unit: "K" }, title: "Peak", captions: ["cube.fits"] };
const meta = { title: "Peak", caption: "", provenance: {}, software: "t" };

// north 30 degrees counter-clockwise from "up" and east 90 further on (CROTA2 = 30, the usual handedness: east on the left)
const rotated: CompassFacts = { available: true, x: 19.5, y: 14.5, north_deg: 120, east_deg: 210, frame: "icrs" };
// a mirrored WCS: east points right
const flipped: CompassFacts = { available: true, x: 19.5, y: 14.5, north_deg: 90, east_deg: 0, frame: "icrs" };
const noWcs: CompassFacts = { available: false, reason: "this file has no complete celestial WCS, so an angle cannot be related to the sky" };
const angle = (p: { x: number; y: number }) => ((Math.atan2(-p.y, p.x) * 180) / Math.PI + 360) % 360;

describe("the arrow geometry the viewer and the exports share", () => {
  it("turns with the WCS: each arrow lies at the angle the worker computed", () => {
    const s = compassShapes({ north_deg: 120, east_deg: 210 }, 30);
    expect(angle(s.north.tip)).toBeCloseTo(120, 6);
    expect(angle(s.east.tip)).toBeCloseTo(210, 6);
    expect(s.north.label).toBe("N");
    expect(s.east.label).toBe("E");
  });

  it("puts east on the right of a mirrored WCS, and north is never assumed up", () => {
    const s = compassShapes({ north_deg: 90, east_deg: 0 }, 30);
    expect(s.east.tip.x).toBeCloseTo(30, 9);
    expect(s.east.tip.y).toBeCloseTo(0, 9);
    expect(s.north.tip.x).toBeCloseTo(0, 9);
    expect(s.north.tip.y).toBeCloseTo(-30, 9); // screen y runs down
    expect(compassShapes({ north_deg: 0, east_deg: 90 }, 30).north.tip.x).toBeCloseTo(30, 9); // north to the right
  });

  it("has a head whose tip is the arrow's tip and a shaft that stops where the head starts", () => {
    const a = compassShapes({ north_deg: 45, east_deg: 135 }, 30, 7).north;
    expect(a.head[0]).toEqual(a.tip);
    const mid = { x: (a.head[1].x + a.head[2].x) / 2, y: (a.head[1].y + a.head[2].y) / 2 };
    expect(mid.x).toBeCloseTo(a.shaftEnd.x, 9);
    expect(mid.y).toBeCloseTo(a.shaftEnd.y, 9);
  });

  it("records arrows only from a usable compass: no celestial WCS, or an undefined one, gives none", () => {
    expect(figureCompassOf(rotated)).toEqual({ x: 19.5, y: 14.5, northDeg: 120, eastDeg: 210, frame: "icrs" });
    expect(figureCompassOf({ ...rotated, corrected: ["PC1_2", "PC2_1"] })!.corrected).toEqual(["PC1_2", "PC2_1"]);
    expect(figureCompassOf(noWcs)).toBeUndefined();
    expect(figureCompassOf(undefined)).toBeUndefined();
    expect(figureCompassOf(null)).toBeUndefined();
    expect(figureCompassOf({ available: true } as CompassFacts)).toBeUndefined(); // no angles: nothing to draw
    expect(figureCompassOf({ ...rotated, north_deg: Number.NaN })).toBeUndefined();
    expect(figureCompassOf({ available: true, north_deg: 90, east_deg: 180 })).toBeUndefined(); // no reference pixel
  });

  it("stands as an inset in the top-right corner, not on the map centre where the source usually is", () => {
    const L = compassLayout({ x: 19.5, y: 14.5, northDeg: 90, eastDeg: 180 }, 40, 30, 400, 300);
    expect(L.at.x).toBeGreaterThan(300);
    expect(L.at.y).toBeLessThan(100);
    // the arrows and letters stay inside the picture whatever the angles
    for (const deg of [0, 45, 90, 135, 180, 225, 270, 315]) {
      const R = compassLayout({ x: 0, y: 0, northDeg: deg, eastDeg: (deg + 90) % 360 }, 40, 30, 400, 300);
      for (const p of [R.north.letter, R.east.letter, R.north.tip, R.east.tip]) {
        expect(R.at.x + p.x).toBeGreaterThan(0);
        expect(R.at.x + p.x).toBeLessThan(400);
        expect(R.at.y + p.y).toBeGreaterThan(0);
        expect(R.at.y + p.y).toBeLessThan(300);
      }
    }
    expect(compassLayout({ x: 0, y: 0, northDeg: 90, eastDeg: 180 }, 4, 3, 80, 60).at).toEqual({ x: 40, y: 30 }); // too small: centred
    expect(L.corrected).toBeNull();
    expect(compassLayout({ x: 19.5, y: 14.5, northDeg: 90, eastDeg: 180, corrected: ["PC1_2"] }, 40, 30, 400, 300).corrected).not.toBeNull();
  });
});

describe("the SVG of a figure with a compass", () => {
  const doc = (o: Partial<ImageFigure>) => {
    const d = new DOMParser().parseFromString(figureSvg({ ...base, ...o, raster: PNG, meta }, measure), "image/svg+xml");
    expect(d.querySelector("parsererror")).toBeNull();
    return d;
  };

  it("draws N and E at their true angles (read at the reference pixel), with letters as well as arrows", () => {
    const d = doc({ compass: figureCompassOf(rotated) });
    const g = d.querySelector("[data-compass]")!;
    expect(g.getAttribute("data-north-deg")).toBe("120.00");
    expect(g.getAttribute("data-east-deg")).toBe("210.00");
    expect(g.getAttribute("transform")).toBe("translate(624.75 55.25)"); // the top-right inset of a 40 x 30 plane drawn 680 x 510
    expect([...g.querySelectorAll("text")].map((t) => t.textContent)).toEqual(["N", "E"]);
    const shafts = [...g.querySelectorAll("[data-role='compass-shaft']")].map((l) => ({ x: Number(l.getAttribute("x2")), y: Number(l.getAttribute("y2")) }));
    expect(shafts).toHaveLength(2);
    expect(angle(shafts[0]!)).toBeCloseTo(120, 1); // coordinates are written to 3 decimals
    expect(angle(shafts[1]!)).toBeCloseTo(210, 1);
    expect(g.querySelectorAll("[data-halo]").length).toBe(4); // a halo under each shaft and head: legible on a bright or a dark plane
    expect(g.querySelector("title")!.textContent).toMatch(/North points 120° and east 210°/);
    expect(g.getAttribute("data-corrected")).toBeNull();
  });

  it("draws the handedness of a mirrored WCS: east to the right", () => {
    const g = doc({ compass: figureCompassOf(flipped) }).querySelector("[data-compass]")!;
    const east = g.querySelectorAll("[data-role='compass-shaft']")[1]!;
    expect(Number(east.getAttribute("x2"))).toBeGreaterThan(0);
    expect(Math.abs(Number(east.getAttribute("y2")))).toBeLessThan(1e-9);
  });

  it("marks corrected arrows in the picture, not only in a caption", () => {
    const g = doc({ compass: figureCompassOf({ ...rotated, corrected: ["PC1_2", "PC2_1"] }) }).querySelector("[data-compass]")!;
    expect(g.getAttribute("data-corrected")).toBe("true");
    expect([...g.querySelectorAll("text")].map((t) => t.textContent)).toEqual(["N", "E", "WCS corrected"]);
    expect(g.querySelector("title")!.textContent).toMatch(/corrected WCS/);
  });

  it("is self-contained pure vector in the figure's ink with a dark halo: no style sheet, no external reference", () => {
    const svg = figureSvg({ ...base, compass: figureCompassOf(rotated), raster: PNG, meta }, measure);
    const compass = svg.slice(svg.indexOf('data-layer="compass"'), svg.indexOf("</g></g>", svg.indexOf('data-layer="compass"')));
    expect(compass).not.toMatch(/<style|class=|var\(|href=|url\(/);
    expect(compass).toContain('fill="#e7ecf2"');
    expect(compass).toContain('stroke="#06080c"');
  });

  it("draws no compass, and no fake arrows, when there is none; and an old figure's SVG is byte for byte what it was", () => {
    const without = figureSvg({ ...base, raster: PNG, meta }, measure);
    expect(without).not.toMatch(/compass|WCS corrected/);
    expect(figureSvg({ ...base, compass: figureCompassOf(noWcs), raster: PNG, meta }, measure)).toBe(without);
    expect(figureSvg({ ...base, compass: figureCompassOf(undefined), raster: PNG, meta }, measure)).toBe(without);
    expect(doc({}).querySelector("[data-compass]")).toBeNull();
  });
});

describe("the PNG of a figure with a compass", () => {
  type Call = [string, ...unknown[]];
  const recorder = () => {
    const calls: Call[] = [];
    const ctx = new Proxy({}, {
      get: (_t, k: string) => (k === "canvas" ? {} : (...a: unknown[]) => { calls.push([k, ...a]); }),
      set: () => true,
    }) as unknown as CanvasRenderingContext2D;
    return { ctx, calls };
  };
  const g = { pad: 20, top: 46, iw: 680, ih: 510, planeW: 40, planeH: 30 };

  it("fills the letters N and E over a halo, at the shared letter positions", () => {
    const { ctx, calls } = recorder();
    drawCompass(ctx, figureCompassOf(rotated)!, g);
    const L = compassLayout(figureCompassOf(rotated)!, 40, 30, 680, 510);
    expect(calls.filter((c) => c[0] === "fillText").map((c) => [c[1], c[2], c[3]])).toEqual([["N", L.north.letter.x, L.north.letter.y], ["E", L.east.letter.x, L.east.letter.y]]);
    expect(calls.filter((c) => c[0] === "strokeText")).toHaveLength(2);
    expect(calls.find((c) => c[0] === "translate")!.slice(1)).toEqual([20 + L.at.x, 46 + L.at.y]);
    expect(calls.filter((c) => c[0] === "lineTo").some((c) => Math.abs((c[1] as number) - L.north.shaftEnd.x) < 1e-9)).toBe(true);
  });

  it("adds the corrected marker only when corrections applied", () => {
    const a = recorder();
    drawCompass(a.ctx, figureCompassOf({ ...rotated, corrected: ["PC1_2"] })!, g);
    expect(a.calls.filter((c) => c[0] === "fillText").map((c) => c[1])).toEqual(["N", "E", "WCS corrected"]);
  });
});

describe("saving a figure", () => {
  const dataset = { id: 3, name: "cube.fits", nature: "" } as Dataset;
  const plane: Plane = {
    head: { channel: 0, channels: 1, width: 10, height: 10, spectral_value: null, spectral_unit: null, preview: null, stats: { finite_count: 100, nonfinite_count: 0, min: 0, max: 9, p01: 1, p99: 8, overflow_count: 0 } } as Plane["head"],
    data: new Float32Array(100),
  };
  const viewer = (compass?: CompassFacts): Viewer => {
    const view = { channels: 1, bunit: "K", bunit_status: "header", spectral: null, flags: { declared: false, channels: [] }, ...(compass ? { compass } : {}) } as unknown as View;
    const scene = newScene(3, DEFAULT_DISPLAY, false, true);
    return { scene, view, plane, interp: null, reliability: null, spectrum: null, canvas: { current: { width: 30, height: 20 } as HTMLCanvasElement }, colormap: "viridis", stretch: "linear", range: "percentile", customLimits: null, region: null, showLines: true } as unknown as Viewer;
  };
  beforeEach(() => { sent.length = 0; composed.length = 0; });

  it("records the arrows in the scene and prints where they come from, by default when the viewer has them", async () => {
    await saveViewFigure(viewer({ ...rotated, corrected: ["PC1_2"] }), dataset, { title: "Peak" });
    expect((sent.at(-1)!.scene as Scene).compass).toEqual({ x: 19.5, y: 14.5, northDeg: 120, eastDeg: 210, frame: "icrs", corrected: ["PC1_2"] });
    expect(composed.at(-1)!.compass).toEqual((sent.at(-1)!.scene as Scene).compass);
    expect(composed.at(-1)!.captions.join(" ")).toContain("N and E: increasing latitude and longitude (icrs) at the map centre, from the file's WCS with your corrections (PC1_2): corrected.");
  });

  it("leaves them out, and out of the scene, when the researcher switched them off", async () => {
    await saveViewFigure(viewer(rotated), dataset, { title: "Peak", compass: false });
    expect((sent.at(-1)!.scene as Scene).compass).toBeUndefined();
    expect(composed.at(-1)!.compass).toBeUndefined();
    expect(composed.at(-1)!.captions.join(" ")).not.toContain("N and E:");
  });

  it("draws and records nothing without a celestial WCS or an undefined compass", async () => {
    for (const c of [noWcs, undefined]) {
      await saveViewFigure(viewer(c), dataset, { title: "Peak" });
      expect((sent.at(-1)!.scene as Scene).compass).toBeUndefined();
      expect(composed.at(-1)!.compass).toBeUndefined();
    }
  });
});

describe("drawing a figure again", () => {
  const dataset = { id: 3, name: "cube.fits", nature: "" } as Dataset;
  const plane = {
    head: { channel: 0, channels: 1, width: 10, height: 10, spectral_value: null, spectral_unit: null, preview: null, stats: { finite_count: 100, nonfinite_count: 0, min: 0, max: 9, p01: 1, p99: 8, overflow_count: 0 } },
    data: new Float32Array(100),
  } as unknown as Plane;
  let view: View;
  const stored = (compass?: Scene["compass"]): Scene => ({
    ...newScene(3, DEFAULT_DISPLAY, false, true),
    render: { canvas: { width: 20, height: 20 }, nodata: [[1, 2, 3], [4, 5, 6]], figureWidth: 720 },
    ...(compass ? { compass } : {}),
  } as Scene);
  const redraw = async (scene: Scene) => {
    vi.doMock("../api/catalog", async (orig) => ({ ...(await orig<typeof import("../api/catalog")>()), listDatasets: vi.fn(async () => [dataset]) }));
    vi.doMock("../api/planes", async (orig) => ({ ...(await orig<typeof import("../api/planes")>()), getView: vi.fn(async () => view), getPlane: vi.fn(async () => plane) }));
    vi.resetModules();
    class FakeImageData { constructor(public data: Uint8ClampedArray, public width: number, public height: number) {} }
    Object.assign(globalThis, { ImageData: FakeImageData });
    HTMLCanvasElement.prototype.getContext = function () { return { putImageData() {}, measureText: (t: string) => ({ width: t.length * 6 }) } as unknown as CanvasRenderingContext2D; } as never;
    HTMLCanvasElement.prototype.toDataURL = () => PNG;
    const { redrawSceneFigure } = await import("./redraw");
    const r = await redrawSceneFigure({ id: 5, datasetId: 3, title: "Peak", caption: "", scene, provenance: {} } as unknown as Figure, { refresh: false });
    await r.save();
    return { r, saved: sent.at(-1)!.scene as Scene, svg: r.svg!() };
  };
  beforeEach(() => {
    sent.length = 0;
    view = { channels: 1, bunit: "K", bunit_status: "header", spectral: null, flags: { declared: false, channels: [] }, compass: rotated } as unknown as View;
  });

  it("an old figure, saved before the option, is drawn again exactly as before: no arrows", async () => {
    const { r, saved, svg } = await redraw(stored());
    expect(saved.compass).toBeUndefined();
    expect(svg).not.toContain("data-compass");
    expect(r.notes).toEqual([]);
  });

  it("a figure that drew the arrows draws them again, from the WCS in force, and keeps the option in the scene", async () => {
    const { r, saved, svg } = await redraw(stored(figureCompassOf(rotated)));
    expect(saved.compass).toEqual(figureCompassOf(rotated));
    expect(svg).toContain('data-compass="true"');
    expect(r.notes).toEqual([]);
  });

  it("follows a WCS that was corrected since, and says so; the arrows are marked corrected", async () => {
    view = { ...view, compass: { ...rotated, north_deg: 100, east_deg: 190, corrected: ["CROTA2"] } } as View;
    const { r, saved, svg } = await redraw(stored(figureCompassOf(rotated)));
    expect(saved.compass).toMatchObject({ northDeg: 100, eastDeg: 190, corrected: ["CROTA2"] });
    expect(svg).toContain('data-corrected="true"');
    expect(r.notes.join(" ")).toMatch(/different WCS state/);
  });

  it("draws no arrows, and says why, when the WCS gives none now: never an invented orientation", async () => {
    view = { ...view, compass: noWcs } as View;
    const { r, saved, svg } = await redraw(stored(figureCompassOf(rotated)));
    expect(saved.compass).toBeUndefined();
    expect(svg).not.toContain("data-compass");
    expect(r.notes.join(" ")).toMatch(/arrows are not drawn: this file has no complete celestial WCS/);
  });
});
