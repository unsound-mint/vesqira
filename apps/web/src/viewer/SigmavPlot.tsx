import { forwardRef, useMemo } from "react";
import type { KeyboardEvent } from "react";
import { niceTicks } from "../catalog/runPlot";
import type { RohsaSigmav } from "../api/rohsa";
import { lut } from "./colormaps";
import { axisLabels, cellRange, cellsOf, key, logScale, rangeText, scaleT, weightingText } from "./sigmav";

/*
 * The sigma-v diagram: component width (rows, up) against centroid velocity (columns), one rectangle per non-empty cell, coloured by
 * the log10 of its summed weight. Everything inside the <svg> is styled inline with the page's tokens (var(--...)), so the exported
 * SVG and PNG are one self-contained drawing (runExport.resolveVars replaces the tokens by their values). Colour is never the only
 * cue: every cell carries its numbers in its own <title>, a cell with contributors but no weight above zero is marked with a cross
 * (never confused with an empty one), the selected cell has a heavy outline, and a table of the heaviest cells sits beside the plot.
 */

const W = 380;
const L = 56;
const R = 14;
const T = 62;
const PH = 210;
const FONT = "var(--font-ui)";
const NUM = "var(--font-data)";
const num = (v: number) => String(Number(v.toPrecision(4)));
const rgb = (c: Uint8ClampedArray, t: number) => {
  const i = Math.round(t * 255) * 3;
  return `rgb(${c[i]},${c[i + 1]},${c[i + 2]})`;
};

export type CellRef = { s: number; v: number };

type Props = {
  d: RohsaSigmav;
  run: string;
  cut: number | null;
  cursor: CellRef | null;
  selected: CellRef | null;
  onCursor?: (c: CellRef) => void;
  onSelect?: (c: CellRef) => void;
  /** Extra lines drawn under the plot (the exported picture states the bins, the counts and the fit). */
  footer?: string[];
};

/** Words in lines of at most `cols` characters (a word longer than that keeps its own line). */
export function wrap(text: string, cols: number): string[] {
  const out: string[] = [];
  let line = "";
  for (const w of text.split(/\s+/)) {
    if (line !== "" && line.length + 1 + w.length > cols) {
      out.push(line);
      line = w;
    } else line = line === "" ? w : `${line} ${w}`;
  }
  return line === "" ? out : [...out, line];
}

/** The lines a picture of the diagram states under itself: bins, what is outside, what is left out, the fit and the cut. */
export function footerLines(d: RohsaSigmav, cut: number | null): string[] {
  const u = axisLabels(d).unit;
  const ve = d.v_edges;
  const se = d.sigma_edges;
  return wrap(footerText(d, cut, u, ve, se), 74);
}

function footerText(d: RohsaSigmav, cut: number | null, u: string, ve: number[], se: number[]): string {
  const lines = [
    `bins: ${ve.length - 1} centre × ${se.length - 1} width, half-open [lo, hi); centre ${num(ve[0]!)} to ${num(ve[ve.length - 1]!)} ${u}, σ ${num(se[0]!)} to ${num(se[se.length - 1]!)} ${u}`,
    `${d.in_diagram} of ${d.components} components in the diagram; ${d.outside} outside its limits, ${d.invalid_sigma} with σ not positive (left out)`,
    `fit ${d.dat.name ?? "?"} · crop ${d.cropName ?? "?"}${d.unit_source && d.unit_source !== "file" ? ` · brightness unit ${d.unit_source}` : ""}`,
  ];
  if (cut !== null && u === "km/s") lines.push(`dashed line: width cut σ = ${num(cut)} km/s (narrow is σ < cut)`);
  return lines.join(" | ");
}

