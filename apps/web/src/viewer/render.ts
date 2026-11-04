import type { PlaneStats } from "../api/planes";

export type Stretch = "linear" | "sqrt" | "asinh";
export type RangeMode = "percentile" | "minmax";

export const STRETCHES: { id: Stretch; label: string }[] = [
  { id: "linear", label: "Linear" },
  { id: "sqrt", label: "Square root" },
  { id: "asinh", label: "Inverse hyperbolic sine" },
];
export const RANGES: { id: RangeMode; label: string }[] = [
  { id: "percentile", label: "1st to 99th percentile" },
  { id: "minmax", label: "Minimum to maximum" },
];

/** Display limits from the plane's finite values only. null when the plane has no finite value. */
export function limits(stats: PlaneStats, mode: RangeMode): [number, number] | null {
  const [lo, hi] = mode === "percentile" ? [stats.p01, stats.p99] : [stats.min, stats.max];
  return lo === null || hi === null ? null : [lo, hi];
}

const ASINH_SOFTENING = 10;

/** Maps a finite value to [0, 1] through the stretch. Out-of-range values clip to the ends. */
export function normalize(v: number, lo: number, hi: number, stretch: Stretch): number {
  if (hi <= lo) return 0.5;
  const t = Math.min(1, Math.max(0, (v - lo) / (hi - lo)));
  if (stretch === "sqrt") return Math.sqrt(t);
  if (stretch === "asinh") return Math.asinh(t * ASINH_SOFTENING) / Math.asinh(ASINH_SOFTENING);
  return t;
}

export type Rgb = readonly [number, number, number];

export type RenderOptions = {
  lut: Uint8ClampedArray;
  range: [number, number] | null;
  stretch: Stretch;
  /** Output size in canvas pixels. At or above the plane size use an integer multiple; below it, the display size. */
  outW: number;
  outH: number;
  nodata: readonly [Rgb, Rgb];
  /** Axial orientation: colour by angle modulo 180 degrees on a cyclic map; `toDegrees` converts file units. */
  axial?: { toDegrees: number };
};

/** Axial angle in [0, 180) degrees, so that 0 and 180 (and -10 and 170) are the same orientation. */
export function axialDegrees(v: number, toDegrees: number): number {
  const d = (v * toDegrees) % 180;
  return d < 0 ? d + 180 : d;
}

const STRIPE = 3;

/** Canvas size for a plane shown at `scale` display pixels per image pixel: sharp integer zoom, or the display size when zoomed out. */
export function outputSize(w: number, h: number, scale: number, maxInteger = 16): { outW: number; outH: number } {
  if (scale >= 1) {
    const k = Math.min(maxInteger, Math.floor(scale));
    return { outW: w * k, outH: h * k };
  }
  return { outW: Math.max(1, Math.round(w * scale)), outH: Math.max(1, Math.round(h * scale)) };
}

/**
 * RGBA pixels for a plane, drawn with north up (plane row 0 is FITS y=1, the bottom row), nearest-neighbour
 * sampled (no averaging, so a value is never blended with its neighbours or with "no value").
 * Pixels that are not finite are always drawn as diagonal stripes in the canvas pixel grid, at any zoom, and never
 * take a colormap entry; zero is an ordinary value.
 */
export function renderPlane(data: Float32Array, w: number, h: number, o: RenderOptions): { width: number; height: number; pixels: Uint8ClampedArray<ArrayBuffer> } {
  const { outW: W, outH: H } = o;
  const out = new Uint8ClampedArray(W * H * 4);
  const [a, b] = o.nodata;
  let p = 0;
  for (let oy = 0; oy < H; oy++) {
    const row = (h - 1 - Math.floor((oy * h) / H)) * w;
    for (let ox = 0; ox < W; ox++) {
      const v = data[row + Math.floor((ox * w) / W)]!;
      if (Number.isFinite(v) && (o.range || o.axial)) {
        const t = o.axial ? axialDegrees(v, o.axial.toDegrees) / 180 : normalize(v, o.range![0], o.range![1], o.stretch);
        const i = Math.round(t * 255) * 3;
        out[p] = o.lut[i]!;
        out[p + 1] = o.lut[i + 1]!;
        out[p + 2] = o.lut[i + 2]!;
      } else {
        const c = Math.floor((ox + oy) / STRIPE) & 1 ? b : a;
        out[p] = c[0];
        out[p + 1] = c[1];
        out[p + 2] = c[2];
      }
      out[p + 3] = 255;
      p += 4;
    }
  }
  return { width: W, height: H, pixels: out };
}
