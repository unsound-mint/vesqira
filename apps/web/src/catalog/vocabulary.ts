import type { Dataset } from "../api/catalog";
import type { Run } from "../api/runs";
import type { Vocabulary, VocabKey } from "../ui/FilterBox";

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

/**
 * What can be asked of a list of runs, from what actually occurs: parameter keys and values, declared result keys with
 * their recorded values only (an unrecorded result is not a value and is never offered), and the QC states in use
 * (including "not assessed", the explicit default state, so runs nobody has assessed can be found). A parameter hides a result of the same name,
 * as the matcher tests the parameter first.
 */
export function runVocabulary(runs: Run[]): Vocabulary {
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
  const keys: VocabKey[] = [
    ...settings.entries().map((e) => build(e.key.toLowerCase(), "setting", e.tally, runs.length)),
    ...results.entries().map((e) => build(e.key.toLowerCase(), "result", e.tally, runs.length)),
  ];
  if (qc.size > 0) keys.push(build("qc", "qc", qc, runs.length, { sep: ":", labels: { not_assessed: "not assessed" } }));
  return { keys: keys.sort(rank), names: runs.map((r) => r.name) };
}

/** What can be asked of a list of datasets: units as declared (and "no unit declared"), hosts and natures that occur. */
export function datasetVocabulary(datasets: Dataset[], hosts: { id: number; name: string }[]): Vocabulary {
  const unit: Tally = new Map();
  const host: Tally = new Map();
  const nature: Tally = new Map();
  for (const d of datasets) {
    const u = String(d.header?.bunit ?? "").trim();
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
  return { keys: keys.sort(rank), names: datasets.map((d) => d.name) };
}
