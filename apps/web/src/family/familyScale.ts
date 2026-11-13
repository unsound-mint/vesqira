import type { PlaneStats } from "../api/planes";

/**
 * The display limits shared by maps that are compared (ADR 0070). One scale for every member, from the widest 1st to 99th percentile of
 * their finite values, so equal colours mean equal values across panels. null when no member has a finite value.
 */
export function sharedRange(stats: PlaneStats[]): [number, number] | null {
  const lo = stats.map((s) => s.p01).filter((v): v is number => v !== null);
  const hi = stats.map((s) => s.p99).filter((v): v is number => v !== null);
  if (lo.length === 0 || hi.length === 0) return null;
  const a = Math.min(...lo);
  const b = Math.max(...hi);
  return b > a ? [a, b] : [a - 0.5, a + 0.5];
}

/** A signed difference is drawn symmetric about zero, so a colour does not mean different amounts above and below it. */
export function symmetricRange(stats: PlaneStats[]): [number, number] | null {
  const r = sharedRange(stats);
  if (!r) return null;
  const m = Math.max(Math.abs(r[0]), Math.abs(r[1]));
  return m > 0 ? [-m, m] : [-1, 1];
}

/** A spread is not negative: its scale starts at zero. */
export function spreadRange(stats: PlaneStats[]): [number, number] | null {
  const hi = stats.map((s) => s.p99).filter((v): v is number => v !== null);
  if (hi.length === 0) return null;
  const m = Math.max(...hi);
  return m > 0 ? [0, m] : [0, 1];
}

/** Axial differences are distances between orientations: 0 to 90 degrees, fixed, so panels of different families are comparable. */
export const AXIAL_DIFFERENCE_RANGE: [number, number] = [0, 90];

const num = (v: number) => String(Number(v.toPrecision(4)));

/** A value for the readout: its text with the unit, or the words for no value (never 0 and never a blank). */
export function valueText(v: number | undefined, unit: string): string {
  if (v === undefined || !Number.isFinite(v)) return "no value";
  return unit ? `${num(v)} ${unit}` : num(v);
}

/** The pixel a cursor names, kept inside a width by height grid (row 0 is the bottom row, the way planes are stored). */
export function clampCursor(c: { x: number; y: number }, w: number, h: number): { x: number; y: number } {
  return { x: Math.min(w - 1, Math.max(0, c.x)), y: Math.min(h - 1, Math.max(0, c.y)) };
}

/** Display size of a w by h plane that stays under `budget` pixels: nearest-neighbour sampling, a value is never blended. */
export function displaySize(w: number, h: number, budget = 1 << 20): { outW: number; outH: number } {
  if (w * h <= budget) return { outW: w, outH: h };
  const k = Math.sqrt(budget / (w * h));
  return { outW: Math.max(1, Math.floor(w * k)), outH: Math.max(1, Math.floor(h * k)) };
}

/** A member's label for a swept or smoothed parameter: "width = 5". */
export const optionLabel = (key: string, value: string) => `${key} = ${value}`;
