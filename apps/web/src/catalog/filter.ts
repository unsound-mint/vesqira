import type { Dataset } from "../api/catalog";
import type { Run } from "../api/runs";

/** Splits a query into words; every word must match (so more words narrow the list). */
const words = (q: string) => q.toLowerCase().split(/\s+/).filter(Boolean);

/**
 * A dataset matches when every word is found in its name, host, brightness unit or declared nature. `unit:K`, `host:name`
 * and `nature:observed` test that field exactly instead; `unit:none` and `nature:none` match a dataset that declares none
 * (a missing unit is not a unit, and is not matched by any unit value).
 */
export function matchDataset(d: Dataset, q: string, hostName: string | undefined): boolean {
  const unit = String(d.header?.bunit ?? "").trim().toLowerCase();
  const host = (hostName ?? "").toLowerCase();
  const nature = (d.nature ?? "").toLowerCase();
  const hay = [d.name, host, unit, nature].join("\n").toLowerCase();
  return words(q).every((w) => {
    const field = /^(unit|host|nature):(.+)$/.exec(w);
    if (!field) return hay.includes(w);
    const key = field[1];
    const want = field[2];
    const have = key === "unit" ? unit : key === "host" ? host : nature;
    return want === "none" && key !== "host" ? have === "" : have !== "" && have === want;
  });
}

const OPS = ["<=", ">=", "=", "<", ">"] as const;

/**
 * A run matches when every word does. A word like `ker=8` or `noise>=0.3` is tested against the run's parameters (as
 * numbers when both sides are numbers, otherwise as text for `=`); `qc:passed` tests the author's assessment;
 * anything else is searched in the name, kind, commit and input name. The same words test the run's declared results when
 * no parameter has that name (`am>0.3`). A parameter or result the run does not have, or one it did not record, never
 * matches: absence is not a value.
 */
export function matchRun(r: Run, q: string): boolean {
  return words(q).every((w) => {
    if (w.startsWith("qc:")) return r.review.qc === w.slice(3);
    for (const op of OPS) {
      const i = w.indexOf(op);
      if (i <= 0) continue;
      const key = w.slice(0, i);
      const want = w.slice(i + op.length);
      const p = r.params.find((x) => x.key.toLowerCase() === key);
      const m = p ? undefined : r.metrics.find((x) => x.key.toLowerCase() === key);
      if ((!p && !m) || want === "") return false;
      if (m) {
        const b = Number(want);
        if (m.value === null || !Number.isFinite(b)) return false; // an unrecorded result is unknown, not 0
        return op === "=" ? m.value === b : op === "<" ? m.value < b : op === ">" ? m.value > b : op === "<=" ? m.value <= b : m.value >= b;
      }
      if (!p) return false;
      const a = Number(p.value);
      const b = Number(want);
      if (Number.isFinite(a) && Number.isFinite(b)) return op === "=" ? a === b : op === "<" ? a < b : op === ">" ? a > b : op === "<=" ? a <= b : a >= b;
      return op === "=" && p.value.toLowerCase() === want;
    }
    return [r.name, r.kind, r.gitCommit, r.inputName].join("\n").toLowerCase().includes(w);
  });
}
