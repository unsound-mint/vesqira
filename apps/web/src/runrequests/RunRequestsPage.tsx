import { useCallback, useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { authState } from "../api/accounts";
import { ApiError } from "../api/http";
import { listPushed } from "../api/push";
import type { PushedSummary } from "../api/push";
import { boundText, declineRunRequest, fileRunRequest, getRunRequest, hours, isOpen, listRunInboxes, listRunRequests, noteRunRequest, previewRunRequest, stateText, withdrawRunRequest } from "../api/runRequests";
import type { GrantOffer, RelayResources, RunInbox, RunRequest, RunRequestDetail, RunRequestFile, RunRequestPreview, TemplateBounds } from "../api/runRequests";
import { Button, Input, LinkButton, Select } from "../ui";
import { useCan } from "../account/permissions";
import s from "../account/Account.module.css";
import r from "./RunRequests.module.css";
import { grantKeysText } from "./StandingApprovals";

const message = (e: unknown) => (e instanceof ApiError || e instanceof Error ? e.message : "Something went wrong.");
const when = (iso: string) => iso.slice(0, 16).replace("T", " ");

/**
 * Asking a key holder to run a VGT or ROHSA job (ADR 0066 section 3), on the group server. The form offers only what the chosen
 * inbox publishes: its labels, templates and the keys its bounds allow. A request never names a path, host or script; the server
 * checks it again and its refusals are shown as sent. Plain on purpose: the owner will redesign it.
 */
export function RunRequestsPage() {
  const can = useCan();
  const [inboxes, setInboxes] = useState<RunInbox[]>([]);
  const [requests, setRequests] = useState<RunRequest[]>([]);
  const [runs, setRuns] = useState<PushedSummary[]>([]);
  const [me, setMe] = useState("");
  const [error, setError] = useState("");
  const refresh = useCallback(() => listRunRequests().then(setRequests, (e) => setError(message(e))), []);
  useEffect(() => {
    void refresh();
    void listRunInboxes().then(setInboxes, (e) => setError(message(e)));
    void authState().then((a) => setMe(a.account?.id ?? ""), () => undefined);
    // the base is optional: without access to pushed work the form simply offers none
    void listPushed().then((l) => setRuns(l.items.filter((x) => x.kind === "run" && !x.archived)), () => undefined);
  }, [refresh]);
  return (
    <main className={`${s.page} ${s.wide}`}>
      <div className={s.row}>
        <h1 className={s.h1}>Run requests</h1>
        <LinkButton href="#/">Back to the workbench</LinkButton>
      </div>
      {error ? <p className={s.error} role="alert">{error}</p> : null}
      {can("run_request.file") ? <RequestForm inboxes={inboxes.filter((i) => i.account !== me)} runs={runs} onFiled={refresh} /> : null}
      <section className={s.card}>
        <h2 className={s.h2}>Requests</h2>
        {requests.length ? <RequestList requests={requests} me={me} onChange={refresh} /> : <p className={s.note}>No requests yet.</p>}
      </section>
    </main>
  );
}

const numberOrUndef = (v: string) => (v.trim() === "" ? undefined : Number(v));

function RequestForm({ inboxes, runs, onFiled }: { inboxes: RunInbox[]; runs: PushedSummary[]; onFiled: () => unknown }) {
  const [account, setAccount] = useState("");
  const [label, setLabel] = useState("");
  const [template, setTemplate] = useState("");
  const [params, setParams] = useState<Record<string, string>>({});
  const [res, setRes] = useState({ cpus: "", memGiB: "", timeMinutes: "" });
  const [grid, setGrid] = useState<{ key: string; values: string }[]>([]);
  const [base, setBase] = useState("");
  const [days, setDays] = useState("14");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState("");
  const [grant, setGrant] = useState("");
  const [preview, setPreview] = useState<RunRequestPreview | null>(null);
  const inbox = inboxes.find((i) => i.account === account);
  const lab = inbox?.inbox.labels.find((l) => l.label === label);
  const bounds: TemplateBounds | undefined = lab?.templates.find((t) => t.template === template);
  const reset = () => { setParams({}); setGrid([]); setRes({ cpus: "", memGiB: "", timeMinutes: "" }); setGrant(""); setPreview(null); };
  const keyBound = useMemo(() => new Map((bounds?.keys ?? []).map((k) => [k.key, k])), [bounds]);
  // the standing approvals the key holder offers this member for this label and tool (the server shows each member only their own)
  const offers: GrantOffer[] = (inbox?.inbox.grants ?? []).filter((o) => o.label === label && o.template === template);
  const offer = offers.find((o) => o.id === grant);

  const build = (): RunRequestFile | null => {
    if (!bounds) return null;
    const asked: Record<string, string> = {};
    for (const [k, v] of Object.entries(params)) if (v.trim() !== "") asked[k] = v.trim();
    const resources: RelayResources = {};
    const cpus = numberOrUndef(res.cpus);
    const memGiB = numberOrUndef(res.memGiB);
    const timeMinutes = numberOrUndef(res.timeMinutes);
    if (cpus !== undefined) resources.cpus = cpus;
    if (memGiB !== undefined) resources.memGiB = memGiB;
    if (timeMinutes !== undefined) resources.timeMinutes = timeMinutes;
    const g = grid.filter((x) => x.key && x.values.trim()).map((x) => ({ key: x.key, values: x.values.split(",").map((v) => v.trim()).filter(Boolean) }));
    return {
      addressee: account,
      ask: { label, template: bounds.template, params: asked, resources, grid: g, note, ...(grant ? { grant } : {}) },
      baseItemId: base ? Number(base) : undefined,
      expiresInDays: Number(days) || undefined,
    };
  };
  const filing = build();
  const previewKey = grant && filing ? JSON.stringify({ ...filing, ask: { ...filing.ask, note: "" } }) : "";
  useEffect(() => {
    if (!previewKey) { setPreview(null); return; }
    let live = true;
    const t = setTimeout(() => {
      previewRunRequest(JSON.parse(previewKey) as RunRequestFile).then((p) => { if (live) setPreview(p); }, (e) => { if (live) setPreview({ route: "review", reason: message(e) }); });
    }, 300);
    return () => { live = false; clearTimeout(t); };
  }, [previewKey]);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const f = build();
    if (!f) return;
    setBusy(true);
    setError("");
    setDone("");
    const through = preview?.route === "grant";
    fileRunRequest(f).then(() => {
      setDone(through ? "Request filed under the standing approval. The key holder's computer submits it at its next check if every term holds there." : "Request filed.");
      setNote("");
      reset();
      return onFiled();
    }, (err) => setError(message(err))).finally(() => setBusy(false));
  };

  return (
    <section className={s.card}>
      <h2 className={s.h2}>Ask a key holder to run a job</h2>
      {inboxes.length ? (
        <form className={s.form} onSubmit={submit}>
          <p className={s.note}>A key holder&apos;s own computer holds the cluster keys. You name a cluster label, a template and a few settings; you never name a path or a script. They read the script before they approve.</p>
          <Select label="Ask" value={account} onChange={(e) => { setAccount(e.target.value); setLabel(""); setTemplate(""); reset(); }} required>
            <option value="">Choose a member</option>
            {inboxes.map((i) => <option key={i.account} value={i.account}>{i.name}{i.inbox.paused ? " (paused)" : ""}</option>)}
          </Select>
          {inbox?.polledAt ? <p className={s.note}>{inbox.name}&apos;s computer last checked at {when(inbox.polledAt)}.</p> : inbox ? <p className={s.note}>{inbox.name}&apos;s computer has not checked yet.</p> : null}
          {inbox ? (
            <Select label="Cluster label" value={label} onChange={(e) => { setLabel(e.target.value); setTemplate(""); reset(); }} required>
              <option value="">Choose a label</option>
              {inbox.inbox.labels.map((l) => <option key={l.label} value={l.label}>{l.label}</option>)}
            </Select>
          ) : null}
          {lab ? (
            <Select label="Template" value={template} onChange={(e) => { setTemplate(e.target.value); reset(); }} required>
              <option value="">Choose a template</option>
              {lab.templates.map((t) => <option key={t.template} value={t.template}>{t.template}</option>)}
            </Select>
          ) : null}
          {bounds && offers.length ? (
            <Select label="Standing approval" value={grant} onChange={(e) => setGrant(e.target.value)}
              description="The key holder lets some of your requests run without a click, inside narrower terms and a budget.">
              <option value="">None: the key holder reads the script and approves</option>
              {offers.map((o) => <option key={o.id} value={o.id}>{grantKeysText(o.keys)}, until {o.expiresAt.slice(0, 10)}, {o.maxJobs - o.jobsUsed} of {o.maxJobs} jobs left</option>)}
            </Select>
          ) : null}
          {offer ? (
            <div className={r.grantNote} aria-live="polite">
              <p className={s.note}>
                Under this standing approval: {grantKeysText(offer.keys)}; at most {offer.perJob.maxCpus} CPUs, {offer.perJob.maxMemGiB} GiB and {offer.perJob.maxTimeMinutes} minutes a job;
                {" "}{offer.jobsUsed} of {offer.maxJobs} jobs and {hours(offer.coreHoursUsed)} of {offer.maxCoreHours} core-hours used, {offer.maxAtOnce} at once. No grid.
              </p>
              {preview ? <p className={s.note} role="status"><strong>{preview.route === "grant" ? "Goes through the standing approval." : "Waits for review."}</strong> {preview.reason}</p> : <p className={s.note}>Checking…</p>}
            </div>
          ) : null}
          {bounds ? (
            <>
              <fieldset className={s.form}>
                <legend className={s.note}>Settings (leave empty to keep the key holder&apos;s value)</legend>
                {bounds.keys.length ? (
                  <div className={r.grid}>
                    {bounds.keys.map((k) => k.choices?.length ? (
                      <Select key={k.key} label={`${k.key} (${boundText(k)})`} value={params[k.key] ?? ""} onChange={(e) => setParams({ ...params, [k.key]: e.target.value })}>
                        <option value="">Not set</option>
                        {k.choices.map((c) => <option key={c} value={c}>{c}</option>)}
                      </Select>
                    ) : (
                      <Input key={k.key} label={`${k.key} (${boundText(k)})`} inputMode="decimal" value={params[k.key] ?? ""} onChange={(e) => setParams({ ...params, [k.key]: e.target.value })} />
                    ))}
                  </div>
                ) : <p className={s.note}>This template allows no settings to be changed.</p>}
              </fieldset>
              <fieldset className={s.form}>
                <legend className={s.note}>Resources (at most {bounds.limits.maxCpus} CPUs, {bounds.limits.maxMemGiB} GiB, {bounds.limits.maxTimeMinutes} minutes; 0 means you may not ask)</legend>
                <div className={r.grid}>
                  <Input label="CPUs" inputMode="numeric" disabled={bounds.limits.maxCpus === 0} value={res.cpus} onChange={(e) => setRes({ ...res, cpus: e.target.value })} />
                  <Input label="Memory (GiB)" inputMode="numeric" disabled={bounds.limits.maxMemGiB === 0} value={res.memGiB} onChange={(e) => setRes({ ...res, memGiB: e.target.value })} />
                  <Input label="Time (minutes)" inputMode="numeric" disabled={bounds.limits.maxTimeMinutes === 0} value={res.timeMinutes} onChange={(e) => setRes({ ...res, timeMinutes: e.target.value })} />
                </div>
              </fieldset>
              {bounds.keys.length ? (
                <fieldset className={s.form}>
                  <legend className={s.note}>Grid (optional): one to three settings, a comma-separated list of values each, at most 64 combinations</legend>
                  {grid.map((g, i) => (
                    <div key={i} className={r.grid}>
                      <Select label="Setting" value={g.key} onChange={(e) => setGrid(grid.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))}>
                        <option value="">Choose a setting</option>
                        {bounds.keys.map((k) => <option key={k.key} value={k.key}>{k.key}</option>)}
                      </Select>
                      <Input label="Values" placeholder="1, 2, 3" value={g.values} onChange={(e) => setGrid(grid.map((x, j) => (j === i ? { ...x, values: e.target.value } : x)))} />
                      <div className={s.row}><Button onClick={() => setGrid(grid.filter((_, j) => j !== i))}>Remove</Button></div>
                    </div>
                  ))}
                  {grid.length < 3 && keyBound.size ? <div className={s.row}><Button onClick={() => setGrid([...grid, { key: "", values: "" }])}>Add a grid setting</Button></div> : null}
                </fieldset>
              ) : null}
              {runs.length ? (
                <Select label="Based on a pushed run (optional)" value={base} onChange={(e) => setBase(e.target.value)}>
                  <option value="">Let the key holder choose</option>
                  {runs.map((x) => <option key={x.id} value={x.id}>{x.title}</option>)}
                </Select>
              ) : null}
              <Input label="Expires after (days, 1 to 30)" inputMode="numeric" value={days} onChange={(e) => setDays(e.target.value)} />
              <label className={r.area}>
                Note (plain text, at most 4000 characters)
                <textarea value={note} maxLength={4000} onChange={(e) => setNote(e.target.value)} />
              </label>
            </>
          ) : null}
          {error ? <p className={s.error} role="alert">{error}</p> : null}
          {done ? <p className={s.note} role="status">{done}</p> : null}
          <div className={s.row}><Button type="submit" variant="primary" disabled={busy || !bounds}>{busy ? "Sending…" : preview?.route === "grant" ? "File under the standing approval" : "File request"}</Button></div>
        </form>
      ) : <p className={s.note}>No one has published an inbox in this workspace yet. A member with cluster keys sets one up on their own computer under Inbox, after linking it with &ldquo;Also take run requests&rdquo;.</p>}
    </section>
  );
}

