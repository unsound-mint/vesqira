/**
 * Display geometry of the sky panel (ADR 0072). Every position that arrives here is already a world coordinate in the panel's frame,
 * computed by the worker from a dataset's WCS. This file only lays such positions out on a screen, the way a plot lays out its axes:
 * a gnomonic (tangent-plane) view about a centre, a coordinate grid and a point-in-outline test. It never reads a header, never converts
 * between frames and never turns a pixel into a position.
 */
export type LonLat = [number, number];
export type View = { lon: number; lat: number; fov: number };
export type Pt = [number, number];

const RAD = Math.PI / 180;
export const MIN_FOV = 0.02;
export const MAX_FOV = 60;

export const wrap360 = (lon: number) => ((lon % 360) + 360) % 360;
export const clampFov = (f: number) => Math.min(MAX_FOV, Math.max(MIN_FOV, f));

/** Unit-sphere position of a longitude and latitude in degrees. */
const vec = (lon: number, lat: number): [number, number, number] => [Math.cos(lat * RAD) * Math.cos(lon * RAD), Math.cos(lat * RAD) * Math.sin(lon * RAD), Math.sin(lat * RAD)];

/** Great-circle distance in degrees. */
export function separation(a: LonLat, b: LonLat): number {
  const [x1, y1, z1] = vec(a[0], a[1]);
  const [x2, y2, z2] = vec(b[0], b[1]);
  const cross = Math.hypot(y1 * z2 - z1 * y2, z1 * x2 - x1 * z2, x1 * y2 - y1 * x2);
  return Math.atan2(cross, x1 * x2 + y1 * y2 + z1 * z2) / RAD;
}

/**
 * Tangent-plane offsets (radians) of a position from the view centre, east to the right and north up; null when the position is
 * 90 degrees or more from the centre, where a tangent plane does not exist.
 */
export function tangent(lon: number, lat: number, c: { lon: number; lat: number }): Pt | null {
  const dl = (lon - c.lon) * RAD;
  const p = lat * RAD;
  const p0 = c.lat * RAD;
  const cosc = Math.sin(p0) * Math.sin(p) + Math.cos(p0) * Math.cos(p) * Math.cos(dl);
  if (!(cosc > 1e-3)) return null;
  return [(Math.cos(p) * Math.sin(dl)) / cosc, (Math.cos(p0) * Math.sin(p) - Math.sin(p0) * Math.cos(p) * Math.cos(dl)) / cosc];
}

/** Screen position in a square of `size` units for a view `fov` degrees across. East is to the left, as on a sky chart. */
export function toScreen(lon: number, lat: number, v: View, size: number): Pt | null {
  const t = tangent(lon, lat, v);
  if (!t) return null;
  const k = size / (2 * Math.tan((v.fov * RAD) / 2));
  return [size / 2 - t[0] * k, size / 2 - t[1] * k];
}

/** The view that holds every outline with a margin: centred on the mean direction, as wide as the farthest vertex needs. */
export function fitView(polygons: LonLat[][]): View | null {
  const pts = polygons.flat();
  if (pts.length === 0) return null;
  let x = 0, y = 0, z = 0;
  for (const [lon, lat] of pts) {
    const v = vec(lon, lat);
    x += v[0]; y += v[1]; z += v[2];
  }
  const n = Math.hypot(x, y, z);
  if (n < 1e-9) return null;
  const c: LonLat = [wrap360(Math.atan2(y, x) / RAD), Math.asin(Math.max(-1, Math.min(1, z / n))) / RAD];
  let far = 0;
  for (const p of pts) far = Math.max(far, separation(c, p));
  return { lon: c[0], lat: c[1], fov: clampFov(Math.max(far * 2 * 1.25, MIN_FOV * 4)) };
}

/** True when the screen point (0, 0) of an outline projected about a position lies inside it: is the position within the outline. */
export function contains(polygon: LonLat[], at: LonLat): boolean {
  const pts: Pt[] = [];
  for (const [lon, lat] of polygon) {
    const t = tangent(lon, lat, { lon: at[0], lat: at[1] });
    if (!t) return false; // part of the outline is not on this side of the sky: it is not a small field
    pts.push(t);
  }
  let inside = false;
  for (let i = 0, j = pts.length - 1; i < pts.length; j = i++) {
    const [xi, yi] = pts[i]!;
    const [xj, yj] = pts[j]!;
    if (yi > 0 !== yj > 0 && 0 < ((xj - xi) * (0 - yi)) / (yj - yi) + xi) inside = !inside;
  }
  return inside;
}

/** The view moved by a fraction of its width: east and north in the sky's own sense, so "right" moves toward lower longitude. */
export function pan(v: View, right: number, up: number): View {
  const lat = Math.max(-89.9, Math.min(89.9, v.lat + up * v.fov));
  const dl = (right * v.fov) / Math.max(0.05, Math.cos(v.lat * RAD));
  return { ...v, lon: wrap360(v.lon - dl), lat };
}

export const zoom = (v: View, factor: number): View => ({ ...v, fov: clampFov(v.fov * factor) });

