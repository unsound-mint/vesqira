import { useEffect, useMemo, useState } from "react";
import { Select } from "../ui";
import { sweepGrid } from "../api/sweeps";
import type { SweepGrid as Grid, SweepPlot, SweepRow } from "../api/sweeps";
import { EM } from "../catalog/format";
import s from "./Sweeps.module.css";

const num = (x: number) => String(Number(x.toPrecision(6)));

/** The value of a row's metric as text, with its unit; a value that was not recorded reads "not recorded", never 0. */
export function cell(row: SweepRow, key: string): string {
  if (!row.runId) return EM;
  const m = row.metrics.find((x) => x.key === key);
  if (!m || m.value === null) return "not recorded";
  return `${num(m.value)}${m.unit ? ` ${m.unit}` : ""}${m.uncertainty !== null ? ` ± ${num(m.uncertainty)}` : ""}`;
}

/** Rows sorted by a parameter (numerically when every value is a number) or by a metric value; rows without a value go last. */
export function sortRows(rows: SweepRow[], by: { kind: "param" | "metric"; key: string } | null, dir: 1 | -1): SweepRow[] {
  if (!by) return rows;
  const raw = (r: SweepRow): string | number | null => {
    if (by.kind === "param") return r.values[by.key] ?? null;
    return r.metrics.find((m) => m.key === by.key)?.value ?? null;
  };
  const numericParam = by.kind === "param" && rows.every((r) => Number.isFinite(Number(raw(r))));
  return [...rows].sort((a, b) => {
    let x = raw(a), y = raw(b);
    if (x === null && y === null) return 0;
    if (x === null) return 1;
    if (y === null) return -1;
    if (numericParam) { x = Number(x); y = Number(y); }
    return x < y ? -dir : x > y ? dir : 0;
  });
}

const DASH = ["", "6 3", "2 3", "8 3 2 3"];

function Marker({ shape, x, y }: { shape: number; x: number; y: number }) {
  if (shape % 3 === 0) return <circle cx={x} cy={y} r={4} />;
  if (shape % 3 === 1) return <rect x={x - 4} y={y - 4} width={8} height={8} />;
  return <polygon points={`${x},${y - 5} ${x + 5},${y + 4} ${x - 5},${y + 4}`} />;
}

/** A metric against one parameter, a line per combination of the others. Lines differ by dash and marker as well as hue; a missing value breaks the line. */
export function Plot({ plot }: { plot: SweepPlot }) {
  const W = 560, H = 280, L = 56, R = 120, T = 16, B = 40;
  const pts = plot.lines.flatMap((l) => l.points);
  const ys = pts.filter((p) => p.value !== null).flatMap((p) => [p.value! - (p.uncertainty ?? 0), p.value! + (p.uncertainty ?? 0)]);
  if (ys.length === 0) return <p className={s.note}>No run has a recorded value of {plot.metric} yet, so there is nothing to draw.</p>;
  let lo = Math.min(...ys), hi = Math.max(...ys);
  if (lo === hi) { lo -= 1; hi += 1; }
  const xs = plot.numeric ? plot.xValues.map(Number) : plot.xValues.map((_, i) => i);
  const xlo = Math.min(...xs), xhi = Math.max(...xs);
  const px = (i: number) => (xhi === xlo ? (L + W - R) / 2 : L + ((xs[i]! - xlo) / (xhi - xlo)) * (W - L - R));
  const py = (v: number) => T + (1 - (v - lo) / (hi - lo)) * (H - T - B);
  const yt = [lo, (lo + hi) / 2, hi];
  const title = `${plot.metric}${plot.unit ? ` (${plot.unit})` : ""} against ${plot.xName}`;
  return (
    <figure className={s.plot}>
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`${title}; the values are in the table`} className={s.svg}>
        {yt.map((v) => (
          <g key={v}>
            <line x1={L} x2={W - R} y1={py(v)} y2={py(v)} className={s.grid} />
            <text x={L - 6} y={py(v) + 4} textAnchor="end" className={s.tick}>{num(v)}</text>
          </g>
        ))}
        {plot.xValues.map((x, i) => <text key={x} x={px(i)} y={H - B + 16} textAnchor="middle" className={s.tick}>{x}</text>)}
        <text x={(L + W - R) / 2} y={H - 6} textAnchor="middle" className={s.tick}>{plot.xName}</text>
        {plot.lines.map((ln, li) => {
          const segs: number[][] = [];
          let cur: number[] = [];
          ln.points.forEach((p, i) => { if (p.value === null) { if (cur.length) segs.push(cur); cur = []; } else cur.push(i); });
          if (cur.length) segs.push(cur);
          const last = [...ln.points.keys()].reverse().find((i) => ln.points[i]!.value !== null);
          return (
            <g key={ln.label} className={s.series} data-series={li % 4}>
              {segs.map((seg) => seg.length > 1 ? <polyline key={seg[0]} fill="none" strokeWidth={2} strokeDasharray={DASH[li % 4]} points={seg.map((i) => `${px(i)},${py(ln.points[i]!.value!)}`).join(" ")} /> : null)}
              {ln.points.map((p, i) => p.value === null ? null : (
                <g key={i}>
                  {p.uncertainty !== null ? <line x1={px(i)} x2={px(i)} y1={py(p.value - p.uncertainty)} y2={py(p.value + p.uncertainty)} strokeWidth={1.5} /> : null}
                  <Marker shape={li} x={px(i)} y={py(p.value)} />
                </g>
              ))}
              {last !== undefined ? <text x={px(last) + 8} y={py(ln.points[last]!.value!) + 4} className={s.direct}>{ln.label}</text> : null}
            </g>
          );
        })}
      </svg>
      {plot.lines.length > 1 ? (
        <figcaption className={s.legend}>
          {plot.lines.map((ln, li) => (
            <span key={ln.label} className={s.series} data-series={li % 4}>
              <svg width="28" height="12" aria-hidden="true"><line x1="0" x2="28" y1="6" y2="6" strokeWidth={2} strokeDasharray={DASH[li % 4]} /></svg> {ln.label}
            </span>
          ))}
        </figcaption>
      ) : null}
      {plot.notes.map((n) => <p key={n} className={s.warn} role="note">{n}</p>)}
    </figure>
  );
}