function RequestList({ requests, me, onChange }: { requests: RunRequest[]; me: string; onChange: () => unknown }) {
  const [open, setOpen] = useState<RunRequestDetail | null>(null);
  const [error, setError] = useState("");
  const show = (id: number) => getRunRequest(id).then(setOpen, (e) => setError(message(e)));
  return (
    <>
      {error ? <p className={s.error} role="alert">{error}</p> : null}
      <table className={s.table}>
        <thead><tr><th>Request</th><th>From</th><th>To</th><th>State</th><th>Expires</th></tr></thead>
        <tbody>
          {requests.map((q) => (
            <tr key={q.id}>
              <td><Button variant="ghost" onClick={() => void show(q.id)}>{q.ask.template} on {q.ask.label}</Button></td>
              <td>{q.requesterName}{q.requester === me ? " (you)" : ""}</td>
              <td>{q.addresseeName}{q.addressee === me ? " (you)" : ""}</td>
              <td>{stateText(q.state)}</td>
              <td>{isOpen(q.state) ? when(q.expiresAt) : ""}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {open ? <RequestDetail key={open.request.id} detail={open} me={me} onChange={() => { void onChange(); return show(open.request.id); }} /> : null}
    </>
  );
}

function RequestDetail({ detail, me, onChange }: { detail: RunRequestDetail; me: string; onChange: () => unknown }) {
  const q = detail.request;
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const run = (p: Promise<unknown>) => { setError(""); p.then(() => { setText(""); return onChange(); }, (e) => setError(message(e))); };
  const open = isOpen(q.state);
  return (
    <section className={s.card} aria-label={`Request ${q.id}`}>
      <h3 className={s.h2}>{q.ask.template} on {q.ask.label}</h3>
      <p className={s.note}>{q.requesterName} to {q.addresseeName}: {stateText(q.state)}{q.reportedAt ? `, reported by ${q.addresseeName}'s computer at ${when(q.reportedAt)}` : ""}.</p>
      {q.baseTitle ? <p className={s.note}>Based on {q.baseTitle}.</p> : null}
      {Object.keys(q.ask.params).length ? <p className={s.note}>Settings: {Object.entries(q.ask.params).map(([k, v]) => `${k} = ${v}`).join(", ")}</p> : null}
      {q.ask.grant ? <p className={s.note}>Filed under a standing approval: the key holder&apos;s computer submits it without a click if every term holds there; otherwise it waits for review and the events below say why.</p> : null}
      {q.ask.grid.length ? <p className={s.note}>Grid: {q.ask.grid.map((g) => `${g.key} in ${g.values.join(", ")}`).join("; ")}</p> : null}
      {q.ask.note ? <p className={`${s.note} ${r.plain}`}>{q.ask.note}</p> : null}
      {q.declineNote ? <p className={`${s.note} ${r.plain}`}>Declined: {q.declineNote}</p> : null}
      {q.closedNote ? <p className={s.note}>{q.closedNote}</p> : null}
      <ol>
        {detail.events.map((ev, i) => <li key={i} className={s.note}>{when(ev.at)} {ev.name || ev.actor}: {ev.event}{ev.detail ? `, ${ev.detail}` : ""} ({ev.via})</li>)}
      </ol>
      {error ? <p className={s.error} role="alert">{error}</p> : null}
      <label className={r.area}>
        Note
        <textarea value={text} maxLength={4000} onChange={(e) => setText(e.target.value)} />
      </label>
      <div className={s.row}>
        <Button disabled={!text.trim()} onClick={() => run(noteRunRequest(q.id, text))}>Add note</Button>
        {open && q.requester === me ? <Button onClick={() => run(withdrawRunRequest(q.id))}>Withdraw</Button> : null}
        {open && q.addressee === me ? <Button variant="danger" disabled={!text.trim()} onClick={() => run(declineRunRequest(q.id, text))}>Decline with this note</Button> : null}
      </div>
    </section>
  );
}
