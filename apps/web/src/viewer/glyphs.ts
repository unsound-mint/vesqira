import { axialDegrees } from "./render";

export type Glyph = { x1: number; y1: number; x2: number; y2: number; opacity: number };

const MIN_OPACITY = 0.2;

/**
 * Headless orientation lines for an axial map, sampled on a grid. Coordinates are in picture pixels (y down), so a
 * plane row y is drawn at height-1-y. Angles run from the +x pixel axis towards +y (counter-clockwise on screen);
 * that is a declared convention, not a position angle on the sky. Opacity follows the companion reliability map
 * relative to `relScale` (the map's range is not assumed to be 0 to 1); unknown reliability is drawn faint.
 */
export function orientationLines(
  angles: Float32Array,
  w: number,
  h: number,
  toDegrees: number,
  o: { stride: number; reliability?: Float32Array | null; relScale?: number },
): Glyph[] {
  const out: Glyph[] = [];
  const half = o.stride * 0.4;
  for (let y = Math.floor(o.stride / 2); y < h; y += o.stride) {
    for (let x = Math.floor(o.stride / 2); x < w; x += o.stride) {
      const v = angles[y * w + x]!;
      if (!Number.isFinite(v)) continue;
      const t = (axialDegrees(v, toDegrees) * Math.PI) / 180;
      const dx = Math.cos(t);
      const dy = -Math.sin(t); // picture y points down
      const cx = x + 0.5;
      const cy = h - 1 - y + 0.5;
      let opacity = 1;
      if (o.reliability) {
        const r = o.reliability[y * w + x]!;
        opacity = Number.isFinite(r) && o.relScale && o.relScale > 0 ? Math.min(1, Math.max(MIN_OPACITY, r / o.relScale)) : MIN_OPACITY;
      }
      out.push({ x1: cx - half * dx, y1: cy - half * dy, x2: cx + half * dx, y2: cy + half * dy, opacity });
    }
  }
  return out;
}

/** Sampling step in image pixels so that lines are about `gap` display pixels apart. */
export function glyphStride(cell: number, gap = 16): number {
  return cell > 0 ? Math.max(1, Math.ceil(gap / cell)) : 1;
}
