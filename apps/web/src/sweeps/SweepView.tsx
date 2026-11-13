import { useCallback, useEffect, useState } from "react";
import { Basis, Button, Checkbox, Input, Pill } from "../ui";
import { Modal, ModalActions } from "../ui/Modal";
import { cancelSweep, getSweep, gib, reviewSweep, submitSweep, sweepBoard } from "../api/sweeps";
import { formatDuration } from "../ui/duration";
import type { Sweep, SweepReview } from "../api/sweeps";
import { TONE } from "../jobs/JobsView";
import { parseMem } from "../jobs/usageText";
import { SweepGrid } from "./SweepGrid";
import s from "./Sweeps.module.css";

const SWEEP_TONE = { draft: "neutral", running: "accent", stopped: "warn", finished: "ok" } as const;
const KIND_TEXT = { parameter: "swept setting", output: "output folder (set per job)", name: "job name (set per job)", other: "NOT EXPECTED" } as const;

/** One sweep: its jobs, the submit dialog, cancel, the board, and the grid of declared metrics. */
export function SweepView({ sweep, onChange, onOpenJob, onBoard }: { sweep: Sweep; onChange: (s: Sweep) => void; onOpenJob: (id: number) => void; onBoard?: (boardId: number) => void }) {
  const [dialog, setDialog] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [tick, setTick] = useState(0);
  const active = sweep.state === "running" || sweep.jobs.some((j) => ["submitting", "submitted", "running"].includes(j.state));

  const reload = useCallback(() => getSweep(sweep.id).then((x) => { onChange(x); setTick((t) => t + 1); }).catch(() => undefined), [sweep.id, onChange]);
  useEffect(() => {
    if (!active) return;
    const t = setInterval(() => void reload(), 20000);
    return () => clearInterval(t);
  }, [active, reload]);

  async function act<T>(f: () => Promise<T>, then?: (r: T) => void) {
    setBusy(true);
    try { then?.(await f()); setErr(null); } catch (e) { setErr(e instanceof Error ? e.message : "That did not work."); }
    setBusy(false);
  }

  return (
    <div className={s.root}>
      <div className={s.row}>
        <h2 className={s.h}>{sweep.name}</h2>
        <Pill tone={SWEEP_TONE[sweep.state]}>{sweep.state}</Pill>
        <span className={s.note}>{sweep.jobs.length} jobs · {Object.entries(sweep.counts).map(([k, n]) => `${n} ${k}`).join(", ")}{sweep.state !== "draft" ? ` · at most ${sweep.concurrency} at a time${sweep.stopOnFailure ? ", stops on a failure" : ""}` : ""}</span>
      </div>
      {sweep.note ? <p className={s.note}>Why: {sweep.note}</p> : null}
      {sweep.state === "draft" ? <p className={s.note}>Nothing has been submitted. Open a job to read or edit its script, or use Submit sweep… to read all of them and start.</p> : null}
      {sweep.detail ? <p className={s.warn} role="status">{sweep.detail}</p> : null}
      {sweep.waitingOn.map((w) => <p key={w} className={s.warn} role="status">{w}</p>)}
      <ul className={s.jobs} aria-label="Jobs of the sweep">
        {sweep.jobs.map((j) => (
          <li key={j.jobId}>
            <Pill tone={TONE[j.state]}>{j.state}</Pill>
            <button type="button" className={s.sortBtn} onClick={() => onOpenJob(j.jobId)}>{j.label}</button>
            <span className={s.note}>{j.name}{j.held ? " · held: edited after approval" : ""}</span>
          </li>
        ))}
      </ul>
      <div className={s.row}>
        {sweep.pending > 0 && sweep.state !== "running" ? <Button variant="primary" disabled={busy} onClick={() => setDialog(true)}>Submit sweep…</Button> : null}
        {sweep.state === "running" ? <Button disabled={busy} onClick={() => act(() => cancelSweep(sweep.id, false), onChange)}>Cancel sweep (stop submitting)</Button> : null}
        {active ? <Button disabled={busy} onClick={() => { if (window.confirm("Also ask Slurm to cancel every queued and running job of this sweep?")) void act(() => cancelSweep(sweep.id, true), onChange); }}>Cancel sweep and its running jobs</Button> : null}
        {sweep.boardId === null ? (
          <Button disabled={busy} onClick={() => act(() => sweepBoard(sweep.id), (b) => { onChange({ ...sweep, boardId: b.id }); onBoard?.(b.id); })}>Make a board from this sweep</Button>
        ) : onBoard ? <Button onClick={() => onBoard(sweep.boardId!)}>Open its board</Button> : <span className={s.note}>A board was made from this sweep (Boards).</span>}
        <Button disabled={busy} onClick={() => void reload()}>Refresh</Button>
      </div>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      <SweepGrid sweepId={sweep.id} refreshKey={tick} />
      {dialog ? <SubmitDialog sweep={sweep} onClose={() => setDialog(false)} onSubmitted={(x) => { setDialog(false); onChange(x); }} /> : null}
    </div>
  );
}

