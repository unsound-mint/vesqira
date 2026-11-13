import { useEffect, useState } from "react";
import { Button, Checkbox, Input, PathField, Pill, Select } from "../ui";
import { createJob, hostText, listJobTemplates, readSetting } from "../api/jobs";
import type { Job, JobTemplate } from "../api/jobs";
import type { Host } from "../api/hosts";
import { listBoards } from "../api/boards";
import { addJob } from "../undo/boardCommands";
import type { Board } from "../api/boards";
import { getRerun } from "../api/runs";
import type { Rerun, RerunMode, Run } from "../api/runs";
import { bytes, EM } from "../catalog/format";
import { SizingHint } from "../jobs/JobUsage";
import { RohsaSizingHint } from "../jobs/RohsaSizingHint";
import { PIN_RERUN_NOTE, choices, editableKeys, keyDescription, pinLabel, readConfigSetting, sameValue } from "../jobs/templateForm";
import { formatDuration, slurmLimitGloss } from "../ui/duration";
import s from "./RerunDraft.module.css";

const MAX_NOTE = 500;

/** What the run declared for a setting. Declared parameters may be namespaced ("vgt.ker"); a missing one is unknown, "". */
function declared(rerun: Rerun, key: string): string {
  const p = rerun.params.find((x) => x.key === key) ?? rerun.params.find((x) => x.key.endsWith(`.${key}`));
  return p?.value ?? "";
}

/** The value a setting had in the run being repeated: the job's own override, else the config text it started from. */
function baseline(rerun: Rerun, key: string, byName: boolean): string {
  if (byName) return rerun.settings?.[key] ?? ""; // ROHSA: read by the server from the config the job ran, or declared by the run's trace
  if (rerun.source === "imported") return declared(rerun, key);
  return rerun.vgt.set[key] ?? (rerun.vgt.baseConfigText ? readSetting(rerun.vgt.baseConfigText, key) : "");
}

const MODES: { id: RerunMode; name: string; what: string }[] = [
  { id: "adapted", name: "Adapted", what: "Change settings. The settings that differ from the parent are marked. The code is the folder on the host as it is when you submit." },
  { id: "best_effort", name: "Best effort", what: "Repeat it with the code as it is now. Before you submit, every difference from what the parent run observed is listed." },
  { id: "strict", name: "Strict", what: "Same code revision, no changed file, same Manifest.toml and input file as the parent observed. It is not submitted if the host's code folder is not exactly that, and the job stops itself if the folder moves while it waits. Settings stay as they were." },
];

/** Why a strict rerun cannot be offered for this run, or "" when it can. The server decides again; this only saves a failed request. */
export function strictRefusal(run: Run): string {
  const env = run.environment ?? [];
  const val = (k: string) => env.find((f) => f.key === k && f.status === "verified")?.value;
  if (val("code.commit")) {
    const dirty = val("code.dirty");
    if (dirty === "yes") return "the parent run's code had uncommitted changes, which its commit does not describe";
    if (dirty !== "no") return "whether the parent run's tracked files had changed was not observed, so its commit may not describe its code";
    return "";
  }
  const declared = run.gitCommit.replace(/-dirty$/, "");
  if (!declared) return "the parent run recorded no code revision";
  if (run.gitCommit.endsWith("-dirty")) return "the parent run declares uncommitted changes, which its commit does not describe";
  if (!/^[0-9a-f]{40}([0-9a-f]{24})?$/.test(declared)) return "the parent run declares only a short revision, which cannot be compared exactly";
  return "";
}

