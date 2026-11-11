import type { Dataset } from "../api/catalog";
import type { Run } from "../api/runs";

import { tokenize, unquote } from "../ui/queryWords";
import { effectiveUnit } from "./unit";

export { tokenize, unquote };

/** Splits a query into words (`unit:"km / s"` is one word, quotes stripped); every word must match, so more words narrow the list. */
const words = (q: string) => tokenize(q).map((w) => unquote(w).toLowerCase());

/**
 * A dataset matches when every word is found in its name, host, brightness unit or declared nature. `unit:K`, `host:name`
 * and `nature:observed` test that field exactly instead; `unit:none` and `nature:none` match a dataset that declares none
 * (a missing unit is not a unit, and is not matched by any unit value).
 */
export function matchDataset(d: Dataset, q: string, hostName: string | undefined): boolean {
  if (!archivedMatches(d.archived === true, q)) return false;
  const unit = effectiveUnit(d).toLowerCase();
  const host = (hostName ?? "").toLowerCase();
  const nature = (d.nature ?? "").toLowerCase();
  const hay = [d.name, host, unit, nature].join("\n").toLowerCase();
  return words(q).filter((w) => !w.startsWith("archived:")).every((w) => {
    const field = /^(unit|host|nature):(.+)$/.exec(w);
    if (!field) return hay.includes(w);
    const key = field[1];
    const want = field[2];
    const have = key === "unit" ? unit : key === "host" ? host : nature;
    return want === "none" && key !== "host" ? have === "" : have !== "" && have === want;
  });
}

/**
 * Archived entries are hidden unless the query asks: `archived:yes` shows only the archived ones, `archived:no` (the
 * default) only the others, `archived:all` both. The word is a filter, not text to search for.
 */
export function archivedMatches(archived: boolean, q: string): boolean {
  const w = words(q).find((x) => x.startsWith("archived:"));
  const want = w?.slice("archived:".length);
  if (want === "all") return true;
  return want === "yes" ? archived : !archived;
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
  if (!archivedMatches(r.archived === true, q)) return false;
  return words(q).filter((w) => !w.startsWith("archived:")).every((w) => {
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
