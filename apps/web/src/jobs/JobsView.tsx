import { useCallback, useEffect, useState } from "react";
import { Button, Checkbox, Pill } from "../ui";
import { NotifyToggle } from "../notifications/NotifyToggle";
import { jobResult } from "../api/analysis";
import { NewJob } from "./NewJob";
import { cancelJob, editScript, getJob, jobLog, listJobTemplates, listJobs, refreshJob, registerOutputs, reopenJob, submitJob } from "../api/jobs";
import type { Job, JobState, JobTemplate } from "../api/jobs";
import type { Host } from "../api/hosts";
import type { Run } from "../api/runs";
import { HostStorage } from "../hosts/HostStorage";
import { UsageSummary } from "./JobUsage";
import { RerunCheck } from "./RerunCheck";
import { listSweeps } from "../api/sweeps";
import type { Sweep } from "../api/sweeps";
import { SweepForm } from "../sweeps/SweepForm";
import { SweepView } from "../sweeps/SweepView";
import s from "./JobsView.module.css";

export { previousUse } from "./NewJob";

export const TONE: Record<JobState, "neutral" | "ok" | "warn" | "err" | "accent"> = { draft: "neutral", submitting: "warn", submitted: "accent", running: "accent", completed: "ok", failed: "err", cancelled: "warn" };
const ACTIVE: JobState[] = ["submitted", "running"];

export function jobFor(jobs: Job[], runId: number): Job | null {
  return jobs.find((j) => j.runId === runId) ?? null;
}

/** Slurm jobs on your hosts: render a script from settings, read it, submit exactly that text, follow it, import its results. */
export function JobsView({ hosts, focusJob = null, onImported, onOpenRun }: { hosts: Host[]; focusJob?: number | null; onImported: (run: Run | null) => void; onOpenRun: (run: Run) => void }) {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [sel, setSel] = useState<number | null>(focusJob);
  const [err, setErr] = useState<string | null>(null);
  const [sweeps, setSweeps] = useState<Sweep[]>([]);
  const [selSweep, setSelSweep] = useState<number | null>(null);
  const [newSweep, setNewSweep] = useState<{ base: number | null } | null>(null);
  const [templates, setTemplates] = useState<JobTemplate[] | null>(null);

  const reload = useCallback(async () => {
    listSweeps().then((x) => Array.isArray(x) && setSweeps(x)).catch(() => undefined); // sweeps are optional here: without them the list is simply empty
    try {
      setJobs(await listJobs());
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not load jobs.");
    }
  }, []);
  useEffect(() => {
    void reload();
  }, [reload, focusJob]);
  useEffect(() => {
    // the tools the job form can be drawn for; without them the form says so instead of guessing at a tool's settings
    listJobTemplates().then(setTemplates).catch((e) => { setTemplates([]); setErr(e instanceof Error ? e.message : "Could not load the job templates."); });
  }, []);
  useEffect(() => {
    if (focusJob !== null) setSel(focusJob);
  }, [focusJob]);
  const job = jobs.find((j) => j.id === sel) ?? null;
  const sweep = sweeps.find((x) => x.id === selSweep) ?? null;
  const pickJob = (id: number | null) => { setSel(id); setSelSweep(null); setNewSweep(null); };
  const replaceSweep = (x: Sweep) => { setSweeps((cur) => (cur.some((y) => y.id === x.id) ? cur.map((y) => (y.id === x.id ? x : y)) : [x, ...cur])); void listJobs().then(setJobs).catch(() => undefined); };
  const replace = (j: Job) => setJobs((cur) => (cur.some((x) => x.id === j.id) ? cur.map((x) => (x.id === j.id ? j : x)) : [j, ...cur]));

  // follow an active job: ask the scheduler every 15 s while it is queued or running
  useEffect(() => {
    if (!job || !ACTIVE.includes(job.state)) return;
    const t = setInterval(() => refreshJob(job.id).then(replace).catch(() => undefined), 15000);
    return () => clearInterval(t);
  }, [job?.id, job?.state]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className={s.root}>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      <div className={s.cols}>
        <section className={s.list} aria-label="Jobs">
          <h2 className={s.h}>Jobs</h2>
          <button type="button" className={s.item} aria-pressed={sel === null && selSweep === null && !newSweep} onClick={() => pickJob(null)}>+ New job</button>
          <button type="button" className={s.item} aria-pressed={newSweep !== null} onClick={() => { setSel(null); setSelSweep(null); setNewSweep({ base: null }); }}>+ New sweep</button>
          {jobs.map((j) => (
            <button key={j.id} type="button" className={s.item} aria-pressed={sel === j.id} onClick={() => pickJob(j.id)}>
              <span className={s.nm}>{j.name}</span>
              <Pill tone={TONE[j.state]}>{j.state}</Pill>
            </button>
          ))}
          {jobs.length === 0 ? <p className={s.note}>No jobs yet.</p> : null}
          {sweeps.length > 0 ? <h2 className={s.h}>Sweeps</h2> : null}
          {sweeps.map((x) => (
            <button key={`sweep-${x.id}`} type="button" className={s.item} aria-pressed={selSweep === x.id} onClick={() => { setSel(null); setNewSweep(null); setSelSweep(x.id); }}>
              <span className={s.nm}>{x.name}</span>
              <Pill tone="neutral">{x.state}</Pill>
            </button>
          ))}
          <NotifyToggle className={s.note} />
        </section>
        <div className={s.main}>
          {newSweep ? (
            <SweepForm jobs={jobs} templates={templates} baseJobId={newSweep.base} onCreated={(x) => { replaceSweep(x); setNewSweep(null); setSelSweep(x.id); }} />
          ) : sweep ? (
            <SweepView sweep={sweep} onChange={replaceSweep} onOpenJob={pickJob} />
          ) : job ? (
            <JobDetail job={job} host={hosts.find((h) => h.id === job.hostId) ?? null} onChange={replace} onImported={onImported} onOpenRun={onOpenRun} onSweep={job.template === "vgt" || job.template === "rohsa" ? () => { setSel(null); setNewSweep({ base: job.id }); } : undefined} />
          ) : (
            <NewJob hosts={hosts} jobs={jobs} templates={templates} onCreated={(j) => { replace(j); setSel(j.id); }} />
          )}
        </div>
      </div>
    </div>
  );
}

