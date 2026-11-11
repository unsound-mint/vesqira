import { useEffect, useState } from "react";
import { Button, Input, PathField, Pill, Select } from "../ui";
import { createJob, hostText, readSetting } from "../api/jobs";
import type { Job } from "../api/jobs";
import type { Host } from "../api/hosts";
import { addJobToBoard, listBoards } from "../api/boards";
import type { Board } from "../api/boards";
import { getRerun } from "../api/runs";
import type { Rerun, Run } from "../api/runs";
import { bytes, EM } from "../catalog/format";
import { NOISE_MODES, SETTINGS } from "../jobs/JobsView";
import { SizingHint } from "../jobs/JobUsage";
import { RohsaSizingHint } from "../jobs/RohsaSizingHint";
import { KERNELS, ROHSA_SETTINGS, readRohsaSetting, sameValue } from "../jobs/rohsaSettings";
import { formatDuration, slurmLimitGloss } from "../ui/duration";
import s from "./RerunDraft.module.css";

const MAX_NOTE = 500;

/** What the run declared for a setting. Declared parameters may be namespaced ("vgt.ker"); a missing one is unknown, "". */
function declared(rerun: Rerun, key: string): string {
  const p = rerun.params.find((x) => x.key === key) ?? rerun.params.find((x) => x.key.endsWith(`.${key}`));
  return p?.value ?? "";
}

/** The value a setting had in the run being repeated: the job's own override, else the config text it started from. */
function baseline(rerun: Rerun, key: string): string {
  if (rerun.template === "rohsa") return rerun.settings?.[key] ?? ""; // read by the server from the config the job ran, or declared by the run's trace
  if (rerun.source === "imported") return declared(rerun, key);
  return rerun.vgt.set[key] ?? (rerun.vgt.baseConfigText ? readSetting(rerun.vgt.baseConfigText, key) : "");
}

type Field = { key: string; label: string; kind: string };

/** "Run again with…": a draft job that starts from a finished run. Nothing is submitted here; the script is read and submitted on the Jobs screen. */
export function RerunDraft({ run, hosts, onBack, onPrepared }: { run: Run; hosts: Host[]; onBack: () => void; onPrepared: (job: Job) => void }) {
  const [rerun, setRerun] = useState<Rerun | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    setRerun(null);
    setLoadErr(null);
    getRerun(run.id).then((r) => live && setRerun(r)).catch((e) => live && setLoadErr(e instanceof Error ? e.message : "Could not read the run's settings."));
    return () => { live = false; };
  }, [run.id]);

  return (
    <div className={s.root}>
      <div className={s.page}>
        <nav className={s.crumbs} aria-label="Breadcrumb">
          <Button type="button" onClick={onBack}>Back</Button>
          <span className={s.trail}>Runs / <span className="mono">{run.name}</span> / run again</span>
        </nav>
        {loadErr ? <p role="alert" className={s.err}>{loadErr}</p> : null}
        {!rerun && !loadErr ? <p className={s.note} role="status">Reading how {run.name} was set up…</p> : null}
        {rerun ? <Form key={run.id} run={run} rerun={rerun} hosts={hosts} onPrepared={onPrepared} /> : null}
      </div>
    </div>
  );
}

