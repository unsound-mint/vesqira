import { bootstrapNote, niceLength } from "./RelativeOrientation";

describe("bootstrap interval wording", () => {
  it("explains an interval with no width instead of showing 1.000 to 1.000 bare", () => {
    expect(bootstrapNote([1, 1], 4)).toMatch(/No spread: all 2000 resamples of the 4 used sources gave the same value/);
    expect(bootstrapNote([1, 1], 1)).toMatch(/the 1 used source gave/);
  });
  it("says nothing when the interval has a width", () => {
    expect(bootstrapNote([0.3, 0.5], 40)).toBeNull();
  });
});

describe("scale bar length", () => {
  it("takes the longest 1, 2 or 5 times a power of ten that fits", () => {
    expect(niceLength(0.3 * 38)).toBe(10);
    expect(niceLength(4.9)).toBe(2);
    expect(niceLength(5)).toBe(5);
    expect(niceLength(0.07)).toBe(0.05);
    expect(niceLength(0)).toBe(0);
  });
});
