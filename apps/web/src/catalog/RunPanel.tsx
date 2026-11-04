import type { Dataset } from "../api/catalog";
import type { Run } from "../api/runs";
import { Button, Pill } from "../ui";
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
export function RunPanel({ run, role, datasets, onOpen, onCompare }: { run: Run; role: string; datasets: Dataset[]; onOpen: (id: number) => void; onCompare: (id: number) => void }) {
  const input = run.inputHint ? datasets.find((d) => d.id === run.inputHint!.datasetId) : null;
  return (
    <div className={s.wrap}>
      <dl className={s.facts}>
        <dt>Run</dt>
        <dd className="mono" title={run.name}>{run.name}</dd>
        <dt>This file</dt>
        <dd>{ROLE[role] ?? role}</dd>
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
