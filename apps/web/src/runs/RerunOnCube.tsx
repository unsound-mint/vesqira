import { useEffect, useMemo, useState } from "react";
import type { Dataset } from "../api/catalog";
import type { Job } from "../api/jobs";
import { createRerunOnInput, previewRerunOnInput } from "../api/runs";
import type { InputRerunRecord, Run, RerunOnInputSummary } from "../api/runs";
import { bytes, EM } from "../catalog/format";
import { Button, Select } from "../ui";
import s from "./RerunOnCube.module.css";

const short = (hash: string) => (hash ? hash.slice(0, 12) : EM);

/** The input's identity as the catalog (or the draft) has it. Equal sampled values never prove equal bytes, and the screen says what each value covers. */
export function InputIdentity({ input }: { input: RerunOnInputSummary["input"] }) {
  return (
    <dl className={s.facts}>
      <dt>File</dt>
      <dd><span className="mono">{input.path}</span>, {bytes(input.sizeBytes)}</dd>
      <dt>Sampled fingerprint</dt>
      <dd>{input.sampledFp ? <><span className="mono">{short(input.sampledFp)}</span> (sampled/1: a hash of the header and 16 blocks, not of the whole file; a different value proves the files differ, an equal one does not prove they are the same)</> : "not computed"}</dd>
      <dt>SHA-256 of every byte</dt>
      <dd>{input.fullSha256 ? <span className="mono" title={input.fullSha256}>{short(input.fullSha256)}</span> : "not computed in the catalog; the job hashes the file itself before it starts and the run records that value"}</dd>
    </dl>
  );
}

/** What a configuration run on another cube carries over from the recorded run and what changes. Pure: it is what both the preview and the created draft show. */
export function RerunSummary({ summary }: { summary: RerunOnInputSummary }) {
  return (
    <div className={s.summary} aria-label="What the draft carries over and what changes">
      <h4 className={s.h}>What changes</h4>
      <ul className={s.changes}>
        {summary.changed.map((c) => (
          <li key={c.key}>
            <strong>{c.key === "input" ? "Input cube" : c.key}</strong>{" "}
            <span className="mono">{c.parent ?? EM}</span> → <span className="mono">{c.now ?? EM}</span>
          </li>
        ))}
      </ul>
      <h4 className={s.h}>The new input</h4>
      <InputIdentity input={summary.input} />
      <details className={s.carried}>
        <summary>Carried over unchanged ({summary.carried.length} settings, the code folder and the resources)</summary>
        <dl className={s.settings}>
          {summary.carried.map((c) => (
            <div key={c.key} className={s.row}><dt className="mono">{c.key}</dt><dd className="mono">{c.value || EM}</dd></div>
          ))}
        </dl>
      </details>
      <p className={s.hash}>
        Configuration <span className="mono" title={summary.configHash}>{short(summary.configHash)}</span>: the same as run {summary.sourceRunId} ({summary.sourceRunName}). The hash covers the whole config apart from the input and output paths.
      </p>
      {summary.notes.length > 0 ? (
        <ul className={s.notes} aria-label="Read before submitting">
          {summary.notes.map((n) => <li key={n}>{n}</li>)}
        </ul>
      ) : null}
    </div>
  );
}

/**
 * "Run this configuration on another cube": pick a cube already registered on the host the run's job used; the server says what
 * would be carried over and what changes, or why it refuses (a setting that does not fit the cube, no kept settings). Creating
 * the draft submits nothing: the script is read and submitted on the Jobs screen.
 */
