import { cellsOf, decodeMask, logScale, parseBins, parseEdges, scaleT, weightingText } from "./sigmav";
import { countPixels, positiveRange } from "./ClassMapView";
import { footerLines, wrap } from "./SigmavPlot";
import { diagram } from "./sigmavFixture";

describe("the log scale of the diagram", () => {
  it("spans the cells above zero and leaves a zero-weight cell off the scale", () => {
    const sc = logScale(cellsOf(diagram).values())!;
    expect(sc.lo).toBeCloseTo(Math.log10(12.5));
    expect(sc.hi).toBeCloseTo(Math.log10(1250));
    expect(sc.decades).toEqual([2, 3]);
    expect(scaleT(sc, 1250)).toBe(1);
    expect(scaleT(sc, 12.5)).toBe(0);
  });
  it("has no scale when no cell has weight above zero, and puts a single value in the middle", () => {
    expect(logScale([{ s: 0, v: 0, count: 2, weight: 0 }])).toBeNull();
    const one = logScale([{ s: 0, v: 0, count: 1, weight: 4 }])!;
    expect(scaleT(one, 4)).toBe(0.5);
  });
  it("states the weighting with its unit", () => {
    expect(weightingText(diagram)).toBe("weighted by integrated intensity (K km/s)");
  });
});

describe("typed bins and edges", () => {
  it("takes empty fields as the defaults and refuses a partial set", () => {
    expect(parseBins({ min: "", max: "", n: "" }, "x")).toEqual({ bins: null, error: null });
    expect(parseBins({ min: "0", max: "", n: "4" }, "x").error).toMatch(/all three/);
    expect(parseBins({ min: "5", max: "5", n: "4" }, "x").error).toMatch(/above min/);
    expect(parseBins({ min: "0", max: "5", n: "129" }, "x").error).toMatch(/1 to 128/);
    expect(parseBins({ min: "-3", max: "5", n: "8" }, "x").bins).toEqual({ min: -3, max: 5, n: 8 });
  });
  it("needs positive increasing edges", () => {
    expect(parseEdges("2.03").edges).toEqual([2.03]);
    expect(parseEdges("1.5, 4").edges).toEqual([1.5, 4]);
    expect(parseEdges("4 1.5").error).toMatch(/increasing/);
    expect(parseEdges("0").error).toMatch(/positive/);
    expect(parseEdges("").error).toMatch(/1 to 7/);
  });
});

describe("the pixel mask", () => {
  // made by the worker's pack_mask on rows [[0,1,0,0,1],[1,0,1,0,0]] (5 wide, 2 high, row 0 first)
  it("decodes the worker's packed bits in row-major order on a non-square mask", async () => {
    const bits = await decodeMask({ width: 5, height: 2, encoding: "packbits-msb-row-major+zlib+base64", data: "eJzzZQAAAJwATg==" });
    expect(Array.from(bits)).toEqual([0, 1, 0, 0, 1, 1, 0, 1, 0, 0]);
  });
  it("refuses an encoding it does not know and a mask shorter than its size", async () => {
    await expect(decodeMask({ width: 5, height: 2, encoding: "other" as never, data: "" })).rejects.toThrow(/Unknown mask encoding/);
    await expect(decodeMask({ width: 50, height: 2, encoding: "packbits-msb-row-major+zlib+base64", data: "eJzzZQAAAJwATg==" })).rejects.toThrow(/shorter/);
  });
});

describe("the class map pixels", () => {
  it("tells zero from NaN and ranges over the positive values only", () => {
    const d = Float32Array.from([NaN, 0, 10, 1000, NaN]);
    expect(countPixels(d)).toEqual({ positive: 2, zero: 1, nan: 2 });
    expect(positiveRange(d)).toEqual({ lo: 1, hi: 3 });
    expect(positiveRange(Float32Array.from([0, NaN]))).toBeNull();
  });
});

describe("the lines an exported picture states", () => {
  it("names bins, counts, what is left out, the fit and the cut, wrapped", () => {
    const text = footerLines(diagram, 2.03).join(" ");
    expect(text).toContain("6 of 10 components in the diagram");
    expect(text).toContain("3 outside its limits");
    expect(text).toContain("1 with σ not positive");
    expect(text).toContain("x.dat");
    expect(text).toContain("σ = 2.03 km/s");
    expect(footerLines(diagram, null).join(" ")).not.toContain("dashed line");
    expect(wrap("aaa bbb ccc", 7)).toEqual(["aaa bbb", "ccc"]);
  });
});