/** "Run again with…": a draft job that starts from a finished run. Nothing is submitted here; the script is read and submitted on the Jobs screen. */
export function RerunDraft({ run, hosts, onBack, onPrepared }: { run: Run; hosts: Host[]; onBack: () => void; onPrepared: (job: Job) => void }) {
  const [rerun, setRerun] = useState<Rerun | null>(null);
  const [templates, setTemplates] = useState<JobTemplate[] | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    setRerun(null);
    setLoadErr(null);
    getRerun(run.id).then((r) => live && setRerun(r)).catch((e) => live && setLoadErr(e instanceof Error ? e.message : "Could not read the run's settings."));
    return () => { live = false; };
  }, [run.id]);
  // the settings offered are the ones the tool's profile declares (ADR 0068), the same as the job form
  useEffect(() => {
    let live = true;
    listJobTemplates().then((t) => live && setTemplates(t)).catch((e) => live && (setTemplates([]), setLoadErr(e instanceof Error ? e.message : "Could not load the job templates.")));
    return () => { live = false; };
  }, []);
  const template = rerun ? templates?.find((x) => x.id === rerun.template) ?? null : null;

  return (
    <div className={s.root}>
      <div className={s.page}>
        <nav className={s.crumbs} aria-label="Breadcrumb">
          <Button type="button" onClick={onBack}>Back</Button>
          <span className={s.trail}>Runs / <span className="mono">{run.name}</span> / run again</span>
        </nav>
        {loadErr ? <p role="alert" className={s.err}>{loadErr}</p> : null}
        {(!rerun || templates === null) && !loadErr ? <p className={s.note} role="status">Reading how {run.name} was set up…</p> : null}
        {rerun && templates !== null && !template && !loadErr ? <p role="alert" className={s.err}>No tool profile declares the job template {rerun.template} of this run, so there are no settings to offer.</p> : null}
        {rerun && template ? <Form key={run.id} run={run} rerun={rerun} template={template} hosts={hosts} onPrepared={onPrepared} /> : null}
      </div>
    </div>
  );
}