export function RerunOnCube({ run, job, datasets, onDrafted }: { run: Run; job: Job; datasets: Dataset[]; onDrafted: (job: Job) => void }) {
  const candidates = useMemo(
    () => datasets.filter((d) => d.kind !== "catalog" && d.archived !== true && d.hostId === job.hostId && d.id !== run.inputHint?.datasetId),
    [datasets, job.hostId, run.inputHint?.datasetId],
  );
  const [choice, setChoice] = useState<number | null>(null);
  const [state, setState] = useState<{ summary: RerunOnInputSummary | null; refusal: string | null; loading: boolean }>({ summary: null, refusal: null, loading: false });
  const [creating, setCreating] = useState(false);
  const [createErr, setCreateErr] = useState<string | null>(null);

  useEffect(() => {
    setCreateErr(null);
    if (choice === null) {
      setState({ summary: null, refusal: null, loading: false });
      return;
    }
    let live = true;
    setState({ summary: null, refusal: null, loading: true });
    previewRerunOnInput(run.id, choice)
      .then((summary) => live && setState({ summary, refusal: null, loading: false }))
      .catch((e: unknown) => live && setState({ summary: null, refusal: e instanceof Error ? e.message : "Could not check the cube.", loading: false }));
    return () => { live = false; };
  }, [run.id, choice]);

  async function create() {
    if (choice === null) return;
    setCreating(true);
    setCreateErr(null);
    try {
      const r = await createRerunOnInput(run.id, choice);
      onDrafted(r.job);
    } catch (e) {
      setCreateErr(e instanceof Error ? e.message : "Could not make the draft.");
    } finally {
      setCreating(false);
    }
  }

  return (
    <div className={s.root}>
      <p className={s.lead}>
        Runs the settings this run was recorded with on a different input cube. The draft is rendered from the recorded settings, not from the script text; the code folder and the resources stay, and only the input file is replaced. Nothing is converted or adjusted, and nothing is submitted here.
      </p>
      {candidates.length === 0 ? (
        <p className={s.note}>No other cube is registered on this run's host. Register the new cube there (or transfer it to the host), then come back.</p>
      ) : (
        <Select label="Cube on the same host" value={choice ?? ""} onChange={(e) => setChoice(e.target.value === "" ? null : Number(e.target.value))}>
          <option value="">Choose a cube…</option>
          {candidates.map((d) => <option key={d.id} value={d.id}>{d.name} ({bytes(d.sizeBytes)})</option>)}
        </Select>
      )}
      {state.loading ? <p className={s.note} role="status">Checking the settings against the cube…</p> : null}
      {state.refusal ? <p role="alert" className={s.err}>Not possible: {state.refusal}</p> : null}
      {state.summary ? (
        <>
          <RerunSummary summary={state.summary} />
          <div className={s.actions}>
            <Button type="button" variant="primary" onClick={() => void create()} disabled={creating}>{creating ? "Making the draft…" : "Make the draft job"}</Button>
          </div>
        </>
      ) : null}
      {createErr ? <p role="alert" className={s.err}>{createErr}</p> : null}
    </div>
  );
}

/** On the page of a run that was made this way: which run it repeats, and what its input was when the draft was made. */
export function SameConfiguration({ record, sourceName, others, onOpenRun }: { record: InputRerunRecord; sourceName: string | null; others: Run[]; onOpenRun: (id: number) => void }) {
  return (
    <div className={s.root}>
      <p className={s.lead}>
        {record.sameConfiguration
          ? <>The same configuration as <Button type="button" variant="ghost" onClick={() => onOpenRun(record.sourceRunId)}>{sourceName ?? `run ${record.sourceRunId}`}</Button>, on a different input. Configuration <span className="mono" title={record.configHash}>{short(record.configHash)}</span>.</>
          : <>This draft was made from <Button type="button" variant="ghost" onClick={() => onOpenRun(record.sourceRunId)}>{sourceName ?? `run ${record.sourceRunId}`}</Button>, but its recorded settings no longer hash to the same configuration, so it is not shown as the same.</>}
      </p>
      <h4 className={s.h}>The input as the catalog knew it when the draft was made</h4>
      <InputIdentity input={record.input} />
      <p className={s.note}>The run's own record (Compared with the parent run) lists what the job itself observed of the input when it ran, including the SHA-256 of every byte.</p>
      {record.previousInput ? <p className={s.note}>The recorded run read <span className="mono">{record.previousInput}</span>.</p> : null}
      {others.length > 0 ? (
        <p className={s.note}>
          Other runs with this configuration:{" "}
          {others.map((r, i) => (
            <span key={r.id}>{i > 0 ? ", " : ""}<Button type="button" variant="ghost" onClick={() => onOpenRun(r.id)}>{r.name}</Button></span>
          ))}
        </p>
      ) : null}
    </div>
  );
}
