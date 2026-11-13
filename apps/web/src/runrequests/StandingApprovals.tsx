import { useCallback, useEffect, useMemo, useState } from "react";
import { ApiError } from "../api/http";
import { boundText, createGrant, hours, listGrants, relayMembers, revokeGrant } from "../api/runRequests";
import type { Bound, Grant, GrantList, RelayLocal, RelayMember } from "../api/runRequests";
import { Button, Checkbox, Input, Pill, Select } from "../ui";
import s from "../account/Account.module.css";
import r from "./RunRequests.module.css";

const message = (e: unknown) => (e instanceof ApiError || e instanceof Error ? e.message : "Something went wrong.");
const day = (iso: string) => iso.slice(0, 10);
const lines = (t: string) => t.split("\n").map((x) => x.trim()).filter(Boolean);

/** A grant's narrowed settings as one line: "ker 4 to 8; dn one of 8, 16". */
export function grantKeysText(keys: Bound[]): string {
  if (!keys.length) return "no settings changed";
  return keys.map((k) => (k.choices?.length ? `${k.key} one of ${k.choices.join(", ")}` : `${k.key} ${k.min} to ${k.max}`)).join("; ");
}

function status(g: Grant): { tone: "ok" | "warn" | "neutral"; text: string } {
  if (g.revokedAt) return { tone: "neutral", text: `revoked ${day(g.revokedAt)}` };
  if (!g.live) return { tone: "neutral", text: `expired ${day(g.expiresAt)}` };
  if (g.jobsUsed >= g.maxJobs || g.coreMinutesUsed >= g.maxCoreHours * 60) return { tone: "warn", text: "budget used up" };
  return { tone: "ok", text: `until ${day(g.expiresAt)}` };
}

/** Usage against one budget line, as text and a bar (the text carries the meaning; the bar is a glance). */
function Usage({ label, used, max, unit }: { label: string; used: number; max: number; unit: string }) {
  const pct = max > 0 ? Math.min(100, (used / max) * 100) : 0;
  return (
    <div className={r.usage}>
      <span>{label}: {hours(used)} of {max} {unit}</span>
      <span className={r.track} aria-hidden="true"><span className={r.fill} style={{ width: `${pct}%` }} /></span>
    </div>
  );
}

/**
 * Standing approvals (ADR 0074) on the key holder's computer: a collaborator's requests inside narrow terms and a budget are
 * submitted without a click, through the same template, and listed here afterwards. This computer checks every term again for
 * every request; anything outside waits in the requests list with the reason. Revoking is immediate and cancels nothing.
 */
export function StandingApprovals({ local, onChanged }: { local: RelayLocal; onChanged: () => unknown }) {
  const [list, setList] = useState<GrantList | null>(null);
  const [members, setMembers] = useState<RelayMember[] | null>(null);
  const [error, setError] = useState("");
  const [done, setDone] = useState("");
  const [creating, setCreating] = useState(false);
  const [confirm, setConfirm] = useState("");
  const load = useCallback(() => listGrants().then(setList, (e) => setError(message(e))), []);
  useEffect(() => {
    void load();
    void relayMembers().then(setMembers, () => setMembers([]));
  }, [load]);
  const revoke = (g: Grant) => {
    setError("");
    revokeGrant(g.id).then((x) => {
      setConfirm("");
      const open = x.openJobs.length
        ? ` ${x.openJobs.length} job(s) started under it keep running and were not cancelled: ${x.openJobs.map((j) => `${j.name || `job ${j.id}`} (${j.state})`).join(", ")}.`
        : " No job under it is queued or running.";
      setDone(`Revoked the standing approval for ${x.requesterName}. Requests naming it now wait for you.${open}`);
      void onChanged();
      return load();
    }, (e) => setError(message(e)));
  };
  const grants = list?.grants ?? [];
  const live = grants.filter((g) => g.live);
  const past = grants.filter((g) => !g.live);
  return (
    <section className={s.card} aria-labelledby="standing-approvals">
      <div className={s.row}>
        <h2 className={s.h2} id="standing-approvals">Standing approvals</h2>
      </div>
      <p className={s.note}>
        Let one collaborator&apos;s requests run without your click, inside narrow terms: one label and tool, the settings and input folders you allow, a code commit,
        and a budget. Each request is still rendered by the same template, checked here against every term, recorded, and listed for you with its exact script.
        Anything outside waits for you with the reason.
      </p>
      {error ? <p className={s.error} role="alert">{error}</p> : null}
      {done ? <p className={s.note} role="status">{done}</p> : null}
      {live.length ? (
        <ul className={r.grants} aria-label="Live standing approvals">
          {live.map((g) => <GrantItem key={g.id} g={g} confirming={confirm === g.id} onAsk={() => setConfirm(g.id)} onCancel={() => setConfirm("")} onRevoke={() => revoke(g)} />)}
        </ul>
      ) : <p className={s.note}>{list ? "No standing approval is live. Every request waits for you." : "Loading…"}</p>}
      {creating ? (
        <GrantForm local={local} list={list} members={members} onCancel={() => setCreating(false)}
          onMade={(g) => { setCreating(false); setDone(`Made a standing approval for ${g.requesterName}, until ${day(g.expiresAt)}. It is offered at the next check.`); void onChanged(); return load(); }} />
      ) : (
        <div className={s.row}><Button onClick={() => { setDone(""); setCreating(true); }} disabled={!local.state?.inbox.labels.length}>New standing approval</Button></div>
      )}
      {past.length ? (
        <details>
          <summary className={s.note}>Revoked and expired ({past.length})</summary>
          <ul className={r.grants}>{past.map((g) => <GrantItem key={g.id} g={g} />)}</ul>
        </details>
      ) : null}
    </section>
  );
}

