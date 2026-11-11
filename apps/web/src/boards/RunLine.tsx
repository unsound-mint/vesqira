import type { Provenance } from "../api/boards";
import { metricText } from "../api/metrics";
import { ControlsView } from "../runs/ControlsView";

type RunInfo = NonNullable<Provenance["run"]>;

/** The run behind a figure, as a reviewer or the author reads it: code version, whether it is reproducible, parameters. */
export function RunLine({ run }: { run: RunInfo }) {
  return (
    <div>
      <p style={{ margin: 0 }}>
        Run <span className="mono">{run.name}</span> ({run.kind}), code <span className="mono">{run.gitCommit || "unknown"}</span>
        {run.reproducibleFromCommit ? "" : run.dirty ? ": uncommitted changes, so not reproducible from the commit" : ": no commit recorded"}.
      </p>
      {run.review ? (
        <p style={{ margin: 0 }}>
          Author’s assessment: scientific QC {run.review.qc.replace(/_/g, " ")}; interpretation {run.review.interpretation.replace(/_/g, " ")}.{run.review.note ? ` ${run.review.note}` : ""} (The run having finished is not an assessment.)
        </p>
      ) : null}
      {run.controls && (run.controls.smoothing || run.controls.noise || run.controls.construction) ? (
        <details open>
          <summary>Controls, as stated for the run</summary>
          <ControlsView controls={run.controls} />
        </details>
      ) : null}
      {run.metrics && run.metrics.length > 0 ? (
        <details open>
          <summary>Run results ({run.metrics.length}), as declared by the run</summary>
          <ul style={{ margin: "4px 0", paddingLeft: 16 }}>
            {run.metrics.map((m) => (
              <li key={m.key}>
                <span className="mono">{m.key}</span> = <span className="mono">{metricText(m)}</span>
                {m.definition ? <span style={{ display: "block", opacity: 0.8 }}>{m.definition}</span> : null}
              </li>
            ))}
          </ul>
        </details>
      ) : null}
      <details>
        <summary>Run parameters ({run.params.length})</summary>
        <ul style={{ margin: "4px 0", paddingLeft: 16 }}>
          {run.params.map((p) => (
            <li key={p.key}><span className="mono">{p.key}</span> = <span className="mono">{p.value}</span></li>
          ))}
        </ul>
      </details>
    </div>
  );
}
