import { useCallback, useEffect, useState } from "react";
import { ApiError } from "../api/http";
import { listRuns } from "../api/runs";
import type { Run } from "../api/runs";
import { approveRelayed, declineRelayed, detachRelayed, draftRelayed, getRelayed, stateText } from "../api/runRequests";
import type { Relayed, RelayedDetail } from "../api/runRequests";
import { Button, Checkbox, Input, Select } from "../ui";
import s from "../account/Account.module.css";
import r from "./RunRequests.module.css";

const when = (iso: string) => iso.slice(0, 16).replace("T", " ");

/** An approval that did not go through, in the server's words plus what it means for the job: nothing is submitted without the claim. */
function approvalError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 502) return `${e.message} The group server could not be reached, so nothing was claimed and nothing was submitted. Try again when it answers, or detach to keep the draft as your own job.`;
    if (e.status === 409) return `${e.message} The request was not claimed and nothing was submitted.`;
    return e.message;
  }
  return e instanceof Error ? e.message : "Something went wrong.";
}
const message = (e: unknown) => (e instanceof ApiError || e instanceof Error ? e.message : "Something went wrong.");

const changesText = (p: Record<string, string>) => Object.entries(p).map(([k, v]) => `${k} = ${v}`).join("\n");
function parseChanges(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const i = line.indexOf("=");
    if (i > 0 && line.slice(0, i).trim()) out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}
const numberOrUndef = (v: string) => (v.trim() === "" ? undefined : Number(v));

/**
 * One relayed request on the key holder's computer: what was asked, the draft rendered on this machine, the exact script (or the
 * sweep's scripts) and the three things the key holder may do. Approve sends the hash of the script as shown here; the server
 * refuses it if the script has changed since.
 */
export function RelayedView({ summary, onChanged, onClose }: { summary: Relayed; onChanged: () => unknown; onClose: () => void }) {
  const [detail, setDetail] = useState<RelayedDetail | null>(null);
  const [runs, setRuns] = useState<Run[]>([]);
  const [error, setError] = useState("");
  const [done, setDone] = useState("");
  const load = useCallback(() => getRelayed(summary.id).then(setDetail, (e) => setError(message(e))), [summary.id]);
  useEffect(() => {
    void load();
    void listRuns().then(setRuns, () => undefined);
  }, [load]);
  const q = detail?.request ?? summary;
  const ask = q.request.ask;
  const editable = q.localState === "received" || q.localState === "draft";
  return (
    <section className={s.card} aria-label={`Request ${q.serverId}`}>
      <div className={s.row}>
        <h2 className={s.h2}>{ask.template} on {ask.label}, from {q.request.requester}</h2>
        <Button onClick={onClose}>Close</Button>
      </div>
      <p className={s.note}>{q.localState}{q.refusal ? `: ${q.refusal}` : ""}. On the server: {stateText(q.serverState)}. Expires {when(q.request.expiresAt)}.{q.request.base ? ` Based on ${q.request.base.title}.` : ""}</p>
      {detail?.outsideBounds ? <p className={r.warn} role="alert">{detail.outsideBounds}</p> : null}
      <Ask relayed={q} />
      {error ? <p className={s.error} role="alert">{error}</p> : null}
      {done ? <p className={s.note} role="status">{done}</p> : null}
      {editable ? <DraftForm key={`${q.updatedAt}`} relayed={q} runs={runs} onDrafted={() => { setError(""); setDone(""); void onChanged(); return load(); }} onError={setError} /> : null}
      {/* an approved request whose draft was never submitted (refused before Slurm, or this computer stopped) is approved again */}
      {detail?.job && (q.localState === "draft" || (q.localState === "approved" && detail.job.state === "draft")) ? <SingleReview detail={detail} onError={(m) => { setError(m); setDone(""); }} onDone={(m) => { setError(""); setDone(m); void onChanged(); void load(); }} /> : null}
      {detail?.sweep && q.localState === "draft" ? <SweepReview detail={detail} onError={(m) => { setError(m); setDone(""); }} onDone={(m) => { setError(""); setDone(m); void onChanged(); void load(); }} /> : null}
      {editable ? <Actions relayed={q} onError={(m) => { setError(m); setDone(""); }} onDone={(m) => { setError(""); setDone(m); void onChanged(); void load(); }} /> : null}
    </section>
  );
}

