import type { Provenance } from "../api/boards";

type RunInfo = NonNullable<Provenance["run"]>;

/** The run behind a figure, as a reviewer or the author reads it: code version, whether it is reproducible, parameters. */
export function RunLine({ run }: { run: RunInfo }) {
  return (
    <div>
      <p style={{ margin: 0 }}>
        Run <span className="mono">{run.name}</span> ({run.kind}), code <span className="mono">{run.gitCommit || "unknown"}</span>
        {run.reproducibleFromCommit ? "" : run.dirty ? ": uncommitted changes, so not reproducible from the commit" : ": no commit recorded"}.
      </p>
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
