import { describe, expect, it } from "vitest";
import { expandPreview, previewStep } from "./planes";
import type { Plane } from "./planes";

const head = { width: 2, height: 2, channel: 0, channels: 3, nan_meaning: "", stats: { finite_count: 3, nonfinite_count: 1, min: 1, max: 3, p01: 1, p99: 3, overflow_count: 0 }, spectral_value: null, spectral_unit: null, flagged: null, source_bitpix: -32, scaled: false };

describe("preview", () => {
  it("repeats each block over its pixels, keeps NaN as no value and keeps the preview mark", () => {
    const small: Plane = { head: { ...head, preview: { step: 2, source_width: 3, source_height: 3, method: "x" } }, data: new Float32Array([1, 2, 3, NaN]) };
    const big = expandPreview(small);
    expect(big.head.width).toBe(3);
    expect(big.head.height).toBe(3);
    expect(big.head.preview?.step).toBe(2);
    expect(Array.from(big.data.slice(0, 3))).toEqual([1, 1, 2]); // edge block is partial: the third column is block 1
    expect(Array.from(big.data.slice(6, 9)).map((v) => (Number.isNaN(v) ? "nan" : v))).toEqual([3, 3, "nan"]);
    expect(Number.isNaN(big.data[8])).toBe(true);
    expect(big.data.includes(0)).toBe(false); // an unknown block never becomes zero
  });

  it("returns a full-resolution plane unchanged", () => {
    const p: Plane = { head: { ...head, preview: null }, data: new Float32Array(4) };
    expect(expandPreview(p)).toBe(p);
  });

  it("chooses a reduction that leaves about a quarter of a megapixel and never less than 2", () => {
    expect(previewStep(512, 512)).toBe(2);
    expect(previewStep(3600, 3600)).toBe(8);
    expect(previewStep(100000, 100000)).toBe(64);
  });
});
