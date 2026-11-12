import type { Run } from "../api/runs";
import type { Metric } from "../api/metrics";
import { KIND } from "../api/metrics";
import { metricOf, paramOf } from "./runTable";

/** What a plot axis shows: a parameter of the runs, or a declared result. */
export type Axis = { kind: "param" | "metric"; key: string };

/** Error bars are drawn only for an uncertainty whose kind says what the bar means; "" is a series drawn without bars. */
export type BarKind = "" | "standard_deviation" | "standard_error" | "confidence_68" | "confidence_95";
export const BAR_KINDS: BarKind[] = ["standard_deviation", "standard_error", "confidence_68", "confidence_95"];

export const NO_CATEGORY = "not recorded";

export type PlotPoint = {
  runId: number;
  name: string;
  /** Position on a numeric axis, or the index of the category on a categorical one. */
  x: number;
  xText: string;
  y: number;
  /** The half-width of the bar, only when the series' kind allows one. */
  err: number | null;
  /** The result was inferred by a rule rather than stated by the run: drawn hollow. */
  inferred: boolean;
  category: string;
};

export type PlotSeries = { id: string; category: string; categoryIndex: number; barKind: BarKind; points: PlotPoint[] };

export type PlotPanel = {
  id: string;
  /** Results recorded in different units or defined differently get their own axis; nothing converts between them. */
  unit: string;
  definition: string;
  /** The panel the others are measured against (the one with the most runs); the rest are "not directly comparable". */
  primary: boolean;
  /** Why this panel is not drawn with the primary one, in words. Empty for the primary panel. */
  apart: string;
  series: PlotSeries[];
  count: number;
};

export type Exclusion = { runId: number; name: string; reason: string };

export type PlotModel = {
  y: string;
  x: Axis;
  by: string;
  xUnit: string;
  xNumeric: boolean;
  xCategories: string[];
  categories: string[];
  panels: PlotPanel[];
  excluded: Exclusion[];
  /** Kinds of bar present, in a fixed order, for the legend. */
  barKinds: BarKind[];
  /** Points whose uncertainty is not drawn because its kind is not stated (or "other"). */
  barsWithheld: number;
  anyInferred: boolean;
  anyDeclared: boolean;
  /** The x axis is a result with an uncertainty of its own; it is not drawn. */
  xUncertaintyNotDrawn: boolean;
};

const SEP = "\u0000";
const natural = (a: string, b: string) => a.localeCompare(b, undefined, { numeric: true });
const quantity = (m: Metric) => `${m.unit}${SEP}${m.definition}`;
const unitName = (u: string) => u || "no stated unit";

export function barText(k: BarKind): string {
  return k === "" ? "no bars" : `± ${KIND[k] ?? k}`;
}

/** Result keys that some run recorded, sorted. */
export function metricKeys(runs: Run[]): string[] {
  const seen = new Map<string, string>();
  for (const r of runs) for (const m of r.metrics) if (m.value !== null && !seen.has(m.key.toLowerCase())) seen.set(m.key.toLowerCase(), m.key);
  return [...seen.values()].sort(natural);
}

/** Parameter keys that some run recorded, sorted. */
export function paramKeys(runs: Run[]): string[] {
  const seen = new Map<string, string>();
  for (const r of runs) for (const p of r.params) if (!seen.has(p.key.toLowerCase())) seen.set(p.key.toLowerCase(), p.key);
  return [...seen.values()].sort(natural);
}

type Cand = { r: Run; ym: Metric & { value: number }; xv: string; xm?: Metric & { value: number } };

/**
 * Points of one result over the given runs. What it will not do:
 *  - put results of different units, or different definitions, on one axis: each (unit, definition) gets its own panel,
 *    the largest first, the rest labelled "not directly comparable" (there is no conversion for run results to use);
 *  - draw a bar whose kind is not stated: a series has one kind of bar, and a point without a usable one is drawn without;
 *  - hide a run: one that cannot be placed is listed in `excluded` with the reason.
 */
