import type { Run } from "../api/runs";
import { useEffect, useState } from "react";
import { Button, Pill, Select } from "../ui";
import { runReviewHistory, setRunReview } from "../api/runs";
import type { RunReview, RunReviewRevision } from "../api/runs";
import s from "./RunPanel.module.css";

export const ROLE: Record<string, string> = { psi: "Orientation (psi)", reliability: "Reliability", support: "Support", fit_rbar2: "Fit R̄²" };

export function runFor(runs: Run[], datasetId: number | null): { run: Run; role: string } | null {
  if (datasetId === null) return null;
  for (const run of runs) {
    const o = run.outputs.find((x) => x.datasetId === datasetId);
    if (o) return { run, role: o.role };
  }
  return null;
}

/** Compact summary in the inspector; the run itself opens as a page. */
export function RunPanel({ run, role, job, onOpenPage }: { run: Run; role: string; job?: { name: string; slurmId: string; state: string } | null; onOpenPage: () => void }) {
  return (
    <div className={s.wrap}>
      <dl className={s.facts}>
        <dt>Run</dt>
        <dd className="mono" title={run.name}>{run.name}</dd>
        <dt>This file</dt>
        <dd>{ROLE[role] ?? role}</dd>
        {job ? (
          <>
            <dt>Job</dt>
            <dd className="mono" title="This run was produced by a Slurm job submitted from Vesqira">{job.name} · {job.slurmId} · {job.state}</dd>
          </>
        ) : null}
      </dl>
      {run.reproducibleFromCommit ? (
        <Pill tone="ok">Commit recorded</Pill>
      ) : (
        <Pill tone="warn">{run.dirty ? "Not reproducible from the commit: uncommitted changes" : "No commit recorded"}</Pill>
      )}
      <div className={s.row}>
        <Button type="button" onClick={onOpenPage}>Open run page</Button>
      </div>
    </div>
  );
}

const QC = { not_assessed: "Not assessed", passed: "Passed my checks", failed: "Failed my checks", inconclusive: "Inconclusive" } as const;
const INTERP = { not_interpreted: "Not interpreted", supports: "Supports the hypothesis", challenges: "Challenges the hypothesis", inconclusive: "Inconclusive" } as const;

/** Your assessment of the science, kept apart from the fact that the computation finished. */
export function Assessment({ run }: { run: Run }) {
  const [rv, setRv] = useState<RunReview>(run.review);
  const [msg, setMsg] = useState<string | null>(null);
  const [history, setHistory] = useState<RunReviewRevision[]>([]);
  useEffect(() => {
    let live = true;
    runReviewHistory(run.id).then((h) => live && setHistory(h)).catch(() => undefined);
    return () => { live = false; };
  }, [run.id]);
  async function save(next: RunReview) {
    setRv(next);
    try {
      await setRunReview(run.id, next);
      setHistory(await runReviewHistory(run.id));
      setMsg("Saved.");
    } catch (e) {
      setMsg(e instanceof Error ? e.message : "Could not save.");
    }
  }
  return (
    <div className={s.block}>
      <p className={s.note}>The run finished; whether the science holds is a separate question that only you can answer.</p>
      <Select label="Scientific QC" value={rv.qc} onChange={(e) => save({ ...rv, qc: e.target.value as RunReview["qc"] })}>
        {Object.entries(QC).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
      </Select>
      <Select label="Interpretation" value={rv.interpretation} onChange={(e) => save({ ...rv, interpretation: e.target.value as RunReview["interpretation"] })}>
        {Object.entries(INTERP).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
      </Select>
      <label className={s.note}>
        Note (limitations, coverage)
        <textarea className={s.area} rows={2} maxLength={500} defaultValue={rv.note} onBlur={(e) => { if (e.target.value !== rv.note) void save({ ...rv, note: e.target.value }); }} />
      </label>
      {msg ? <p className={s.note} role="status">{msg}</p> : null}
      {history.length > 1 ? (
        <details>
          <summary className={s.note}>Earlier assessments ({history.length - 1})</summary>
          <ul className={s.note}>
            {history.slice(1).map((h) => (
              <li key={h.at + h.qc + h.interpretation + h.note}>{new Date(h.at).toLocaleString()} · {QC[h.qc]} · {INTERP[h.interpretation]}{h.note ? ` · ${h.note}` : ""}</li>
            ))}
          </ul>
        </details>
      ) : null}
    </div>
  );
}
