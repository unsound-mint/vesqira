import { useEffect, useState } from "react";
import { Button, Checkbox, Input, PathField, Select } from "../ui";
import { slurmLimitGloss } from "../ui/duration";
import { createJob, hostText } from "../api/jobs";
import type { Job, JobTemplate, TemplateKey } from "../api/jobs";
import type { Host } from "../api/hosts";
import { RohsaSizingHint } from "./RohsaSizingHint";
import { SizingHint } from "./JobUsage";
import { changedSettings, choices, commandLine, editableKeys, keyHint, readConfigSetting, stepLine } from "./templateForm";
import s from "./JobsView.module.css";

/** The most recent finished job of this kind on this host that has a measured use, to size a new request from. */
export function previousUse(jobs: Job[], hostId: number | null, template: string): Job | null {
  let best: Job | null = null;
  for (const j of jobs) if (j.hostId === hostId && j.template === template && j.usage && (!best || j.id > best.id)) best = j;
  return best;
}

const startingRequest = (t: JobTemplate) => ({ cpus: String(t.resources.cpus), mem: t.resources.mem, time: t.resources.time, partition: "", account: "" });

/**
 * A new job, drawn from the tool profile's template (ADR 0068): the folder and config it asks for, one field per key the template
 * lets a person change (with its range, its default and a plain description), what the script does before the command, and the
 * request. Only the settings the person changed are sent; the server checks each against the template, writes the script and
 * shows it before anything is submitted.
 */
