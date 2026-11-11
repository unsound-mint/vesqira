import { axisName } from "./overlays";
import { LABEL_INSET, placeLabels } from "./labelPlacement";
import type { Box, TextMark } from "./labelPlacement";

const text = (over: Partial<TextMark> & { x: number; y: number }): TextMark => ({ type: "text", role: "grid", text: "RA 236.8°", color: "white", anchor: "middle", baseline: "bottom", ...over });
const FRAME = { w: 600, h: 400 };
const boxOf = (p: { sx: number; sy: number; text: string; anchor: string }) => {
  const w = Math.ceil(p.text.length * 11 * 0.62);
  return { x: p.anchor === "start" ? p.sx : p.anchor === "end" ? p.sx - w : p.sx - w / 2, w };
};

describe("overlay label placement", () => {
  it("keeps a label inside the inset of the frame", () => {
    const [l] = placeLabels([text({ x: 0, y: 0, anchor: "start", baseline: "top" })], 1, FRAME, []);
    expect(l!.sx).toBeGreaterThanOrEqual(LABEL_INSET);
    expect(l!.sy).toBeGreaterThanOrEqual(LABEL_INSET);
    const [r] = placeLabels([text({ x: 600, y: 400, anchor: "end", baseline: "bottom" })], 1, FRAME, []);
    expect(r!.sx).toBeLessThanOrEqual(FRAME.w - LABEL_INSET);
    expect(r!.sy).toBeLessThanOrEqual(FRAME.h - LABEL_INSET);
  });

  it("slides a label off the viewer's own controls (zoom bar, channel caption)", () => {
    const zoomBar: Box = { x: 480, y: 8, w: 110, h: 36 };
    const at = text({ x: 540, y: 0, baseline: "top" });
    const [l] = placeLabels([at], 1, FRAME, [zoomBar]);
    expect(l).toBeDefined();
    const b = boxOf(l!);
    expect(b.x + b.w <= zoomBar.x || b.x >= zoomBar.x + zoomBar.w).toBe(true); // clear sideways, same edge
  });

  it("drops a label that has no free place rather than draw it under a control", () => {
    const everything: Box = { x: 0, y: 0, w: 600, h: 400 };
    expect(placeLabels([text({ x: 300, y: 0, baseline: "top" })], 1, FRAME, [everything])).toEqual([]);
  });

  it("does not let two labels overlap", () => {
    const [a, b] = placeLabels([text({ x: 300, y: 400 }), text({ x: 302, y: 400, text: "RA 237.0°" })], 1, FRAME, []);
    const ba = boxOf(a!), bb = boxOf(b!);
    expect(ba.x + ba.w <= bb.x || bb.x + bb.w <= ba.x).toBe(true);
  });

  it("scales picture positions by the screen cell, so text keeps its size at every zoom", () => {
    const [a] = placeLabels([text({ x: 10, y: 10, anchor: "start", baseline: "top" })], 20, FRAME, []);
    expect(a!.sx).toBe(200);
    expect(a!.sy).toBe(200);
  });
});

describe("grid label axis names", () => {
  it("names right ascension and declination in the equatorial frames, l and b in Galactic", () => {
    expect(axisName("ICRS", "lon")).toBe("RA");
    expect(axisName("FK5 (J2000)", "lat")).toBe("Dec");
    expect(axisName("Galactic", "lon")).toBe("l");
    expect(axisName("Galactic", "lat")).toBe("b");
  });
});
