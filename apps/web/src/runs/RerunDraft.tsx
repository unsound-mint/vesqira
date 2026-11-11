import { useEffect, useState } from "react";
import { Button, Input, PathField, Pill, Select } from "../ui";
import { createJob, readSetting } from "../api/jobs";
import type { Job } from "../api/jobs";
import type { Host } from "../api/hosts";
import { addJobToBoard, listBoards } from "../api/boards";
import type { Board } from "../api/boards";
import { getRerun } from "../api/runs";
import type { Rerun, Run } from "../api/runs";
import { bytes, EM } from "../catalog/format";
import { NOISE_MODES, SETTINGS } from "../jobs/JobsView";
import s from "./RerunDraft.module.css";

const MAX_NOTE = 500;

/** "1 h 46 min", "12 min", "45 s": how long the run took, for sizing the next request. */
export function duration(seconds: number): string {
  const t = Math.round(seconds);
  if (t < 60) return `${t} s`;
  const h = Math.floor(t / 3600);
  const m = Math.floor((t % 3600) / 60);
  return h > 0 ? `${h} h ${m} min` : `${m} min`;
}

/** What the run declared for a setting. Declared parameters may be namespaced ("vgt.ker"); a missing one is unknown, "". */
function declared(rerun: Rerun, key: string): string {
  const p = rerun.params.find((x) => x.key === key) ?? rerun.params.find((x) => x.key.endsWith(`.${key}`));
  return p?.value ?? "";
}

/** The value a setting had in the run being repeated: the job's own override, else the config text it started from. */
function baseline(rerun: Rerun, key: string): string {
  if (rerun.source === "imported") return declared(rerun, key);
  return rerun.vgt.set[key] ?? (rerun.vgt.baseConfigText ? readSetting(rerun.vgt.baseConfigText, key) : "");
}

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
  const base: Record<string, string> = {};
  for (const st of SETTINGS) base[st.key] = baseline(rerun, st.key);
  const hostKnown = rerun.hostId !== null && hosts.some((h) => h.id === rerun.hostId);

  const [vals, setVals] = useState<Record<string, string>>(base);
  const [name, setName] = useState(rerun.name);
  const [why, setWhy] = useState("");
  const [hostId, setHostId] = useState(hostKnown ? String(rerun.hostId) : "");
  const hostNum = hostId ? Number(hostId) : null; // no host chosen: the path fields do not complete
  const [repoDir, setRepoDir] = useState(rerun.vgt.repoDir ?? "");
  const [configPath, setConfigPath] = useState(rerun.vgt.configPath ?? "");
  const [res, setRes] = useState({
    cpus: rerun.vgt.cpus === null ? "" : String(rerun.vgt.cpus),
    mem: rerun.vgt.mem ?? "",
    time: rerun.vgt.time ?? "",
    partition: rerun.vgt.partition ?? "",
    account: rerun.vgt.account ?? "",
  });
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

  const changed = (key: string) => (vals[key] ?? "").trim() !== (base[key] ?? "").trim();
  const nChanged = SETTINGS.filter((st) => changed(st.key)).length;
  const missing = !hostId || !repoDir.trim() || !configPath.trim() || !name.trim() || !res.cpus.trim() || !res.mem.trim() || !res.time.trim();

  async function prepare() {
    setBusy(true);
    setErr(null);
    try {
      // a job's draft keeps the parent's own overrides and adds what changed; an imported run declared its values, so
      // the ones it declared are sent explicitly and the config file on the host supplies everything else
      const set: Record<string, string> = imported ? {} : { ...rerun.vgt.set };
      for (const st of SETTINGS) {
        const v = (vals[st.key] ?? "").trim();
        if (v && (imported || changed(st.key))) set[st.key] = v;
      }
      const job = await createJob({
        hostId: Number(hostId),
        template: "vgt",
        name: name.trim(),
        note: why.trim() || undefined,
        parentRunId: run.id,
        vgt: {
          repoDir: repoDir.trim(),
          configPath: configPath.trim(),
          ...(!imported && rerun.vgt.baseConfigText ? { configText: rerun.vgt.baseConfigText } : {}),
          set,
          cpus: Number(res.cpus),
          mem: res.mem.trim(),
          time: res.time.trim(),
          partition: res.partition.trim(),
          account: res.account.trim(),
        },
      });
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
        {SETTINGS.map((st) => {
          const hint = `was-${st.key}`;
          const isChanged = changed(st.key);
          const wasText = base[st.key] ? base[st.key] : EM;
          return (
            <div key={st.key} className={`${s.setting} ${isChanged ? s.changed : ""}`}>
              {st.kind === "noise_mode" ? (
                <Select label={st.label} value={vals[st.key] ?? ""} aria-describedby={isChanged ? hint : undefined} onChange={(e) => setVals({ ...vals, [st.key]: e.target.value })}>
                  <option value="">{imported ? "(not declared)" : "(keep)"}</option>
                  {NOISE_MODES.map((m) => <option key={m} value={m}>{m}</option>)}
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
        <dt>Input</dt>
        <dd>
          <span className="mono">{run.inputName || EM}</span>
          {run.inputSizeBytes > 0 ? ` · ${bytes(run.inputSizeBytes)}` : ""} <span className={s.src}>(as recorded by the parent run; the input file setting above decides what is read)</span>
        </dd>
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
        <PathField label="Config on the host" kind="file" ext={[".toml"]} hostId={hostNum} hosts={hosts} onHost={(id) => setHostId(String(id))} value={configPath} onChange={setConfigPath} required placeholder="/shared/home/you/vgt/configs/run.toml" />
        <Input label="CPUs" value={res.cpus} onChange={(e) => setRes({ ...res, cpus: e.target.value })} inputMode="numeric" required />
        <Input label="Memory" value={res.mem} onChange={(e) => setRes({ ...res, mem: e.target.value })} required />
        <Input label="Time limit" value={res.time} onChange={(e) => setRes({ ...res, time: e.target.value })} required />
        <Input label="Partition (optional)" value={res.partition} onChange={(e) => setRes({ ...res, partition: e.target.value })} />
        <Input label="Account (optional)" value={res.account} onChange={(e) => setRes({ ...res, account: e.target.value })} />
      </fieldset>
      {run.elapsedSeconds !== null && run.elapsedSeconds !== undefined ? <p className={s.note}>The last run took {duration(run.elapsedSeconds)}.</p> : null}

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
