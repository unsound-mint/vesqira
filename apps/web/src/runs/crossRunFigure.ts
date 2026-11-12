import { createRunFigure } from "../api/boards";
import type { CrossRunDescription, Figure, RunFigureRequest } from "../api/boards";
import { listHosts } from "../api/hosts";
import { listJobs } from "../api/jobs";
import { convertRunValues } from "../api/runConvert";
import { listRuns } from "../api/runs";
import type { Run } from "../api/runs";
import { evaluateRuns } from "../catalog/runQuery";
import { buildPlot, conversionFrom } from "../catalog/runPlot";
import type { Axis, Conversion, PlotModel } from "../catalog/runPlot";
import { contextIndex, metricOf, paramOf } from "../catalog/runTable";
import { blobToBase64 } from "../viewer/figure";
import type { Redrawn } from "../viewer/redraw";
import { plotPng } from "./runPicture";

/*
 * A cross-run plot as a board figure (ADR 0065). The figure's picture is made from the same model the page draws; its description
 * (which runs, the recorded values) is written by the server from the catalog, and "Draw again" here reads the runs again, builds
 * the plot from the saved query and axes, asks the worker again for any conversion, and says what is different before anything is saved.
 */

/** What the figure writes on the picture: the query and how many runs are drawn (no time, so the same runs give the same pixels). */
export const footerOf = (query: string, drawn: number) => `${query.trim() === "" ? "all runs" : `query: ${query.trim()}`} · ${drawn} runs`;

/** The runs a plot draws, in id order. */
export function drawnRunIds(model: PlotModel): number[] {
  return [...new Set(model.panels.flatMap((p) => p.series.flatMap((s) => s.points.map((q) => q.runId))))].sort((a, b) => a - b);
}

export type RunPlotSpec = { query: string; y: string; x: string; xKind: Axis["kind"]; by: string };

/** The request that saves the plot: which runs, which axes, and the conversion with the factor the browser used (the server asks the worker again). */
export function figureRequest(model: PlotModel, spec: RunPlotSpec, conv: Conversion | null, text: { title: string; caption: string }, previewPng: string, redrawnFrom?: number): RunFigureRequest {
  const ids = drawnRunIds(model);
  const converted = new Set(model.panels.flatMap((p) => p.series.flatMap((s) => s.points.filter((q) => q.converted).map((q) => q.runId))));
  return {
    title: text.title,
    caption: text.caption,
    previewPng,
    query: spec.query,
    y: spec.y,
    x: spec.x,
    xKind: spec.xKind,
    by: spec.by,
    runIds: ids,
    conversion: conv && model.conversion && converted.size > 0 ? { to: conv.to, entries: [...converted].map((runId) => ({ runId, factor: conv.entries[runId]!.factor })) } : null,
    ...(redrawnFrom !== undefined ? { redrawnFrom } : {}),
  };
}

/** Saves the plot the page shows as a figure. The caller has drawn `model` from `spec` and `conv`. */
export async function saveRunFigure(model: PlotModel, spec: RunPlotSpec, conv: Conversion | null, text: { title: string; caption: string }): Promise<Figure> {
  const png = await plotPng(model, footerOf(spec.query, drawnRunIds(model).length));
  return createRunFigure(figureRequest(model, spec, conv, text, await blobToBase64(png)));
}

// ---- what changed since the figure was saved

export type RunChange = { runId: number; name: string; what: string };
export type PlotDiff = { added: RunChange[]; gone: RunChange[]; changed: RunChange[] };

const num = (x: number) => String(Number(x.toPrecision(6)));
const same = (a: string, b: string) => (a.trim() !== "" && b.trim() !== "" && Number.isFinite(Number(a)) && Number.isFinite(Number(b)) ? Number(a) === Number(b) : a === b);
const withUnit = (v: number, u: string) => `${num(v)}${u ? ` ${u}` : ""}`;

/**
 * Compares what the figure recorded for each run with what the catalog says now. A run is `added` when it is drawn now and was not,
 * `gone` when it was drawn and is not (with the reason: deleted, archived, no longer matching the filter, or no longer recording
 * what the plot needs) and `changed` when a number, a unit, a definition or the conversion differs. Nothing is judged to be better.
 */
