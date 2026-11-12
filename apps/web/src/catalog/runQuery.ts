import type { Run } from "../api/runs";
import { archivedMatches, tokenize, unquote } from "../ui/queryWords";

/**
 * The one query model for runs (ADR 0062). The filter box, the runs table, the cross-run plot, the CSV header and a saved
 * view all read the same words, so what the table shows is exactly what the filter box says.
 *
 * A word is one of
 *   `qc:passed`                        the researcher's assessment
 *   `status:completed`  `host:name`    where the run came from (from its job; an imported run records neither)
 *   `generated>=2026-08-01`            dates, as UTC days: `generated:2026-08-01`, `imported:2026-08-01..2026-08-31`
 *   `block_size=32`  `noise>=0.3`      a parameter, and when no parameter has that name, a declared result
 *   `block_size:16..64`  `am:..0.2`    an inclusive range (either end may be left open)
 *   `am>0.3rad`                        a unit on a result: only a run that recorded the result in that unit is compared
 *   anything else                      searched in the name, kind, commit and input name
 *
 * Every word is judged "yes", "no", "missing" (the run has no such parameter or result, or did not record it) or "unit"
 * (the result is in another unit). Absence is never a value, so a run that is missing something a word asks about does not
 * match; `evaluateRuns` returns those runs apart so a screen can still show them and say why.
 */

export type RunContext = {
  /** The state of the job that made the run ("completed", "failed", ...), or "" for a run that is only imported. */
  status: string;
  /** The name of the host the job ran on, or "" when the run records none. */
  host: string;
};

export type Verdict = "yes" | "no" | "missing" | "unit";

type Op = "=" | "<" | "<=" | ">" | ">=";
type Numeric = { op: Op } & { n: number } | { lo: number | null; hi: number | null };
type Test = { num: Numeric; unit: string } | { text: string } | { invalid: true };

export type Condition =
  | { kind: "qc" | "status" | "host"; word: string; value: string }
  | { kind: "date"; word: string; field: "generated" | "imported"; day: Day }
  | { kind: "value"; word: string; key: string; test: Test }
  | { kind: "text"; word: string };

type Day = { op: Op } & { d: string } | { lo: string | null; hi: string | null } | null;

const NUMBER = String.raw`[+-]?(?:\d+(?:\.\d+)?|\.\d+)(?:e[+-]?\d+)?`;
const WITH_UNIT = new RegExp(`^(${NUMBER})(.*)$`, "i");
const DAY = /^\d{4}-\d{2}-\d{2}$/;
const WORD = /^([^<>=:]+)(<=|>=|=|<|>|:)(.*)$/;
const RESERVED = new Set(["qc", "status", "host", "generated", "imported", "archived"]);

const num = (s: string): number | null => {
  const n = Number(s);
  return s.trim() !== "" && Number.isFinite(n) ? n : null;
};

/** `16`, `0.3rad`, `1e3`, or `5 km/s` (a quoted unit may contain spaces) as a number and the unit written after it. */
function numberWithUnit(s: string): { n: number; unit: string } | null {
  const m = WITH_UNIT.exec(s.trim());
  const n = m ? num(m[1] ?? "") : null;
  return m && n !== null ? { n, unit: (m[2] ?? "").trim() } : null;
}

function numericTest(op: string, rest: string): Test {
  if (op === ":" || op === "=") {
    const r = rest.indexOf("..");
    if (r >= 0) {
      const a = rest.slice(0, r).trim();
      const b = numberWithUnit(rest.slice(r + 2));
      const lo = a === "" ? null : numberWithUnit(a);
      if ((a !== "" && !lo) || (rest.slice(r + 2).trim() !== "" && !b) || (lo === null && b === null) || (lo && lo.unit !== "")) return { invalid: true };
      if (lo !== null && b !== null && lo.n > b.n) return { invalid: true };
      return { num: { lo: lo?.n ?? null, hi: b?.n ?? null }, unit: b?.unit ?? "" };
    }
    const v = numberWithUnit(rest);
    return v ? { num: { op: "=", n: v.n }, unit: v.unit } : { text: rest.toLowerCase() };
  }
  const v = numberWithUnit(rest);
  return v ? { num: { op: op as Op, n: v.n }, unit: v.unit } : { invalid: true };
}

function dayTest(op: string, rest: string): Day {
  if (op === ":" || op === "=") {
    const r = rest.indexOf("..");
    if (r >= 0) {
      const a = rest.slice(0, r);
      const b = rest.slice(r + 2);
      if ((a !== "" && !DAY.test(a)) || (b !== "" && !DAY.test(b)) || (a === "" && b === "")) return null;
      return { lo: a || null, hi: b || null };
    }
    return DAY.test(rest) ? { op: "=", d: rest } : null;
  }
  return DAY.test(rest) ? { op: op as Op, d: rest } : null;
}

/** The words of a query as conditions, in order. `archived:` is not one: it chooses which entries are listed at all. */
export function parseRunQuery(q: string): Condition[] {
  const out: Condition[] = [];
  for (const w of tokenize(q)) {
    const raw = unquote(w);
    if (raw.toLowerCase().startsWith("archived:")) continue;
    const m = WORD.exec(raw);
    const key = (m?.[1] ?? "").trim().toLowerCase();
    if (!m || key === "") {
      out.push({ kind: "text", word: raw.toLowerCase() });
      continue;
    }
    const op = m[2] ?? "";
    const rest = (m[3] ?? "").trim();
    if (key === "qc" || key === "status" || key === "host") {
      if (op === ":") out.push({ kind: key, word: raw, value: rest.toLowerCase() });
      else out.push({ kind: "text", word: raw.toLowerCase() });
    } else if (key === "generated" || key === "imported") {
      out.push({ kind: "date", word: raw, field: key, day: dayTest(op, rest) });
    } else {
      out.push({ kind: "value", word: raw, key, test: rest === "" ? { invalid: true } : numericTest(op, rest) });
    }
  }
  return out;
}

