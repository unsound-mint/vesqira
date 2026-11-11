import { useCallback, useEffect, useState } from "react";
import { Button, Checkbox, Input, PathField, Pill, Select } from "../ui";
import { slurmLimitGloss } from "../ui/duration";
import { NotifyToggle } from "../notifications/NotifyToggle";
import { jobResult } from "../api/analysis";
import { KERNELS, ROHSA_SETTINGS, readRohsaSetting, sameValue } from "./rohsaSettings";
import { RohsaSizingHint } from "./RohsaSizingHint";
import { cancelJob, createJob, editScript, getJob, hostText, jobLog, listJobs, readSetting, refreshJob, registerOutputs, reopenJob, submitJob } from "../api/jobs";
import type { Job, JobState } from "../api/jobs";
import type { Host } from "../api/hosts";
import type { Run } from "../api/runs";
import { HostStorage } from "../hosts/HostStorage";
import { SizingHint, UsageSummary } from "./JobUsage";
import { listSweeps } from "../api/sweeps";
import type { Sweep } from "../api/sweeps";
import { SweepForm } from "../sweeps/SweepForm";
import { SweepView } from "../sweeps/SweepView";
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
  const [sweeps, setSweeps] = useState<Sweep[]>([]);
  const [selSweep, setSelSweep] = useState<number | null>(null);
  const [newSweep, setNewSweep] = useState<{ base: number | null } | null>(null);

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
            <SweepForm jobs={jobs} baseJobId={newSweep.base} onCreated={(x) => { replaceSweep(x); setNewSweep(null); setSelSweep(x.id); }} />
          ) : sweep ? (
            <SweepView sweep={sweep} onChange={replaceSweep} onOpenJob={pickJob} />
          ) : job ? (
            <JobDetail job={job} host={hosts.find((h) => h.id === job.hostId) ?? null} onChange={replace} onImported={onImported} onOpenRun={onOpenRun} onSweep={job.template === "vgt" || job.template === "rohsa" ? () => { setSel(null); setNewSweep({ base: job.id }); } : undefined} />
          ) : (
            <NewJob hosts={hosts} jobs={jobs} onCreated={(j) => { replace(j); setSel(j.id); }} />
          )}
        </div>
      </div>
    </div>
  );
}

/** The most recent finished job of this kind on this host that has a measured use, to size a new request from. */
export function previousUse(jobs: Job[], hostId: number | null, template: string): Job | null {
  let best: Job | null = null;
  for (const j of jobs) if (j.hostId === hostId && j.template === template && j.usage && (!best || j.id > best.id)) best = j;
  return best;
}