export function diffPlot(saved: CrossRunDescription, model: PlotModel, runs: Run[], matched: Run[]): PlotDiff {
  const now = new Map(runs.map((r) => [r.id, r]));
  const matches = new Set(matched.map((r) => r.id));
  const drawn = new Set(drawnRunIds(model));
  const convertedNow = new Map(model.panels.flatMap((p) => p.series.flatMap((s) => s.points.filter((q) => q.converted).map((q) => [q.runId, q.converted!.fromUnit] as const))));
  const excluded = new Map(model.excluded.map((e) => [e.runId, e.reason]));
  const was = new Map(saved.runs.map((m) => [m.id, m]));
  const out: PlotDiff = { added: [], gone: [], changed: [] };
  for (const id of drawn) {
    if (!was.has(id)) out.added.push({ runId: id, name: now.get(id)?.name ?? `run ${id}`, what: "" });
  }
  for (const m of saved.runs) {
    const r = now.get(m.id);
    if (!drawn.has(m.id)) {
      const why = !r ? "no longer in the catalog" : r.archived ? "archived" : !matches.has(m.id) ? "no longer matches the filter" : (excluded.get(m.id) ?? "not drawn");
      out.gone.push({ runId: m.id, name: r?.name ?? m.name, what: why });
      continue;
    }
    const cur = metricOf(r!, saved.y);
    const parts: string[] = [];
    if (cur && cur.value !== null) {
      if (cur.value !== m.y.value || cur.unit !== m.y.unit) parts.push(`${saved.y} ${withUnit(m.y.value, m.y.unit)} → ${withUnit(cur.value, cur.unit)}`);
      if ((cur.uncertainty ?? null) !== (m.y.uncertainty ?? null) || cur.uncertaintyKind !== m.y.uncertaintyKind) parts.push("its uncertainty");
      if (cur.definition !== m.y.definition) parts.push("its definition");
    }
    const xNow = saved.xKind === "param" ? (paramOf(r!, saved.x)?.value ?? "") : (() => { const xm = metricOf(r!, saved.x); return xm && xm.value !== null ? String(xm.value) : ""; })();
    if (!same(xNow, m.x)) parts.push(`${saved.x} ${m.x} → ${xNow || "not recorded"}`);
    const wasUnit = m.converted ? m.y.unit : "";
    if ((convertedNow.get(m.id) ?? "") !== wasUnit) parts.push(m.converted ? "no longer converted" : `now converted from ${convertedNow.get(m.id)}`);
    if (parts.length > 0) out.changed.push({ runId: m.id, name: r!.name, what: parts.join("; ") });
  }
  return out;
}

const list = (xs: RunChange[], show: (x: RunChange) => string) => `${xs.slice(0, 5).map(show).join(", ")}${xs.length > 5 ? `, and ${xs.length - 5} more` : ""}`;
const runs_ = (n: number) => `${n} run${n === 1 ? "" : "s"}`;

/** The diff in sentences, in the order a researcher needs it: what no longer matches, what is new, what changed. Empty when nothing did. */
export function diffLines(d: PlotDiff): string[] {
  const out: string[] = [];
  if (d.gone.length > 0) out.push(`${runs_(d.gone.length)} drawn in the saved figure ${d.gone.length === 1 ? "is" : "are"} not drawn now: ${list(d.gone, (x) => `${x.name} (${x.what})`)}.`);
  if (d.added.length > 0) out.push(`${runs_(d.added.length)} now match${d.added.length === 1 ? "es" : ""} that did not when it was saved: ${list(d.added, (x) => x.name)}.`);
  if (d.changed.length > 0) out.push(`${runs_(d.changed.length)} record${d.changed.length === 1 ? "s" : ""} something different now: ${list(d.changed, (x) => `${x.name} (${x.what})`)}.`);
  return out;
}

// ---- Draw again

/**
 * Draws a saved cross-run plot again from the runs in the catalog now: the saved query and axes, the conversion asked of the worker
 * again (a stored factor is never reused), and the changes announced before anything is saved. It replaces nothing: it becomes a new,
 * linked figure only when the owner saves it.
 */
export async function redrawCrossRun(f: Figure): Promise<Redrawn> {
  const saved = f.scene as unknown as CrossRunDescription;
  const spec: RunPlotSpec = { query: saved.query, y: saved.y, x: saved.x, xKind: saved.xKind, by: saved.by };
  const [runs, jobs, hosts] = await Promise.all([listRuns({ archived: "all" }), listJobs(), listHosts().catch(() => [])]);
  const ctxOf = contextIndex(runs, jobs, (id) => hosts.find((h) => h.id === id)?.name);
  const matched = evaluateRuns(runs, spec.query, ctxOf).matched;
  const axis: Axis = { kind: spec.xKind, key: spec.x };
  let model = buildPlot(matched, spec.y, axis, spec.by);
  let conv: Conversion | null = null;
  const notes: string[] = [];
  if (saved.conversion) {
    const wanted = new Set(saved.conversion.from.map((x) => x.unit));
    for (const offer of model.offers.filter((o) => wanted.has(o.unit) && o.to === saved.conversion!.to)) {
      const res = await convertRunValues(offer.to, offer.values.map((v) => ({ value: v.value, uncertainty: v.uncertainty, from: offer.unit })));
      if (res.refused || !res.results) throw new Error(`The conversion this figure used is refused now, so it is not drawn: ${res.reason ?? "the worker refused it"}`);
      conv = conversionFrom(spec.y, offer, res, conv);
    }
    if (conv) model = buildPlot(matched, spec.y, axis, spec.by, conv);
    else notes.push(`The figure converted runs to ${saved.conversion.to}; no panel can be converted into that unit now, so nothing is converted.`);
  }
  const ids = drawnRunIds(model);
  if (ids.length === 0) throw new Error(`None of the runs that match “${spec.query || "all runs"}” record both ${spec.y} and ${spec.x} now, so there is nothing to draw.`);
  const diff = diffPlot(saved, model, runs, matched);
  const lines = diffLines(diff);
  notes.unshift(...(lines.length > 0 ? lines : ["The same runs with the same recorded values as when the figure was saved."]));
  const blob = await plotPng(model, footerOf(spec.query, ids.length));
  return {
    kind: "cross-run",
    blob,
    notes,
    dataChanged: lines.length > 0,
    svg: null,
    save: async () => createRunFigure(figureRequest(model, spec, conv, { title: f.title, caption: f.caption }, await blobToBase64(blob), f.id)),
  };
}