/** The UTC day (`2026-08-01`) of a timestamp, or null when there is none or it does not read as one. */
export function dayOf(ts: string | undefined): string | null {
  if (!ts || ts.trim() === "") return null;
  const t = Date.parse(ts);
  return Number.isFinite(t) ? new Date(t).toISOString().slice(0, 10) : null;
}

const cmp = (a: number, op: Op, b: number) => (op === "=" ? a === b : op === "<" ? a < b : op === ">" ? a > b : op === "<=" ? a <= b : a >= b);
const within = (a: number, n: { lo: number | null; hi: number | null }) => (n.lo === null || a >= n.lo) && (n.hi === null || a <= n.hi);

function judgeDay(day: Day, d: string): boolean {
  if (day === null) return false;
  if ("op" in day) return day.op === "=" ? d === day.d : day.op === "<" ? d < day.d : day.op === ">" ? d > day.d : day.op === "<=" ? d <= day.d : d >= day.d;
  return (day.lo === null || d >= day.lo) && (day.hi === null || d <= day.hi);
}

function judgeNumber(a: number, t: { num: Numeric }): boolean {
  return "op" in t.num ? cmp(a, t.num.op, t.num.n) : within(a, t.num);
}

/** One word against one run. */
export function judge(r: Run, c: Condition, ctx: RunContext): Verdict {
  switch (c.kind) {
    case "qc":
      return r.review.qc === c.value ? "yes" : "no";
    case "status":
      return ctx.status === "" ? "missing" : ctx.status.toLowerCase() === c.value ? "yes" : "no";
    case "host":
      return ctx.host === "" ? "missing" : ctx.host.toLowerCase() === c.value ? "yes" : "no";
    case "date": {
      const d = dayOf(c.field === "generated" ? r.generatedAt : r.importedAt);
      if (d === null) return "missing";
      return judgeDay(c.day, d) ? "yes" : "no";
    }
    case "text":
      return [r.name, r.kind, r.gitCommit, r.inputName].join("\n").toLowerCase().includes(c.word) ? "yes" : "no";
    case "value": {
      if ("invalid" in c.test) return "no";
      const p = r.params.find((x) => x.key.toLowerCase() === c.key);
      const m = p ? undefined : r.metrics.find((x) => x.key.toLowerCase() === c.key);
      if (!p && !m) return "missing";
      if (m) {
        if (m.value === null || !Number.isFinite(m.value)) return "missing"; // an unrecorded result is unknown, not 0
        if ("text" in c.test) return "no";
        // A unit on the word is a promise about the number: a result recorded in another unit is not compared (no conversion is invented)
        if (c.test.unit !== "" && c.test.unit !== m.unit) return "unit";
        return judgeNumber(m.value, c.test) ? "yes" : "no";
      }
      const v = p?.value ?? "";
      if ("text" in c.test) return v.toLowerCase() === c.test.text ? "yes" : "no";
      if (c.test.unit !== "") return "unit"; // a parameter carries no unit to check against
      const a = num(v);
      return a !== null && judgeNumber(a, c.test) ? "yes" : "no";
    }
  }
}

const noContext: RunContext = { status: "", host: "" };

/** True when every word matches (and the run is not archived unless `archived:` asks for it). */
export function matchRunQuery(r: Run, q: string, ctx: RunContext = noContext): boolean {
  if (!archivedMatches(r.archived === true, q)) return false;
  return parseRunQuery(q).every((c) => judge(r, c, ctx) === "yes");
}

export type Evaluation = {
  /** Every word matched. */
  matched: Run[];
  /** No word said "no", but at least one asks about something the run did not record: shown apart, never matched. */
  notRecorded: { run: Run; words: string[] }[];
  /** Runs that recorded a result in another unit than the word names, and nothing else rules them out. */
  unitDiffers: { run: Run; words: string[] }[];
};

/** Splits the runs by what the query can say about them; the order of `runs` is kept inside each part. */
export function evaluateRuns(runs: Run[], q: string, ctxOf: (r: Run) => RunContext): Evaluation {
  const conds = parseRunQuery(q);
  const out: Evaluation = { matched: [], notRecorded: [], unitDiffers: [] };
  for (const r of runs) {
    if (!archivedMatches(r.archived === true, q)) continue;
    const ctx = ctxOf(r);
    const missing: string[] = [];
    const unit: string[] = [];
    let no = false;
    for (const c of conds) {
      const v = judge(r, c, ctx);
      if (v === "no") {
        no = true;
        break;
      }
      if (v === "missing") missing.push(c.word);
      else if (v === "unit") unit.push(c.word);
    }
    if (no) continue;
    if (unit.length > 0) out.unitDiffers.push({ run: r, words: unit });
    else if (missing.length > 0) out.notRecorded.push({ run: r, words: missing });
    else out.matched.push(r);
  }
  return out;
}

/** The keys a query asks about (parameters or results), in order and without repeats, for the table to show as columns. */
export function queriedKeys(q: string): string[] {
  const keys: string[] = [];
  for (const c of parseRunQuery(q)) if (c.kind === "value" && !RESERVED.has(c.key) && !keys.includes(c.key)) keys.push(c.key);
  return keys;
}