function Form({ run, rerun, hosts, onPrepared }: { run: Run; rerun: Rerun; hosts: Host[]; onPrepared: (job: Job) => void }) {
  const imported = rerun.source === "imported";
  const rohsa = rerun.template === "rohsa";
  const req = (rohsa ? rerun.rohsa : null) ?? rerun.vgt;
  const fields: Field[] = rohsa ? ROHSA_SETTINGS : SETTINGS;
  const declaredBase: Record<string, string> = {};
  for (const st of fields) declaredBase[st.key] = baseline(rerun, st.key);
  const hostKnown = rerun.hostId !== null && hosts.some((h) => h.id === rerun.hostId);

  const [vals, setVals] = useState<Record<string, string>>(declaredBase);
  const [name, setName] = useState(rerun.name);
  const [why, setWhy] = useState("");
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
  // An imported ROHSA run does not declare its crop or its spectral kernel: once a config is chosen on a host, its text gives the
  // "was" of those fields (and the crop for the sizing table). The server reads the config itself when it renders the job.
  const [cfgText, setCfgText] = useState("");
  useEffect(() => {
    if (!rohsa || !imported || !hostId || !configPath.trim()) { setCfgText(""); return; }
    let live = true;
    hostText(Number(hostId), configPath.trim()).then((t) => live && setCfgText(t)).catch(() => live && setCfgText(""));
    return () => { live = false; };
  }, [rohsa, imported, hostId, configPath]);
  const base: Record<string, string> = {};
  for (const st of fields) base[st.key] = declaredBase[st.key] || (rohsa && cfgText ? readRohsaSetting(cfgText, st.key) : "");
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

  const changed = (key: string) => rohsa ? (vals[key] ?? "").trim() !== "" && !sameValue(vals[key] ?? "", base[key] ?? "") : (vals[key] ?? "").trim() !== (base[key] ?? "").trim();
  const nChanged = fields.filter((st) => changed(st.key)).length;
  const missing = !hostId || !repoDir.trim() || !configPath.trim() || !name.trim() || !res.cpus.trim() || !res.mem.trim() || !res.time.trim();

  async function prepare() {
    setBusy(true);
    setErr(null);
    try {
      // a job's draft keeps the parent's own overrides and adds what changed; an imported run declared its values, so
      // the ones it declared are sent explicitly and the config file on the host supplies everything else
      const set: Record<string, string> = imported ? {} : { ...req.set };
      for (const st of fields) {
        const v = (vals[st.key] ?? "").trim();
        if (v && (imported || changed(st.key))) set[st.key] = v;
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
      };
      const job = await createJob(rohsa
        ? { hostId: Number(hostId), template: "rohsa", name: name.trim(), note: why.trim() || undefined, parentRunId: run.id, rohsa: common }
        : { hostId: Number(hostId), template: "vgt", name: name.trim(), note: why.trim() || undefined, parentRunId: run.id, vgt: common });
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
      await addJobToBoard(board.id, job.id);
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

      <fieldset className={s.fields}>
        <legend>Settings (as the pipeline reads them){nChanged > 0 ? ` · ${nChanged} changed` : ""}</legend>
        {fields.map((st) => {
          const hint = `was-${st.key}`;
          const isChanged = changed(st.key);
          const wasText = base[st.key] ? base[st.key] : EM;
          const options = st.kind === "noise_mode" ? NOISE_MODES : st.kind === "bool" ? ["true", "false"] : st.kind === "kernel" ? KERNELS : null;
          return (
            <div key={st.key} className={`${s.setting} ${isChanged ? s.changed : ""}`}>
              {options ? (
                <Select label={st.label} value={vals[st.key] ?? ""} aria-describedby={isChanged ? hint : undefined} onChange={(e) => setVals({ ...vals, [st.key]: e.target.value })}>
                  <option value="">{imported ? "(not declared)" : "(keep)"}</option>
                  {options.map((m) => <option key={m} value={m}>{m}</option>)}
                </Select>
              ) : (
                <Input label={st.label} value={vals[st.key] ?? ""} aria-describedby={isChanged ? hint : undefined} onChange={(e) => setVals({ ...vals, [st.key]: e.target.value })} spellCheck={false} />
              )}
              {isChanged ? (
                <span id={hint} className={s.was}>changed · was <span className="mono">{wasText}</span></span>
              ) : imported ? (
                <span className={s.was}>{base[st.key] ? "declared by the run" : "not declared by the run"}</span>
              ) : null}
            </div>
          );
        })}
      </fieldset>

      <dl className={s.facts}>
        <dt>Code</dt>
        <dd>
          <span className="mono">{run.gitCommit ? run.gitCommit.slice(0, 12) : EM}</span>
          {run.dirty ? " · uncommitted changes" : ""} <span className={s.src}>(from the parent run; the job runs the code folder on the host as it is now)</span>
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

      <label className={s.label} htmlFor="rerun-why">Why (kept with the job)</label>
      <textarea id="rerun-why" className={s.area} rows={2} maxLength={MAX_NOTE} value={why} onChange={(e) => setWhy(e.target.value)} />
      <span className={s.count}>{why.length} / {MAX_NOTE}</span>

      <fieldset className={s.fields}>
        <legend>Where and how much</legend>
        <Select label="Host" value={hostId} onChange={(e) => setHostId(e.target.value)} required>
          <option value="">{imported ? "(choose; the run does not say)" : "(choose)"}</option>
          {hosts.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
        </Select>
        <PathField label="VGT folder on the host" kind="dir" hostId={hostNum} hosts={hosts} onHost={(id) => setHostId(String(id))} value={repoDir} onChange={setRepoDir} required placeholder="/shared/home/you/vgt" />
        <PathField label={rohsa ? "ROHSA config on the host" : "Config on the host"} kind="file" ext={[".toml"]} hostId={hostNum} hosts={hosts} onHost={(id) => setHostId(String(id))} value={configPath} onChange={setConfigPath} required placeholder="/shared/home/you/vgt/configs/run.toml" />
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
