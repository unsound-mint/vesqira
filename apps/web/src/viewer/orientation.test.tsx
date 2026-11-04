import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Dataset } from "../api/catalog";
import { lut } from "./colormaps";
import { glyphStride, orientationLines } from "./glyphs";
import { axialMeanText, InterpretationPanel, suggestCompanion } from "./OrientationPanels";
import { axialDegrees, renderPlane } from "./render";
import type { Viewer } from "./useViewer";

describe("axial angles", () => {
  it("treats 0 and 180 degrees, and -10 and 170, as the same orientation", () => {
    expect(axialDegrees(180, 1)).toBe(0);
    expect(axialDegrees(-10, 1)).toBe(170);
    expect(axialDegrees(Math.PI, 180 / Math.PI)).toBeCloseTo(0, 9);
  });

  it("colours 0 and 180 degrees identically on the cyclic map, and never with a seam at the ends", () => {
    const t = lut("twilight");
    expect(Array.from(t.slice(0, 3))).toEqual(Array.from(t.slice(765, 768)));
    const px = (v: number) => {
      const img = renderPlane(new Float32Array([v]), 1, 1, { lut: t, range: null, stretch: "linear", outW: 1, outH: 1, nodata: [[0, 0, 0], [9, 9, 9]], axial: { toDegrees: 1 } });
      return Array.from(img.pixels.slice(0, 3));
    };
    expect(px(0)).toEqual(px(180));
    expect(px(1)).toEqual(px(181));
    expect(px(45)).not.toEqual(px(135));
  });

  it("draws NaN angles as no value, not as an orientation", () => {
    const img = renderPlane(new Float32Array([NaN]), 1, 1, { lut: lut("twilight"), range: null, stretch: "linear", outW: 1, outH: 1, nodata: [[1, 2, 3], [9, 9, 9]], axial: { toDegrees: 1 } });
    expect(Array.from(img.pixels.slice(0, 3))).toEqual([1, 2, 3]);
  });
});

describe("axial mean text", () => {
  it("shows an orientation that rounds to 180 as 0, since they are the same", () => {
    expect(axialMeanText(179.9999999)).toBe("0.00°");
    expect(axialMeanText(0)).toBe("0.00°");
    expect(axialMeanText(152.0683)).toBe("152.07°");
  });
});

describe("orientation lines", () => {
  // the fixture's tangential field around the centre pixel: angle from +x towards +y (y up)
  const n = 33;
  const c = 16;
  const field = new Float32Array(n * n).map((_, i) => {
    const y = Math.floor(i / n);
    const x = i % n;
    return x === c && y === c ? NaN : (((Math.atan2(y - c, x - c) * 180) / Math.PI + 90) % 180 + 180) % 180;
  });
  const at = (dx: number, dy: number) => {
    const lines = orientationLines(field, n, n, 1, { stride: 1 });
    const cx = c + dx + 0.5;
    const cy = n - 1 - (c + dy) + 0.5;
    const l = lines.find((g) => Math.abs((g.x1 + g.x2) / 2 - cx) < 1e-9 && Math.abs((g.y1 + g.y2) / 2 - cy) < 1e-9)!;
    return { dx: l.x2 - l.x1, dy: l.y2 - l.y1 };
  };

  it("draws a tangential field the right way round (a mirrored convention would fail here)", () => {
    const right = at(5, 0);
    expect(Math.abs(right.dx)).toBeLessThan(1e-9); // right of the centre: vertical
    const above = at(0, 5);
    expect(Math.abs(above.dy)).toBeLessThan(1e-9); // above the centre: horizontal
    const upRight = at(5, 5);
    expect(upRight.dx * upRight.dy).toBeGreaterThan(0); // up-right: runs from up-left to down-right in the picture
    const upLeft = at(-5, 5);
    expect(upLeft.dx * upLeft.dy).toBeLessThan(0);
  });

  it("skips pixels with no angle and fades unknown reliability", () => {
    const lines = orientationLines(new Float32Array([NaN, 10]), 2, 1, 1, { stride: 1, reliability: new Float32Array([1, NaN]), relScale: 1 });
    expect(lines).toHaveLength(1);
    expect(lines[0]!.opacity).toBe(0.2);
  });

  it("scales opacity to the companion map's own range, not to 0..1", () => {
    const lines = orientationLines(new Float32Array([10, 10]), 2, 1, 1, { stride: 1, reliability: new Float32Array([0.5, 0.25]), relScale: 0.5 });
    expect(lines.map((l) => l.opacity)).toEqual([1, 0.5]);
    expect(glyphStride(4)).toBe(4);
  });
});

describe("declaring an interpretation", () => {
  const ds = (id: number, name: string): Dataset => ({ id, name, path: `/d/${name}`, sizeBytes: 1, modifiedAt: "", addedAt: "", interpretation: null, header: {} as Dataset["header"] });
  const viewer = { view: { channels: 1 }, plane: { head: { stats: { min: 0, max: 3.1 } } } } as unknown as Viewer;

  it("suggests a reliability map by file name only", () => {
    expect(suggestCompanion("psi_hnv1.fits", [ds(2, "psi_reliability_hnv1.fits"), ds(3, "other.fits")])?.id).toBe(2);
    expect(suggestCompanion("psi_hnv1.fits", [ds(3, "other.fits")])).toBeNull();
  });

  it("does not let the unit be left unchosen and says the value range is only a hint", async () => {
    const calls: unknown[] = [];
    render(<InterpretationPanel v={viewer} dataset={ds(1, "psi_hnv1.fits")} datasets={[ds(1, "psi_hnv1.fits"), ds(2, "psi_reliability_hnv1.fits")]} declare={async (_id, b) => (calls.push(b), null)} />);
    await userEvent.selectOptions(screen.getByLabelText("Treat this map as"), "axial");
    const apply = screen.getByRole("button", { name: "Declare axial" });
    expect(apply).toBeDisabled();
    expect(screen.getByText(/never infers the unit/)).toBeInTheDocument();
    expect(screen.getByText(/may be its reliability map/)).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Angle unit (required)"), "rad");
    await userEvent.click(apply);
    expect(calls).toEqual([{ kind: "axial_orientation", unit: "rad", reliabilityId: null }]);
  });
});
