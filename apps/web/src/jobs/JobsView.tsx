import { useCallback, useEffect, useState } from "react";
import { Button, Input, Pill, Select } from "../ui";
import { jobResult } from "../api/analysis";
import { cancelJob, createJob, editScript, getJob, hostText, jobLog, listJobs, readSetting, refreshJob, registerOutputs, reopenJob, submitJob } from "../api/jobs";
import type { Job, JobState } from "../api/jobs";
import type { Host } from "../api/hosts";
import type { Run } from "../api/runs";
import s from "./JobsView.module.css";

export const TONE: Record<JobState, "neutral" | "ok" | "warn" | "err" | "accent"> = { draft: "neutral", submitting: "warn", submitted: "accent", running: "accent", completed: "ok", failed: "err", cancelled: "warn" };
const ACTIVE: JobState[] = ["submitted", "running"];
export const SETTINGS: { key: string; label: string; kind: "text" | "noise_mode" }[] = [
  { key: "input_file", label: "Input cube on the host", kind: "text" },
  { key: "ker", label: "ker (smoothing, pixels)", kind: "text" },
  { key: "dn", label: "dn (window, pixels)", kind: "text" },
  { key: "min_valid", label: "min_valid", kind: "text" },
  { key: "noise", label: "noise (1 sigma)", kind: "text" },
  { key: "noise_mode", label: "noise mode", kind: "noise_mode" },
  { key: "noise_sigma_multiplier", label: "noise sigma multiplier", kind: "text" },
];
export const NOISE_MODES = ["scalar", "channel", "spatial", "separable", "separable_shrinkage", "auto"];

export function jobFor(jobs: Job[], runId: number): Job | null {
  return jobs.find((j) => j.runId === runId) ?? null;
}

/** Slurm jobs on your hosts: render a script from settings, read it, submit exactly that text, follow it, import its results. */
export function JobsView({ hosts, focusJob = null, onImported, onOpenRun }: { hosts: Host[]; focusJob?: number | null; onImported: (run: Run | null) => void; onOpenRun: (run: Run) => void }) {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [sel, setSel] = useState<number | null>(focusJob);
  const [err, setErr] = useState<string | null>(null);

  const reload = useCallback(async () => {
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
    if (focusJob !== null) setSel(focusJob);
  }, [focusJob]);
  const job = jobs.find((j) => j.id === sel) ?? null;
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
          <button type="button" className={s.item} aria-pressed={sel === null} onClick={() => setSel(null)}>+ New job</button>
          {jobs.map((j) => (
            <button key={j.id} type="button" className={s.item} aria-pressed={sel === j.id} onClick={() => setSel(j.id)}>
              <span className={s.nm}>{j.name}</span>
              <Pill tone={TONE[j.state]}>{j.state}</Pill>
            </button>
          ))}
          {jobs.length === 0 ? <p className={s.note}>No jobs yet.</p> : null}
        </section>
        <div className={s.main}>
          {job ? (
            <JobDetail job={job} host={hosts.find((h) => h.id === job.hostId) ?? null} onChange={replace} onImported={onImported} onOpenRun={onOpenRun} />
          ) : (
            <NewJob hosts={hosts} onCreated={(j) => { replace(j); setSel(j.id); }} />
          )}
        </div>
      </div>
    </div>
  );
}

