import type { components } from "../api/schema";
import s from "./Compass.module.css";

/** North and east at the map centre, read by the worker from the file's own WCS (Compass in the contract). */
export type CompassFacts = components["schemas"]["Compass"];

type Available = CompassFacts & { available: true; north_deg: number; east_deg: number };

/** The compass only when the worker computed one: a rotated, mirrored or flipped WCS has its own arrows, and none is guessed. */
export function usableCompass(c: CompassFacts | null | undefined): Available | null {
  return c && c.available && Number.isFinite(c.north_deg) && Number.isFinite(c.east_deg) ? (c as Available) : null;
}

/** What to say next to a figure: where the arrows come from, or why there are none. */
export function compassNote(c: CompassFacts | null | undefined): string {
  const u = usableCompass(c);
  if (u) {
    const fixed = u.corrected && u.corrected.length > 0 ? ` with your corrections (${u.corrected.join(", ")}): corrected` : "";
    return `N and E: increasing latitude and longitude${u.frame ? ` (${u.frame})` : ""} at the map centre, from the file's WCS${fixed}.`;
  }
  if (c && !c.available && c.reason) return `No north/east arrows: ${c.reason}.`;
  return "No north/east arrows: this result does not record where north and east point in the map.";
}

/**
 * Arrow tips in screen coordinates. Worker angles run counter-clockwise from +x with y up (FITS), screen y runs down, so the
 * vertical component changes sign. The arrow lengths are equal: the figure shows direction, not scale.
 */
export function compassTips(c: { north_deg: number; east_deg: number }, len: number) {
  const tip = (deg: number) => ({ x: Math.cos((deg * Math.PI) / 180) * len, y: -Math.sin((deg * Math.PI) / 180) * len });
  return { north: tip(c.north_deg), east: tip(c.east_deg) };
}

const HEAD = 4;

function Arrow({ x, y, label }: { x: number; y: number; label: string }) {
  const L = Math.hypot(x, y) || 1;
  const ux = x / L, uy = y / L;
  const head = `${x},${y} ${x - ux * HEAD - uy * HEAD * 0.5},${y - uy * HEAD + ux * HEAD * 0.5} ${x - ux * HEAD + uy * HEAD * 0.5},${y - uy * HEAD - ux * HEAD * 0.5}`;
  return (
    <g>
      <line className={s.halo} x1={0} y1={0} x2={x - ux * HEAD} y2={y - uy * HEAD} />
      <polygon className={s.haloFill} points={head} />
      <line className={s.stroke} x1={0} y1={0} x2={x - ux * HEAD} y2={y - uy * HEAD} />
      <polygon className={s.fill} points={head} />
      <text className={s.letter} x={x + ux * 7} y={y + uy * 7} textAnchor="middle" dominantBaseline="central">{label}</text>
    </g>
  );
}

/** Two arrows from (cx, cy): N and E. Draws nothing without a usable compass. Put it inside an <svg>. */
export function CompassGlyph({ compass, cx, cy, len = 18 }: { compass: CompassFacts | null | undefined; cx: number; cy: number; len?: number }) {
  const u = usableCompass(compass);
  if (!u) return null;
  const { north, east } = compassTips(u, len);
  return (
    <g transform={`translate(${cx} ${cy})`} data-testid="compass" data-north-deg={u.north_deg.toFixed(2)} data-east-deg={u.east_deg.toFixed(2)}>
      <title>{`North points ${Math.round(u.north_deg)}° and east ${Math.round(u.east_deg)}° counter-clockwise from the +x pixel axis`}</title>
      <Arrow x={north.x} y={north.y} label="N" />
      <Arrow x={east.x} y={east.y} label="E" />
    </g>
  );
}