function GrantItem({ g, confirming, onAsk, onCancel, onRevoke }: { g: Grant; confirming?: boolean; onAsk?: () => void; onCancel?: () => void; onRevoke?: () => void }) {
  const st = status(g);
  const inputs = [...g.roots.map((x) => `${x}/`), ...g.files];
  return (
    <li className={r.grant}>
      <div className={s.row}>
        <strong>{g.requesterName}</strong>
        <span className={s.note}>{g.template} on {g.label}{g.hostName ? ` (${g.hostName})` : ""}</span>
        <Pill tone={st.tone}>{st.text}</Pill>
      </div>
      <dl className={r.terms}>
        <dt>Settings</dt><dd>{grantKeysText(g.keys)}</dd>
        <dt>Inputs</dt><dd className={r.paths}>{inputs.join(", ")}</dd>
        <dt>Code</dt><dd><code>{g.commit.slice(0, 12)}</code>, {g.commitBasis === "profile" ? `the commit profile ${g.profile || g.template} was written against` : "typed by you"}</dd>
        <dt>Per job</dt><dd>at most {g.perJob.maxCpus} CPUs, {g.perJob.maxMemGiB} GiB, {g.perJob.maxTimeMinutes} minutes</dd>
        <dt>Used</dt>
        <dd className={r.usages}>
          <Usage label="Jobs" used={g.jobsUsed} max={g.maxJobs} unit="" />
          <Usage label="Core-hours" used={g.coreMinutesUsed / 60} max={g.maxCoreHours} unit="" />
          <Usage label="Queued or running" used={g.atOnce} max={g.maxAtOnce} unit="at once" />
        </dd>
        {g.note ? <><dt>Note</dt><dd className={r.plain}>{g.note}</dd></> : null}
        {g.openJobs.length ? <><dt>Jobs</dt><dd>{g.openJobs.map((j) => `${j.name || `job ${j.id}`} (${j.state})`).join(", ")}</dd></> : null}
      </dl>
      {onAsk ? (
        confirming ? (
          <div className={s.row} role="group" aria-label={`Revoke the standing approval for ${g.requesterName}`}>
            <span className={s.note}>Revoke now? Requests naming it will wait for you. Jobs already queued or running keep running.</span>
            <Button variant="danger" onClick={onRevoke}>Revoke now</Button>
            {/* the safe choice takes the focus the replaced Revoke button had */}
            <Button onClick={onCancel} autoFocus>Keep it</Button>
          </div>
        ) : <div className={s.row}><Button onClick={onAsk}>Revoke</Button></div>
      ) : null}
    </li>
  );
}

type KeyRow = { key: string; use: boolean; min: string; max: string; choices: string; inbox: Bound };

