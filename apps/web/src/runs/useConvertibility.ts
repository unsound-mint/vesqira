import { useEffect, useMemo, useReducer } from "react";
import { probeRunConversions } from "../api/runConvert";

/**
 * Whether each offered conversion will work, asked of the worker before a button is shown (ADR 0062, amendment). A pair is asked
 * once per session: the answer is kept per (recorded unit, target unit), because it depends on the two unit names and nothing else,
 * and every pair the plot still lacks goes in one request. Drawing never waits on it: until the answer is in, a pair has no button
 * and no note, so nothing appears and then goes away.
 */
export type Convertibility = { state: "pending" } | { state: "yes" } | { state: "no"; reason: string } | { state: "unknown" };
type Pair = { unit: string; to: string };

const SEP = "\u0000";
const answers = new Map<string, { ok: true } | { ok: false; reason: string }>();
const asking = new Map<string, Promise<void>>();
const keyOf = (unit: string, to: string) => `${unit}${SEP}${to}`;

/** Forgets what was learned. For tests: a real session keeps its answers. */
export function resetConvertibility(): void {
  answers.clear();
  asking.clear();
}

export function useConvertibility(offers: Pair[]): (o: Pair) => Convertibility {
  const [, bump] = useReducer((n: number) => n + 1, 0);
  const wanted = useMemo(() => [...new Set(offers.map((o) => keyOf(o.unit, o.to)))].sort(), [offers]);
  const wantedKey = wanted.join("\u0001");
  // pairs whose request failed: not remembered across plots, so the next plot asks again
  const failed = useMemo(() => new Set<string>(), [wantedKey]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    let alive = true;
    const missing = wanted.filter((k) => !answers.has(k) && !asking.has(k));
    if (missing.length > 0) {
      const p = probeRunConversions(missing.map((k) => { const [from = "", to = ""] = k.split(SEP); return { from, to }; }))
        .then((got) => {
          for (const g of got) answers.set(keyOf(g.from, g.to), g.refused ? { ok: false, reason: g.reason ?? "the worker refused it" } : { ok: true });
        })
        .catch(() => { missing.forEach((k) => failed.add(k)); })
        .finally(() => missing.forEach((k) => asking.delete(k)));
      missing.forEach((k) => asking.set(k, p));
    }
    for (const p of new Set(wanted.flatMap((k) => (asking.has(k) ? [asking.get(k)!] : [])))) void p.then(() => { if (alive) bump(); });
    return () => { alive = false; };
  }, [wantedKey]); // eslint-disable-line react-hooks/exhaustive-deps

  return (o) => {
    const k = keyOf(o.unit, o.to);
    const a = answers.get(k);
    if (a) return a.ok ? { state: "yes" } : { state: "no", reason: a.reason };
    return failed.has(k) && !asking.has(k) ? { state: "unknown" } : { state: "pending" };
  };
}
