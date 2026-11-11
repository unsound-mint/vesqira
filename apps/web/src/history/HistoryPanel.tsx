import { useState } from "react";
import { Button } from "../ui";
import type { ExplorationStep } from "../api/history";
import s from "./HistoryPanel.module.css";

const KIND: Record<ExplorationStep["kind"], string> = { opened: "Opened", channel: "Channel", region: "Region", overlay: "Overlay", link: "Link", continued: "Continued" };

const clock = (iso: string) => {
  const d = new Date(iso);
  const today = new Date();
  const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  return d.toDateString() === today.toDateString() ? time : `${d.toLocaleDateString([], { month: "short", day: "numeric" })} ${time}`;
};

/**
 * Where a step came from, for a reader of the list: nothing when it follows the step listed just before it, otherwise the step it
 * branched from (or that the older step was pruned). Steps are never deleted when someone continues from an earlier one.
 */
export function branchNote(steps: ExplorationStep[], i: number): string | null {
  const st = steps[i]!;
  if (st.parentId === null) return i === 0 ? null : "starts a new line";
  if (steps[i - 1]?.id === st.parentId) return null;
  const parent = steps.find((p) => p.id === st.parentId);
  return parent ? `branched from “${parent.label}”` : "continues a step that was pruned";
}

/** The exploration timeline of the open project, newest first. */
export function HistoryPanel({ steps, keep, error, onContinue }: {
  steps: ExplorationStep[];
  keep: number;
  error: string | null;
  onContinue: (s: ExplorationStep) => Promise<boolean>;
}) {
  const [busy, setBusy] = useState<number | null>(null);
  const head = steps[steps.length - 1]?.id ?? null;
  const rows = steps.map((st, i) => ({ st, note: branchNote(steps, i) })).reverse();
  return (
    <div className={s.root}>
      {error ? <p role="alert" className={s.err}>{error}</p> : null}
      {steps.length === 0 ? <p className={s.note}>Nothing yet. Open a dataset: channels you stay on, regions you draw, overlays and links you add are kept here.</p> : null}
      {rows.length > 0 ? (
        <ol className={s.list} aria-label="Exploration steps, newest first">
          {rows.map(({ st, note }) => (
            <li key={st.id} className={st.id === head ? s.head : s.step} aria-current={st.id === head ? "step" : undefined}>
              <span className={s.kind}>{KIND[st.kind]}</span>
              <span className={s.label}>{st.label}</span>
              <time className={s.time} dateTime={st.at}>{clock(st.at)}</time>
              {note ? <span className={s.branch}>{note}</span> : null}
              <Button disabled={busy !== null} aria-label={`Continue from here: ${st.label}`} onClick={() => { setBusy(st.id); void onContinue(st).finally(() => setBusy(null)); }}>
                Continue from here
              </Button>
            </li>
          ))}
        </ol>
      ) : null}
      <p className={s.note}>Keeps the latest {keep} steps of this project; the oldest are dropped. Continuing from a step restores its view (you can undo that) and starts a new branch; later steps stay. A step holds a view, not data.</p>
    </div>
  );
}