export function buildPlot(runs: Run[], y: string, x: Axis, by: string): PlotModel {
  const excluded: Exclusion[] = [];
  const out = (r: Run, reason: string) => excluded.push({ runId: r.id, name: r.name, reason });
  const recorded = (m: Metric | undefined): m is Metric & { value: number } => !!m && m.value !== null && Number.isFinite(m.value);

  const cands: Cand[] = [];
  for (const r of runs) {
    const ym = metricOf(r, y);
    if (!recorded(ym)) {
      out(r, `${y} not recorded`);
      continue;
    }
    if (x.kind === "param") {
      const p = paramOf(r, x.key);
      if (!p || p.value === "") out(r, `${x.key} not recorded`);
      else cands.push({ r, ym, xv: p.value });
    } else {
      const xm = metricOf(r, x.key);
      if (!recorded(xm)) out(r, `${x.key} not recorded`);
      else cands.push({ r, ym, xv: String(xm.value), xm });
    }
  }

  // An x that is itself a result must be one quantity on every point: keep the most common (unit, definition).
  let kept = cands;
  let xUnit = "";
  if (x.kind === "metric") {
    const tally = new Map<string, number>();
    for (const c of cands) tally.set(quantity(c.xm!), (tally.get(quantity(c.xm!)) ?? 0) + 1);
    const top = [...tally.entries()].sort((a, b) => b[1] - a[1])[0]?.[0];
    kept = [];
    for (const c of cands) {
      if (quantity(c.xm!) === top) kept.push(c);
      else out(c.r, `${x.key} is recorded in ${unitName(c.xm!.unit)}${c.xm!.definition === "" ? " with no definition" : ""}: not directly comparable with the other runs`);
    }
    xUnit = top ? (top.split(SEP)[0] ?? "") : "";
  }

  const xNumeric = kept.length > 0 && kept.every((c) => c.xv.trim() !== "" && Number.isFinite(Number(c.xv)));
  const xCategories = xNumeric ? [] : [...new Set(kept.map((c) => c.xv))].sort(natural);

  const groups = new Map<string, Cand[]>();
  for (const c of kept) groups.set(quantity(c.ym), [...(groups.get(quantity(c.ym)) ?? []), c]);
  const ordered = [...groups.entries()].sort((a, b) => b[1].length - a[1].length);
  const [mainUnit = "", mainDef = ""] = (ordered[0]?.[0] ?? "").split(SEP);

  const catOf = (r: Run) => (by === "" ? "" : paramOf(r, by)?.value || NO_CATEGORY);
  const categories = by === "" ? [""] : [...new Set(kept.map((c) => catOf(c.r)))].sort((a, b) => (a === NO_CATEGORY ? 1 : b === NO_CATEGORY ? -1 : natural(a, b)));
  const catIndex = new Map(categories.map((c, i) => [c, i]));

  let barsWithheld = 0;
  let anyInferred = false;
  let anyDeclared = false;
  const kinds = new Set<BarKind>();
  const panels: PlotPanel[] = ordered.map(([k, cs], i) => {
    const [unit = "", definition = ""] = k.split(SEP);
    const series = new Map<string, PlotSeries>();
    for (const c of cs) {
      const u = c.ym.uncertainty;
      const kind: BarKind = u !== null && u >= 0 && BAR_KINDS.includes(c.ym.uncertaintyKind as BarKind) ? (c.ym.uncertaintyKind as BarKind) : "";
      if (u !== null && kind === "") barsWithheld += 1;
      if (kind !== "") kinds.add(kind);
      const cat = catOf(c.r);
      const id = `${i}|${cat}|${kind}`;
      const s = series.get(id) ?? { id, category: cat, categoryIndex: catIndex.get(cat) ?? 0, barKind: kind, points: [] };
      series.set(id, s);
      const inferred = (c.ym.status as string) === "inferred";
      if (inferred) anyInferred = true;
      else anyDeclared = true;
      s.points.push({ runId: c.r.id, name: c.r.name, x: xNumeric ? Number(c.xv) : xCategories.indexOf(c.xv), xText: c.xv, y: c.ym.value, err: kind === "" ? null : u, inferred, category: cat });
    }
    const why: string[] = [];
    if (i > 0) {
      if (unit !== mainUnit) why.push(`recorded in ${unitName(unit)}, not ${unitName(mainUnit)}`);
      if (definition !== mainDef) why.push(definition === "" ? "no definition stated" : "defined differently");
    }
    return {
      id: `panel-${i}`,
      unit,
      definition,
      primary: i === 0,
      apart: why.join("; "),
      count: cs.length,
      series: [...series.values()].sort((a, b) => a.categoryIndex - b.categoryIndex || BAR_KINDS.indexOf(a.barKind) - BAR_KINDS.indexOf(b.barKind)),
    };
  });

  return {
    y,
    x,
    by,
    xUnit,
    xNumeric,
    xCategories,
    categories: by === "" ? [] : categories,
    panels,
    excluded,
    barKinds: BAR_KINDS.filter((k) => kinds.has(k)),
    barsWithheld,
    anyInferred,
    anyDeclared,
    xUncertaintyNotDrawn: x.kind === "metric" && kept.some((c) => c.xm!.uncertainty !== null),
  };
}

/** Round tick values covering [lo, hi]: the first is at or below lo and the last at or above hi. */
export function niceTicks(lo: number, hi: number, target = 5): number[] {
  if (!Number.isFinite(lo) || !Number.isFinite(hi)) return [];
  if (lo === hi) {
    const d = lo === 0 ? 1 : Math.abs(lo) * 0.1;
    lo -= d;
    hi += d;
  }
  const raw = (hi - lo) / Math.max(1, target);
  const mag = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 5, 10].map((m) => m * mag).find((s) => s >= raw) ?? 10 * mag;
  const ticks: number[] = [];
  for (let t = Math.floor(lo / step) * step; t < hi + step * 1e-9; t += step) ticks.push(Number(t.toPrecision(12)));
  if (ticks[ticks.length - 1]! < hi) ticks.push(Number(((ticks[ticks.length - 1] ?? 0) + step).toPrecision(12)));
  return ticks;
}
