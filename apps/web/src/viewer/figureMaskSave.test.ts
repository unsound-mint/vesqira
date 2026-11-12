import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Dataset } from "../api/catalog";
import type { Figure, FigureRequest } from "../api/boards";
import type { Mask, MaskPlane } from "../api/masks";
import type { Plane, View } from "../api/planes";
import { DEFAULT_DISPLAY, newScene, sceneReducer } from "../scene/scene";
import type { Scene } from "../scene/scene";
import type { Viewer } from "./useViewer";

const PNG = "data:image/png;base64,iVBORw0KGgo=";
const sent: FigureRequest[] = [];
const composed: { masks?: { type: string; label: string; d: string }[]; captions: string[] }[] = [];
const mask = (over: Partial<Mask>): Mask => ({
  id: 7, datasetId: 3, type: "saturated", name: "Bright", width: 10, height: 10, channels: 0, flagged: 2, total: 100, tier: "declared", mappingVersion: "", method: "computed",
  rule: { rule: "value >= 100" }, meaning: "", author: "user", createdAt: "", sourceIdentity: "", fileSize: 1, sha256: "ab".repeat(32), stale: false, ...over,
});
let listed: Mask[] = [];
const planeOfMask = (flagged: number[]): MaskPlane => { const data = new Uint8Array(100); flagged.forEach((i) => { data[i] = 1; }); return { width: 10, height: 10, data }; };
vi.mock("../api/masks", async (orig) => ({
  ...(await orig<typeof import("../api/masks")>()),
  listMasks: vi.fn(async () => listed),
  getMaskPlane: vi.fn(async (id: number) => planeOfMask(id === 7 ? [11, 12] : [55])),
}));
vi.mock("../api/boards", async (orig) => ({ ...(await orig<typeof import("../api/boards")>()), createFigure: vi.fn(async (r: FigureRequest) => { sent.push(r); return { id: 1 }; }) }));
vi.mock("./figure", async (orig) => ({
  ...(await orig<typeof import("./figure")>()),
  composeImageFigure: vi.fn(async (o: (typeof composed)[number]) => { composed.push(o); return new Blob(["png"]); }),
  blobToBase64: async () => "cG5n",
}));
const { saveViewFigure } = await import("./saveFigure");
const { setMaskShown } = await import("./useMasks");

const dataset = { id: 3, name: "cube.fits", nature: "" } as Dataset;
const plane: Plane = {
  head: { channel: 4, channels: 8, width: 10, height: 10, spectral_value: 8000, spectral_unit: "m/s", preview: null, stats: { finite_count: 100, nonfinite_count: 0, min: 0, max: 9, p01: 1, p99: 8, overflow_count: 0 } } as Plane["head"],
  data: new Float32Array(100),
};
const view = { channels: 8, bunit: "K", bunit_status: "header", spectral: null, flags: { declared: false, channels: [] } } as unknown as View;
function viewer(): Viewer {
  const scene = sceneReducer(newScene(3, DEFAULT_DISPLAY, false, true), { type: "setChannel", channel: 4 });
  return { scene, view, plane, interp: null, reliability: null, spectrum: null, canvas: { current: { width: 30, height: 20 } as HTMLCanvasElement }, colormap: "viridis", stretch: "linear", range: "percentile", customLimits: null, region: null, showLines: true } as unknown as Viewer;
}

beforeEach(() => {
  sent.length = 0;
  composed.length = 0;
  listed = [mask({ id: 7 }), mask({ id: 8, type: "low_snr", name: "S/N < 3", tier: "verified" })];
  for (const id of [7, 8]) setMaskShown(3, id, false);
});

