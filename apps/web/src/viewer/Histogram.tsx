import { useRef } from "react";
import type { PointerEvent } from "react";
import type { PlaneStats, RegionShape } from "../api/planes";
import { useHistogram } from "./useHistogram";
import { limitNumber, limitText } from "./format";
import s from "./Histogram.module.css";

const BINS = 64;

/** A region to count instead of the whole plane. `preview`: the plane is a block-mean preview, not the full-resolution data. */
export type HistogramScope = { name: string; shape: RegionShape; width: number; height: number; preview: boolean };

/**
 * The values of the visible plane (or of `scope`, a region of it, drawn on the plane's own value range so the limits keep their place) with the two display limits on them. Drag near a limit, or type the numbers.
 * Only finite pixels are counted: no-value pixels are not in the histogram and zero is just a value.
 */
export function Histogram({ data, stats, limits, unit, onChange, scope = null }: { data: Float32Array; stats: PlaneStats; limits: [number, number] | null; unit: string | null; onChange: (lo: number, hi: number) => void; scope?: HistogramScope | null }) {
  const svg = useRef<SVGSVGElement>(null);
  const drag = useRef<0 | 1 | null>(null);
  const { min, max } = stats;
  const result = useHistogram(data, min, max, BINS, scope?.shape ?? null, scope?.width, scope?.height);
  const counted = result ? result.counts : null;
  const counts = counted ?? []; // null while counting: the plot stays empty rather than show another channel's counts
  const fmt = (n: number | null) => (n === null ? "…" : n.toLocaleString("en-US"));
  const regionDone = scope && result && result.pixels !== null;
  const finite = scope ? (regionDone ? result.pixels! - (result.nonfinite ?? 0) : null) : stats.finite_count;
  const nonfinite = scope ? (result?.nonfinite ?? null) : stats.nonfinite_count;
  const label = scope
    ? `Region: ${scope.name}${regionDone ? `, ${fmt(result.pixels)} pixels, ${fmt(result.nonfinite)} not finite` : ""}${scope.preview ? " (from the preview)" : ""}`
    : "Plane";
  if (min === null || max === null || (!scope && counted !== null && counted.length === 0)) return <p className={s.note}>No histogram: the plane has no finite value.</p>;
  if (scope && finite === 0) return <p className={s.note} data-testid="histogram-scope">{label}. No histogram: the region has no finite value.</p>;
  const lo0: number = min;
  const hi0: number = max;
  const top = Math.max(...counts.map(Math.sqrt), 1);
  const span = max > min ? max - min : 1;
  const x = (v: number) => ((v - min) / span) * 100;
  const at = (e: PointerEvent) => {
    const r = svg.current!.getBoundingClientRect();
    return lo0 + Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)) * (hi0 - lo0);
  };
  function move(e: PointerEvent) {
    if (drag.current === null || !limits) return;
    const v = at(e);
    const eps = (hi0 - lo0) / 1000;
    if (drag.current === 0) onChange(Math.min(v, limits[1] - eps), limits[1]);
    else onChange(limits[0], Math.max(v, limits[0] + eps));
  }
  function down(e: PointerEvent) {
    if (!limits) return;
    const v = at(e);
    drag.current = Math.abs(v - limits[0]) <= Math.abs(v - limits[1]) ? 0 : 1;
    e.currentTarget.setPointerCapture?.(e.pointerId);
    move(e);
  }
  const set = (i: 0 | 1, text: string) => {
    const n = Number(text);
    if (!limits || text.trim() === "" || !Number.isFinite(n)) return;
    if (i === 0 && n < limits[1]) onChange(n, limits[1]);
    if (i === 1 && n > limits[0]) onChange(limits[0], n);
  };
  return (
    <div className={s.wrap}>
      <p className={s.note} data-testid="histogram-scope">{label}</p>
      <div className={s.plotRow}>
      <span className={s.yLabel} aria-hidden="true">pixels per bin (√)</span>
      <svg
        ref={svg}
        className={s.plot}
        viewBox="0 0 100 40"
        preserveAspectRatio="none"
        role="img"
        aria-busy={counted === null}
        aria-label={`Histogram of ${fmt(finite)} finite pixels${scope ? " in the region" : ""} from ${limitText(min, unit)} to ${limitText(max, unit)}${limits ? `; display limits ${limitText(limits[0], unit)} to ${limitText(limits[1], unit)}` : ""}`}
        onPointerDown={down}
        onPointerMove={move}
        onPointerUp={() => { drag.current = null; }}
      >
        {counts.map((c, i) => <rect key={i} className={s.bar} x={(i * 100) / BINS} y={40 - (Math.sqrt(c) / top) * 38} width={100 / BINS - 0.3} height={(Math.sqrt(c) / top) * 38} />)}
        {limits ? (
          <>
            <rect className={s.shade} x={Math.max(0, x(limits[0]))} y={0} width={Math.max(0, Math.min(100, x(limits[1])) - Math.max(0, x(limits[0])))} height={40} />
            <line className={s.limit} x1={x(limits[0])} x2={x(limits[0])} y1={0} y2={40} />
            <line className={s.limit} x1={x(limits[1])} x2={x(limits[1])} y1={0} y2={40} />
          </>
        ) : null}
      </svg>
      <div className={s.xAxis} aria-hidden="true">
        <span className="mono">{limitText(min, unit)}</span>
        <span>pixel value</span>
        <span className="mono">{limitText(max, unit)}</span>
      </div>
      </div>
      <p className={s.note}>Counts are drawn on a square-root scale. {fmt(nonfinite)} pixels with no value are not counted.</p>
      {limits ? (
        <div className={s.inputs}>
          <label>Lower limit<input key={`lo${limits[0]}`} aria-label={`Lower limit${unit ? ` (${unit})` : ""}`} className={s.num} inputMode="decimal" defaultValue={limitNumber(limits[0])} onBlur={(e) => set(0, e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") set(0, e.currentTarget.value); }} /></label>
          <label>Upper limit<input key={`hi${limits[1]}`} aria-label={`Upper limit${unit ? ` (${unit})` : ""}`} className={s.num} inputMode="decimal" defaultValue={limitNumber(limits[1])} onBlur={(e) => set(1, e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") set(1, e.currentTarget.value); }} /></label>
        </div>
      ) : null}
    </div>
  );
}
