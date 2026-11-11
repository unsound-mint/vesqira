import { render, screen } from "@testing-library/react";
import { Histogram } from "./Histogram";
import { nominalPixels, regionPixelCount } from "./region";
import { regionHistogram } from "./render";
import type { PlaneStats, RegionShape } from "../api/planes";

const W = 8, H = 6;
/** value = 1 + x + 10 y, so every pixel is identifiable; (3,2) is exactly 0 and (4,2) is NaN. */
const plane = () => {
  const d = new Float32Array(W * H);
  for (let y = 0; y < H; y++) for (let x = 0; x < W; x++) d[y * W + x] = 1 + x + 10 * y;
  d[2 * W + 3] = 0;
  d[2 * W + 4] = NaN;
  return d;
};
const total = (c: number[]) => c.reduce((a, b) => a + b, 0);
const run = (s: RegionShape, d = plane()) => regionHistogram(d, W, H, s, 0, 60, 6);

describe("region histogram", () => {
  it("counts a box: finite values binned, no-value pixels apart, zero a value", () => {
    const r = run({ shape: "rect", x0: 3, y0: 2, x1: 5, y1: 2 });  // values 0, NaN, 26
    expect(r.pixels).toBe(3);
    expect(r.nonfinite).toBe(1);
    expect(total(r.counts)).toBe(2);
    expect(r.counts).toEqual([1, 0, 1, 0, 0, 0]);                  // zero in [0, 10), 26 in [20, 30)
  });

  it("counts a circle by the pixel-centre rule", () => {
    const r = run({ shape: "circle", cx: 3, cy: 2, r: 1 });        // centre and its four neighbours
    expect(r.pixels).toBe(5);
    expect(r.nonfinite).toBe(1);                                   // (4,2) is NaN
    expect(total(r.counts)).toBe(4);
  });

  it("keeps only the part of a region inside the plane", () => {
    const r = run({ shape: "rect", x0: -3, y0: 4, x1: 1, y1: 9 });
    expect(r.pixels).toBe(2 * 2);                                  // x 0..1, y 4..5
    expect(total(r.counts)).toBe(4);
    expect(run({ shape: "rect", x0: 20, y0: 0, x1: 25, y1: 3 }).pixels).toBe(0);
    expect(run({ shape: "circle", cx: -10, cy: -10, r: 2 }).pixels).toBe(0);
    const edge = run({ shape: "circle", cx: 0, cy: 0, r: 2 });     // a quarter disc: 1 + 2 + 2 ... by dx^2+dy^2 <= 4
    expect(edge.pixels).toBe(6);
  });

  it("reports an all-NaN region as pixels without any counted value", () => {
    const d = new Float32Array(W * H).fill(NaN);
    const r = run({ shape: "rect", x0: 1, y0: 1, x1: 2, y1: 2 }, d);
    expect(r).toEqual({ counts: [0, 0, 0, 0, 0, 0], pixels: 4, nonfinite: 4 });
  });

  it("does not treat zero as missing in a region of zeros", () => {
    const d = new Float32Array(W * H);
    const r = run({ shape: "rect", x0: 0, y0: 0, x1: 1, y1: 1 }, d);
    expect(r).toMatchObject({ pixels: 4, nonfinite: 0 });
    expect(r.counts[0]).toBe(4);
  });

  it("counts the same pixels as the region panel's pixel count", () => {
    const shapes: RegionShape[] = [
      { shape: "rect", x0: 1, y0: 1, x1: 4, y1: 3 },
      { shape: "circle", cx: 4, cy: 3, r: 2 },
      { shape: "circle", cx: 3, cy: 2, r: 0 },
    ];
    for (const s of shapes) {
      const r = run(s);
      expect(r.pixels).toBe(nominalPixels(s));                     // what the panel shows for a region inside the image
      expect(r.pixels).toBe(regionPixelCount(s, W, H));
    }
    // large circle: the integer row spans match the brute-force definition
    const big: RegionShape = { shape: "circle", cx: 50, cy: 50, r: 37 };
    expect(regionPixelCount(big, 200, 200)).toBe(nominalPixels(big));
  });
});

describe("histogram label", () => {
  const stats = { min: 0, max: 60, p01: 1, p99: 59, finite_count: 46, nonfinite_count: 2, overflow_count: 0 } as unknown as PlaneStats;
  const scope = (preview = false) => ({ name: "core", shape: { shape: "rect", x0: 3, y0: 2, x1: 5, y1: 2 } as RegionShape, width: W, height: H, preview });

  it("says plane or region with its counts, and still drags limits", () => {
    const onChange = vi.fn();
    const { rerender } = render(<Histogram data={plane()} stats={stats} limits={[10, 50]} unit="K" onChange={onChange} />);
    expect(screen.getByTestId("histogram-scope")).toHaveTextContent("Plane");
    rerender(<Histogram data={plane()} stats={stats} limits={[10, 50]} unit="K" onChange={onChange} scope={scope()} />);
    expect(screen.getByTestId("histogram-scope")).toHaveTextContent("Region: core, 3 pixels, 1 not finite");
    expect(screen.getByRole("img", { name: /Histogram of 2 finite pixels in the region/ })).toBeInTheDocument();
  });

  it("marks a region counted on a preview, and an all-NaN region", () => {
    render(<Histogram data={plane()} stats={stats} limits={null} unit="K" onChange={() => undefined} scope={scope(true)} />);
    expect(screen.getByTestId("histogram-scope")).toHaveTextContent("(from the preview)");
    const nan = new Float32Array(W * H).fill(NaN);
    render(<Histogram data={nan} stats={stats} limits={null} unit="K" onChange={() => undefined} scope={scope()} />);
    expect(screen.getByText(/the region has no finite value/)).toBeInTheDocument();
  });
});
