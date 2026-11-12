import { forwardRef, useMemo } from "react";
import type { KeyboardEvent, ReactNode } from "react";
import { barText, niceTicks } from "../catalog/runPlot";
import type { BarKind, PlotModel, PlotPanel, PlotPoint } from "../catalog/runPlot";

/*
 * Everything inside the <svg> is styled inline with the page's tokens (var(--...)), so the exported SVG and PNG are one
 * self-contained drawing (runExport.resolveVars replaces the tokens by their values). Colour is never the only cue: each
 * category has its own marker shape, a result inferred by a rule is hollow, and each kind of error bar has its own line.
 */

const W = 720;
const L = 68;
const R = 20;
const PH = 190;
const PANEL = 24 + PH + 48;
// Five colours against six shapes, so a (colour, shape) pair repeats only after thirty categories.
const COLOURS = ["var(--accent)", "var(--warn)", "var(--ok)", "var(--text)", "var(--text-2)"];
const FONT = "var(--font-ui)";
const NUM = "var(--font-data)";
const MARK = 5;

export const SHAPES = ["circle", "square", "triangle", "diamond", "cross", "triangle-down"] as const;

const num = (x: number) => String(Number(x.toPrecision(5)));

function Marker({ shape, x, y, colour, hollow, r = MARK }: { shape: number; x: number; y: number; colour: string; hollow: boolean; r?: number }) {
  const common = { stroke: colour, strokeWidth: 1.8, fill: hollow ? "var(--bg-1)" : colour };
  switch (SHAPES[shape % SHAPES.length]) {
    case "square":
      return <rect x={x - r} y={y - r} width={2 * r} height={2 * r} {...common} />;
    case "triangle":
      return <polygon points={`${x},${y - r - 1} ${x + r + 1},${y + r} ${x - r - 1},${y + r}`} {...common} />;
    case "triangle-down":
      return <polygon points={`${x},${y + r + 1} ${x + r + 1},${y - r} ${x - r - 1},${y - r}`} {...common} />;
    case "diamond":
      return <polygon points={`${x},${y - r - 1} ${x + r + 1},${y} ${x},${y + r + 1} ${x - r - 1},${y}`} {...common} />;
    case "cross":
      return <path d={`M${x - r},${y - r}L${x + r},${y + r}M${x - r},${y + r}L${x + r},${y - r}`} stroke={colour} strokeWidth={2.2} fill="none" strokeDasharray={hollow ? "3 1.5" : undefined} />;
    default:
      return <circle cx={x} cy={y} r={r} {...common} />;
  }
}

const BAR_DASH: Record<BarKind, string | undefined> = { "": undefined, standard_deviation: undefined, standard_error: "1.5 2.5", confidence_68: "6 3", confidence_95: "9 3 2 3" };
const BAR_CAP: Record<BarKind, number> = { "": 0, standard_deviation: 4, standard_error: 0, confidence_68: 4, confidence_95: 4 };

type Geometry = { px: (x: number) => number; py: (y: number) => number; ticksY: number[]; ticksX: { v: number; label: string }[]; top: number };

function geometry(model: PlotModel, panel: PlotPanel, index: number): Geometry {
  const top = index * PANEL + 24;
  const ys: number[] = [];
  // x is the same quantity in every panel, so they share one x range; only y differs by unit
  const xs: number[] = model.panels.flatMap((q) => q.series.flatMap((s) => s.points.map((p) => p.x)));
  for (const s of panel.series) for (const p of s.points) ys.push(p.y - (p.err ?? 0), p.y + (p.err ?? 0));
  const ticksY = niceTicks(Math.min(...ys), Math.max(...ys), 4);
  const lo = ticksY[0] ?? 0;
  const hi = ticksY[ticksY.length - 1] ?? 1;
  const py = (y: number) => top + PH - ((y - lo) / (hi - lo || 1)) * PH;
  if (model.xNumeric) {
    const t = niceTicks(Math.min(...xs), Math.max(...xs), 6);
    const a = t[0] ?? 0;
    const b = t[t.length - 1] ?? 1;
    return { top, py, ticksY, px: (x) => L + ((x - a) / (b - a || 1)) * (W - L - R), ticksX: t.map((v) => ({ v, label: num(v) })) };
  }
  const k = Math.max(1, model.xCategories.length);
  const px = (x: number) => L + ((x + 0.5) / k) * (W - L - R);
  return { top, py, ticksY, px, ticksX: model.xCategories.map((c, i) => ({ v: i, label: c.length > 14 ? `${c.slice(0, 13)}…` : c })) };
}

export type PlotProps = {
  model: PlotModel;
  selected: number | null;
  onSelect: (runId: number | null) => void;
  onOpen: (runId: number) => void;
  /** The filter words and how many runs are drawn, written into the figure so a saved image says what it shows. */
  footer: string;
};

const clip = (s: string, n: number) => (s.length > n ? `${s.slice(0, n - 1)}…` : s);