function Form({ run, rerun, template: t, hosts, onPrepared }: { run: Run; rerun: Rerun; template: JobTemplate; hosts: Host[]; onPrepared: (job: Job) => void }) {
  const imported = rerun.source === "imported";
  const rohsa = t.sizing === "rohsa_crop_table";
  const req = rerun.rohsa ?? rerun.vgt;
  const fields = editableKeys(t);
  const declaredBase: Record<string, string> = {};
  for (const st of fields) declaredBase[st.name] = baseline(rerun, st.name, t.config.edit === "by_name");
  const hostKnown = rerun.hostId !== null && hosts.some((h) => h.id === rerun.hostId);

  const [vals, setVals] = useState<Record<string, string>>(declaredBase);
  const [name, setName] = useState(rerun.name);
  const [why, setWhy] = useState("");
  const [mode, setMode] = useState<RerunMode>("adapted");
  const [pin, setPin] = useState(req.pinProfile === true); // the parent job's script insisted on the profile's commit: a new draft starts the same
  const strictNo = strictRefusal(run);
  const [hostId, setHostId] = useState(hostKnown ? String(rerun.hostId) : "");
  const hostNum = hostId ? Number(hostId) : null; // no host chosen: the path fields do not complete
  const [repoDir, setRepoDir] = useState(req.repoDir ?? "");
  const [configPath, setConfigPath] = useState(req.configPath ?? "");
  const [res, setRes] = useState({
    cpus: req.cpus === null ? "" : String(req.cpus),
    mem: req.mem ?? "",
    time: req.time ?? "",
    partition: req.partition ?? "",
    account: req.account ?? "",
  });
  // An imported run that is edited by key name (ROHSA) does not declare its crop or its spectral kernel: once a config is chosen on a
  // host, its text gives the "was" of those fields (and the crop for the sizing table). The server reads the config itself.
  const byName = t.config.edit === "by_name";
  const [cfgText, setCfgText] = useState("");
  useEffect(() => {
    if (!byName || !imported || !hostId || !configPath.trim()) { setCfgText(""); return; }
    let live = true;
    hostText(Number(hostId), configPath.trim()).then((t) => live && setCfgText(t)).catch(() => live && setCfgText(""));
    return () => { live = false; };
  }, [byName, imported, hostId, configPath]);
  const base: Record<string, string> = {};
  for (const st of fields) base[st.name] = declaredBase[st.name] || (byName && cfgText ? readConfigSetting(cfgText, st) : "");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [boards, setBoards] = useState<Board[]>([]);
  const [boardId, setBoardId] = useState(""); // "" is no board
  // the draft exists once created; it is kept so a failed board add is retried without creating a second draft
  const [draft, setDraft] = useState<{ job: Job; board: Board | null; error: string | null } | null>(null);
  useEffect(() => {
    let live = true;
    listBoards().then((b) => live && Array.isArray(b) && setBoards(b)).catch(() => {}); // the board choice is optional: without the list it is simply not offered
    return () => { live = false; };
  }, []);

  // a field left empty is not a change (it is "keep"); the same quantity written two ways ("10" and "10.0") is not one either
  const changed = (key: string) => (vals[key] ?? "").trim() !== "" && !sameValue(vals[key] ?? "", base[key] ?? "");
  const nChanged = fields.filter((st) => changed(st.name)).length;
  const strictBlocked = mode === "strict" && (strictNo !== "" || nChanged > 0);
  const missing = strictBlocked || !hostId || !repoDir.trim() || !configPath.trim() || !name.trim() || !res.cpus.trim() || !res.mem.trim() || !res.time.trim();

  async function prepare() {
    setBusy(true);
    setErr(null);
    try {
      // a job's draft keeps the parent's own overrides and adds what changed; an imported run declared its values, so
      // the ones it declared are sent explicitly and the config file on the host supplies everything else
      const set: Record<string, string> = imported ? {} : { ...req.set };
      for (const st of fields) {
        const v = (vals[st.name] ?? "").trim();
        if (v && (imported || changed(st.name))) set[st.name] = v;
      }
      const common = {
        repoDir: repoDir.trim(),
        configPath: configPath.trim(),
        ...(!imported && req.baseConfigText ? { configText: req.baseConfigText } : {}),
        set,
        cpus: Number(res.cpus),
        mem: res.mem.trim(),
        time: res.time.trim(),
        partition: res.partition.trim(),
        account: res.account.trim(),
        ...(pin ? { pinProfileCommit: true } : {}),
      };
      const job = await createJob({ hostId: Number(hostId), template: t.id, name: name.trim(), note: why.trim() || undefined, parentRunId: run.id, rerunMode: mode, tool: common });
      const board = boards.find((b) => String(b.id) === boardId) ?? null;
      if (!board) {
        onPrepared(job);
        return;
      }
      await addToChosen(job, board);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not prepare the job.");
      setBusy(false);
    }
  }

  async function addToChosen(job: Job, board: Board) {
    try {
      await addJob(board.id, job.id);
      setDraft({ job, board, error: null });
    } catch (e) {
      setDraft({ job, board, error: e instanceof Error ? e.message : "Could not add the job to the board." });
    }
    setBusy(false);
  }

  return (
    <form className={s.form} aria-label="New run draft" onSubmit={(e) => { e.preventDefault(); if (!missing && !busy) void prepare(); }}>
      <header className={s.head}>
        <h2 className={s.title}>New run · draft from <span className="mono">{run.name}</span></h2>
        <Pill>not submitted</Pill>
      </header>

      {rerun.notes.map((n) => <p key={n} className={s.warn} role="note">{n}</p>)}
      {rerun.configChanged === true ? (
        <p className={s.warn} role="alert">The config file on the host has changed since this run. This draft uses the text the run started from, not the file as it is now.</p>
      ) : null}
      {rerun.configChanged === null && !imported ? <p className={s.warn} role="note">Could not check whether the config file on the host changed.</p> : null}

      <fieldset className={s.modes}>
        <legend>How closely to repeat it</legend>
        {MODES.map((m) => {
          const refused = m.id === "strict" && strictNo !== "";
          const id = `rerun-mode-${m.id}`;
          return (
            <div key={m.id} className={s.mode}>
              <input type="radio" id={id} name="rerun-mode" value={m.id} checked={mode === m.id} disabled={refused} aria-describedby={`${id}-what`} onChange={() => setMode(m.id)} />
              <label htmlFor={id} className={s.modeText}>
                <strong>{m.name}</strong>
                <span id={`${id}-what`} className={s.modeNote}>{m.what}{refused ? ` Not available: ${strictNo}.` : ""}</span>
              </label>
            </div>
          );
        })}
        {mode === "strict" && nChanged > 0 ? <p className={s.warn} role="alert">A strict rerun repeats the run with its own settings, and {nChanged} {nChanged === 1 ? "is" : "are"} changed below. Choose Adapted to change settings.</p> : null}
        {mode === "strict" ? <p className={s.note}>The code folder is read on the host (read-only) when you submit. Julia version, loaded modules, node and CPU are known only when the job runs.</p> : null}
      </fieldset>

      <fieldset className={s.fields}>
        <legend>Settings (as the pipeline reads them){nChanged > 0 ? ` · ${nChanged} changed` : ""}</legend>
        {fields.map((st) => {
          const hint = `was-${st.name}`;
          const isChanged = changed(st.name);
          const wasText = base[st.name] ? base[st.name] : EM;
          const options = choices(st);
          return (
            <div key={st.name} className={`${s.setting} ${isChanged ? s.changed : ""}`}>
              {options ? (
                <Select label={st.label} description={keyDescription(st)} value={vals[st.name] ?? ""} aria-describedby={isChanged ? hint : undefined} onChange={(e) => setVals({ ...vals, [st.name]: e.target.value })}>
                  <option value="">{imported ? "(not declared)" : "(keep)"}</option>
                  {options.map((m) => <option key={m} value={m}>{m}</option>)}
                  {vals[st.name] && !options.includes(vals[st.name]!) ? <option value={vals[st.name]}>{vals[st.name]} (not accepted for a new run)</option> : null}
                </Select>
              ) : (
                <Input label={st.label} description={keyDescription(st)} value={vals[st.name] ?? ""} aria-describedby={isChanged ? hint : undefined} onChange={(e) => setVals({ ...vals, [st.name]: e.target.value })} spellCheck={false} />
              )}
              {isChanged ? (
                <span id={hint} className={s.was}>changed · was <span className="mono">{wasText}</span></span>
              ) : imported ? (
                <span className={s.was}>{base[st.name] ? "declared by the run" : "not declared by the run"}</span>
              ) : null}
            </div>
          );
        })}
      </fieldset>

      <dl className={s.facts}>
        <dt>Code</dt>
        <dd>
          <span className="mono">{run.gitCommit ? run.gitCommit.slice(0, 12) : EM}</span>
          {run.dirty ? " · uncommitted changes" : ""} <span className={s.src}>(from the parent run; the job runs the code folder on the host as it is now{mode === "strict" ? ", and a strict rerun stops unless it is at this commit" : ""})</span>
        </dd>
        {rohsa ? null : (
          <>
            <dt>Input</dt>
            <dd>
              <span className="mono">{run.inputName || EM}</span>
              {run.inputSizeBytes > 0 ? ` · ${bytes(run.inputSizeBytes)}` : ""} <span className={s.src}>(as recorded by the parent run; the input file setting above decides what is read)</span>
            </dd>
          </>
        )}
      </dl>

      <Checkbox label={pinLabel(t)} checked={pin} onChange={(e) => setPin(e.target.checked)} />
      <p className={s.note}>{PIN_RERUN_NOTE}</p>

      <label className={s.label} htmlFor="rerun-why">Why (kept with the job)</label>
      <textarea id="rerun-why" className={s.area} rows={2} maxLength={MAX_NOTE} value={why} onChange={(e) => setWhy(e.target.value)} />
      <span className={s.count}>{why.length} / {MAX_NOTE}</span>

      <fieldset className={s.fields}>
        <legend>Where and how much</legend>
        <Select label="Host" value={hostId} onChange={(e) => setHostId(e.target.value)} required>
          <option value="">{imported ? "(choose; the run does not say)" : "(choose)"}</option>
          {hosts.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
        </Select>
        <div className={s.wide}>
          <PathField label={t.repo.folderLabel} kind="dir" hostId={hostNum} hosts={hosts} onHost={(id) => setHostId(String(id))} value={repoDir} onChange={setRepoDir} required placeholder={t.repo.placeholder} />
          {t.repo.hint ? <p className={s.note}>{t.repo.hint}</p> : null}
        </div>
        <div className={s.wide}>
          <PathField label={t.config.label} kind="file" ext={[".toml"]} hostId={hostNum} hosts={hosts} onHost={(id) => setHostId(String(id))} value={configPath} onChange={setConfigPath} required placeholder={t.config.placeholder} />
        </div>
        <Input label="CPUs" value={res.cpus} onChange={(e) => setRes({ ...res, cpus: e.target.value })} inputMode="numeric" required />
        <Input label="Memory" value={res.mem} onChange={(e) => setRes({ ...res, mem: e.target.value })} required />
        <Input label="Time limit" hint={slurmLimitGloss(res.time)} value={res.time} onChange={(e) => setRes({ ...res, time: e.target.value })} required />
        <Input label="Partition (optional)" value={res.partition} onChange={(e) => setRes({ ...res, partition: e.target.value })} />
        <Input label="Account (optional)" value={res.account} onChange={(e) => setRes({ ...res, account: e.target.value })} />
      </fieldset>
      {run.elapsedSeconds !== null && run.elapsedSeconds !== undefined ? <p className={s.note}>The last run took {formatDuration(run.elapsedSeconds)}.</p> : null}
      {rerun.lastJob?.usage ? <SizingHint jobId={rerun.lastJob.id} jobName={rerun.lastJob.name} usage={rerun.lastJob.usage} mem={res.mem} time={res.time} onUseMem={(mem) => setRes((r) => ({ ...r, mem }))} onUseTime={(time) => setRes((r) => ({ ...r, time }))} /> : null}

      {rohsa ? (
        <>
          <p className={s.note}>The new job writes its own copy of the config with the settings above applied (every other line as it was, the output folder set for this job), so it never writes over the earlier run. The input cube is the config&rsquo;s.</p>
          <RohsaSizingHint cropSize={(vals.crop_size ?? "").trim() || base.crop_size || ""} nGauss={(vals.n_gauss ?? "").trim() || base.n_gauss || ""} cpus={res.cpus} mem={res.mem} time={res.time}
            onUse={(r) => setRes((x) => ({ ...x, ...r }))} />
        </>
      ) : null}
      <Input label="Job name" value={name} onChange={(e) => setName(e.target.value)} required />
      {boards.length > 0 ? (
        <Select label="Add to a board (optional)" value={boardId} onChange={(e) => setBoardId(e.target.value)} disabled={draft !== null}>
          <option value="">No board</option>
          {boards.map((b) => <option key={b.id} value={b.id}>{b.title}</option>)}
        </Select>
      ) : null}

      <div className={s.row}>
        <Button type="submit" variant="primary" disabled={busy || missing || draft !== null}>Prepare the job</Button>
        <span className={s.note}>Nothing is submitted until you press Submit on the next screen.</span>
      </div>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {draft?.error ? (
        <div role="alert" className={s.err}>
          <p>The draft was created but was not added to the board {draft.board?.title}: {draft.error}</p>
          <div className={s.row}>
            <Button type="button" disabled={busy || !draft.board} onClick={() => { setBusy(true); void addToChosen(draft.job, draft.board!); }}>Try adding again</Button>
            <Button type="button" onClick={() => onPrepared(draft.job)}>Open the draft without a board</Button>
          </div>
        </div>
      ) : null}
      {draft && !draft.error ? (
        <div className={s.row}>
          <span className={s.note} role="status">The draft was added to the board {draft.board?.title}.</span>
          <Button type="button" onClick={() => onPrepared(draft.job)}>Open the job</Button>
        </div>
      ) : null}
    </form>
  );
}
