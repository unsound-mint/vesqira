import { EM } from "../catalog/format";
import { metricText } from "../api/metrics";
import type { Metric } from "../api/metrics";

export type ParamDiff = { key: string; a: string; b: string };

/** Parameters recorded differently by two runs (or recorded by only one). Equal settings are not listed. */
export function paramDiff<A extends { key: string; value: string }, B extends { key: string; value: string }>(a: A[], b: B[]): ParamDiff[] {
  const ma = new Map(a.map((p) => [p.key, p.value]));
  const mb = new Map(b.map((p) => [p.key, p.value]));
  const keys = [...new Set([...ma.keys(), ...mb.keys()])].sort();
  return keys.filter((k) => ma.get(k) !== mb.get(k)).map((k) => ({ key: k, a: ma.get(k) ?? EM, b: mb.get(k) ?? EM }));
}

export type MetricRow = { key: string; a: string; b: string; basis: string };

/**
 * The results two runs report, side by side. Whether two numbers may be compared depends on what they measure, so each row
 * says if both runs gave the same definition; the same name with different or missing definitions is not offered as comparable.
 */
export function metricRows(a: Metric[], b: Metric[]): MetricRow[] {
  const ma = new Map(a.map((m) => [m.key, m]));
  const mb = new Map(b.map((m) => [m.key, m]));
  return [...new Set([...ma.keys(), ...mb.keys()])].sort().map((key) => {
    const x = ma.get(key);
    const y = mb.get(key);
    let basis = "only one run reports it";
    if (x && y) {
      if (!x.definition || !y.definition) basis = "definition not stated by both: not shown as comparable";
      else if (x.definition !== y.definition) basis = "definitions differ: not directly comparable";
      else if (x.unit !== y.unit) basis = "units differ: not directly comparable";
      else basis = "same definition and unit";
    }
    return { key, a: x ? metricText(x) : EM, b: y ? metricText(y) : EM, basis };
  });
}
