import type { Run } from "../api/runs";
import type { Metric } from "../api/metrics";
import type { Job } from "../api/jobs";
import { dayOf } from "./runQuery";
import type { RunContext } from "./runQuery";

/** A column of the runs table: a fixed one, or a parameter (`p:block_size`) or declared result (`m:am`) the researcher exposes. */
export type ColumnId = string;

export const FIXED_COLUMNS: { id: ColumnId; label: string }[] = [
  { id: "status", label: "Status" },
  { id: "host", label: "Host" },
  { id: "generated", label: "Generated" },
  { id: "imported", label: "Imported" },
  { id: "qc", label: "QC" },
  { id: "kind", label: "Kind" },
];
export const DEFAULT_COLUMNS: ColumnId[] = ["status", "host", "generated", "imported"];

/** A cell the table can say something about: a number (sorted as one), text, or nothing recorded. Missing is never 0 and never "". */
export type Cell = { kind: "num"; n: number; text: string; metric?: Metric } | { kind: "text"; text: string } | { kind: "missing" };

export const NOT_RECORDED = "not recorded";

const prec = (x: number) => String(Number(x.toPrecision(6)));

/** The status and host of a run come from the job that made it (looked up once, not per keystroke); a run that was only imported records neither. */
export function contextIndex(runs: Run[], jobs: Job[], hostName: (id: number) => string | undefined): (r: Run) => RunContext {
  const byId = new Map(jobs.map((j) => [j.id, j]));
  const byRun = new Map<number, Job>();
  for (const j of jobs) if (j.runId != null) byRun.set(j.runId, j);
  const cache = new Map<number, RunContext>();
  for (const r of runs) {
    const job = (r.jobId != null ? byId.get(r.jobId) : undefined) ?? byRun.get(r.id);
    cache.set(r.id, job ? { status: job.state, host: hostName(job.hostId) ?? "" } : { status: "", host: "" });
  }
  return (r) => cache.get(r.id) ?? { status: "", host: "" };
}

const lowerKey = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

export const paramOf = (r: Run, key: string) => r.params.find((p) => lowerKey(p.key, key));
export const metricOf = (r: Run, key: string) => r.metrics.find((m) => lowerKey(m.key, key));

const stamp = (ts: string): Cell => {
  const d = dayOf(ts);
  if (d === null) return { kind: "missing" };
  const t = new Date(Date.parse(ts)).toISOString();
  return { kind: "num", n: Date.parse(ts), text: `${t.slice(0, 10)} ${t.slice(11, 16)}Z` };
};

export function cellOf(r: Run, col: ColumnId, ctx: RunContext): Cell {
  if (col === "name") return { kind: "text", text: r.name };
  if (col === "status") return ctx.status === "" ? { kind: "missing" } : { kind: "text", text: ctx.status };
  if (col === "host") return ctx.host === "" ? { kind: "missing" } : { kind: "text", text: ctx.host };
  if (col === "generated") return stamp(r.generatedAt);
  if (col === "imported") return stamp(r.importedAt);
  if (col === "qc") return { kind: "text", text: r.review.qc.replace(/_/g, " ") };
  if (col === "kind") return r.kind === "" ? { kind: "missing" } : { kind: "text", text: r.kind };
  if (col.startsWith("p:")) {
    const p = paramOf(r, col.slice(2));
    if (!p || p.value === "") return { kind: "missing" };
    const n = Number(p.value);
    return Number.isFinite(n) ? { kind: "num", n, text: p.value } : { kind: "text", text: p.value };
  }
  if (col.startsWith("m:")) {
    const m = metricOf(r, col.slice(2));
    if (!m || m.value === null || !Number.isFinite(m.value)) return { kind: "missing" };
    return { kind: "num", n: m.value, text: `${prec(m.value)}${m.unit ? ` ${m.unit}` : ""}`, metric: m };
  }
  return { kind: "missing" };
}

/**
 * Sorts by a column. A run without a value always goes last, in either direction, and keeps its place among the others
 * without one (the order it came in). Numbers sort as numbers when every recorded value is one; a result recorded in
 * several units sorts by unit first, since 0.3 rad and 0.3 deg are not the same size.
 */
export function sortRuns(runs: Run[], col: ColumnId, dir: 1 | -1, ctxOf: (r: Run) => RunContext): Run[] {
  const cells = new Map<number, Cell>();
  for (const r of runs) cells.set(r.id, cellOf(r, col, ctxOf(r)));
  const have = [...cells.values()].filter((c) => c.kind !== "missing");
  const numeric = have.length > 0 && have.every((c) => c.kind === "num");
  const unitOf = (c: Cell) => (c.kind === "num" ? (c.metric?.unit ?? "") : "");
  const units = new Set(have.map(unitOf));
  const order = new Map(runs.map((r, i) => [r.id, i]));
  return [...runs].sort((a, b) => {
    const x = cells.get(a.id)!;
    const y = cells.get(b.id)!;
    if (x.kind === "missing" || y.kind === "missing") {
      if (x.kind === y.kind) return order.get(a.id)! - order.get(b.id)!;
      return x.kind === "missing" ? 1 : -1;
    }
    let c = 0;
    if (units.size > 1) c = unitOf(x).localeCompare(unitOf(y)) * dir;
    if (c === 0) {
      if (numeric && x.kind === "num" && y.kind === "num") c = (x.n - y.n) * dir;
      else c = x.text.localeCompare(y.text, undefined, { numeric: true }) * dir;
    }
    return c !== 0 ? c : order.get(a.id)! - order.get(b.id)!;
  });
}

/** The parameter and result keys that occur among the runs, each with how many runs record it, most recorded first. */
export function columnChoices(runs: Run[]): { params: { key: string; count: number }[]; metrics: { key: string; count: number; units: string[] }[] } {
  const params = new Map<string, { key: string; count: number }>();
  const metrics = new Map<string, { key: string; count: number; units: Set<string> }>();
  for (const r of runs) {
    for (const p of r.params) {
      const e = params.get(p.key.toLowerCase()) ?? { key: p.key, count: 0 };
      e.count += 1;
      params.set(p.key.toLowerCase(), e);
    }
    for (const m of r.metrics) {
      if (m.value === null) continue;
      const e = metrics.get(m.key.toLowerCase()) ?? { key: m.key, count: 0, units: new Set<string>() };
      e.count += 1;
      e.units.add(m.unit);
      metrics.set(m.key.toLowerCase(), e);
    }
  }
  const by = (a: { key: string; count: number }, b: { key: string; count: number }) => b.count - a.count || a.key.localeCompare(b.key);
  return { params: [...params.values()].sort(by), metrics: [...metrics.values()].map((e) => ({ ...e, units: [...e.units] })).sort(by) };
}