function JobDetail({ job, host, onChange, onImported, onOpenRun, onSweep }: { job: Job; host: Host | null; onChange: (j: Job) => void; onImported: (run: Run | null) => void; onOpenRun: (run: Run) => void; onSweep?: () => void }) {
  const [script, setScript] = useState(job.script);
  const [masksNote, setMasksNote] = useState<string | null>(null); // a sentence when the result registered but its carried masks did not
  const [log, setLog] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [run, setRun] = useState<Run | null>(null);
  const [autoImport, setAutoImport] = useState(job.autoImport);
  useEffect(() => {
    setScript(job.script);
    setAutoImport(job.autoImport);
    setLog(null);
    setErr(null);
    setRun(null);
  }, [job.id]); // eslint-disable-line react-hooks/exhaustive-deps

  async function act<T>(f: () => Promise<T>, then?: (r: T) => void) {
    setBusy(true);
    try {
      const r = await f();
      then?.(r);
      setErr(null);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "That did not work.");
    }
    setBusy(false);
  }
  const draft = job.state === "draft";
  const dirty = script !== job.script;

  return (
    <div className={s.detail}>
      <div className={s.head}>
        <h2 className={s.h}>{job.name}</h2>
        <Pill tone={TONE[job.state]}>{job.state}</Pill>
        <span className={s.note}>{host?.name ?? "host removed"}{job.slurmId ? ` · Slurm job ${job.slurmId}` : ""}{job.detail ? ` · ${job.detail}` : ""}</span>
      </div>
      {job.note ? <p className={s.note}>Why: {job.note}</p> : null}
      {draft && job.parentRunId != null && (job.template === "vgt" || job.template === "rohsa") ? <RerunCheck key={job.id} job={job} /> : null}
      {job.usage ? <UsageSummary usage={job.usage} /> : null}
      <label className={s.label} htmlFor="job-script">Script{draft ? " (this exact text is what will be submitted; you can edit it)" : " as submitted"} · sha256 <span className="mono">{job.scriptHash.slice(0, 12)}</span></label>
      <textarea id="job-script" className={s.script} readOnly={!draft} rows={18} value={script} onChange={(e) => setScript(e.target.value)} spellCheck={false} />
      {draft ? (
        <div className={s.row}>
          <Button disabled={busy || !dirty} onClick={() => act(() => editScript(job.id, script), onChange)}>Save edits</Button>
          <Button variant="primary" disabled={busy || dirty} onClick={() => act(() => submitJob(job.id, job.scriptHash, job.template === "vgt" || job.template === "rohsa" ? autoImport : undefined).catch(async (e) => { const j = await getJob(job.id).catch(() => null); if (j) onChange(j); throw e; }), onChange)}>Submit this script</Button>
        </div>
      ) : null}
      {draft && (job.template === "vgt" || job.template === "rohsa") ? <Checkbox label={job.template === "rohsa" ? "When the job completes, import its fit as a run (the host reads the fit's small records and map headers; the .dat is not read; checks nothing)" : "When the job completes, import its results as a run (reads the output folder; checks nothing)"} checked={autoImport} onChange={(e) => setAutoImport(e.target.checked)} /> : null}
      {draft ? <p className={s.note}>Submitted from this computer to your own cluster account, so there is no extra approval step: the script above is what runs.{dirty ? " Save your edits before submitting." : ""}</p> : null}
      {!draft ? (
        <div className={s.row}>
          <Button disabled={busy} onClick={() => act(() => refreshJob(job.id), onChange)}>{job.state === "submitting" ? "Check on the host" : "Refresh status"}</Button>
          {job.state === "submitting" ? <Button disabled={busy} onClick={() => act(() => reopenJob(job.id), onChange)}>Not submitted: reopen as draft</Button> : null}
          {ACTIVE.includes(job.state) ? <Button disabled={busy} onClick={() => act(() => cancelJob(job.id), onChange)}>Cancel job</Button> : null}
          <Button disabled={busy} onClick={() => act(() => jobLog(job.id), setLog)}>Show log</Button>
          {job.template === "analysis" && job.state === "completed" ? <Button disabled={busy} onClick={() => act(() => jobResult(job.id), (r) => setLog(JSON.stringify(r.summary, null, 2)))}>Show the result</Button> : null}
          {job.template === "analysis" && job.state === "completed" ? <Button variant="primary" disabled={busy} onClick={() => act(() => registerOutputs(job.id), (r) => { setMasksNote((r as { masksNote?: string }).masksNote ?? null); onImported(null); })}>Register the result file</Button> : null}
          {(job.template === "vgt" || job.template === "rohsa") && job.state === "completed" && !job.runId ? <Button variant="primary" disabled={busy} onClick={() => act(() => registerOutputs(job.id), (r) => { setRun(r); onChange({ ...job, runId: r.id }); onImported(r); })}>Import results as a run</Button> : null}
        </div>
      ) : null}
      {!draft && (job.template === "vgt" || job.template === "rohsa") && !job.runId ? <p className={s.note}>{job.autoImport ? "Its results will be imported as a run when it completes." : "Its results are not imported automatically."}</p> : null}
      {onSweep ? <div className={s.row}><Button onClick={onSweep}>Sweep from this job…</Button></div> : null}
      {job.runId ? <p className={s.note}>Results imported. {run ? <Button onClick={() => onOpenRun(run)}>Open the run</Button> : "Find the run in the explorer."}</p> : null}
      {masksNote ? <p role="status" className={s.note} data-testid="masks-note">{masksNote}</p> : null}
      {log !== null ? <pre className={s.log} aria-label="Job log">{log || "No output yet."}</pre> : null}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {host ? <HostStorage host={host} /> : null}
    </div>
  );
}
