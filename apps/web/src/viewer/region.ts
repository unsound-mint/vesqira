import type { RegionShape } from "../api/planes";

export type Tool = "pointer" | "rect" | "circle";
export type Pt = { x: number; y: number };

/** A region from a drag between two image pixels (0-based, y from the bottom). Circles are centred on the first point. */
export function fromDrag(tool: "rect" | "circle", a: Pt, b: Pt): RegionShape {
  if (tool === "rect") return { shape: "rect", x0: Math.min(a.x, b.x), y0: Math.min(a.y, b.y), x1: Math.max(a.x, b.x), y1: Math.max(a.y, b.y) };
  return { shape: "circle", cx: a.x, cy: a.y, r: Math.round(Math.hypot(b.x - a.x, b.y - a.y)) };
}

/** Geometric size by the same pixel-centre rule the worker uses, before clipping to the image. */
export function nominalPixels(r: RegionShape): number {
  if (r.shape === "rect") return (r.x1 - r.x0 + 1) * (r.y1 - r.y0 + 1);
  let n = 0;
  for (let y = -r.r; y <= r.r; y++) for (let x = -r.r; x <= r.r; x++) if (x * x + y * y <= r.r * r.r) n++;
  return n;
}

/** Image-space bounds as drawn: pixel edges, y from the top of the picture. */
export function outline(r: RegionShape, height: number): { kind: "rect"; x: number; y: number; w: number; h: number } | { kind: "circle"; cx: number; cy: number; r: number } {
  if (r.shape === "rect") return { kind: "rect", x: r.x0, y: height - 1 - r.y1, w: r.x1 - r.x0 + 1, h: r.y1 - r.y0 + 1 };
  return { kind: "circle", cx: r.cx + 0.5, cy: height - 1 - r.cy + 0.5, r: r.r + 0.5 };
}

export const sameRegion = (a: RegionShape | null, b: RegionShape | null) => JSON.stringify(a) === JSON.stringify(b);