export function NewJob({ hosts, jobs, templates, onCreated }: { hosts: Host[]; jobs: Job[]; templates: JobTemplate[] | null; onCreated: (j: Job) => void }) {
  const [hostId, setHostId] = useState<string>(hosts[0] ? String(hosts[0].id) : "");
  const [tid, setTid] = useState<string>("");
  const t = templates?.find((x) => x.id === tid) ?? templates?.[0] ?? null;
  const [name, setName] = useState("");
  const hostNum = hostId ? Number(hostId) : null; // no host chosen: the path fields do not complete
  const [repoDir, setRepoDir] = useState("");
  const [configPath, setConfigPath] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [vals, setVals] = useState<Record<string, string>>({});
  const [was, setWas] = useState<Record<string, string>>({}); // what the config said, so only what the person changed is sent
  const [res, setRes] = useState({ cpus: "32", mem: "64G", time: "1-00:00:00", partition: "", account: "" });
  const [pin, setPin] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // the form follows the template it is drawn from: the name, the starting request and the pin start again with each
  const id = t?.id ?? "";
  useEffect(() => {
    if (!t) return;
    setName(`${t.id}-run`);
    setRes(startingRequest(t));
    setPin(false);
    setLoaded(false);
    setVals({});
    setWas({});
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  if (hosts.length === 0) return <p className={s.note}>Add a host first (Hosts in the explorer). Jobs run on a host with Slurm, never on the login node itself.</p>;
  if (!t) return <p className={s.note}>{templates === null ? "Loading the tools this app can write jobs for…" : "No tool profile declares a job template."}</p>;

  async function load() {
    if (!t) return;
    setBusy(true);
    try {
      const text = await hostText(Number(hostId), configPath);
      const v: Record<string, string> = {};
      for (const k of editableKeys(t)) v[k.name] = readConfigSetting(text, k);
      setVals(v);
      setWas(v);
      setLoaded(true);
      setErr(null);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not read the config.");
    }
    setBusy(false);
  }

  async function create() {
    if (!t) return;
    setBusy(true);
    try {
      const j = await createJob({
        hostId: Number(hostId),
        template: t.id,
        name,
        tool: { repoDir, configPath, set: changedSettings(t, vals, was), cpus: Number(res.cpus), mem: res.mem, time: res.time, partition: res.partition, account: res.account, ...(pin ? { pinProfileCommit: true } : {}) },
      });
      onCreated(j);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not create the job.");
    }
    setBusy(false);
  }

  const field = (k: TemplateKey) => {
    const hint = keyHint(k);
    const description = hint ? `${k.description} (${hint})` : k.description;
    const opts = choices(k);
    const set = (v: string) => setVals((cur) => ({ ...cur, [k.name]: v }));
    return opts ? (
      <Select key={k.name} label={k.label} description={description} value={vals[k.name] ?? ""} onChange={(e) => set(e.target.value)}>
        <option value="">(keep)</option>
        {opts.map((m) => <option key={m} value={m}>{m}</option>)}
      </Select>
    ) : (
      <Input key={k.name} label={k.label} description={description} value={vals[k.name] ?? ""} onChange={(e) => set(e.target.value)} spellCheck={false} />
    );
  };
  const steps = t.steps.map(stepLine).filter((x): x is string => x !== null);
  const prev = previousUse(jobs, hostNum, t.id);
  const short = t.repo.commit.slice(0, 7);

  return (
    <div className={s.form}>
      <h2 className={s.h}>New {t.tool} job</h2>
      {templates && templates.length > 1 ? (
        <Select label="Kind" value={t.id} onChange={(e) => setTid(e.target.value)}>
          {templates.map((x) => <option key={x.id} value={x.id}>{x.title}</option>)}
        </Select>
      ) : null}
      <p className={s.note}>{t.about}</p>
      <p className={s.note}>Start from a configuration that already exists on the host, adjust what you need, and read the generated script before anything is sent. Declared by the profile {t.profile}.</p>
      <Select label="Host" value={hostId} onChange={(e) => setHostId(e.target.value)}>
        {hosts.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
      </Select>
      <Input label="Job name" value={name} onChange={(e) => setName(e.target.value)} />
      <PathField label={t.repo.folderLabel} kind="dir" hostId={hostNum} hosts={hosts} onHost={(h) => setHostId(String(h))} value={repoDir} onChange={setRepoDir} placeholder={t.repo.placeholder} />
      <p className={s.note}>{t.repo.hint || `The checkout of ${t.repo.url}.`} Code: <span className="mono">{t.repo.url}</span> at <span className="mono">{short}</span>.</p>
      <Checkbox label={`Stop the job unless the folder is at commit ${short}, with no changed tracked files (the revision this profile was written against)`} checked={pin} onChange={(e) => setPin(e.target.checked)} />
      <div className={s.inline}>
        <PathField label={t.config.label} kind="file" ext={[".toml"]} hostId={hostNum} hosts={hosts} onHost={(h) => setHostId(String(h))} value={configPath} onChange={setConfigPath} placeholder={t.config.placeholder} />
        <Button type="button" disabled={busy || !configPath || !hostId} onClick={load}>Load settings</Button>
      </div>
      {loaded ? (
        <fieldset className={s.fields}>
          <legend>Settings (from the config; change what this run needs)</legend>
          {editableKeys(t).map(field)}
        </fieldset>
      ) : null}
      {t.sizing === "rohsa_crop_table" ? <RohsaSizingHint cropSize={vals.crop_size ?? ""} nGauss={vals.n_gauss ?? ""} cpus={res.cpus} mem={res.mem} time={res.time} onUse={(r) => setRes((x) => ({ ...x, ...r }))} /> : null}
      {prev?.usage ? <SizingHint jobId={prev.id} jobName={prev.name} usage={prev.usage} mem={res.mem} time={res.time} onUseMem={(mem) => setRes((r) => ({ ...r, mem }))} onUseTime={(time) => setRes((r) => ({ ...r, time }))} /> : null}
      <fieldset className={s.fields}>
        <legend>Resources</legend>
        <Input label="CPUs" value={res.cpus} onChange={(e) => setRes({ ...res, cpus: e.target.value })} inputMode="numeric" />
        <Input label="Memory" value={res.mem} onChange={(e) => setRes({ ...res, mem: e.target.value })} />
        <Input label="Time limit" hint={slurmLimitGloss(res.time)} value={res.time} onChange={(e) => setRes({ ...res, time: e.target.value })} />
        <Input label="Partition (optional)" value={res.partition} onChange={(e) => setRes({ ...res, partition: e.target.value })} />
        <Input label="Account (optional)" value={res.account} onChange={(e) => setRes({ ...res, account: e.target.value })} />
      </fieldset>
      <p className={s.note}>{t.resources.reason}</p>
      <details className={s.prep}>
        <summary>What the script does before and when it runs</summary>
        <ol>
          <li>Writes the config, with your changes, into the job folder.</li>
          {steps.map((x, i) => <li key={i}>{x}</li>)}
          <li>Runs <span className="mono">{commandLine(t)}</span></li>
        </ol>
        <p className={s.note}>{t.repo.verified}</p>
      </details>
      <Button variant="primary" disabled={busy || (t.config.required ? !configPath : !loaded) || !repoDir || !name} onClick={create}>Create draft and show the script</Button>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
    </div>
  );
}