function NewJob({ hosts, jobs, onCreated }: { hosts: Host[]; jobs: Job[]; onCreated: (j: Job) => void }) {
  const [hostId, setHostId] = useState<string>(hosts[0] ? String(hosts[0].id) : "");
  const [kind, setKind] = useState<"vgt" | "rohsa">("vgt");
  const [name, setName] = useState("vgt-run");
  const hostNum = hostId ? Number(hostId) : null; // no host chosen: the path fields do not complete
  const [repoDir, setRepoDir] = useState("");
  const [configPath, setConfigPath] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [vals, setVals] = useState<Record<string, string>>({});
  const [loadedVals, setLoadedVals] = useState<Record<string, string>>({}); // ROHSA: what the config said, so only what the person changed is sent
  const [res, setRes] = useState({ cpus: "32", mem: "64G", time: "1-00:00:00", partition: "", account: "" });
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (hosts.length === 0) return <p className={s.note}>Add a host first (Hosts in the explorer). Jobs run on a host with Slurm, never on the login node itself.</p>;

  async function load() {
    setBusy(true);
    try {
      const text = await hostText(Number(hostId), configPath);
      const v: Record<string, string> = {};
      if (kind === "rohsa") for (const st of ROHSA_SETTINGS) v[st.key] = readRohsaSetting(text, st.key);
      else for (const st of SETTINGS) v[st.key] = readSetting(text, st.key);
      setVals(v);
      setLoadedVals(v);
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
        const rset: Record<string, string> = {};
        for (const st of ROHSA_SETTINGS) { const x = (vals[st.key] ?? "").trim(); if (x && !sameValue(x, loadedVals[st.key] ?? "")) rset[st.key] = x; }
        onCreated(await createJob({ hostId: Number(hostId), template: "rohsa", name, rohsa: { repoDir, configPath, set: rset, cpus: Number(res.cpus), mem: res.mem, time: res.time, partition: res.partition, account: res.account } }));
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
      {kind === "rohsa" ? <p className={s.note}>Runs the ROHSA build-and-decompose script of the pipeline on a copy of a ROHSA config from the host, with the settings you change applied (every other line as it was). The output folder is set for each job; a fit is read from files on this machine, so copy its folder under a source folder here and import its rohsa_*.dat from Runs. A sizing table for the request appears below.</p> : null}
      <p className={s.note}>Start from a configuration that already exists on the host, adjust what you need, and read the generated script before anything is sent. The output folder is set for you inside the host’s source folders so the results can be opened here.</p>
      <Select label="Host" value={hostId} onChange={(e) => setHostId(e.target.value)}>
        {hosts.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
      </Select>
      <Input label="Job name" value={name} onChange={(e) => setName(e.target.value)} />
      <PathField label="VGT folder on the host" kind="dir" hostId={hostNum} hosts={hosts} onHost={(id) => setHostId(String(id))} value={repoDir} onChange={setRepoDir} placeholder="/shared/home/you/vgt" />
      <div className={s.inline}>
        <PathField label={kind === "rohsa" ? "ROHSA config on the host" : "Base config on the host"} kind="file" ext={[".toml"]} hostId={hostNum} hosts={hosts} onHost={(id) => setHostId(String(id))} value={configPath} onChange={setConfigPath} placeholder={kind === "rohsa" ? "/shared/home/you/vgt/configs/rohsa/run.toml" : "/shared/home/you/vgt/configs/run.toml"} />
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
      {loaded && kind === "rohsa" ? (
        <fieldset className={s.fields}>
          <legend>Settings (from the config; change what this run needs)</legend>
          {ROHSA_SETTINGS.map((st) => {
            const opts = st.kind === "bool" ? ["true", "false"] : st.kind === "kernel" ? KERNELS : null;
            return opts ? (
              <Select key={st.key} label={st.label} value={vals[st.key] ?? ""} onChange={(e) => setVals({ ...vals, [st.key]: e.target.value })}>
                <option value="">(keep)</option>
                {opts.map((m) => <option key={m} value={m}>{m}</option>)}
              </Select>
            ) : (
              <Input key={st.key} label={st.label} value={vals[st.key] ?? ""} onChange={(e) => setVals({ ...vals, [st.key]: e.target.value })} spellCheck={false} />
            );
          })}
        </fieldset>
      ) : null}
      {kind === "rohsa" ? <RohsaSizingHint cropSize={vals.crop_size ?? ""} nGauss={vals.n_gauss ?? ""} cpus={res.cpus} mem={res.mem} time={res.time} onUse={(r) => setRes((x) => ({ ...x, ...r }))} /> : null}
      {(() => {
        const prev = previousUse(jobs, hostNum, kind);
        return prev?.usage ? <SizingHint jobId={prev.id} jobName={prev.name} usage={prev.usage} mem={res.mem} time={res.time} onUseMem={(mem) => setRes((r) => ({ ...r, mem }))} onUseTime={(time) => setRes((r) => ({ ...r, time }))} /> : null;
      })()}
      <fieldset className={s.fields}>
        <legend>Resources</legend>
        <Input label="CPUs" value={res.cpus} onChange={(e) => setRes({ ...res, cpus: e.target.value })} inputMode="numeric" />
        <Input label="Memory" value={res.mem} onChange={(e) => setRes({ ...res, mem: e.target.value })} />
        <Input label="Time limit" hint={slurmLimitGloss(res.time)} value={res.time} onChange={(e) => setRes({ ...res, time: e.target.value })} />
        <Input label="Partition (optional)" value={res.partition} onChange={(e) => setRes({ ...res, partition: e.target.value })} />
        <Input label="Account (optional)" value={res.account} onChange={(e) => setRes({ ...res, account: e.target.value })} />
      </fieldset>
      <Button variant="primary" disabled={busy || (kind === "vgt" && !loaded) || (kind === "rohsa" && !configPath) || !repoDir || !name} onClick={create}>Create draft and show the script</Button>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
    </div>
  );
}

function JobDetail({ job, host, onChange, onImported, onOpenRun, onSweep }: { job: Job; host: Host | null; onChange: (j: Job) => void; onImported: (run: Run | null) => void; onOpenRun: (run: Run) => void; onSweep?: () => void }) {
  const [script, setScript] = useState(job.script);
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
      {job.usage ? <UsageSummary usage={job.usage} /> : null}
      <label className={s.label} htmlFor="job-script">Script{draft ? " (this exact text is what will be submitted; you can edit it)" : " as submitted"} · sha256 <span className="mono">{job.scriptHash.slice(0, 12)}</span></label>
      <textarea id="job-script" className={s.script} readOnly={!draft} rows={18} value={script} onChange={(e) => setScript(e.target.value)} spellCheck={false} />
      {draft ? (
        <div className={s.row}>
          <Button disabled={busy || !dirty} onClick={() => act(() => editScript(job.id, script), onChange)}>Save edits</Button>
          <Button variant="primary" disabled={busy || dirty} onClick={() => act(() => submitJob(job.id, job.scriptHash, job.template === "vgt" ? autoImport : undefined).catch(async (e) => { const j = await getJob(job.id).catch(() => null); if (j) onChange(j); throw e; }), onChange)}>Submit this script</Button>
        </div>
      ) : null}
      {draft && job.template === "vgt" ? <Checkbox label="When the job completes, import its results as a run (reads the output folder; checks nothing)" checked={autoImport} onChange={(e) => setAutoImport(e.target.checked)} /> : null}
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
      {!draft && job.template === "vgt" && !job.runId ? <p className={s.note}>{job.autoImport ? "Its results will be imported as a run when it completes." : "Its results are not imported automatically."}</p> : null}
      {onSweep ? <div className={s.row}><Button onClick={onSweep}>Sweep from this job…</Button></div> : null}
      {job.runId ? <p className={s.note}>Results imported. {run ? <Button onClick={() => onOpenRun(run)}>Open the run</Button> : "Find the run in the explorer."}</p> : null}
      {log !== null ? <pre className={s.log} aria-label="Job log">{log || "No output yet."}</pre> : null}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {host ? <HostStorage host={host} /> : null}
    </div>
  );
}
