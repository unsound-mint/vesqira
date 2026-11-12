import type { Dataset } from "../api/catalog";
import { archivedMatches, tokenize, unquote } from "../ui/queryWords";
import { matchRunQuery } from "./runQuery";
import { effectiveUnit } from "./unit";

export { archivedMatches, tokenize, unquote };

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
 * A run matches when every word does (the query model is in runQuery.ts): `ker=8`, `noise>=0.3`, `block_size:16..64`,
 * `am>0.3` against the run's parameters and then its declared results, `qc:passed`, `status:`, `host:`, `generated>=2026-08-01`;
 * anything else is searched in the name, kind, commit and input name. A parameter or result the run does not have, or one it
 * did not record, never matches: absence is not a value.
 */
export const matchRun = matchRunQuery;
