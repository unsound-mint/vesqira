import type { MaskType } from "../api/masks";

/**
 * Mask overlays are hatched, one pattern per type, so they are told apart without colour (the legend names them too). The
 * patterns are drawn in screen pixels and never replace the picture: pixels with no value keep the viewer's own hatch, and a mask
 * of the type `missing` is a different dotted pattern, not the same one.
 */
export type HatchDef = { id: MaskType; label: string; /** an SVG path in a 6 x 6 tile */ tile: string; dots?: boolean };

export const HATCH: Record<MaskType, HatchDef> = {
  missing: { id: "missing", label: "no value (dots)", tile: "", dots: true },
  invalid: { id: "invalid", label: "invalid (cross-hatch)", tile: "M0 0L6 6M0 6L6 0" },
  low_snr: { id: "low_snr", label: "low signal-to-noise (diagonal /)", tile: "M0 6L6 0" },
  saturated: { id: "saturated", label: "saturated (diagonal \\)", tile: "M0 0L6 6" },
  user_excluded: { id: "user_excluded", label: "excluded by you (horizontal)", tile: "M0 3H6" },
  other: { id: "other", label: "other (vertical)", tile: "M3 0V6" },
};

/** The `d` of horizontal runs of flagged pixels, in screen pixels (`cell` per image pixel), north up: row 0 of the data is the bottom. */
export function maskPath(data: Uint8Array, w: number, h: number, cell: number, cap = 40000): { d: string; step: number } {
  for (let step = 1; ; step *= 2) {
    const bw = Math.ceil(w / step), bh = Math.ceil(h / step);
    const runs: string[] = [];
    let over = false;
    for (let by = 0; by < bh && !over; by++) {
      let x = 0;
      while (x < bw) {
        const on = (bx: number) => {
          for (let yy = by * step; yy < Math.min(h, (by + 1) * step); yy++)
            for (let xx = bx * step; xx < Math.min(w, (bx + 1) * step); xx++) if (data[yy * w + xx] !== 0) return true;
          return false;
        };
        if (!on(x)) { x++; continue; }
        let e = x;
        while (e < bw && on(e)) e++;
        const sx = x * step * cell, sw = (Math.min(w, e * step) - x * step) * cell;
        const sy = (h - Math.min(h, (by + 1) * step)) * cell, sh = (Math.min(h, (by + 1) * step) - by * step) * cell;
        runs.push(`M${+sx.toFixed(2)} ${+sy.toFixed(2)}h${+sw.toFixed(2)}v${+sh.toFixed(2)}h${+(-sw).toFixed(2)}z`);
        if (runs.length > cap) { over = true; break; }
        x = e;
      }
    }
    if (!over) return { d: runs.join(""), step };
  }
}
