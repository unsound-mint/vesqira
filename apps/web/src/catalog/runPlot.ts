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
  /** Present when the point was converted into the panel's unit: what the run recorded, which is never replaced. */
  converted?: { fromUnit: string; value: number; uncertainty: number | null };
};

export type PlotSeries = { id: string; category: string; categoryIndex: number; barKind: BarKind; points: PlotPoint[] };

export type PlotPanel = {
  id: string;
  /** Results recorded in different units or defined differently get their own axis; they merge only by a conversion the researcher asked for. */
  unit: string;
  definition: string;
  /** The panel the others are measured against (the one with the most runs); the rest are "not directly comparable". */
  primary: boolean;
  /** Why this panel is not drawn with the primary one, in words. Empty for the primary panel. */
  apart: string;
  series: PlotSeries[];
  count: number;
  /** The units whose runs were converted into this panel, in words; empty when none were. */
  convertedFrom: string[];
};

/**
 * A conversion the researcher asked for (ADR 0062, amendment): runs that recorded `y` in another unit, expressed in `to` by the
 * worker's plain unit conversion. Held per run with what the run recorded, so a value that changed since is never converted by
 * a stale entry. It holds numbers, so a saved view never carries it.
 */
export type Conversion = {
  y: string;
  to: string;
  method: string;
  entries: Record<number, { definition: string; unit: string; value: number; uncertainty: number | null; factor: number; out: { value: number; uncertainty: number | null } }>;
};

/** A panel that differs from the main one only by its unit, which the worker may be able to convert. */
export type ConvertOffer = { unit: string; to: string; definition: string; values: { runId: number; value: number; uncertainty: number | null }[] };

/** The conversion entry that still describes this run's recorded result, or undefined when there is none or the run changed since. */
export function convertedOf(conv: Conversion | null | undefined, run: Run): Conversion["entries"][number] | undefined {
  if (!conv) return undefined;
  const e = conv.entries[run.id];
  const m = metricOf(run, conv.y);
  if (!e || !m || m.value !== e.value || m.unit !== e.unit || m.definition !== e.definition || m.uncertainty !== e.uncertainty) return undefined;
  return e;
}

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
  /** Panels that differ from the main one only by unit, each of which the researcher may ask to have converted. */
  offers: ConvertOffer[];
  /** What was converted into the main panel, for the note, the legend and the provenance; null when nothing was. */
  conversion: { to: string; method: string; from: { unit: string; factor: number; count: number }[] } | null;
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

type Cand = { r: Run; ym: Metric & { value: number }; xv: string; xm?: Metric & { value: number }; converted?: NonNullable<PlotPoint["converted"]> & { factor: number } };

/**
 * Points of one result over the given runs. What it will not do:
 *  - put results of different units, or different definitions, on one axis: each (unit, definition) gets its own panel,
 *    the largest first, the rest labelled "not directly comparable". The one exception is a run in `conv`, which the researcher
 *    asked to have converted by the worker's plain unit conversion: it joins the panel of the target unit and is marked
 *    converted, with what it recorded kept on the point. A panel differing only by unit is offered for conversion (`offers`);
 *  - draw a bar whose kind is not stated: a series has one kind of bar, and a point without a usable one is drawn without;
 *  - hide a run: one that cannot be placed is listed in `excluded` with the reason.
 */
export function buildPlot(runs: Run[], y: string, x: Axis, by: string, conv: Conversion | null = null): PlotModel {
  const excluded: Exclusion[] = [];
  const out = (r: Run, reason: string) => excluded.push({ runId: r.id, name: r.name, reason });
  const recorded = (m: Metric | undefined): m is Metric & { value: number } => !!m && m.value !== null && Number.isFinite(m.value);

  const cands: Cand[] = [];
  for (const r of runs) {
    const rec = metricOf(r, y);
    if (!recorded(rec)) {
      out(r, `${y} not recorded`);
      continue;
    }
    // a run the researcher had converted is drawn as the converted number in the target unit, never as a recorded one
    const e = conv && conv.y === y ? convertedOf(conv, r) : undefined;
    const ym = e && conv ? { ...rec, value: e.out.value, uncertainty: e.out.uncertainty, unit: conv.to } : rec;
    const converted = e ? { fromUnit: e.unit, value: e.value, uncertainty: e.uncertainty, factor: e.factor } : undefined;
    if (x.kind === "param") {
      const p = paramOf(r, x.key);
      if (!p || p.value === "") out(r, `${x.key} not recorded`);
      else cands.push({ r, ym, xv: p.value, converted });
    } else {
      const xm = metricOf(r, x.key);
      if (!recorded(xm)) out(r, `${x.key} not recorded`);
      else cands.push({ r, ym, xv: String(xm.value), xm, converted });
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
      s.points.push({ runId: c.r.id, name: c.r.name, x: xNumeric ? Number(c.xv) : xCategories.indexOf(c.xv), xText: c.xv, y: c.ym.value, err: kind === "" ? null : u, inferred, category: cat, ...(c.converted ? { converted: { fromUnit: c.converted.fromUnit, value: c.converted.value, uncertainty: c.converted.uncertainty } } : {}) });
    }
    const why: string[] = [];
    if (i > 0) {
      if (unit !== mainUnit) why.push(`unit differs: ${unit || "none stated"} here, ${mainUnit || "none stated"} in the main panel`);
      if (definition !== mainDef) why.push(definition === "" ? "no definition stated" : "defined differently");
    }
    return {
      id: `panel-${i}`,
      unit,
      definition,
      primary: i === 0,
      apart: why.join("; "),
      count: cs.length,
      convertedFrom: [...new Set(cs.flatMap((c) => (c.converted ? [c.converted.fromUnit] : [])))],
      series: [...series.values()].sort((a, b) => a.categoryIndex - b.categoryIndex || BAR_KINDS.indexOf(a.barKind) - BAR_KINDS.indexOf(b.barKind)),
    };
  });

  // A panel that differs from the main one only by its unit may be converted into it; a differing or missing definition may not
  const offers: ConvertOffer[] = [];
  if (mainUnit !== "" && mainDef !== "") {
    ordered.forEach(([k, cs], i) => {
      const [unit = "", definition = ""] = k.split(SEP);
      if (i > 0 && unit !== "" && unit !== mainUnit && definition === mainDef) {
        offers.push({ unit, to: mainUnit, definition, values: cs.map((c) => ({ runId: c.r.id, value: c.ym.value, uncertainty: c.ym.uncertainty })) });
      }
    });
  }
  const mergedFrom = new Map<string, { factor: number; count: number }>();
  for (const c of kept) {
    if (!c.converted) continue;
    const t = mergedFrom.get(c.converted.fromUnit) ?? { factor: c.converted.factor, count: 0 };
    mergedFrom.set(c.converted.fromUnit, { factor: t.factor, count: t.count + 1 });
  }

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
    offers,
    conversion: conv && mergedFrom.size > 0 ? { to: conv.to, method: conv.method, from: [...mergedFrom.entries()].map(([unit, v]) => ({ unit, ...v })) } : null,
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
