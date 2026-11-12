import { compassShapes, usableCompass } from "./compassGeometry";
import type { ArrowShape, CompassFacts } from "./compassGeometry";
import s from "./Compass.module.css";

// The pure parts live in compassGeometry.ts so a figure export can use them without React or CSS modules.
export { compassNote, compassShapes, compassTips, figureCompassOf, usableCompass } from "./compassGeometry";
export type { CompassFacts, FigureCompass } from "./compassGeometry";

const HEAD = 4;

function Arrow({ a }: { a: ArrowShape }) {
  const head = a.head.map((p) => `${p.x},${p.y}`).join(" ");
  return (
    <g>
      <line className={s.halo} x1={0} y1={0} x2={a.shaftEnd.x} y2={a.shaftEnd.y} />
      <polygon className={s.haloFill} points={head} />
      <line className={s.stroke} x1={0} y1={0} x2={a.shaftEnd.x} y2={a.shaftEnd.y} />
      <polygon className={s.fill} points={head} />
      <text className={s.letter} x={a.letter.x} y={a.letter.y} textAnchor="middle" dominantBaseline="central">{a.label}</text>
    </g>
  );
}

/** Two arrows from (cx, cy): N and E. Draws nothing without a usable compass. Put it inside an <svg>. */
export function CompassGlyph({ compass, cx, cy, len = 18 }: { compass: CompassFacts | null | undefined; cx: number; cy: number; len?: number }) {
  const u = usableCompass(compass);
  if (!u) return null;
  const { north, east } = compassShapes(u, len, HEAD);
  return (
    <g transform={`translate(${cx} ${cy})`} data-testid="compass" data-north-deg={u.north_deg.toFixed(2)} data-east-deg={u.east_deg.toFixed(2)}>
      <title>{`North points ${Math.round(u.north_deg)}° and east ${Math.round(u.east_deg)}° counter-clockwise from the +x pixel axis`}</title>
      <Arrow a={north} />
      <Arrow a={east} />
    </g>
  );
}