export const SigmavPlot = forwardRef<SVGSVGElement, Props>(function SigmavPlot({ d, run, cut, cursor, selected, onCursor, onSelect, footer }, ref) {
  const cells = useMemo(() => cellsOf(d), [d]);
  const scale = useMemo(() => logScale(cells.values()), [cells]);
  const map = useMemo(() => lut("viridis"), []);
  const ax = axisLabels(d);
  const nv = d.v_edges.length - 1;
  const ns = d.sigma_edges.length - 1;
  const v0 = d.v_edges[0]!;
  const v1 = d.v_edges[nv]!;
  const s0 = d.sigma_edges[0]!;
  const s1 = d.sigma_edges[ns]!;
  const PW = W - L - R;
  const px = (v: number) => L + ((v - v0) / (v1 - v0)) * PW;
  const py = (s: number) => T + PH - ((s - s0) / (s1 - s0)) * PH;
  const xt = niceTicks(v0, v1, 5).filter((t) => t >= v0 - 1e-9 && t <= v1 + 1e-9);
  const yt = niceTicks(s0, s1, 4).filter((t) => t >= s0 - 1e-9 && t <= s1 + 1e-9);
  const BAR_Y = T + PH + 44;
  const lines = footer ?? [];
  const H = BAR_Y + 34 + lines.length * 13 + 6;
  const cellTitle = (s: number, v: number) => {
    const c = cells.get(key(s, v));
    const head = `σ ${rangeText(cellRange(d.sigma_edges, s))} ${ax.unit}, centre ${rangeText(cellRange(d.v_edges, v))} ${ax.unit}`;
    return c ? `${head}: ${c.count} contributor${c.count === 1 ? "" : "s"}, weight ${num(c.weight)} ${d.weight_unit}` : `${head}: no component`;
  };
  const move = (e: KeyboardEvent<SVGSVGElement>) => {
    const cur = cursor ?? { s: 0, v: 0 };
    const step: Record<string, [number, number]> = { ArrowRight: [0, 1], ArrowLeft: [0, -1], ArrowUp: [1, 0], ArrowDown: [-1, 0] };
    const st = step[e.key];
    if (st) {
      e.preventDefault();
      onCursor?.({ s: Math.min(ns - 1, Math.max(0, cur.s + st[0])), v: Math.min(nv - 1, Math.max(0, cur.v + st[1])) });
    } else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onSelect?.(cur);
    }
  };
  const label = `Sigma-v diagram of ${run}, ${weightingText(d)}, colour is log10 of the summed weight per cell. ${cells.size} cells with components. ${selected ? cellTitle(selected.s, selected.v) : "Arrow keys move between cells; Enter selects one."}`;
  const outline = (c: CellRef, w: number, dash?: string) => (
    <rect x={px(d.v_edges[c.v]!)} y={py(d.sigma_edges[c.s + 1]!)} width={px(d.v_edges[c.v + 1]!) - px(d.v_edges[c.v]!)} height={py(d.sigma_edges[c.s]!) - py(d.sigma_edges[c.s + 1]!)} fill="none" stroke="var(--text)" strokeWidth={w} strokeDasharray={dash} pointerEvents="none" />
  );
  return (
    <svg
      ref={ref}
      viewBox={`0 0 ${W} ${H}`}
      width="100%"
      role="application"
      aria-label={label}
      tabIndex={0}
      onKeyDown={move}
      style={{ display: "block", background: "var(--bg-2)", border: "var(--bw) solid var(--line)", borderRadius: "var(--r-md)", fontFamily: FONT }}
    >
      <text x={L} y={16} fontSize={12} fontWeight={600} fill="var(--text)">{`σ–v diagram · ${run}`}</text>
      <text x={L} y={31} fontSize={10.5} fill="var(--text-2)">{weightingText(d)}</text>
      <text x={L} y={44} fontSize={10.5} fill="var(--text-2)">colour: log₁₀ of the summed weight per cell (scale below)</text>
      <rect x={L} y={T} width={PW} height={PH} fill="var(--bg-1)" stroke="var(--line-strong)" />
      {cells.size === 0 ? <text x={L + PW / 2} y={T + PH / 2} textAnchor="middle" fontSize={11} fill="var(--text-2)">No component falls inside these bins.</text> : null}
      {[...cells.values()].map((c) => {
        const x = px(d.v_edges[c.v]!);
        const y = py(d.sigma_edges[c.s + 1]!);
        const w = px(d.v_edges[c.v + 1]!) - x;
        const h = py(d.sigma_edges[c.s]!) - y;
        const onScale = c.weight > 0 && scale !== null;
        return (
          <g key={`${c.s},${c.v}`} data-cell={`${c.s},${c.v}`} onClick={() => { onCursor?.({ s: c.s, v: c.v }); onSelect?.({ s: c.s, v: c.v }); }} style={{ cursor: onSelect ? "pointer" : undefined }}>
            <title>{cellTitle(c.s, c.v)}</title>
            <rect x={x} y={y} width={w} height={h} fill={onScale ? rgb(map, scaleT(scale!, c.weight)) : "var(--bg-2)"} stroke="var(--bg-1)" strokeWidth={0.5} />
            {!onScale ? <path d={`M${x + 1},${y + 1}L${x + w - 1},${y + h - 1}M${x + 1},${y + h - 1}L${x + w - 1},${y + 1}`} stroke="var(--text-2)" strokeWidth={1} /> : null}
          </g>
        );
      })}
      {cut !== null && ax.unit === "km/s" && cut > s0 && cut < s1 ? (
        <g pointerEvents="none">
          <line x1={L} x2={L + PW} y1={py(cut)} y2={py(cut)} stroke="var(--text)" strokeWidth={1.25} strokeDasharray="5 3" />
          <text x={L + PW - 3} y={py(cut) - 3} textAnchor="end" fontSize={9.5} fill="var(--text)">{`cut σ = ${num(cut)} km/s`}</text>
        </g>
      ) : null}
      {cursor && !(selected && selected.s === cursor.s && selected.v === cursor.v) ? outline(cursor, 1.25, "3 2") : null}
      {selected ? outline(selected, 2.25) : null}
      {xt.map((t) => (
        <g key={`x${t}`}>
          <line x1={px(t)} x2={px(t)} y1={T + PH} y2={T + PH + 4} stroke="var(--line-strong)" />
          <text x={px(t)} y={T + PH + 15} textAnchor="middle" fontSize={10} fill="var(--text-2)" fontFamily={NUM}>{num(t)}</text>
        </g>
      ))}
      {yt.map((t) => (
        <g key={`y${t}`}>
          <line x1={L - 4} x2={L} y1={py(t)} y2={py(t)} stroke="var(--line-strong)" />
          <text x={L - 7} y={py(t) + 3.5} textAnchor="end" fontSize={10} fill="var(--text-2)" fontFamily={NUM}>{num(t)}</text>
        </g>
      ))}
      <text x={L + PW / 2} y={T + PH + 30} textAnchor="middle" fontSize={11} fill="var(--text)">{ax.x}</text>
      <text transform={`translate(13 ${T + PH / 2}) rotate(-90)`} textAnchor="middle" fontSize={11} fill="var(--text)">{ax.y}</text>
      {/* the colour bar: the log scale, stated in decades of the weight's own unit */}
      {scale ? (
        <g>
          {Array.from({ length: 64 }, (_, i) => <rect key={i} x={L + (i * PW) / 64} y={BAR_Y} width={PW / 64 + 0.5} height={9} fill={rgb(map, i / 63)} />)}
          <rect x={L} y={BAR_Y} width={PW} height={9} fill="none" stroke="var(--line-strong)" />
          {scale.decades.map((k) => {
            const x = L + ((k - scale.lo) / (scale.hi === scale.lo ? 1 : scale.hi - scale.lo)) * PW;
            return x >= L - 0.5 && x <= L + PW + 0.5 ? (
              <g key={k}>
                <line x1={x} x2={x} y1={BAR_Y + 9} y2={BAR_Y + 13} stroke="var(--line-strong)" />
                <text x={x} y={BAR_Y + 23} textAnchor="middle" fontSize={9.5} fill="var(--text-2)" fontFamily={NUM}>{`10^${k}`}</text>
              </g>
            ) : null;
          })}
          <text x={L} y={BAR_Y - 4} fontSize={10} fill="var(--text-2)">{`summed weight per cell (${d.weight_unit}), log scale: ${num(10 ** scale.lo)} to ${num(10 ** scale.hi)}`}</text>
        </g>
      ) : (
        <text x={L} y={BAR_Y + 8} fontSize={10} fill="var(--text-2)">No cell has a weight above zero, so there is no log scale; cells are marked ×.</text>
      )}
      {lines.map((t, i) => <text key={i} x={L} y={BAR_Y + 38 + i * 13} fontSize={9.5} fill="var(--text-2)">{t}</text>)}
    </svg>
  );
});