type ReviewJob = SweepReview["jobs"][number];

/** The draft asks for less memory than the table gives for its crop and N (nothing is said when either is not known). */
function shortOfTable(j: ReviewJob): boolean {
  const want = j.sizing ? parseMem(j.sizing.mem) : null;
  return want !== null && j.requested.memBytes > 0 && j.requested.memBytes < want;
}

/** A draft's request beside what the owner's sizing table gives for its own crop and N. The drafts all request what the base job did. */
function SizingLine({ sizing, requested, short }: { sizing: NonNullable<ReviewJob["sizing"]>; requested: { cpus: number; memBytes: number }; short: boolean }) {
  return (
    <p className={short ? s.warn : s.note} role={short ? "alert" : "note"}>
      For crop_size {sizing.cropSize} and n_gauss {sizing.nGauss} your sizing table suggests {sizing.cpus} CPUs, {sizing.mem}, {sizing.time}; this draft requests {requested.cpus} CPUs and {gib(requested.memBytes)}.
      {short ? " That is less memory than the table gives for this size." : ""}{sizing.assumed.length > 0 ? ` Not in the config, read as the script reads it: ${sizing.assumed.join("; ")}.` : ""}
    </p>
  );
}

/** What approving sends: every script (or its difference from the base), the total request, a concurrency limit. Submitting is one explicit act. */
export function SubmitDialog({ sweep, onClose, onSubmitted }: { sweep: Sweep; onClose: () => void; onSubmitted: (s: Sweep) => void }) {
  const [rev, setRev] = useState<SweepReview | null>(null);
  const [conc, setConc] = useState("1");
  const [stop, setStop] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    reviewSweep(sweep.id).then((r) => { setRev(r); setConc(String(r.defaultConcurrency)); }).catch((e) => setErr(e instanceof Error ? e.message : "Could not read the scripts."));
  }, [sweep.id]);

  const n = Number(conc);
  const valid = Number.isInteger(n) && n >= 1 && n <= (rev?.maxConcurrency ?? 16);
  const unexpected = rev?.jobs.reduce((a, j) => a + j.unexpected, 0) ?? 0;
  const tooSmall = rev?.jobs.filter(shortOfTable).length ?? 0;
  const t = rev?.totals;

  async function go() {
    if (!rev) return;
    setBusy(true);
    try { onSubmitted(await submitSweep(sweep.id, rev.sweepHash, n, stop)); } catch (e) { setErr(e instanceof Error ? e.message : "Could not submit."); setBusy(false); }
  }

  const rounds = rev ? Math.ceil(rev.jobs.length / Math.max(1, valid ? n : 1)) : 0;

  return (
    <Modal label="Submit sweep" size="lg" onClose={onClose}>
      <h2 className={s.h}>Submit sweep {sweep.name}</h2>
      {!rev && !err ? <p className={s.note} role="status">Reading the scripts…</p> : null}
      {rev && t ? (
        <>
          <dl className={s.kv} aria-label="What approving sends">
            <dt>Scripts</dt>
            <dd>{rev.jobs.length}, each differing from the base job <span className="mono">{rev.baseName}</span> only on the lines below</dd>
            <dt>Requested in total</dt>
            <dd>{t.cpusTotal} CPUs, {gib(t.memBytesTotal)}, up to {formatDuration(t.timeLimitSecondsTotal)} of time limits</dd>
            <dt>Each job</dt>
            <dd>{t.cpusEach} CPUs, {gib(t.memBytesEach)}</dd>
            {rev.estimate ? (
              <>
                <dt>Time, as a guide</dt>
                <dd>{rev.estimate.baseJobName} took {formatDuration(rev.estimate.elapsedSeconds)}: {rounds} {rounds === 1 ? "round" : "rounds"} at this limit would take about {formatDuration(rev.estimate.elapsedSeconds * rounds)} if each job took as long</dd>
              </>
            ) : null}
          </dl>
          {rev.baseEditedLines > 0 ? (
            <Basis summary={`The base job's script was edited by hand (${rev.baseEditedLines} lines). The drafts are rendered from its settings and do not carry those edits.`}>
              <p>Each draft is compared below with what the base job&rsquo;s settings render, so a hand edit of the base is not counted against the drafts. If the edit matters for the sweep, apply it to each draft script before submitting.</p>
            </Basis>
          ) : null}
          {tooSmall > 0 ? <p className={s.warn} role="alert">{tooSmall} of {rev.jobs.length} drafts request less memory than your sizing table gives for their size (every draft requests what the base job did). Open a draft below for the numbers; a job that runs out of memory is lost time.</p> : null}
          {unexpected > 0 ? <p className={s.warn} role="alert">{unexpected} differing lines are not a swept setting, the output folder or the job name. Read them below before submitting.</p> : <p className={s.note}>No other line differs.</p>}
          <p className={s.note}><strong>Code commit.</strong> {rev.codePin.why}</p>
          <Input label={`Jobs at the same time (1 to ${rev.maxConcurrency})`} value={conc} inputMode="numeric" onChange={(e) => setConc(e.target.value)} aria-invalid={!valid} />
          {rev.concurrencyNote && n > 1 ? <p className={s.warn} role="alert">{rev.concurrencyNote}</p> : null}
          <Basis summary={n === 1 ? "Jobs run one after another: the next is submitted when the previous has ended." : `Up to ${valid ? n : "?"} jobs at once. Make sure they do not share a folder they write to.`}>
            {n === 1 && rev.concurrencyNote ? <p>{rev.concurrencyNote}</p> : null}
            <p>One at a time is the safe choice when jobs share a source folder, because a build step in it would collide.</p>
          </Basis>
          <Checkbox label="Stop the sweep when a job fails (otherwise a failure changes nothing for the others, and nothing is retried)" checked={stop} onChange={(e) => setStop(e.target.checked)} />
          {rev.jobs.map((j) => (
            <details key={j.jobId}>
              <summary>{j.label} <span className="mono">{j.name}</span> · {j.diff.length} lines differ{j.sizing ? ` · asks ${gib(j.requested.memBytes)}, table suggests ${j.sizing.mem}` : ""}</summary>
              {j.sizing ? <SizingLine sizing={j.sizing} requested={j.requested} short={shortOfTable(j)} /> : null}
              <pre className={s.diff} aria-label={`Differences for ${j.label}`}>
                {j.diff.map((l, i) => <span key={i} className={`${l.op === "-" ? s.del : s.add} ${l.kind === "other" ? s.other : ""}`}>{l.op} {l.text}   [{KIND_TEXT[l.kind]}]{"\n"}</span>)}
              </pre>
              <details>
                <summary>Whole script (sha256 {j.scriptHash.slice(0, 12)})</summary>
                <pre className={s.diff}>{j.script}</pre>
              </details>
            </details>
          ))}
        </>
      ) : null}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      <ModalActions>
        <Button onClick={onClose}>Not now</Button>
        <Button variant="primary" disabled={busy || !rev || !valid} onClick={go}>Submit {rev ? rev.jobs.length : ""} jobs, {valid ? n : "?"} at a time</Button>
      </ModalActions>
    </Modal>
  );
}
