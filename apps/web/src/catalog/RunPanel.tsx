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

/**
 * What a ROHSA run records beyond the common run model, each with where it comes from. The crop is the run's input, found by
 * name beside the .dat; a missing one is said, never replaced by another file.
 */
export function RohsaFacts({ run, role }: { run: Run; role?: string }) {
  const r = run.rohsa;
  if (!r) return null;
  const unitNote = role ? r.unitNotes[role] : undefined;
  return (
    <div className={s.wrap}>
      <dl className={s.facts}>
        <dt>Gaussians per pixel</dt>
        <dd>{r.nGauss > 0 ? `${r.nGauss} (${r.nGaussBasis})` : "not declared"}</dd>
        <dt>Region</dt>
        <dd className="mono">{r.region} <span className={s.note}>({r.regionBasis})</span></dd>
        <dt>Crop FITS</dt>
        <dd className="mono">{r.cropDatasetId !== null ? r.cropName : "not found: no spectral calibration"}</dd>
      </dl>
      {unitNote ? <p className={s.note}>Unit: {unitNote}</p> : null}
      {r.notes.map((n) => <p key={n} className={s.note}>{n}</p>)}
    </div>
  );
}

/** Compact summary in the inspector; the run itself opens as a page. */
export function RunPanel({ run, role, job, onOpenPage, onRunAgain }: { run: Run; role: string; job?: { name: string; slurmId: string; state: string } | null; onOpenPage: () => void; onRunAgain?: () => void }) {
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
      <RohsaFacts run={run} role={role} />
      {run.reproducibleFromCommit ? (
        <Pill tone="ok">Commit recorded</Pill>
      ) : (
        <Pill tone="warn">{run.dirty ? "Not reproducible from the commit: uncommitted changes" : "No commit recorded"}</Pill>
      )}
      <div className={s.row}>
        <Button type="button" onClick={onOpenPage}>Open run page</Button>
        {run.kind === "vgt" && onRunAgain ? <Button type="button" onClick={onRunAgain}>Run again with…</Button> : null}
      </div>
    </div>
  );
}

/** Runs whose input was matched to this file. The match is by name and size only, so every candidate is listed and none is picked. */
export function RunsUsingFile({ runs, datasetId, onRunAgain }: { runs: Run[]; datasetId: number; onRunAgain: (runId: number) => void }) {
  const used = runs.filter((r) => r.kind === "vgt" && r.inputHint?.datasetId === datasetId);
  if (used.length === 0) return null;
  return (
    <div className={s.wrap}>
      <ul className={s.list}>
        {used.map((r) => (
          <li key={r.id} className={s.item}>
            <span className="mono" title={r.name}>{r.name}</span>
            <span className={s.note}>matched by name and size, not verified</span>
            <Button type="button" onClick={() => onRunAgain(r.id)} aria-label={`Run again with… ${r.name}`}>Run again with…</Button>
          </li>
        ))}
      </ul>
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
