import type { Preflight, Snapshot } from "../api/boards";
import { lineText, summary } from "./rounds";
import s from "./BoardsView.module.css";

/** What one round changed since the round before it, for the author's history of a board's snapshots (ADR 0073). */
export function RoundChangesList({ sn }: { sn: Snapshot }) {
  const c = sn.changes;
  if (!sn.round || sn.round < 2 || !c) return null;
  return (
    <details className={s.note}>
      <summary>Changed since round {sn.round - 1}, {new Date(c.since).toLocaleDateString()} ({c.lines.length} change{c.lines.length === 1 ? "" : "s"})</summary>
      {c.lines.length > 0 ? <ul className={s.comments}>{c.lines.map((l, i) => <li key={i}>{lineText(l)}</li>)}</ul> : <p className={s.note}>No figure or text differs from the round before.</p>}
      {c.carried > 0 ? <p className={s.note}>{c.carried} earlier comment{c.carried === 1 ? " is" : "s are"} shown beside the same figure in this round.</p> : null}
    </details>
  );
}

/** Before publishing: which round this will be and what it will say changed. Nothing is made yet. */
export function PreflightRound({ pre }: { pre: Preflight }) {
  const round = pre.frozen.round;
  if (!round) return null;
  const since = round.since;
  if (!since) return <p className={s.note}>This will be round {round.number}, the first of this board.</p>;
  return (
    <div className={s.note}>
      <p className={s.note}>This will be round {round.number}. Since round {round.number - 1} ({new Date(since.at).toLocaleDateString()}): {summary(since.items, since.removedCount)}.</p>
      <ul className={s.comments} aria-label="Changes since the last round">{since.lines.map((l, i) => <li key={i}>{lineText(l)}</li>)}</ul>
      {since.carried.length > 0 ? <p className={s.note}>{since.carried.length} earlier comment{since.carried.length === 1 ? "" : "s"} will show beside the same figures, read-only.</p> : null}
    </div>
  );
}