function GrantForm({ local, list, members, onCancel, onMade }: { local: RelayLocal; list: GrantList | null; members: RelayMember[] | null; onCancel: () => void; onMade: (g: Grant) => unknown }) {
  const labels = local.state?.inbox.labels ?? [];
  const [who, setWho] = useState("");
  const [label, setLabel] = useState(labels[0]?.label ?? "");
  const lab = labels.find((l) => l.label === label);
  const [template, setTemplate] = useState(lab?.templates[0]?.template ?? "");
  const bounds = lab?.templates.find((t) => t.template === template);
  const fresh = useMemo<KeyRow[]>(() => (bounds?.keys ?? []).map((k) => ({
    key: k.key, use: false, inbox: k, min: k.min === undefined ? "" : String(k.min), max: k.max === undefined ? "" : String(k.max), choices: (k.choices ?? []).join(", "),
  })), [bounds]);
  const [keys, setKeys] = useState<KeyRow[]>(fresh);
  useEffect(() => setKeys(fresh), [fresh]);
  const lim = bounds?.limits;
  const [roots, setRoots] = useState("");
  const [files, setFiles] = useState("");
  const [basis, setBasis] = useState<"profile" | "typed">("profile");
  const [commit, setCommit] = useState("");
  const [maxJobs, setMaxJobs] = useState("10");
  const [coreHours, setCoreHours] = useState("200");
  const [atOnce, setAtOnce] = useState("2");
  const [cpus, setCpus] = useState(lim?.maxCpus ? String(lim.maxCpus) : "8");
  const [mem, setMem] = useState(lim?.maxMemGiB ? String(lim.maxMemGiB) : "16");
  const [time, setTime] = useState(lim?.maxTimeMinutes ? String(lim.maxTimeMinutes) : "120");
  const [days, setDays] = useState("30");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const profile = template ? list?.templates[template] : undefined;
  const host = lab ? local.hosts.find((h) => h.id === local.state?.hosts[lab.label]) : undefined;
  const person = members?.find((m) => m.id === who);
  const setKey = (i: number, f: (k: KeyRow) => KeyRow) => setKeys(keys.map((k, j) => (j === i ? f(k) : k)));
  const make = () => {
    setBusy(true);
    setError("");
    createGrant({
      requesterId: who, requesterName: person?.name ?? "", label, template: template as "vgt" | "rohsa", commitBasis: basis, commit: basis === "typed" ? commit.trim() : undefined,
      keys: keys.filter((k) => k.use).map((k) => {
        const choices = k.choices.split(",").map((c) => c.trim()).filter(Boolean);
        // the inbox's own kind of bound: a list stays a list, a range stays a range
        return k.inbox.choices?.length ? { key: k.key, choices } : { key: k.key, min: Number(k.min), max: Number(k.max) };
      }),
      roots: lines(roots), files: lines(files),
      maxJobs: Number(maxJobs) || 0, maxCoreHours: Number(coreHours) || 0, maxAtOnce: Number(atOnce) || 0,
      perJob: { maxCpus: Number(cpus) || 0, maxMemGiB: Number(mem) || 0, maxTimeMinutes: Number(time) || 0 },
      expiresInDays: Number(days) || 0, note,
    }).then((g) => onMade(g), (e) => setError(message(e))).finally(() => setBusy(false));
  };
  return (
    <fieldset className={s.form}>
      <legend className={s.h2}>New standing approval</legend>
      <div className={r.grid}>
        <Select label="Collaborator" value={who} onChange={(e) => setWho(e.target.value)} required
          description={members && !members.length ? "The group server named no other member, or could not be asked." : undefined}>
          <option value="">Choose a member</option>
          {(members ?? []).map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}
        </Select>
        <Select label="Label" value={label} onChange={(e) => { setLabel(e.target.value); setTemplate(labels.find((l) => l.label === e.target.value)?.templates[0]?.template ?? ""); }}
          description={host ? `Stands for ${host.name} on this computer.` : undefined}>
          {labels.map((l) => <option key={l.label} value={l.label}>{l.label}</option>)}
        </Select>
        <Select label="Tool" value={template} onChange={(e) => setTemplate(e.target.value)}>
          {(lab?.templates ?? []).map((t) => <option key={t.template} value={t.template}>{t.template}</option>)}
        </Select>
      </div>

      <fieldset className={s.form}>
        <legend className={s.note}>Settings the collaborator may change, narrowed inside your inbox&apos;s bounds (a grant is never wider than the inbox). Unticked settings keep your base run&apos;s value.</legend>
        {keys.length ? keys.map((k, i) => (
          <div key={k.key} className={r.keyrow}>
            <Checkbox label={`${k.key} (inbox: ${boundText(k.inbox)})`} checked={k.use} onChange={(e) => setKey(i, (x) => ({ ...x, use: e.target.checked }))} />
            {k.use ? (
              k.inbox.choices?.length ? (
                <Input label={`${k.key}: allowed values`} placeholder="a, b" value={k.choices} onChange={(e) => setKey(i, (x) => ({ ...x, choices: e.target.value }))} />
              ) : (
                <div className={r.pair}>
                  <Input label={`${k.key}: from`} inputMode="decimal" value={k.min} onChange={(e) => setKey(i, (x) => ({ ...x, min: e.target.value }))} />
                  <Input label={`${k.key}: to`} inputMode="decimal" value={k.max} onChange={(e) => setKey(i, (x) => ({ ...x, max: e.target.value }))} />
                </div>
              )
            ) : null}
          </div>
        )) : <p className={s.note}>Your inbox lets no setting of {template || "this tool"} be changed on this label.</p>}
      </fieldset>

      <div className={r.grid}>
        <label className={r.area}>
          Input folders on the host, one per line (the base run&apos;s input must lie under one)
          <textarea value={roots} onChange={(e) => setRoots(e.target.value)} spellCheck={false} placeholder="/scratch/lmc/cubes" />
        </label>
        <label className={r.area}>
          Or single input files, one per line
          <textarea value={files} onChange={(e) => setFiles(e.target.value)} spellCheck={false} />
        </label>
      </div>
      <p className={s.note}>Each path must be absolute and inside one of {host ? `${host.name}'s` : "the host's"} source roots.</p>

      <fieldset className={s.form}>
        <legend className={s.note}>Code commit the jobs must run</legend>
        <Select label="Commit" value={basis} onChange={(e) => setBasis(e.target.value === "typed" ? "typed" : "profile")}>
          <option value="profile">{profile ? `The one profile ${profile.profile || template} was written against (${profile.commit.slice(0, 12)})` : "The tool profile's commit"}</option>
          <option value="typed">A commit I type</option>
        </Select>
        {basis === "typed" ? <Input label="Commit (40 characters)" value={commit} spellCheck={false} onChange={(e) => setCommit(e.target.value)} /> : null}
        <p className={s.note}>A base run that insists on its own commit keeps it; if that differs, the request waits for you.</p>
      </fieldset>

      <fieldset className={s.form}>
        <legend className={s.note}>Budget{lim && (lim.maxCpus || lim.maxMemGiB || lim.maxTimeMinutes) ? ` (your inbox allows at most ${lim.maxCpus} CPUs, ${lim.maxMemGiB} GiB, ${lim.maxTimeMinutes} minutes a job)` : ""}</legend>
        <div className={r.grid}>
          <Input label="Jobs in total" inputMode="numeric" value={maxJobs} onChange={(e) => setMaxJobs(e.target.value)} />
          <Input label="Core-hours in total" inputMode="numeric" value={coreHours} onChange={(e) => setCoreHours(e.target.value)} />
          <Input label="Jobs at once" inputMode="numeric" value={atOnce} onChange={(e) => setAtOnce(e.target.value)} />
          <Input label="CPUs per job" inputMode="numeric" value={cpus} onChange={(e) => setCpus(e.target.value)} />
          <Input label="Memory per job (GiB)" inputMode="numeric" value={mem} onChange={(e) => setMem(e.target.value)} />
          <Input label="Wall time per job (minutes)" inputMode="numeric" value={time} onChange={(e) => setTime(e.target.value)} />
          <Input label="Expires after (days, up to 180)" inputMode="numeric" value={days} onChange={(e) => setDays(e.target.value)} />
        </div>
      </fieldset>
      <label className={r.area}>
        Note (for your own list)
        <textarea value={note} maxLength={4000} onChange={(e) => setNote(e.target.value)} />
      </label>
      {error ? <p className={s.error} role="alert">{error}</p> : null}
      <div className={s.row}>
        <Button variant="primary" disabled={busy || !who || !label || !template} onClick={make}>{busy ? "Making…" : "Make standing approval"}</Button>
        <Button onClick={onCancel}>Cancel</Button>
      </div>
    </fieldset>
  );
}
