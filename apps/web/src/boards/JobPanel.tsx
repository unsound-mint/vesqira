import { useCallback, useEffect, useRef, useState } from "react";
import { Button, Pill } from "../ui";
import { formatDuration } from "../ui/duration";
import { pinJobItem } from "../api/boards";
import type { Board, JobInfo } from "../api/boards";
import type { DatasetSummary } from "../api/catalog";
import type { Run } from "../api/runs";
import { PlaneViewer } from "../viewer/PlaneViewer";
import { saveViewFigure } from "../viewer/saveFigure";
import { useViewer } from "../viewer/useViewer";
import s from "./JobPanel.module.css";

const ACTIVE = new Set<JobInfo["state"]>(["draft", "submitting", "submitted", "running"]);
export const jobIsActive = (j: JobInfo) => ACTIVE.has(j.state);

const TONE: Record<JobInfo["state"], "neutral" | "ok" | "warn" | "err" | "accent"> = {
  draft: "neutral", submitting: "neutral", submitted: "neutral", running: "accent", completed: "ok", failed: "err", cancelled: "warn", unknown: "warn",
};

/** The state as text, with the percentage when the job's log gave one. Status is never colour alone. */
export function jobPillText(j: JobInfo): string {
  if (j.progress && j.progress.total > 0 && jobIsActive(j)) return `${j.state} · ${Math.floor((100 * j.progress.done) / j.progress.total)} %`;
  return j.state;
}

/** What is known about a followed job, in words. The time left is an estimate and says so; with no progress it is left out. */
export function JobLines({ job, now }: { job: JobInfo; now: number }) {
  const active = jobIsActive(job);
  const elapsed = active && job.submittedAt ? Math.max(0, (now - new Date(job.submittedAt).getTime()) / 1000) : null;
  const p = job.progress;
  return (
    <div className={s.lines}>
      <p className={s.src}>
        <Pill tone={TONE[job.state]}>{jobPillText(job)}</Pill> {job.name}
        {active ? " · not finished" : ""}
      </p>
      {active && p ? (
        <p className={s.src}>
          {p.done} of {p.total} chunks done
          {p.etaSeconds !== null ? ` · about ${formatDuration(p.etaSeconds)} left (estimate at the rate so far)` : ""}
        </p>
      ) : null}
      {active && elapsed !== null ? <p className={s.src}>{p ? "" : "The job's log reports no progress. "}Elapsed {formatDuration(elapsed)} since it was submitted.</p> : null}
      {active ? <p className={s.src}>Becomes the result when the job finishes.</p> : null}
    </div>
  );
}

/** The placeholder frame where the picture will be. */
export function JobFrame({ job }: { job: JobInfo }) {
  const p = job.progress;
  return (
    <div className={s.frame} role="group" aria-label={`Run ${job.name}: ${jobPillText(job)}`}>
      <Pill tone={TONE[job.state]}>{jobPillText(job)}</Pill>
      {jobIsActive(job) && p && p.total > 0 ? <progress className={s.bar} max={p.total} value={p.done} aria-label={`${p.done} of ${p.total} chunks done`} /> : null}
    </div>
  );
}

/** The run's default output: the orientation field when there is one, else the first output. */
export function defaultOutput(run: Run) {
  return run.outputs.find((o) => o.role === "orientation") ?? run.outputs[0];
}

/**
 * Draws the finished run's default output with the default display settings and, once it is on the canvas, saves it as
 * a figure and pins it over the job panel. It never pins a reduced preview. A failure is shown with a way to try again;
 * nothing is pinned silently.
 */
export function LiveResult({ boardId, position, job, run, dataset, onPinned }: { boardId: number; position: number; job: JobInfo; run: Run; dataset: DatasetSummary; onPinned: (b: Board) => void }) {
  const v = useViewer(dataset);
  const [drawnFor, setDrawnFor] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const tried = useRef(false);
  const plane = v.plane;
  // an orientation field is saved with its reliability lines, so wait for them when the dataset names a reliability map
  const ready = plane !== null && drawnFor === plane && !plane.head.preview && (v.interp?.reliabilityId == null || v.reliability !== null);

  const pin = useCallback(async () => {
    setBusy(true);
    setErr(null);
    try {
      const fig = await saveViewFigure(v, dataset, {
        title: `${job.name}: ${dataset.name}`,
        caption: `Result of the run "${run.name}", saved when its job finished, with the default display settings.`,
      });
      onPinned(await pinJobItem(boardId, position, fig.id));
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not pin the result.");
    } finally {
      setBusy(false);
    }
  }, [v, dataset, job.name, run.name, boardId, position, onPinned]);

  useEffect(() => {
    if (ready && !tried.current) {
      tried.current = true;
      void pin();
    }
  }, [ready, pin]);

  return (
    <div className={s.live}>
      <div className={s.viewer}>
        <PlaneViewer v={v} onDrawn={setDrawnFor} />
      </div>
      {busy ? <p className={s.src} role="status">Saving the result as a figure…</p> : null}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {err || (ready && !busy && tried.current) ? <Button onClick={() => void pin()} disabled={busy}>Pin the result</Button> : null}
    </div>
  );
}