/** A legend row laid out left to right, wrapping when it would pass the right edge. Returns the nodes and the height used. */
function legendRows(items: { key: string; width: number; node: (x: number, y: number) => ReactNode }[], y0: number): { nodes: ReactNode[]; height: number } {
  const nodes: ReactNode[] = [];
  let x = L;
  let y = y0;
  for (const it of items) {
    if (x + it.width > W - R && x > L) {
      x = L;
      y += 18;
    }
    nodes.push(<g key={it.key}>{it.node(x, y)}</g>);
    x += it.width + 14;
  }
  return { nodes, height: items.length === 0 ? 0 : y - y0 + 18 };
}

export const RunsPlot = forwardRef<SVGSVGElement, PlotProps>(function RunsPlot({ model, selected, onSelect, onOpen, footer }, ref) {
  const geo = useMemo(() => model.panels.map((p, i) => geometry(model, p, i)), [model]);
  const order = useMemo(() => model.panels.flatMap((p) => p.series.flatMap((s) => s.points)).sort((a, b) => a.x - b.x || a.y - b.y), [model]);
  const colourOf = (cat: number) => COLOURS[cat % COLOURS.length]!;
  const hasCategories = model.categories.length > 0;

  const legend = useMemo(() => {
    const items: { key: string; width: number; node: (x: number, y: number) => ReactNode }[] = [];
    if (hasCategories) {
      model.categories.forEach((c, i) => {
        const label = clip(c, 24);
        items.push({ key: `c${i}`, width: 16 + label.length * 6.6, node: (x, y) => (<><Marker shape={i} x={x + 6} y={y - 4} colour={colourOf(i)} hollow={false} /><text x={x + 16} y={y} fontSize={11.5} fontFamily={FONT} fill="var(--text)">{label}</text></>) });
      });
    }
    for (const k of model.barKinds) {
      const label = barText(k);
      items.push({ key: `b${k}`, width: 40 + label.length * 6.6, node: (x, y) => (<><line x1={x} x2={x + 30} y1={y - 4} y2={y - 4} stroke="var(--text)" strokeWidth={1.6} strokeDasharray={BAR_DASH[k]} /><text x={x + 36} y={y} fontSize={11.5} fontFamily={FONT} fill="var(--text)">{label}</text></>) });
    }
    if (model.anyDeclared && model.anyInferred) {
      items.push({ key: "decl", width: 190, node: (x, y) => (<><Marker shape={0} x={x + 6} y={y - 4} colour="var(--text)" hollow={false} /><text x={x + 16} y={y} fontSize={11.5} fontFamily={FONT} fill="var(--text)">filled: stated by the run</text></>) });
      items.push({ key: "inf", width: 230, node: (x, y) => (<><Marker shape={0} x={x + 6} y={y - 4} colour="var(--text)" hollow /><text x={x + 16} y={y} fontSize={11.5} fontFamily={FONT} fill="var(--text)">hollow: inferred by a rule</text></>) });
    } else if (model.anyInferred) {
      items.push({ key: "inf", width: 260, node: (x, y) => (<><Marker shape={0} x={x + 6} y={y - 4} colour="var(--text)" hollow /><text x={x + 16} y={y} fontSize={11.5} fontFamily={FONT} fill="var(--text)">hollow: inferred by a rule, not stated</text></>) });
    } else if (model.anyDeclared) {
      items.push({ key: "decl", width: 270, node: (x, y) => (<text x={x} y={y} fontSize={11.5} fontFamily={FONT} fill="var(--text-2)">filled: every result is stated by its run</text>) });
    }
    return items;
  }, [model, hasCategories]); // eslint-disable-line react-hooks/exhaustive-deps

  const legendTop = model.panels.length * PANEL + 8;
  const rows = legendRows(legend, legendTop + 12);
  const notes: string[] = [];
  if (model.barsWithheld > 0) notes.push(`${model.barsWithheld} point${model.barsWithheld === 1 ? " has" : "s have"} an uncertainty whose kind is not stated: it is not drawn as a bar.`);
  if (model.xUncertaintyNotDrawn) notes.push(`${model.x.key} has uncertainties of its own: not drawn on the x axis.`);
  const notesTop = legendTop + 12 + rows.height + 4;
  const footerTop = notesTop + notes.length * 16 + 10;
  const H = footerTop + 22;

  function onKey(e: KeyboardEvent<SVGSVGElement>) {
    if (e.key === "Escape") return onSelect(null);
    if (e.key === "Enter" && selected !== null) {
      e.preventDefault();
      return onOpen(selected);
    }
    if (e.key !== "ArrowRight" && e.key !== "ArrowLeft" && e.key !== "Home" && e.key !== "End") return;
    if (order.length === 0) return;
    e.preventDefault();
    const at = order.findIndex((p) => p.runId === selected);
    const next = e.key === "Home" ? 0 : e.key === "End" ? order.length - 1 : at < 0 ? (e.key === "ArrowRight" ? 0 : order.length - 1) : Math.min(order.length - 1, Math.max(0, at + (e.key === "ArrowRight" ? 1 : -1)));
    onSelect(order[next]!.runId);
  }

  const yTitle = (p: PlotPanel) => `${model.y}${p.unit ? ` (${p.unit})` : ""}`;
  const xTitle = `${model.x.key}${model.xUnit ? ` (${model.xUnit})` : ""}`;
  const label = `${yTitle(model.panels[0] ?? { unit: "" } as PlotPanel)} against ${xTitle}, ${order.length} runs. Use the left and right arrow keys to move between runs and Enter to open one; the same values are in the table.`;

  const point = (p: PlotPoint, g: Geometry, s: { categoryIndex: number; barKind: BarKind }) => {
    const x = g.px(p.x);
    const y = g.py(p.y);
    const on = p.runId === selected;
    const c = colourOf(hasCategories ? s.categoryIndex : 0);
    const cap = BAR_CAP[s.barKind];
    return (
      <g key={`${p.runId}`} data-run={p.runId} data-testid="plot-point" data-selected={on || undefined} data-inferred={p.inferred || undefined} onClick={() => onSelect(p.runId)} onDoubleClick={() => onOpen(p.runId)} style={{ cursor: "pointer" }}>
        <title>{`${p.name}: ${model.y} = ${num(p.y)}${p.err !== null ? ` ${barText(s.barKind)} ${num(p.err)}` : ""}; ${model.x.key} = ${p.xText}${p.inferred ? " (inferred)" : ""}`}</title>
        <circle cx={x} cy={y} r={11} fill="transparent" />
        {p.err !== null ? (
          <g stroke={c} strokeWidth={1.5} strokeDasharray={BAR_DASH[s.barKind]} opacity={0.9}>
            <line x1={x} x2={x} y1={g.py(p.y - p.err)} y2={g.py(p.y + p.err)} />
            {cap > 0 ? <path d={`M${x - cap},${g.py(p.y - p.err)}H${x + cap}M${x - cap},${g.py(p.y + p.err)}H${x + cap}`} strokeDasharray="none" /> : null}
          </g>
        ) : null}
        {on ? <circle cx={x} cy={y} r={MARK + 6} fill="none" stroke="var(--text)" strokeWidth={2} /> : null}
        <Marker shape={hasCategories ? s.categoryIndex : 0} x={x} y={y} colour={c} hollow={p.inferred} />
      </g>
    );
  };

  return (
    <svg ref={ref} data-testid="runs-plot" viewBox={`0 0 ${W} ${H}`} width="100%" role="group" aria-label={label} tabIndex={0} onKeyDown={onKey} style={{ display: "block", maxWidth: W * 1.6, background: "var(--bg-1)", fontFamily: FONT }}>
      <rect x={0} y={0} width={W} height={H} fill="var(--bg-1)" />
      {model.panels.map((panel, i) => {
        const g = geo[i]!;
        return (
          <g key={panel.id} data-panel={panel.id}>
            <text x={L} y={g.top - 8} fontSize={12.5} fontWeight={600} fontFamily={FONT} fill="var(--text)">{yTitle(panel)}</text>
            {!panel.primary ? <text x={L + 8 + (yTitle(panel).length * 7.4)} y={g.top - 8} fontSize={12} fontFamily={FONT} fill="var(--warn)">not directly comparable: {panel.apart}</text> : null}
            {panel.primary && panel.definition === "" ? <text x={L + 8 + (yTitle(panel).length * 7.4)} y={g.top - 8} fontSize={12} fontFamily={FONT} fill="var(--warn)">definition not stated: not shown as comparable</text> : null}
            {g.ticksY.map((v) => (
              <g key={v}>
                <line x1={L} x2={W - R} y1={g.py(v)} y2={g.py(v)} stroke="var(--line)" strokeWidth={1} />
                <text x={L - 8} y={g.py(v) + 4} textAnchor="end" fontSize={11} fontFamily={NUM} fill="var(--text-2)">{num(v)}</text>
              </g>
            ))}
            <line x1={L} x2={W - R} y1={g.top + PH} y2={g.top + PH} stroke="var(--line-strong)" />
            {g.ticksX.map((t) => (
              <g key={t.v}>
                <line x1={g.px(t.v)} x2={g.px(t.v)} y1={g.top + PH} y2={g.top + PH + 4} stroke="var(--line-strong)" />
                <text x={g.px(t.v)} y={g.top + PH + 17} textAnchor="middle" fontSize={11} fontFamily={NUM} fill="var(--text-2)">{t.label}</text>
              </g>
            ))}
            <text x={(L + W - R) / 2} y={g.top + PH + 36} textAnchor="middle" fontSize={12} fontFamily={FONT} fill="var(--text-2)">{xTitle}</text>
            {panel.series.map((s) => <g key={s.id} data-series={s.id}>{s.points.map((p) => point(p, g, s))}</g>)}
          </g>
        );
      })}
      {rows.nodes}
      {notes.map((n, i) => <text key={n} x={L} y={notesTop + 12 + i * 16} fontSize={11.5} fontFamily={FONT} fill="var(--text-2)">{n}</text>)}
      <text x={L} y={footerTop + 12} fontSize={10.5} fontFamily={NUM} fill="var(--text-3)">{clip(footer, 130)}</text>
    </svg>
  );
});
