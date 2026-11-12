import type { components } from "../api/schema";

/** North and east at the map centre, read by the worker from the file's own WCS (Compass in the contract). */
export type CompassFacts = components["schemas"]["Compass"];
/** The arrows a saved figure drew and the WCS state they rest on (SceneCompass in the contract). */
export type FigureCompass = components["schemas"]["SceneCompass"];

type Available = CompassFacts & { available: true; north_deg: number; east_deg: number };

/** The compass only when the worker computed one: a rotated, mirrored or flipped WCS has its own arrows, and none is guessed. */
export function usableCompass(c: CompassFacts | null | undefined): Available | null {
  return c && c.available && Number.isFinite(c.north_deg) && Number.isFinite(c.east_deg) ? (c as Available) : null;
}

/** The sentence that says where the arrows come from, for a legend or a figure caption. */
export function compassSentence(frame: string | null | undefined, corrected: string[] | undefined): string {
  const fixed = corrected && corrected.length > 0 ? ` with your corrections (${corrected.join(", ")}): corrected` : "";
  return `N and E: increasing latitude and longitude${frame ? ` (${frame})` : ""} at the map centre, from the file's WCS${fixed}.`;
}

/** What to say next to a figure: where the arrows come from, or why there are none. */
export function compassNote(c: CompassFacts | null | undefined): string {
  const u = usableCompass(c);
  if (u) return compassSentence(u.frame, u.corrected);
  if (c && !c.available && c.reason) return `No north/east arrows: ${c.reason}.`;
  return "No north/east arrows: this result does not record where north and east point in the map.";
}

/**
 * The arrows a figure records, from what the worker reported for the viewer; undefined when there is no usable compass (no celestial
 * WCS, or none computed), so a figure never records or draws arrows it has no reading for. Never invents an orientation.
 */
export function figureCompassOf(c: CompassFacts | null | undefined): FigureCompass | undefined {
  const u = usableCompass(c);
  if (!u || typeof u.x !== "number" || typeof u.y !== "number" || !Number.isFinite(u.x) || !Number.isFinite(u.y)) return undefined;
  return { x: u.x, y: u.y, northDeg: u.north_deg, eastDeg: u.east_deg, ...(u.frame ? { frame: u.frame } : {}), ...(u.corrected && u.corrected.length > 0 ? { corrected: [...u.corrected] } : {}) };
}

/**
 * Arrow tips in screen coordinates. Worker angles run counter-clockwise from +x with y up (FITS), screen y runs down, so the
 * vertical component changes sign. The arrow lengths are equal: the figure shows direction, not scale.
 */
export function compassTips(c: { north_deg: number; east_deg: number }, len: number) {
  const tip = (deg: number) => ({ x: Math.cos((deg * Math.PI) / 180) * len, y: -Math.sin((deg * Math.PI) / 180) * len });
  return { north: tip(c.north_deg), east: tip(c.east_deg) };
}

export type Pt = { x: number; y: number };
/** One arrow from the origin: the shaft ends where the head starts, the head is a triangle, the letter stands beyond the tip. */
export type ArrowShape = { label: "N" | "E"; tip: Pt; shaftEnd: Pt; head: [Pt, Pt, Pt]; letter: Pt };

/**
 * The geometry of both arrows, in screen coordinates around (0, 0): the one function the viewer's glyph (Compass.tsx), the PNG and
 * the SVG of a figure all draw from, so they cannot disagree about where north is. A handedness flip needs nothing special: east
 * is wherever the worker says it is, so a mirrored WCS has east on the right.
 */
export function compassShapes(c: { north_deg: number; east_deg: number }, len: number, head = 4): { north: ArrowShape; east: ArrowShape } {
  const t = compassTips(c, len);
  const shape = (label: "N" | "E", tip: Pt): ArrowShape => {
    const L = Math.hypot(tip.x, tip.y) || 1;
    const ux = tip.x / L, uy = tip.y / L;
    return {
      label,
      tip,
      shaftEnd: { x: tip.x - ux * head, y: tip.y - uy * head },
      head: [tip, { x: tip.x - ux * head - uy * head * 0.5, y: tip.y - uy * head + ux * head * 0.5 }, { x: tip.x - ux * head + uy * head * 0.5, y: tip.y - uy * head - ux * head * 0.5 }],
      letter: { x: tip.x + ux * (head * 1.75), y: tip.y + uy * (head * 1.75) },
    };
  };
  return { north: shape("N", t.north), east: shape("E", t.east) };
}
