import { grid, toScreen, wrap360 } from "./projection";
import type { Pt, View } from "./projection";
import type { SkyItem } from "./useSky";
import type { SkyFrame } from "../api/sky";

/** Screen units of the square the panel is laid out in; the SVG scales it to its width. */
export const SIZE = 360;

export type Footprint = { id: number; name: string; path: Pt[]; selected: boolean; extent: number };
export type SkyScene = {
  grid: { runs: Pt[][]; labels: { at: Pt; text: string }[] };
  footprints: Footprint[];
  /** Footprints whose outline is wholly outside the view, by name: said in the list, not drawn. */
  offscreen: number;
  cursor: Pt | null;
  /** Naming the frame on the picture itself: a chart without its frame is not a chart. */
  frameLabel: string;
};

export const frameName = (f: SkyFrame) => (f === "galactic" ? "Galactic (l, b)" : "ICRS (RA, Dec)");

/** The picture of the panel as data: both the SVG on screen and the saved image are drawn from this, so they cannot disagree. */
export function buildScene(items: SkyItem[], view: View, frame: SkyFrame, selectedId: number | null, cursor: { lon: number; lat: number } | null): SkyScene {
  const g = grid(view, SIZE);
  const footprints: Footprint[] = [];
  let offscreen = 0;
  for (const it of items) {
    if (!it.outline) continue;
    const path: Pt[] = [];
    let drawable = true;
    for (const [lon, lat] of it.outline.polygon) {
      const s = toScreen(lon, lat, view, SIZE);
      if (!s) { drawable = false; break; }
      path.push(s);
    }
    const hit = drawable && path.some((p) => p[0] > -SIZE && p[0] < 2 * SIZE && p[1] > -SIZE && p[1] < 2 * SIZE);
    if (!hit) { offscreen++; continue; }
    footprints.push({ id: it.id, name: it.name, path, selected: it.id === selectedId, extent: it.outline.extent });
  }
  // larger outlines first, so a small map inside a large one stays reachable; the selected one last, on top (its fill takes no clicks)
  footprints.sort((a, b) => Number(a.selected) - Number(b.selected) || b.extent - a.extent);
  return {
    grid: { runs: g.flatMap((l) => l.runs), labels: g.flatMap((l) => (l.label ? [l.label] : [])) },
    footprints,
    offscreen,
    cursor: cursor ? toScreen(cursor.lon, cursor.lat, view, SIZE) : null,
    frameLabel: frameName(frame),
  };
}

export const pathData = (pts: Pt[], close: boolean) => pts.map((p, i) => `${i ? "L" : "M"}${p[0].toFixed(1)} ${p[1].toFixed(1)}`).join("") + (close ? "Z" : "");

const hms = (deg: number) => {
  const h = wrap360(deg) / 15;
  const hh = Math.floor(h);
  const m = Math.floor((h - hh) * 60);
  const s = ((h - hh) * 60 - m) * 60;
  return `${String(hh).padStart(2, "0")}h ${String(m).padStart(2, "0")}m ${s.toFixed(2).padStart(5, "0")}s`;
};
const dms = (deg: number) => {
  const a = Math.abs(deg);
  const d = Math.floor(a);
  const m = Math.floor((a - d) * 60);
  const s = ((a - d) * 60 - m) * 60;
  return `${deg < 0 ? "-" : "+"}${String(d).padStart(2, "0")}° ${String(m).padStart(2, "0")}′ ${s.toFixed(1).padStart(4, "0")}″`;
};

/** A world position as the readout writes it: decimal degrees, and for ICRS also hours-minutes-seconds and degrees-arcminutes-arcseconds. */
export function positionText(frame: SkyFrame, lon: number, lat: number): { names: [string, string]; deg: [string, string]; sexagesimal: [string, string] | null } {
  const f = (v: number) => `${v.toFixed(5)}°`;
  return frame === "icrs"
    ? { names: ["RA", "Dec"], deg: [f(lon), f(lat)], sexagesimal: [hms(lon), dms(lat)] }
    : { names: ["l", "b"], deg: [f(lon), f(lat)], sexagesimal: null };
}
