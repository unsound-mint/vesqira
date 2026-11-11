import { useCallback, useState } from "react";
import type { Dataset } from "../api/catalog";
import type { Job } from "../api/jobs";
import { jobLog } from "../api/jobs";
import { metricText } from "../api/metrics";
import type { Run } from "../api/runs";
import { Assessment, ROLE, RohsaFacts } from "../catalog/RunPanel";
import { bytes, EM } from "../catalog/format";
import { TONE } from "../jobs/JobsView";
import { PlaneViewer } from "../viewer/PlaneViewer";
import { paramDiff } from "../compare/diff";
import { useViewer } from "../viewer/useViewer";
import { Button, Pill, Section } from "../ui";
import { EntryActions } from "../catalog/EntryActions";
import type { EntryChange } from "../catalog/EntryActions";
import s from "./RunPage.module.css";

/** A run as a page of blocks: its result, the numbers it declared, how it was set up, your assessment, and its log. */
export function RunPage({ run, runs, datasets, job, onBack, onOpenRun, onOpenInViewer, onCompareInViewer, onRunAgain, onEntryChanged }: {
  run: Run;
  runs: Run[];
  datasets: Dataset[];
  job: Job | null;
  onBack: () => void;
  onOpenRun: (id: number) => void;
  onOpenInViewer: (datasetId: number) => void;
  /** Opens the viewer with two maps side by side. */
  onCompareInViewer?: (a: number, b: number) => void;
  /** Opens the draft form that repeats this run with changes. */
  onRunAgain?: (id: number) => void;
  /** The run was archived, restored or deleted from this page. */
  onEntryChanged?: (change: EntryChange) => void;
}) {
  const first = run.outputs.find((o) => o.role === "psi") ?? run.outputs[0] ?? null;
  const [shown, setShown] = useState<number | null>(first?.datasetId ?? null);
  const output = run.outputs.find((o) => o.datasetId === shown) ?? first;
  const dataset = output ? datasets.find((d) => d.id === output.datasetId) ?? null : null;
  const v = useViewer(dataset);
  const others = run.outputs.filter((o) => o !== output);

  const hasParent = run.parentRunId !== null && run.parentRunId !== undefined;
  const parent = hasParent ? runs.find((r) => r.id === run.parentRunId) ?? null : null;
  const diffs = parent ? paramDiff(run.params, parent.params) : [];
  const diffMap = new Map(diffs.map((d) => [d.key, d]));
  const own = new Set(run.params.map((p) => p.key));
  const input = run.inputHint ? datasets.find((d) => d.id === run.inputHint!.datasetId) : null;
  const unresolved = (run.reproducibility ?? []).filter((f) => f.status === "unresolved").length;

  return (
    <div className={s.root}>
      <article className={s.page} aria-label={`Run ${run.name}`}>
        <nav className={s.crumbs} aria-label="Breadcrumb">
          <Button type="button" onClick={onBack}>Back</Button>
          <span className={s.trail}>Runs / <span className="mono">{run.name}</span></span>
          {job ? <Pill tone={TONE[job.state]}>{job.state} · job {job.slurmId || `#${job.id}`}</Pill> : <Pill>imported</Pill>}
        </nav>
        <header className={s.head}>
          <h2 className={`${s.title} mono`}>{run.name}</h2>
          {hasParent ? (
            parent ? (
              <span className={s.sub}>child of <Button type="button" variant="ghost" onClick={() => onOpenRun(parent.id)}>{parent.name}</Button></span>
            ) : (
              <span className={s.sub}>parent run not found</span>
            )
          ) : null}
          {run.dirty ? <Pill tone="warn">code has uncommitted changes</Pill> : null}
          {run.kind === "vgt" && onRunAgain ? <Button type="button" onClick={() => onRunAgain(run.id)}>Run again with…</Button> : null}
        </header>

        {onEntryChanged ? (
          <Section title="Archive or delete" defaultOpen={false} meta={run.archived ? "archived" : undefined}>
            <div className={s.block}>
              <EntryActions kind="runs" id={run.id} name={run.name} archived={run.archived === true} onChanged={onEntryChanged} />
            </div>
          </Section>
        ) : null}

        {run.rohsa ? (
          <Section title="Decomposition">
            <RohsaFacts run={run} role={output?.role} />
          </Section>
        ) : null}

        <Section title={`Result${output ? ` · ${ROLE[output.role] ?? output.role}` : ""}`} meta={output && output === first ? "default" : undefined}>
          {output ? (
            dataset ? (
              <div className={s.block}>
                <div className={s.map}><PlaneViewer v={v} /></div>
                {v.interp ? <p className={s.note}>Axial orientation, 0°–180° (cyclic colours: 0° and 180° are the same)</p> : null}
                <div className={s.row}>
                  <Button type="button" onClick={() => onOpenInViewer(dataset.id)}>Open in the viewer</Button>
                </div>
              </div>
            ) : (
              <p className={s.block}>This output is not registered in the project: {output.name}.</p>
            )
          ) : (
            <p className={s.block}>This run has no output maps.</p>
          )}
        </Section>

        {others.length > 0 ? (
          <Section title={`More results (${others.length})`} meta={others.length <= 6 ? others.map((o) => ROLE[o.role] ?? o.role).join(" · ") : undefined}>
            <ul className={s.chips}>
              {others.map((o) => (
                <li key={o.datasetId} className={s.chipRow}>
                  <button type="button" className={s.chip} onClick={() => setShown(o.datasetId)} aria-label={`Show ${ROLE[o.role] ?? o.role} in the result block`}>{ROLE[o.role] ?? o.role}</button>
                  <Button type="button" variant="ghost" onClick={() => onOpenInViewer(o.datasetId)} aria-label={`Open ${ROLE[o.role] ?? o.role} in the viewer`}>Open in the viewer</Button>
                  {onCompareInViewer && dataset ? <Button type="button" variant="ghost" onClick={() => onCompareInViewer(dataset.id, o.datasetId)} aria-label={`Compare ${ROLE[o.role] ?? o.role} with ${output ? ROLE[output.role] ?? output.role : "the result"} side by side`}>Side by side</Button> : null}
                </li>
              ))}
            </ul>
          </Section>
        ) : null}

        {run.metrics.length > 0 ? (
          <Section title={`Numbers (${run.metrics.length})`} meta="as declared by the run">
            <div className={s.block}>
              <div className={s.scrollX}>
                <table className={s.table}>
                  <tbody>
                    {run.metrics.map((m) => (
                      <tr key={m.key}>
                        <th scope="row" title={m.definition || undefined}>{m.key}</th>
                        <td className="mono">
                          {metricText(m)}
                          {m.definition ? <span className={`${s.note} ${s.def}`}>{m.definition}</span> : null}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <p className={s.note}>As declared by the run; Vesqira did not compute or check them. “Not recorded” means unknown.</p>
            </div>
          </Section>
        ) : null}

        <Section title="Settings and code" meta={parent ? (diffs.length > 0 ? `differs from parent: ${diffs.map((d) => d.key).join(", ")}` : "same settings as parent") : undefined}>
          <div className={s.block}>
            <div className={s.scrollX}>
              <table className={s.table}>
                <tbody>
                  {run.params.map((p) => {
                    const d = diffMap.get(p.key);
                    return (
                      <tr key={p.source + p.key}>
                        <th scope="row">{p.key}</th>
                        <td className="mono">{p.value}</td>
                        <td className={`${s.note} mono`}>{d ? `parent ${d.b}` : ""}</td>
                      </tr>
                    );
                  })}
                  {diffs.filter((d) => !own.has(d.key)).map((d) => (
                    <tr key={`only-${d.key}`}>
                      <th scope="row">{d.key}</th>
                      <td className="mono">{EM}</td>
                      <td className={`${s.note} mono`}>parent {d.b}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {run.params.length === 0 ? <p className={s.note}>No settings were recorded.</p> : null}
            <p className={s.note}>As written by the pipeline. Paths and host names are not kept.</p>
            <p className={s.line}>
              Code <span className="mono">{run.gitCommit ? run.gitCommit.slice(0, 12) + (run.dirty ? "-dirty" : "") : EM}</span>
              {" · "}
              {run.reproducibleFromCommit ? (
                <Pill tone="ok">Commit recorded</Pill>
              ) : (
                <>Reproducible: No, {run.dirty ? "the commit does not describe the code that ran." : "no commit is recorded."}</>
              )}
            </p>
            <p className={s.line}>
              Finished <span className="mono">{run.generatedAt || EM}</span> · Elapsed <span className="mono">{run.elapsedSeconds === null ? EM : `${run.elapsedSeconds.toFixed(1)} s`}</span>
            </p>
            <p className={s.line}>
              Input <span className="mono">{run.inputName || EM}</span>{run.inputSizeBytes ? `, ${bytes(run.inputSizeBytes)}` : ""}.{" "}
              {input ? (
                <>
                  A registered file has the same name and size ({run.inputHint!.basis}).{" "}
                  <Button type="button" onClick={() => onOpenInViewer(input.id)}>Open it</Button>
                </>
              ) : (
                "No registered file matches it by name and size; the path on the run’s machine is not kept."
              )}
            </p>
          </div>
        </Section>

        {run.reproducibility && run.reproducibility.length > 0 ? (
          <Section title="What is known about rerunning this" meta={`(${unresolved} unresolved)`} defaultOpen={false}>
            <p className={s.block}>Declared means the run’s own metadata says so and it was not checked here; unresolved means nothing records it. This says nothing about whether the result is right.</p>
            <ul className={s.known}>
              {run.reproducibility.map((f) => (
                <li key={f.key}>
                  <Pill tone={f.status === "unresolved" ? "warn" : "neutral"}>{f.status}</Pill> <strong>{f.label}.</strong> {f.detail}
                </li>
              ))}
            </ul>
          </Section>
        ) : null}

        <Section title="Your assessment">
          <div className={s.block}>
            <Assessment run={run} />
          </div>
        </Section>

        {job ? <LogBlock job={job} /> : null}
      </article>
    </div>
  );
}

function LogBlock({ job }: { job: Job }) {
  const [log, setLog] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const load = useCallback(() => {
    setBusy(true);
    setErr(null);
    jobLog(job.id)
      .then(setLog)
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : "Could not read the log."))
      .finally(() => setBusy(false));
  }, [job.id]);
  return (
    <Section title={`Log · job ${job.slurmId || `#${job.id}`}`} defaultOpen={false} onToggle={(open) => { if (open && log === null && !busy) load(); }}>
      <div className={s.block}>
        <div className={s.row}>
          <Button type="button" onClick={load} disabled={busy}>{busy ? "Reading…" : "Refresh"}</Button>
        </div>
        {err ? <p role="alert" className={s.err}>{err}</p> : null}
        {log !== null ? <pre className={s.log} tabIndex={0} aria-label="Job log">{log || "The log is empty."}</pre> : null}
      </div>
    </Section>
  );
}
