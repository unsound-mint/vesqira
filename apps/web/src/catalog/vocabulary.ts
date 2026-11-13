import type { DatasetSummary } from "../api/catalog";
import type { Run } from "../api/runs";
import type { Vocabulary, VocabKey } from "../ui/FilterBox";
import { effectiveUnit } from "./unit";
import { dayOf } from "./runQuery";
import type { RunContext } from "./runQuery";

const TOP = 20;

/** A value the filter can carry: one with a space is written in quotes; one with an operator or a quote cannot be written. */
const wordy = (v: string) => v.trim() !== "" && !/[<>="]/.test(v);

type Tally = Map<string, number>;
const bump = (t: Tally, v: string) => t.set(v, (t.get(v) ?? 0) + 1);

/** Tallies per key, merged case-insensitively (the matcher compares keys that way), keeping the first spelling. */
class Keyed {
  private m = new Map<string, { key: string; tally: Tally }>();
  has(k: string) { return this.m.has(k.toLowerCase()); }
  add(k: string, v: string) {
    const e = this.m.get(k.toLowerCase()) ?? { key: k, tally: new Map() };
    this.m.set(k.toLowerCase(), e);
    bump(e.tally, v);
  }
  entries() { return [...this.m.values()]; }
}

function build(key: string, kind: VocabKey["kind"], tally: Tally, total: number, extra: Partial<VocabKey> = {}): VocabKey {
  const all = [...tally.entries()];
  const numeric = (kind === "setting" || kind === "result") && all.length > 0 && all.every(([v]) => v.trim() !== "" && Number.isFinite(Number(v)));
  const top = [...all].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])).slice(0, TOP);
  if (numeric) top.sort((a, b) => Number(a[0]) - Number(b[0]));
  const nums = numeric ? all.map(([v]) => Number(v)) : [];
  return {
    key,
    kind,
    count: all.reduce((n, [, c]) => n + c, 0),
    numeric,
    same: all.length === 1 && all[0]![1] === total,
    values: top.map(([value, count]) => ({ value, count })),
    range: numeric ? { min: Math.min(...nums), max: Math.max(...nums), n: all.length } : undefined,
    ...extra,
  };
}

/** Keys that narrow the list first; keys with the same value everywhere are offered last. */
const rank = (a: VocabKey, b: VocabKey) => Number(a.same) - Number(b.same) || b.count - a.count || a.key.localeCompare(b.key);

/** `archived:yes` is offered only when something is archived: the filter would otherwise suggest a list that is empty. */
function archivedVocab(items: { archived?: boolean }[]): VocabKey | null {
  const n = items.filter((x) => x.archived).length;
  if (n === 0) return null;
  const t: Tally = new Map([["yes", n]]);
  return build("archived", "archived", t, items.length, { sep: ":", labels: { yes: "archived (hidden by default)" } });
}

/**
 * What can be asked of a list of runs, from what actually occurs: parameter keys and values, declared result keys with
 * their recorded values only (an unrecorded result is not a value and is never offered), and the QC states in use
 * (including "not assessed", the explicit default state, so runs nobody has assessed can be found). A parameter hides a result of the same name,
 * as the matcher tests the parameter first.
 */
export function runVocabulary(runs: Run[], ctxOf?: (r: Run) => RunContext): Vocabulary {
  const settings = new Keyed();
  const results = new Keyed();
  const qc: Tally = new Map();
  for (const r of runs) {
    for (const p of r.params) if (wordy(p.value)) settings.add(p.key, p.value);
    bump(qc, r.review.qc);
  }
  for (const r of runs) {
    for (const m of r.metrics) {
      if (m.value === null || !Number.isFinite(m.value) || settings.has(m.key)) continue;
      results.add(m.key, String(m.value));
    }
  }
  const extra: VocabKey[] = [];
  if (ctxOf) {
    // Where a run came from (its job), and when: only what occurs is offered; a run that records none adds nothing
    const status: Tally = new Map();
    const host: Tally = new Map();
    for (const r of runs) {
      const c = ctxOf(r);
      if (c.status !== "" && wordy(c.status)) bump(status, c.status);
      if (c.host !== "" && wordy(c.host)) bump(host, c.host);
    }
    if (status.size > 0) extra.push(build("status", "status", status, runs.length, { sep: ":" }));
    if (host.size > 0) extra.push(build("host", "host", host, runs.length, { sep: ":" }));
  }
  for (const [key, pick] of [["generated", (r: Run) => r.generatedAt], ["imported", (r: Run) => r.importedAt]] as const) {
    const days: Tally = new Map();
    for (const r of runs) {
      const d = dayOf(pick(r));
      if (d) bump(days, d);
    }
    if (days.size > 0) {
      const k = build(key, "date", days, runs.length, { numeric: true });
      k.values.sort((a, b) => b.value.localeCompare(a.value));
      extra.push(k);
    }
  }
  const keys: VocabKey[] = [
    ...extra,
    ...settings.entries().map((e) => build(e.key.toLowerCase(), "setting", e.tally, runs.length)),
    ...results.entries().map((e) => build(e.key.toLowerCase(), "result", e.tally, runs.length)),
  ];
  if (qc.size > 0) keys.push(build("qc", "qc", qc, runs.length, { sep: ":", labels: { not_assessed: "not assessed" } }));
  const archivedKey = archivedVocab(runs);
  if (archivedKey) keys.push(archivedKey);
  return { keys: keys.sort(rank), names: runs.map((r) => r.name) };
}

/** What can be asked of a list of datasets: units as declared (and "no unit declared"), hosts and natures that occur. */
export function datasetVocabulary(datasets: DatasetSummary[], hosts: { id: number; name: string }[]): Vocabulary {
  const unit: Tally = new Map();
  const host: Tally = new Map();
  const nature: Tally = new Map();
  for (const d of datasets) {
    const u = effectiveUnit(d);
    if (u === "") bump(unit, "none");
    else if (wordy(u)) bump(unit, u);
    const h = hosts.find((x) => x.id === d.hostId)?.name;
    if (h && wordy(h)) bump(host, h);
    bump(nature, d.nature ? d.nature : "none");
  }
  const n = datasets.length;
  const keys = [
    build("unit", "unit", unit, n, { sep: ":", labels: { none: "no unit declared" } }),
    build("host", "host", host, n, { sep: ":" }),
    build("nature", "nature", nature, n, { sep: ":", labels: { none: "not declared" } }),
  ].filter((k) => k.values.length > 0);
  const archivedKey = archivedVocab(datasets);
  if (archivedKey) keys.push(archivedKey);
  return { keys: keys.sort(rank), names: datasets.map((d) => d.name) };
}
