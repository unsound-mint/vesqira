import { describe, expect, it, vi } from "vitest";
import type { Mask } from "../api/masks";
import { drawMasks, layoutImageFigure, MASK_ROW } from "./figure";
import type { FigureMask, ImageFigure } from "./figure";
import { figureMaskLayers, maskLegendLine, sceneMaskOf } from "./figureMasks";
import { figureSvg } from "./figureSvg";

const mask = (over: Partial<Mask> = {}): Mask => ({
  id: 7, datasetId: 1, hostId: 0, type: "saturated", name: "Bright", width: 4, height: 3, channels: 0, flagged: 2, total: 12, tier: "declared", mappingVersion: "", method: "computed",
  rule: { rule: "value >= 100" }, meaning: "", author: "user", createdAt: "", sourceIdentity: "", fileSize: 1, sha256: "ab".repeat(32), stale: false, ...over,
});
const planeOf = (flagged: number[], width = 4, height = 3) => {
  const data = new Uint8Array(width * height);
  flagged.forEach((i) => { data[i] = 1; });
  return { width, height, data };
};
const lut = new Uint8ClampedArray(256 * 3);
const PNG = "data:image/png;base64,iVBORw0KGgo=";
const measure = (t: string) => t.length * 6;
const base: ImageFigure = { planeW: 4, planeH: 3, layers: [], legend: { kind: "scalar", lut, limits: [0, 1], unit: "K" }, title: "Peak", captions: ["cube.fits"] };