describe("a figure saved with masks shown", () => {
  it("draws the hatch with its legend and records the masks in the scene by type, name, tier and fingerprint", async () => {
    setMaskShown(3, 7, true);
    setMaskShown(3, 8, true);
    await saveViewFigure(viewer(), dataset, { title: "Peak" });
    const scene = sent.at(-1)!.scene as Scene;
    expect(scene.masks).toEqual([
      { id: 7, type: "saturated", name: "Bright", tier: "declared", fingerprint: `sha256:${"ab".repeat(32)}` },
      { id: 8, type: "low_snr", name: "S/N < 3", tier: "verified", fingerprint: `sha256:${"ab".repeat(32)}` },
    ]);
    const drawn = composed.at(-1)!;
    expect(drawn.masks!.map((m) => [m.type, m.label])).toEqual([
      ["saturated", "Saturated: Bright (declared; 2 pixels flagged)"],
      ["low_snr", "Low signal-to-noise: S/N < 3 (verified; 2 pixels flagged)"],
    ]);
    expect(drawn.masks![0]!.d).not.toBe("");
    expect(drawn.captions.join(" ")).toContain("no hatch does not mean a pixel is good");
  });

  it("records no masks, draws no legend and changes nothing when none is shown", async () => {
    await saveViewFigure(viewer(), dataset, { title: "Peak" });
    expect((sent.at(-1)!.scene as Scene).masks).toBeUndefined();
    expect(composed.at(-1)!.masks).toBeUndefined();
    expect(composed.at(-1)!.captions.join(" ")).not.toContain("Hatched areas");
  });

  it("leaves out a mask that was forgotten after it was shown", async () => {
    setMaskShown(3, 7, true);
    setMaskShown(3, 8, true);
    listed = [mask({ id: 8, type: "low_snr", name: "S/N < 3", tier: "verified" })];
    await saveViewFigure(viewer(), dataset, { title: "Peak" });
    expect((sent.at(-1)!.scene as Scene).masks!.map((m) => m.id)).toEqual([8]);
  });
});

describe("drawing a figure with masks again", () => {
  const stored = (): Scene => ({
    ...sceneReducer(newScene(3, DEFAULT_DISPLAY, false, true), { type: "setChannel", channel: 4 }),
    render: { canvas: { width: 20, height: 20 }, nodata: [[1, 2, 3], [4, 5, 6]], figureWidth: 720 },
    masks: [
      { id: 7, type: "saturated", name: "Bright", tier: "declared", fingerprint: `sha256:${"ab".repeat(32)}` },
      { id: 8, type: "low_snr", name: "S/N < 3", tier: "verified", fingerprint: `sha256:${"cd".repeat(32)}` },
      { id: 9, type: "user_excluded", name: "Knot", tier: "declared", fingerprint: `sha256:${"ef".repeat(32)}` },
    ],
  } as Scene);

  it("hatches the unchanged masks and says, instead of drawing, which one changed and which one was forgotten", async () => {
    vi.doMock("../api/catalog", async (orig) => ({ ...(await orig<typeof import("../api/catalog")>()), listDatasets: vi.fn(async () => [dataset]) }));
    vi.doMock("../api/planes", async (orig) => ({ ...(await orig<typeof import("../api/planes")>()), getView: vi.fn(async () => view), getPlane: vi.fn(async () => plane) }));
    vi.resetModules();
    class FakeImageData { constructor(public data: Uint8ClampedArray, public width: number, public height: number) {} }
    Object.assign(globalThis, { ImageData: FakeImageData });
    HTMLCanvasElement.prototype.getContext = function () { return { putImageData() {}, measureText: (t: string) => ({ width: t.length * 6 }) } as unknown as CanvasRenderingContext2D; } as never;
    HTMLCanvasElement.prototype.toDataURL = () => PNG;
    // 7 is as it was; 8 was made from other bytes (stale) and 9 was forgotten
    listed = [mask({ id: 7 }), mask({ id: 8, type: "low_snr", name: "S/N < 3", tier: "verified", sha256: "cd".repeat(32), stale: true })];
    const { redrawSceneFigure } = await import("./redraw");
    const r = await redrawSceneFigure({ id: 5, datasetId: 3, title: "Peak", caption: "", scene: stored(), provenance: {} } as unknown as Figure, { refresh: false });
    expect(r.notes.filter((n) => /mask/i.test(n)).sort()).toEqual([
      "Excluded by you mask “Knot” was forgotten; its hatch is not drawn.",
      "Low signal-to-noise mask “S/N < 3” is not the mask the figure was drawn with (its fingerprint or its data changed); its hatch is not drawn.",
    ]);
    await r.save();
    const newScene = sent.at(-1)!.scene as Scene;
    expect(newScene.masks!.map((m) => m.id)).toEqual([7]); // the new figure records what its picture shows
    expect(sent.at(-1)!.redrawnFrom).toBe(5);
  });
});
