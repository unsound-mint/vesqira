import { useEffect, useMemo, useState } from "react";
import * as api from "../api/reports";
import type { Change, ReportView } from "../api/reports";
import { Button } from "../ui";
import { joinBlocks } from "./blocks";
import { renderMarkdown, type RenderEnv } from "./markdown";
import s from "./ReportsView.module.css";

/**
 * What the agent changed since the owner last looked, part by part. Each change is kept or undone on its own; undoing
 * saves a new revision by the owner with the earlier text of that part. Nothing is published from here.
 */
export function ChangesReview({ report, agentName, env, onDone, onCancel }: { report: ReportView; agentName: string; env: RenderEnv; onDone: (body: string | null) => void | Promise<void>; onCancel: () => void }) {
  const [data, setData] = useState<api.Changes | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [undo, setUndo] = useState<Record<number, boolean>>({});
  useEffect(() => {
    api.reportChanges(report.id).then(setData).catch((e: unknown) => setErr(e instanceof Error ? e.message : "Could not compare revisions."));
  }, [report.id, report.revision]);
  const changed = useMemo(() => (data?.changes ?? []).map((c, i) => ({ c, i })).filter(({ c }) => c.status !== "same"), [data]);
  function body(): string | null {
    if (!data || !Object.values(undo).some(Boolean)) return null;
    return joinBlocks(data.changes.flatMap((c, i): string[] => (c.status === "same" ? [c.after ?? ""] : undo[i] ? (c.before !== undefined ? [c.before] : []) : c.after !== undefined ? [c.after] : [])));
  }
  return (
    <div className={s.review}>
      <h2>Changes by {agentName} since you last looked</h2>
      {err ? <p className={s.error} role="alert">{err}</p> : !data ? <p className={s.muted}>Comparing…</p> : changed.length === 0 ? <p className={s.muted}>The text is the same as when you last looked.</p> : null}
      {changed.map(({ c, i }) => (
        <ChangeItem key={i} c={c} env={env} undone={!!undo[i]} onToggle={() => setUndo({ ...undo, [i]: !undo[i] })} />
      ))}
      <div className={s.row}>
        <Button variant="primary" onClick={() => void onDone(body())}>{Object.values(undo).some(Boolean) ? "Save and mark as read" : "Keep all and mark as read"}</Button>
        <button type="button" className={s.ghost} onClick={onCancel}>Back to the page</button>
      </div>
    </div>
  );
}

function ChangeItem({ c, env, undone, onToggle }: { c: Change; env: RenderEnv; undone: boolean; onToggle: () => void }) {
  const label = c.status === "added" ? "Added" : c.status === "removed" ? "Removed" : "Changed";
  return (
    <section className={s.change} data-undone={undone ? "true" : undefined}>
      <div className={s.changeHead}>
        <span>{label}</span>
        <button type="button" className={s.ghost} aria-pressed={undone} onClick={onToggle}>{undone ? (c.status === "removed" ? "Will be restored" : "Will be undone") : c.status === "removed" ? "Restore" : "Undo"}</button>
      </div>
      {c.before !== undefined ? <div className={s.before} aria-label="Before" dangerouslySetInnerHTML={{ __html: renderMarkdown(c.before, env) }} /> : null}
      {c.after !== undefined ? <div className={s.after} aria-label="After" dangerouslySetInnerHTML={{ __html: renderMarkdown(c.after, env) }} /> : null}
    </section>
  );
}