// ----- the coordinate grid -----

const STEPS = [1 / 3600, 2 / 3600, 5 / 3600, 10 / 3600, 15 / 3600, 30 / 3600, 1 / 60, 2 / 60, 5 / 60, 10 / 60, 15 / 60, 30 / 60, 1, 2, 5, 10, 15, 30, 45];
const LINES_WANTED = 5;

/** The smallest of the usual spacings (arcseconds, arcminutes, degrees) that leaves about five lines across a view `fov` wide. */
export function gridStep(fov: number): number {
  return STEPS.find((s) => fov / s <= LINES_WANTED + 1) ?? 45;
}

export type GridLine = { axis: "lon" | "lat"; value: number; runs: Pt[][]; label: { at: Pt; text: string } | null };

const SAMPLES = 48;

/** True when the view reaches a pole, where every longitude is in view and cos(latitude) says nothing about spacing. */
const polarish = (v: View) => Math.abs(v.lat) + v.fov * 0.75 >= 90;

function decimals(step: number) {
  return step >= 1 ? 0 : step >= 1 / 60 ? 2 : 4;
}

/** Text of a grid value: degrees, with as many decimals as the spacing needs. */
export function gridLabel(value: number, axis: "lon" | "lat", step: number): string {
  const v = axis === "lon" ? wrap360(value) : value;
  return `${v.toFixed(decimals(step)).replace(/\.0+$/, "")}°`;
}

/**
 * Lines of constant longitude and latitude across the view, in the panel's own frame, as runs of screen points (a line leaves and
 * re-enters the square, so it is several runs), each with one label where it meets the left or bottom edge.
 */
export function grid(v: View, size: number): GridLine[] {
  const step = gridStep(v.fov);
  // a degree of longitude is cos(latitude) as wide on the sky, so its lines are spaced wider in longitude to look as far apart
  const lonStep = polarish(v) ? step : gridStep(v.fov / Math.max(0.05, Math.cos(Math.min(89.9, Math.abs(v.lat)) * RAD)));
  const half = v.fov * 0.75;
  const lat0 = Math.max(-90, v.lat - half);
  const lat1 = Math.min(90, v.lat + half);
  const polar = Math.abs(v.lat) + half >= 90;
  const dl = polar ? 180 : Math.min(180, half / Math.max(0.05, Math.cos(Math.min(89.9, Math.max(Math.abs(lat0), Math.abs(lat1))) * RAD)));
  const out: GridLine[] = [];
  const line = (axis: "lon" | "lat", value: number, pts: LonLat[]) => {
    const runs: Pt[][] = [];
    let cur: Pt[] = [];
    for (const [lo, la] of pts) {
      const s = toScreen(lo, la, v, size);
      if (s && Math.abs(s[0] - size / 2) < size * 1.5 && Math.abs(s[1] - size / 2) < size * 1.5) cur.push(s);
      else if (cur.length) { runs.push(cur); cur = []; }
    }
    if (cur.length) runs.push(cur);
    const inside = runs.flat().filter((p) => p[0] >= 0 && p[0] <= size && p[1] >= 0 && p[1] <= size);
    if (inside.length === 0) return;
    const at = axis === "lat" ? inside.reduce((a, b) => (b[0] < a[0] ? b : a)) : inside.reduce((a, b) => (b[1] > a[1] ? b : a));
    out.push({ axis, value, runs, label: { at, text: gridLabel(value, axis, axis === "lon" ? lonStep : step) } });
  };
  const lonFrom = polar ? 0 : Math.floor((v.lon - dl) / lonStep) * lonStep;
  const lonTo = polar ? 360 : v.lon + dl;
  // a view on a pole sees every longitude: at a small field that would be thousands of lines, so none is drawn
  for (let lo = lonFrom; (lonTo - lonFrom) / lonStep <= 80 && lo <= lonTo + 1e-9; lo += lonStep) {
    const pts: LonLat[] = [];
    for (let i = 0; i <= SAMPLES; i++) pts.push([lo, lat0 + ((lat1 - lat0) * i) / SAMPLES]);
    line("lon", lo, pts);
  }
  for (let la = Math.ceil(lat0 / step) * step; la <= lat1 + 1e-9; la += step) {
    if (Math.abs(la) >= 90) continue;
    const pts: LonLat[] = [];
    const a = polar ? 0 : v.lon - dl;
    const b = polar ? 360 : v.lon + dl;
    for (let i = 0; i <= SAMPLES; i++) pts.push([a + ((b - a) * i) / SAMPLES, la]);
    line("lat", la, pts);
  }
  // labels along one edge that would print over each other: the later one is dropped (its line stays)
  for (const axis of ["lon", "lat"] as const) {
    const key = axis === "lon" ? 0 : 1;
    let last = -Infinity;
    for (const l of out.filter((x) => x.axis === axis && x.label).sort((a, b) => a.label!.at[key] - b.label!.at[key])) {
      if (l.label!.at[key] - last < (axis === "lon" ? 46 : 14)) l.label = null;
      else last = l.label!.at[key];
    }
  }
  return out;
}
