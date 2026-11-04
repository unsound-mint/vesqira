import { parseSpectrum } from "../api/planes";
import { fromDrag, nominalPixels, outline, sameRegion } from "./region";
import { axisDisplay, segments } from "./SpectrumPlot";

describe("regions", () => {
  it("builds a normalised rectangle from a drag in any direction", () => {
    expect(fromDrag("rect", { x: 9, y: 2 }, { x: 3, y: 7 })).toEqual({ shape: "rect", x0: 3, y0: 2, x1: 9, y1: 7 });
  });

  it("draws a circle from its centre with a whole-pixel radius", () => {
    expect(fromDrag("circle", { x: 5, y: 5 }, { x: 8, y: 9 })).toEqual({ shape: "circle", cx: 5, cy: 5, r: 5 });
  });

  it("counts pixels by the pixel-centre rule the worker uses", () => {
    expect(nominalPixels({ shape: "rect", x0: 0, y0: 0, x1: 5, y1: 5 })).toBe(36);
    expect(nominalPixels({ shape: "circle", cx: 0, cy: 0, r: 0 })).toBe(1);
    expect(nominalPixels({ shape: "circle", cx: 0, cy: 0, r: 3 })).toBe(29);
  });

  it("places the outline with y measured from the top of the picture", () => {
    expect(outline({ shape: "rect", x0: 2, y0: 0, x1: 4, y1: 1 }, 24)).toEqual({ kind: "rect", x: 2, y: 22, w: 3, h: 2 });
    expect(sameRegion(null, null)).toBe(true);
  });
});

describe("spectrum", () => {
  it("breaks the line at channels with no value instead of drawing zero", () => {
    expect(segments([0, 1, 2, 3, 4], [1, 2, NaN, 3, 4])).toEqual([["0.00,1.00", "1.00,2.00"], ["3.00,3.00", "4.00,4.00"]]);
    expect(segments([0, 1], [NaN, NaN])).toEqual([]);
  });

  it("shows velocity in km/s and frequency in MHz, and leaves other units alone", () => {
    expect(axisDisplay("m/s")).toEqual({ factor: 1e-3, label: "km/s" });
    expect(axisDisplay("Hz").label).toBe("MHz");
    expect(axisDisplay(null)).toEqual({ factor: 1, label: "" });
  });

  it("reads the binary frame: axis, mean with NaN, counts", () => {
    const head = JSON.stringify({ channels: 2 });
    const json = head + " ".repeat((8 - ((4 + head.length) % 8)) % 8);
    const buf = new ArrayBuffer(4 + json.length + 2 * 20);
    const dv = new DataView(buf);
    dv.setUint32(0, json.length, true);
    new Uint8Array(buf, 4, json.length).set(new TextEncoder().encode(json));
    const at = 4 + json.length;
    [1000, 2000].forEach((a, i) => dv.setFloat64(at + i * 8, a, true));
    dv.setFloat64(at + 16, 3.5, true);
    dv.setFloat64(at + 24, NaN, true);
    dv.setUint32(at + 32, 7, true);
    dv.setUint32(at + 36, 0, true);
    const sp = parseSpectrum(buf);
    expect(Array.from(sp.axis)).toEqual([1000, 2000]);
    expect(sp.mean[0]).toBe(3.5);
    expect(Number.isNaN(sp.mean[1])).toBe(true);
    expect(Array.from(sp.count)).toEqual([7, 0]);
    expect(() => parseSpectrum(buf.slice(0, buf.byteLength - 4))).toThrow();
  });
});