function Ask({ relayed }: { relayed: Relayed }) {
  const ask = relayed.request.ask;
  const res = ask.resources;
  return (
    <div className={s.form}>
      {Object.keys(ask.params).length ? <p className={s.note}>Settings asked for: {Object.entries(ask.params).map(([k, v]) => `${k} = ${v}`).join(", ")}</p> : <p className={s.note}>No settings changed.</p>}
      <p className={s.note}>Resources asked for: {res.cpus ? `${res.cpus} CPUs` : "CPUs as the base"}, {res.memGiB ? `${res.memGiB} GiB` : "memory as the base"}, {res.timeMinutes ? `${res.timeMinutes} minutes` : "time as the base"}.</p>
      {ask.grid.length ? <p className={s.note}>Grid: {ask.grid.map((g) => `${g.key} in ${g.values.join(", ")}`).join("; ")}</p> : null}
      {ask.note ? <p className={`${s.note} ${r.plain}`}>Note from {relayed.request.requester}: {ask.note}</p> : null}
      {relayed.request.notes.map((n, i) => <p key={i} className={`${s.note} ${r.plain}`}>Follow-up ({when(n.at)}): {n.text}</p>)}
    </div>
  );
}

function DraftForm({ relayed, runs, onDrafted, onError }: { relayed: Relayed; runs: Run[]; onDrafted: () => unknown; onError: (m: string) => void }) {
  const [base, setBase] = useState(relayed.baseRunId ? String(relayed.baseRunId) : "");
  const [changes, setChanges] = useState(changesText(relayed.draft.params));
  const [cpus, setCpus] = useState(relayed.draft.resources.cpus ? String(relayed.draft.resources.cpus) : "");
  const [mem, setMem] = useState(relayed.draft.resources.memGiB ? String(relayed.draft.resources.memGiB) : "");
  const [time, setTime] = useState(relayed.draft.resources.timeMinutes ? String(relayed.draft.resources.timeMinutes) : "");
  const [busy, setBusy] = useState(false);
  const go = () => {
    setBusy(true);
    onError("");
    draftRelayed(relayed.id, {
      baseRunId: base ? Number(base) : undefined,
      changes: parseChanges(changes),
      resources: { cpus: numberOrUndef(cpus), memGiB: numberOrUndef(mem), timeMinutes: numberOrUndef(time) },
    }).then(() => onDrafted(), (e) => onError(message(e))).finally(() => setBusy(false));
  };
  return (
    <fieldset className={s.form}>
      <legend className={s.note}>{relayed.localState === "draft" ? "Draft again" : "Draft"}: choose one of your own runs to start from</legend>
      {relayed.baseChosen ? <p className={s.note}>Base chosen by you.</p> : null}
      <Select label="Base run" value={base} onChange={(e) => setBase(e.target.value)}>
        <option value="">{relayed.request.base ? "The run it was based on, if this computer knows it" : "Choose a run"}</option>
        {runs.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
      </Select>
      <label className={r.area}>
        Settings to change, one &ldquo;key = value&rdquo; per line (only what the inbox allows is accepted)
        <textarea value={changes} onChange={(e) => setChanges(e.target.value)} />
      </label>
      <div className={r.grid}>
        <Input label="CPUs (lower only)" inputMode="numeric" value={cpus} onChange={(e) => setCpus(e.target.value)} />
        <Input label="Memory GiB (lower only)" inputMode="numeric" value={mem} onChange={(e) => setMem(e.target.value)} />
        <Input label="Time minutes (lower only)" inputMode="numeric" value={time} onChange={(e) => setTime(e.target.value)} />
      </div>
      <div className={s.row}><Button variant="primary" disabled={busy} onClick={go}>{busy ? "Drafting…" : relayed.localState === "draft" ? "Draft again" : "Draft"}</Button></div>
    </fieldset>
  );
}

type Review = { detail: RelayedDetail; onError: (m: string) => void; onDone: (m: string) => void };

function SingleReview({ detail, onError, onDone }: Review) {
  const job = detail.job;
  const [autoPush, setAutoPush] = useState(true); // the dialog's default (ADR 0066); the key holder sees and may untick it
  const [busy, setBusy] = useState(false);
  if (!job) return null;
  // the hash of the script on screen is the one approval quotes
  const hash = job.scriptHash;
  const approve = () => {
    setBusy(true);
    approveRelayed(detail.request.id, { scriptHash: hash, autoPush }).then((a) => onDone(a.message || (a.submitted ? "Approved and submitted." : "Approved; not yet submitted.")), (e) => onError(approvalError(e))).finally(() => setBusy(false));
  };
  return (
    <section className={s.form} aria-label="Script to approve">
      <h3 className={s.h2}>The script that will run</h3>
      <p className={s.note}>{job.name}, sha256 {hash.slice(0, 12)}{job.editedLines ? `, edited by hand: ${job.editedLines} lines` : ""}. Approving claims the request on the group server first and submits only after the claim.</p>
      <pre className={r.script} tabIndex={0} aria-label="Script">{job.script}</pre>
      <Checkbox label="Push the resulting run line to the group server when it is imported" checked={autoPush} onChange={(e) => setAutoPush(e.target.checked)} />
      <div className={s.row}><Button variant="primary" disabled={busy} onClick={approve}>{busy ? "Approving…" : `Approve script ${hash.slice(0, 8)}`}</Button></div>
    </section>
  );
}

function SweepReview({ detail, onError, onDone }: Review) {
  const sweep = detail.sweep;
  const [dropped, setDropped] = useState<Set<number>>(new Set());
  const [conc, setConc] = useState("1");
  const [stop, setStop] = useState(false);
  const [busy, setBusy] = useState(false);
  if (!sweep) return null;
  const n = Number(conc);
  const valid = Number.isInteger(n) && n >= 1 && n <= Math.min(16, sweep.maxConcurrency || 16);
  const kept = sweep.drafts.length - dropped.size;
  const toggle = (id: number) => setDropped((d) => { const x = new Set(d); if (x.has(id)) x.delete(id); else x.add(id); return x; });
  const approve = () => {
    setBusy(true);
    approveRelayed(detail.request.id, { sweepHash: sweep.sweepHash, drop: [...dropped], concurrency: n, stopOnFailure: stop }).then((a) => onDone(a.message || (a.submitted ? "Approved and submitted." : "Approved; not yet submitted.")), (e) => onError(approvalError(e))).finally(() => setBusy(false));
  };
  return (
    <section className={s.form} aria-label="Sweep to approve">
      <h3 className={s.h2}>The sweep ({sweep.drafts.length} drafts)</h3>
      <p className={s.note}>Sweep sha256 {sweep.sweepHash.slice(0, 12)}. Untick a combination to leave it out. Each draft is rendered from your base run and carries no hand edits. Its run lines are pushed by hand.</p>
      {sweep.drafts.map((d) => (
        <div key={d.jobId} className={s.form}>
          <Checkbox label={`${d.name}: ${Object.entries(d.values).map(([k, v]) => `${k} = ${v}`).join(", ")}`} checked={!dropped.has(d.jobId)} onChange={() => toggle(d.jobId)} />
          <details>
            <summary>Script (sha256 {d.scriptHash.slice(0, 12)})</summary>
            <pre className={r.script} tabIndex={0}>{d.script}</pre>
          </details>
        </div>
      ))}
      <Input label={`Jobs at the same time (1 to ${Math.min(16, sweep.maxConcurrency || 16)})`} inputMode="numeric" value={conc} onChange={(e) => setConc(e.target.value)} aria-invalid={!valid} />
      <Checkbox label="Stop the sweep when a job fails" checked={stop} onChange={(e) => setStop(e.target.checked)} />
      <div className={s.row}><Button variant="primary" disabled={busy || !valid || kept < 1} onClick={approve}>{busy ? "Approving…" : `Approve ${kept} of ${sweep.drafts.length}`}</Button></div>
    </section>
  );
}

function Actions({ relayed, onError, onDone }: { relayed: Relayed; onError: (m: string) => void; onDone: (m: string) => void }) {
  const [note, setNote] = useState("");
  const run = (p: Promise<unknown>, ok: string) => p.then(() => onDone(ok), (e) => onError(message(e)));
  return (
    <fieldset className={s.form}>
      <legend className={s.note}>Not running it</legend>
      <label className={r.area}>
        Note to the requester (sent with a decline)
        <textarea value={note} maxLength={4000} onChange={(e) => setNote(e.target.value)} />
      </label>
      <div className={s.row}>
        <Button variant="danger" disabled={!note.trim()} onClick={() => run(declineRelayed(relayed.id, note), "Declined.")}>Decline with this note</Button>
        {relayed.localState === "draft" ? <Button onClick={() => run(detachRelayed(relayed.id), "Detached: the draft is now your own job.")}>Detach the draft as my own job</Button> : null}
      </div>
    </fieldset>
  );
}
