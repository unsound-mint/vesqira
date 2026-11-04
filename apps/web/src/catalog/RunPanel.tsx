import type { Dataset } from "../api/catalog";
import type { Run } from "../api/runs";
import { useState } from "react";
import { Button, Pill, Select } from "../ui";
import { setRunReview } from "../api/runs";
import type { RunReview } from "../api/runs";
import { bytes, EM } from "./format";
import s from "./RunPanel.module.css";

const ROLE: Record<string, string> = { psi: "Orientation (psi)", reliability: "Reliability", support: "Support", fit_rbar2: "Fit R̄²" };

export function runFor(runs: Run[], datasetId: number | null): { run: Run; role: string } | null {
  if (datasetId === null) return null;
  for (const run of runs) {
    const o = run.outputs.find((x) => x.datasetId === datasetId);
    if (o) return { run, role: o.role };
  }
  return null;
}

/** The run a dataset came from: its parameters, code version and sibling outputs, with honest labels. */
export function RunPanel({ run, role, datasets, job, onOpen, onCompare }: { run: Run; role: string; datasets: Dataset[]; job?: { name: string; slurmId: string; state: string } | null; onOpen: (id: number) => void; onCompare: (id: number) => void }) {
  const input = run.inputHint ? datasets.find((d) => d.id === run.inputHint!.datasetId) : null;
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
        <dt>Code</dt>
        <dd className="mono">{run.gitCommit ? run.gitCommit.slice(0, 12) + (run.dirty ? " -dirty" : "") : EM}</dd>
        <dt>Finished</dt>
        <dd className="mono">{run.generatedAt || EM}</dd>
        <dt>Elapsed</dt>
        <dd className="mono">{run.elapsedSeconds === null ? EM : `${run.elapsedSeconds.toFixed(1)} s`}</dd>
      </dl>
      {run.reproducibleFromCommit ? (
        <Pill tone="ok">Commit recorded</Pill>
      ) : (
        <Pill tone="warn">{run.dirty ? "Not reproducible from the commit: uncommitted changes" : "No commit recorded"}</Pill>
      )}
      <div className={s.block}>
        <h3 className={s.h}>Input</h3>
        <p className={s.note}>
          <span className="mono">{run.inputName || EM}</span>{run.inputSizeBytes ? `, ${bytes(run.inputSizeBytes)}` : ""}.{" "}
          {input ? (
            <>
              A registered file has the same name and size ({run.inputHint!.basis}).{" "}
              <Button type="button" onClick={() => onOpen(input.id)}>Open it</Button>
            </>
          ) : (
            "No registered file matches it by name and size; the path on the run’s machine is not kept."
          )}
        </p>
      </div>
      <div className={s.block}>
        <h3 className={s.h}>Outputs of this run</h3>
        <ul className={s.outs}>
          {run.outputs.map((o) => (
            <li key={o.role}>
              <span>{ROLE[o.role] ?? o.role}</span>
              <span className={s.row}>
                <Button type="button" onClick={() => onOpen(o.datasetId)}>Open</Button>
                <Button type="button" onClick={() => onCompare(o.datasetId)}>Compare</Button>
              </span>
            </li>
          ))}
        </ul>
      </div>
      <Assessment run={run} />
      <details className={s.det}>
        <summary>Parameters ({run.params.length})</summary>
        <table className={s.table}>
          <tbody>
            {run.params.map((p) => (
              <tr key={p.source + p.key}>
                <th scope="row">{p.key}</th>
                <td className="mono">{p.value}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <p className={s.note}>As written by the pipeline. Paths and host names are not kept.</p>
      </details>
    </div>
  );
}

const QC = { not_assessed: "Not assessed", passed: "Passed my checks", failed: "Failed my checks", inconclusive: "Inconclusive" } as const;
const INTERP = { not_interpreted: "Not interpreted", supports: "Supports the hypothesis", challenges: "Challenges the hypothesis", inconclusive: "Inconclusive" } as const;

/** Your assessment of the science, kept apart from the fact that the computation finished. */
function Assessment({ run }: { run: Run }) {
  const [rv, setRv] = useState<RunReview>(run.review);
  const [msg, setMsg] = useState<string | null>(null);
  async function save(next: RunReview) {
    setRv(next);
    try {
      await setRunReview(run.id, next);
      setMsg("Saved.");
    } catch (e) {
      setMsg(e instanceof Error ? e.message : "Could not save.");
    }
  }
  return (
    <div className={s.block}>
      <h3 className={s.h}>Your assessment</h3>
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
    </div>
  );
}