describe("masks on a figure", () => {
  it("draws the flagged pixels north up at the figure's scale and names each mask with its type, tier and pixels in words", () => {
    const [m] = figureMaskLayers([{ mask: mask(), plane: planeOf([1, 2]) }], 4, 3, 10);
    expect(m).toEqual({ type: "saturated", label: "Saturated: Bright (declared; 2 pixels flagged)", d: "M10 20h20v10h-20z" }); // row 0 is the bottom: pixels 1 and 2 of the bottom row
    expect(maskLegendLine(mask({ type: "user_excluded", name: "Knot", tier: "inferred", mappingVersion: "1", flagged: 1200 }))).toBe("Excluded by you: Knot (inferred (mapping 1); 1,200 pixels flagged)");
    expect(maskLegendLine(mask(), 4)).toContain("drawn in blocks of 4 pixels");
  });

  it("draws nothing for a mask that flags nothing: zero is not a flag, an empty plane is an empty hatch", () => {
    expect(figureMaskLayers([{ mask: mask({ flagged: 0 }), plane: planeOf([]) }], 4, 3, 10)[0]!.d).toBe("");
  });

  it("refuses a mask plane that is not the picture's pixel grid rather than drawing it out of register", () => {
    expect(() => figureMaskLayers([{ mask: mask(), plane: planeOf([], 5, 3) }], 4, 3, 10)).toThrow(/not on the picture's pixel grid/);
  });

  it("records a mask in the scene by type, name, tier and the SHA-256 of its file", () => {
    expect(sceneMaskOf(mask())).toEqual({ id: 7, type: "saturated", name: "Bright", tier: "declared", fingerprint: `sha256:${"ab".repeat(32)}` });
  });

  it("makes room for the legend: one row per mask below the plane, nothing changed without masks", () => {
    const none = layoutImageFigure(base, measure);
    const m: FigureMask = { type: "saturated", label: "Saturated: Bright", d: "" };
    const two = layoutImageFigure({ ...base, masks: [m, { ...m, type: "low_snr" }] }, measure);
    expect(two.H - none.H).toBe(2 * MASK_ROW + 6);
    expect(two.by - none.by).toBe(2 * MASK_ROW + 6);
    expect(two.maskTop).toBe(none.top + none.ih + 12);
    expect(layoutImageFigure({ ...base, masks: [] }, measure)).toEqual({ ...none });
  });
});

describe("the SVG of a figure with masks", () => {
  const masks: FigureMask[] = [
    { type: "saturated", label: "Saturated: Bright & <bold> (declared; 2 pixels flagged)", d: "M10 20h20v10h-20z" },
    { type: "low_snr", label: "Low signal-to-noise: S/N < 3 (verified; 5 pixels flagged)", d: "M0 0h10v10h-10z" },
  ];
  const doc = () => {
    const d = new DOMParser().parseFromString(figureSvg({ ...base, masks, raster: PNG, meta: { title: "Peak", caption: "", provenance: {}, software: "t" } }, measure), "image/svg+xml");
    expect(d.querySelector("parsererror")).toBeNull();
    return d;
  };

  it("hatches each mask with the pattern of its type and lists it in a legend, in words", () => {
    const d = doc();
    expect([...d.querySelectorAll("pattern")].map((p) => p.id)).toEqual(["hatch-saturated", "hatch-low_snr"]);
    expect([...d.querySelectorAll("[data-mask-type]")].map((p) => [p.getAttribute("data-mask-type"), p.getAttribute("fill")])).toEqual([["saturated", "url(#hatch-saturated)"], ["low_snr", "url(#hatch-low_snr)"]]);
    const legend = [...d.querySelectorAll("[data-mask-legend]")];
    expect(legend.map((g) => g.textContent)).toEqual([masks[0]!.label, masks[1]!.label]);
    expect(legend[0]!.querySelector("rect")!.getAttribute("fill")).toBe("url(#hatch-saturated)");
    expect(d.querySelectorAll("pattern path").length).toBe(2); // two different tiles: the two types are told apart without colour
    expect(new Set([...d.querySelectorAll("pattern path")].map((p) => p.getAttribute("d"))).size).toBe(2);
  });

  it("writes no id, path or fingerprint of a mask", () => {
    const svg = figureSvg({ ...base, masks, raster: PNG, meta: { title: "Peak", caption: "", provenance: {}, software: "t" } }, measure);
    expect(svg).not.toMatch(/sha256|"id"|datasetId/);
  });

  it("is unchanged when there are no masks", () => {
    const svg = figureSvg({ ...base, raster: PNG, meta: { title: "Peak", caption: "", provenance: {}, software: "t" } }, measure);
    expect(svg).not.toMatch(/hatch-|data-mask/);
  });
});

describe("the PNG's hatch", () => {
  it("fills each mask's runs with a pattern of its type inside the plane, then draws the legend rows with the words", () => {
    class P2D { constructor(public d: string) {} }
    vi.stubGlobal("Path2D", P2D);
    const tileCalls: string[] = [];
    const proto = HTMLCanvasElement.prototype;
    const was = proto.getContext;
    proto.getContext = function () { return { strokeStyle: "", fillStyle: "", lineWidth: 0, beginPath() {}, arc() { tileCalls.push("dots"); }, fill() {}, stroke(p: P2D) { tileCalls.push(p.d); } } as unknown as CanvasRenderingContext2D; } as never;
    const log: string[] = [];
    const ctx = {
      save: () => log.push("save"), restore: () => log.push("restore"), beginPath() {}, rect: (...a: number[]) => log.push(`clip ${a.join(",")}`), clip() {}, translate: (...a: number[]) => log.push(`translate ${a.join(",")}`),
      createPattern: (_t: unknown) => ({ id: log.length }) as unknown as CanvasPattern, fill: (p: P2D) => log.push(`fill ${p.d}`), fillRect: (...a: number[]) => log.push(`swatch ${a.join(",")}`),
      strokeRect: () => {}, fillText: (t: string, x: number, y: number) => log.push(`text ${t} @${x},${y}`), set fillStyle(_v: unknown) {}, set strokeStyle(_v: unknown) {}, set font(_v: string) {},
    } as unknown as CanvasRenderingContext2D;
    try {
      drawMasks(ctx, [{ type: "saturated", label: "Saturated: Bright", d: "M0 0h4v4z" }, { type: "missing", label: "No value: x", d: "M5 5h4v4z" }], { pad: 20, top: 46, iw: 680, ih: 510, maskTop: 560 });
      expect(log).toContain("clip 20,46,680,510");
      expect(log).toContain("translate 20,46");
      expect(log).toContain("fill M0 0h4v4z");
      expect(log).toContain("fill M5 5h4v4z");
      expect(log).toContain("swatch 20,560,18,12");
      expect(log).toContain("swatch 20,578,18,12");
      expect(log).toContain(`text Saturated: Bright @46,570`);
      expect(log).toContain(`text No value: x @46,588`);
      expect(tileCalls).toContain("M0 0L6 6"); // the saturated type's diagonal; the no-value type's dots
      expect(tileCalls).toContain("dots");
    } finally {
      proto.getContext = was;
      vi.unstubAllGlobals();
    }
  });
});
