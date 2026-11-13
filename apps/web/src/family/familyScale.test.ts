import { clampCursor, displaySize, optionLabel, sharedRange, spreadRange, symmetricRange, valueText } from "./familyScale";

const st = (p01: number | null, p99: number | null) => ({ finite_count: 1, nonfinite_count: 0, min: p01, max: p99, p01, p99, overflow_count: 0 });

describe("family scales", () => {
  it("shares one range that covers every member", () => {
    expect(sharedRange([st(1, 5), st(-2, 3)])).toEqual([-2, 5]);
    expect(sharedRange([st(null, null)])).toBeNull();
  });
  it("never returns an empty range", () => {
    expect(sharedRange([st(2, 2)])).toEqual([1.5, 2.5]);
  });
  it("draws a signed difference symmetric about zero and a spread from zero", () => {
    expect(symmetricRange([st(-1, 4)])).toEqual([-4, 4]);
    expect(symmetricRange([st(0, 0)])).toEqual([-0.5, 0.5]);
    expect(spreadRange([st(0.1, 2), st(0, 3)])).toEqual([0, 3]);
  });
  it("reads a missing value as words, and zero as a value", () => {
    expect(valueText(undefined, "K")).toBe("no value");
    expect(valueText(NaN, "K")).toBe("no value");
    expect(valueText(0, "K")).toBe("0 K");
    expect(valueText(1.23456, "")).toBe("1.235");
  });
  it("keeps the cursor on the grid and the display under budget without blending", () => {
    expect(clampCursor({ x: -3, y: 99 }, 10, 20)).toEqual({ x: 0, y: 19 });
    expect(displaySize(100, 50)).toEqual({ outW: 100, outH: 50 });
    const d = displaySize(4000, 4000);
    expect(d.outW * d.outH).toBeLessThanOrEqual(1 << 20);
    expect(optionLabel("width", "5")).toBe("width = 5");
  });
});