function NewJob({ hosts, onCreated }: { hosts: Host[]; onCreated: (j: Job) => void }) {
  const [hostId, setHostId] = useState<string>(hosts[0] ? String(hosts[0].id) : "");
  const [kind, setKind] = useState<"vgt" | "rohsa">("vgt");
  const [name, setName] = useState("vgt-run");
  const [repoDir, setRepoDir] = useState("");
  const [configPath, setConfigPath] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [vals, setVals] = useState<Record<string, string>>({});
  const [res, setRes] = useState({ cpus: "32", mem: "64G", time: "1-00:00:00", partition: "", account: "" });
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (hosts.length === 0) return <p className={s.note}>Add a host first (Hosts in the explorer). Jobs run on a host with Slurm, never on the login node itself.</p>;

  async function load() {
    setBusy(true);
    try {
      const text = await hostText(Number(hostId), configPath);
      const v: Record<string, string> = {};
      for (const st of SETTINGS) v[st.key] = readSetting(text, st.key);
      setVals(v);
      setLoaded(true);
      setErr(null);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not read the config.");
    }
    setBusy(false);
  }

  async function create() {
    setBusy(true);
    try {
      const set: Record<string, string> = {};
      for (const st of SETTINGS) if (vals[st.key]) set[st.key] = vals[st.key]!;
      if (kind === "rohsa") {
        onCreated(await createJob({ hostId: Number(hostId), template: "rohsa", name, rohsa: { repoDir, configPath, cpus: Number(res.cpus), mem: res.mem, time: res.time, partition: res.partition, account: res.account } }));
        setBusy(false);
        return;
      }
      const j = await createJob({ hostId: Number(hostId), template: "vgt", name, vgt: { repoDir, configPath, set, cpus: Number(res.cpus), mem: res.mem, time: res.time, partition: res.partition, account: res.account } });
      onCreated(j);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not create the job.");
    }
    setBusy(false);
  }

  return (
    <div className={s.form}>
      <h2 className={s.h}>New {kind === "vgt" ? "VGT" : "ROHSA"} job</h2>
      <Select label="Kind" value={kind} onChange={(e) => { const k = e.target.value as "vgt" | "rohsa"; setKind(k); setLoaded(false); setName(k === "vgt" ? "vgt-run" : "rohsa-run"); }}>
        <option value="vgt">VGT run</option>
        <option value="rohsa">ROHSA decomposition</option>
      </Select>
      {kind === "rohsa" ? <p className={s.note}>Runs the ROHSA build-and-decompose script of the pipeline on an existing ROHSA config on the host. Typical sizes from the pipeline notes: crop up to 512 → 16 CPUs, 12G, 3 h; up to 1024 → 16 CPUs, 40G, 12 h; up to 2048 → 32 CPUs, 100G, 1 day. Results go where the config&rsquo;s output folder says and are not imported as a run.</p> : null}
      <p className={s.note}>Start from a configuration that already exists on the host, adjust what you need, and read the generated script before anything is sent. The output folder is set for you inside the host’s source folders so the results can be opened here.</p>
      <Select label="Host" value={hostId} onChange={(e) => setHostId(e.target.value)}>
        {hosts.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
      </Select>
      <Input label="Job name" value={name} onChange={(e) => setName(e.target.value)} />
      <Input label="VGT folder on the host" value={repoDir} onChange={(e) => setRepoDir(e.target.value)} placeholder="/shared/home/you/vgt" spellCheck={false} />
      {kind === "rohsa" ? (
        <Input label="ROHSA config on the host" value={configPath} onChange={(e) => setConfigPath(e.target.value)} placeholder="/shared/home/you/vgt/configs/rohsa/run.toml" spellCheck={false} />
      ) : null}
      <div className={s.inline} hidden={kind === "rohsa"}>
        <Input label="Base config on the host" value={configPath} onChange={(e) => setConfigPath(e.target.value)} placeholder="/shared/home/you/vgt/configs/run.toml" spellCheck={false} />
        <Button type="button" disabled={busy || !configPath || !hostId} onClick={load}>Load settings</Button>
      </div>
      {loaded && kind === "vgt" ? (
        <fieldset className={s.fields}>
          <legend>Settings (from the config; change what this run needs)</legend>
          {SETTINGS.map((st) =>
            st.kind === "noise_mode" ? (
              <Select key={st.key} label={st.label} value={vals[st.key] ?? ""} onChange={(e) => setVals({ ...vals, [st.key]: e.target.value })}>
                <option value="">(keep)</option>
                {NOISE_MODES.map((m) => <option key={m} value={m}>{m}</option>)}
              </Select>
            ) : (
              <Input key={st.key} label={st.label} value={vals[st.key] ?? ""} onChange={(e) => setVals({ ...vals, [st.key]: e.target.value })} spellCheck={false} />
            ),
          )}
        </fieldset>
      ) : null}
      <fieldset className={s.fields}>
        <legend>Resources</legend>
        <Input label="CPUs" value={res.cpus} onChange={(e) => setRes({ ...res, cpus: e.target.value })} inputMode="numeric" />
        <Input label="Memory" value={res.mem} onChange={(e) => setRes({ ...res, mem: e.target.value })} />
        <Input label="Time limit" value={res.time} onChange={(e) => setRes({ ...res, time: e.target.value })} />
        <Input label="Partition (optional)" value={res.partition} onChange={(e) => setRes({ ...res, partition: e.target.value })} />
        <Input label="Account (optional)" value={res.account} onChange={(e) => setRes({ ...res, account: e.target.value })} />
      </fieldset>
      <Button variant="primary" disabled={busy || (kind === "vgt" && !loaded) || (kind === "rohsa" && !configPath) || !repoDir || !name} onClick={create}>Create draft and show the script</Button>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
    </div>
  );
}

function JobDetail({ job, host, onChange, onImported, onOpenRun }: { job: Job; host: Host | null; onChange: (j: Job) => void; onImported: (run: Run | null) => void; onOpenRun: (run: Run) => void }) {
  const [script, setScript] = useState(job.script);
  const [log, setLog] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [run, setRun] = useState<Run | null>(null);
  useEffect(() => {
    setScript(job.script);
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
      <label className={s.label} htmlFor="job-script">Script{draft ? " (this exact text is what will be submitted; you can edit it)" : " as submitted"} · sha256 <span className="mono">{job.scriptHash.slice(0, 12)}</span></label>
      <textarea id="job-script" className={s.script} readOnly={!draft} rows={18} value={script} onChange={(e) => setScript(e.target.value)} spellCheck={false} />
      {draft ? (
        <div className={s.row}>
          <Button disabled={busy || !dirty} onClick={() => act(() => editScript(job.id, script), onChange)}>Save edits</Button>
          <Button variant="primary" disabled={busy || dirty} onClick={() => act(() => submitJob(job.id, job.scriptHash).catch(async (e) => { const j = await getJob(job.id).catch(() => null); if (j) onChange(j); throw e; }), onChange)}>Submit this script</Button>
        </div>
      ) : null}
      {draft ? <p className={s.note}>Submitted from this computer to your own cluster account, so there is no extra approval step: the script above is what runs.{dirty ? " Save your edits before submitting." : ""}</p> : null}
      {!draft ? (
        <div className={s.row}>
          <Button disabled={busy} onClick={() => act(() => refreshJob(job.id), onChange)}>{job.state === "submitting" ? "Check on the host" : "Refresh status"}</Button>
          {job.state === "submitting" ? <Button disabled={busy} onClick={() => act(() => reopenJob(job.id), onChange)}>Not submitted: reopen as draft</Button> : null}
          {ACTIVE.includes(job.state) ? <Button disabled={busy} onClick={() => act(() => cancelJob(job.id), onChange)}>Cancel job</Button> : null}
          <Button disabled={busy} onClick={() => act(() => jobLog(job.id), setLog)}>Show log</Button>
          {job.template === "analysis" && job.state === "completed" ? <Button disabled={busy} onClick={() => act(() => jobResult(job.id), (r) => setLog(JSON.stringify(r.summary, null, 2)))}>Show the result</Button> : null}
          {job.template === "analysis" && job.state === "completed" ? <Button variant="primary" disabled={busy} onClick={() => act(() => registerOutputs(job.id), () => onImported(null))}>Register the result file</Button> : null}
          {job.template === "vgt" && job.state === "completed" && !job.runId ? <Button variant="primary" disabled={busy} onClick={() => act(() => registerOutputs(job.id), (r) => { setRun(r); onChange({ ...job, runId: r.id }); onImported(r); })}>Import results as a run</Button> : null}
        </div>
      ) : null}
      {job.runId ? <p className={s.note}>Results imported. {run ? <Button onClick={() => onOpenRun(run)}>Open the run</Button> : "Find the run in the explorer."}</p> : null}
      {log !== null ? <pre className={s.log} aria-label="Job log">{log || "No output yet."}</pre> : null}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
    </div>
  );
}
