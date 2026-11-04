import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Histogram } from "./Histogram";
import { histogram, limits } from "./render";
import { captionLines } from "./figure";
import type { PlaneStats } from "../api/planes";

describe("histogram counts", () => {
  it("counts only finite values, puts the maximum in the last bin and treats zero as a value", () => {
    const data = new Float32Array([0, 0, 1, 2, 3, 4, NaN, Infinity, -Infinity]);
    const c = histogram(data, 0, 4, 4);
    expect(c).toEqual([2, 1, 1, 2]);        // 0,0 | 1 | 2 | 3,4
    expect(c.reduce((a, b) => a + b, 0)).toBe(6);
  });

  it("does not divide by zero when every value is the same", () => {
    expect(histogram(new Float32Array([5, 5, 5, NaN]), 5, 5, 3)).toEqual([3, 0, 0]);
  });
});

describe("custom limits", () => {
  const st = { min: -1, max: 9, p01: 0, p99: 8, finite_count: 6, nonfinite_count: 3, overflow_count: 0 } as unknown as PlaneStats;
  it("uses what the person set, and refuses an empty or inverted range", () => {
    expect(limits(st, "custom", [1, 5])).toEqual([1, 5]);
    expect(limits(st, "custom", [5, 5])).toBeNull();
    expect(limits(st, "custom", null)).toBeNull();
    expect(limits(st, "percentile", [1, 5])).toEqual([0, 8]);
  });

  it("says in the figure caption that the limits were set by the author", () => {
    const facts = { dataset: "c.fits", kind: "plane" as const, channelLine: "", displayLine: "", limits: [1, 5] as [number, number], limitsUnit: "K" };
    const lines = captionLines(facts, null, null, { colormap: "viridis", stretch: "linear", range: "custom", axial: false, region: null });
    expect(lines.join("\n")).toContain("limits set by the author of finite pixels in K (1 to 5)");
  });
});

describe("histogram control", () => {
  const stats = { min: 0, max: 10, p01: 1, p99: 9, finite_count: 5, nonfinite_count: 2, overflow_count: 0 } as unknown as PlaneStats;
  const data = new Float32Array([0, 2, 5, 9, 10, NaN, NaN]);

  it("describes itself, counts no-value pixels apart, and takes limits typed in", async () => {
    const onChange = vi.fn();
    render(<Histogram data={data} stats={stats} limits={[1, 9]} unit="K" onChange={onChange} />);
    expect(screen.getByRole("img", { name: /Histogram of 5 finite pixels.*display limits/ })).toBeInTheDocument();
    expect(screen.getByText(/2 pixels with no value are not counted/)).toBeInTheDocument();
    const lo = screen.getByLabelText("Lower limit");
    await userEvent.clear(lo);
    await userEvent.type(lo, "2{Enter}");
    expect(onChange).toHaveBeenLastCalledWith(2, 9);
    const hi = screen.getByLabelText("Upper limit");
    await userEvent.clear(hi);
    await userEvent.type(hi, "1{Enter}");                     // below the lower limit: ignored
    expect(onChange).not.toHaveBeenCalledWith(2, 1);
    expect(onChange).not.toHaveBeenCalledWith(1, 1);
  });
});