/**
 * The sweep as a table: parameter columns, then the metrics each run declared (with unit and uncertainty), sortable, and a
 * metric against a parameter. It labels nothing "best": the values are shown and the researcher judges them.
 */
export function SweepGrid({ sweepId, refreshKey = 0 }: { sweepId: number; refreshKey?: number }) {
  const [grid, setGrid] = useState<Grid | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [metric, setMetric] = useState("");
  const [x, setX] = useState("");
  const [sort, setSort] = useState<{ kind: "param" | "metric"; key: string } | null>(null);
  const [dir, setDir] = useState<1 | -1>(1);

  useEffect(() => {
    let live = true;
    sweepGrid(sweepId, metric || undefined, x || undefined).then((g) => { if (live) { setGrid(g); setErr(null); } }).catch((e) => live && setErr(e instanceof Error ? e.message : "Could not load the grid."));
    return () => { live = false; };
  }, [sweepId, metric, x, refreshKey]);

  const rows = useMemo(() => (grid ? sortRows(grid.rows, sort, dir) : []), [grid, sort, dir]);
  if (err) return <p role="alert" className={s.err}>{err}</p>;
  if (!grid) return <p className={s.note} role="status">Reading the grid…</p>;
  const keys = grid.metrics.map((m) => m.key);
  const toggle = (kind: "param" | "metric", key: string) => {
    if (sort?.kind === kind && sort.key === key) setDir(dir === 1 ? -1 : 1);
    else { setSort({ kind, key }); setDir(1); }
  };
  const aria = (kind: "param" | "metric", key: string) => (sort?.kind === kind && sort.key === key ? (dir === 1 ? "ascending" : "descending") : "none");
  const arrow = (kind: "param" | "metric", key: string) => (sort?.kind === kind && sort.key === key ? (dir === 1 ? " ▲" : " ▼") : "");
  const xKey = x || grid.params[0]?.key || "";

  return (
    <section className={s.grid2} aria-label="Sweep grid">
      <div className={s.scroll}>
        <table className={s.table}>
          <caption className={s.cap}>One row per job, in parameter order. Metric values are what each run declared; a run that declared none, or a job without a run, shows no number.</caption>
          <thead>
            <tr>
              <th scope="col">Job</th>
              {grid.params.map((p) => (
                <th key={p.key} scope="col" aria-sort={aria("param", p.key)}><button type="button" className={s.sortBtn} onClick={() => toggle("param", p.key)}>{p.label || p.key}{arrow("param", p.key)}</button></th>
              ))}
              <th scope="col">State</th>
              {grid.metrics.map((m) => (
                <th key={m.key} scope="col" aria-sort={aria("metric", m.key)}>
                  <button type="button" className={s.sortBtn} onClick={() => toggle("metric", m.key)}>{m.key}{m.unit ? ` (${m.unit})` : ""}{arrow("metric", m.key)}</button>
                  {m.unitsDiffer ? <span className={s.warn}> units differ: {m.units.join(", ")}</span> : null}
                  {m.definitionsDiffer ? <span className={s.warn}> defined differently</span> : null}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.jobId}>
                <th scope="row" className={s.nm}>{r.name}</th>
                {grid.params.map((p) => <td key={p.key} className="mono">{r.values[p.key]}</td>)}
                <td>{r.state}</td>
                {keys.map((k) => <td key={k} className="mono">{cell(r, k)}</td>)}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {keys.length > 0 ? (
        <div className={s.plotBox}>
          <div className={s.row}>
            <Select label="Plot metric" value={metric} onChange={(e) => setMetric(e.target.value)}>
              <option value="">(choose)</option>
              {keys.map((k) => <option key={k} value={k}>{k}</option>)}
            </Select>
            {grid.params.length > 1 ? (
              <Select label="Against" value={xKey} onChange={(e) => setX(e.target.value)}>
                {grid.params.map((p) => <option key={p.key} value={p.key}>{p.label || p.key}</option>)}
              </Select>
            ) : null}
          </div>
          {grid.plot ? <Plot plot={grid.plot} /> : metric ? <p className={s.warn} role="note">{grid.plotNote}</p> : null}
        </div>
      ) : (
        <p className={s.note}>No run of this sweep has declared a metric yet. When runs are imported, their declared numbers appear here.</p>
      )}
    </section>
  );
}
