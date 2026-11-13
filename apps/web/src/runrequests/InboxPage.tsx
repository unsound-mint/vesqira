import { useCallback, useEffect, useState } from "react";
import { ApiError } from "../api/http";
import { grantFellBack, grantStateText, pollRelay, relayLocal, setRelayInbox, stateText } from "../api/runRequests";
import type { RelayInbox, RelayLocal, TemplateBounds } from "../api/runRequests";
import { Button, Checkbox, Input, LinkButton, Select } from "../ui";
import s from "../account/Account.module.css";
import r from "./RunRequests.module.css";
import { RelayedView } from "./RelayedView";
import { StandingApprovals } from "./StandingApprovals";

const message = (e: unknown) => (e instanceof ApiError || e instanceof Error ? e.message : "Something went wrong.");
const when = (iso: string) => iso.slice(0, 16).replace("T", " ");

const TEMPLATES = ["vgt", "rohsa"] as const;
type Template = (typeof TEMPLATES)[number];
type KeyRow = { key: string; min: string; max: string; choices: string };
type TemplateRow = { enabled: boolean; keys: KeyRow[]; maxCpus: string; maxMemGiB: string; maxTimeMinutes: string };
type LabelRow = { label: string; hostId: string; templates: Record<Template, TemplateRow> };

const emptyTemplate = (): TemplateRow => ({ enabled: false, keys: [], maxCpus: "0", maxMemGiB: "0", maxTimeMinutes: "0" });
const emptyLabel = (): LabelRow => ({ label: "", hostId: "", templates: { vgt: emptyTemplate(), rohsa: emptyTemplate() } });

function toRows(inbox: RelayInbox, hosts: Record<string, number>): LabelRow[] {
  return inbox.labels.map((l) => {
    const row = emptyLabel();
    row.label = l.label;
    row.hostId = hosts[l.label] ? String(hosts[l.label]) : "";
    for (const t of l.templates) {
      row.templates[t.template] = {
        enabled: true,
        keys: t.keys.map((k) => ({ key: k.key, min: k.min === undefined ? "" : String(k.min), max: k.max === undefined ? "" : String(k.max), choices: (k.choices ?? []).join(", ") })),
        maxCpus: String(t.limits.maxCpus),
        maxMemGiB: String(t.limits.maxMemGiB),
        maxTimeMinutes: String(t.limits.maxTimeMinutes),
      };
    }
    return row;
  });
}

function fromRows(rows: LabelRow[], paused: boolean): { inbox: RelayInbox; hosts: Record<string, number> } {
  const hosts: Record<string, number> = {};
  const labels = rows.filter((l) => l.label.trim()).map((l) => {
    const label = l.label.trim();
    if (l.hostId) hosts[label] = Number(l.hostId);
    const templates: TemplateBounds[] = TEMPLATES.filter((t) => l.templates[t].enabled).map((t) => {
      const x = l.templates[t];
      return {
        template: t,
        keys: x.keys.filter((k) => k.key.trim()).map((k) => {
          const choices = k.choices.split(",").map((c) => c.trim()).filter(Boolean);
          return {
            key: k.key.trim(),
            ...(k.min.trim() !== "" ? { min: Number(k.min) } : {}),
            ...(k.max.trim() !== "" ? { max: Number(k.max) } : {}),
            ...(choices.length ? { choices } : {}),
          };
        }),
        limits: { maxCpus: Number(x.maxCpus) || 0, maxMemGiB: Number(x.maxMemGiB) || 0, maxTimeMinutes: Number(x.maxTimeMinutes) || 0 },
      };
    });
    return { label, templates };
  });
  return { inbox: { paused, labels }, hosts };
}

/** The states a key holder acts on first. */
const ORDER = ["draft", "received", "approved", "declined", "detached", "closed", "refused"];

/**
 * The key holder's run-request inbox on their own computer (ADR 0066 section 3): which labels and bounds to publish, the pull
 * state, and the requests that arrived, each opened to read its script and approve, decline or detach. Nothing is ever approved
 * on its own. Plain on purpose: the owner will redesign it.
 */
export function InboxPage() {
  const [local, setLocal] = useState<RelayLocal | null>(null);
  const [error, setError] = useState("");
  const [openId, setOpenId] = useState<number | null>(null);
  const [checking, setChecking] = useState(false);
  const refresh = useCallback(() => relayLocal().then(setLocal, (e) => setError(message(e))), []);
  useEffect(() => {
    void refresh();
    const t = setInterval(() => void refresh(), 15000);
    return () => clearInterval(t);
  }, [refresh]);
  const check = () => {
    setChecking(true);
    setError("");
    pollRelay().then(setLocal, (e) => setError(message(e))).finally(() => setChecking(false));
  };
  const st = local?.state;
  const requests = [...(local?.requests ?? [])].sort((a, b) => ORDER.indexOf(a.localState) - ORDER.indexOf(b.localState) || b.updatedAt.localeCompare(a.updatedAt));
  const open = requests.find((x) => x.id === openId);
  return (
    <main className={`${s.page} ${s.wide}`}>
      <div className={s.row}>
        <h1 className={s.h1}>Run requests inbox</h1>
        <LinkButton href="#/">Back to the workbench</LinkButton>
      </div>
      {error ? <p className={s.error} role="alert">{error}</p> : null}
      {local && !local.linked ? <p className={s.note}>This computer is not linked to a group server. Link it under Group server, using a token made with &ldquo;Also take run requests&rdquo;.</p> : null}
      {local?.linked ? (
        <section className={s.card}>
          <h2 className={s.h2}>Status</h2>
          <p className={s.note} role="status">
            Linked to {local.workspace || "a workspace"}. {st?.polledAt ? `Last checked ${when(st.polledAt)}` : "Not checked yet"}
            {st?.answeredAt ? `, last answer ${when(st.answeredAt)}` : ""}.{st && st.failures > 0 ? ` ${st.failures} failed check(s) in a row.` : ""}{st?.message ? ` ${st.message}` : ""}
            {st && !st.published ? " Your inbox is not published yet." : ""}
          </p>
          <div className={s.row}><Button onClick={check} disabled={checking}>{checking ? "Checking…" : "Check now"}</Button></div>
        </section>
      ) : null}
      {local?.linked && st ? <Settings key={JSON.stringify(st.inbox) + JSON.stringify(st.hosts)} local={local} onSaved={setLocal} /> : null}
      {local?.linked && st?.configured ? <StandingApprovals local={local} onChanged={refresh} /> : null}
      {local?.linked ? (
        <section className={s.card}>
          <h2 className={s.h2}>Requests</h2>
          {requests.length ? (
            <table className={s.table}>
              <thead><tr><th>Request</th><th>From</th><th>Here</th><th>On the server</th><th>Received</th></tr></thead>
              <tbody>
                {requests.map((x) => (
                  <tr key={x.id}>
                    <td><Button variant="ghost" onClick={() => setOpenId(x.id)}>{x.request.ask.template} on {x.request.ask.label}</Button></td>
                    <td>{x.request.requester}</td>
                    <td>
                      {x.localState}{x.refusal ? `: ${x.refusal}` : ""}
                      {x.grantState ? <span className={s.note}>{grantFellBack(x.grantState) ? `; waiting for you: ${grantStateText(x.grantState)}` : `; ${grantStateText(x.grantState)}`}</span> : null}
                    </td>
                    <td>{stateText(x.serverState)}</td>
                    <td>{when(x.receivedAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : <p className={s.note}>No requests have arrived.</p>}
        </section>
      ) : null}
      {open ? <RelayedView key={open.id} summary={open} onChanged={refresh} onClose={() => setOpenId(null)} /> : null}
    </main>
  );
}

function Settings({ local, onSaved }: { local: RelayLocal; onSaved: (l: RelayLocal) => void }) {
  const st = local.state;
  const [rows, setRows] = useState<LabelRow[]>(() => (st ? toRows(st.inbox, st.hosts) : []));
  const [paused, setPaused] = useState(st?.inbox.paused ?? false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const setLabel = (i: number, f: (l: LabelRow) => LabelRow) => setRows(rows.map((l, j) => (j === i ? f(l) : l)));
  const setTpl = (i: number, t: Template, f: (x: TemplateRow) => TemplateRow) => setLabel(i, (l) => ({ ...l, templates: { ...l.templates, [t]: f(l.templates[t]) } }));
  const save = () => {
    setError("");
    setSaved(false);
    const { inbox, hosts } = fromRows(rows, paused);
    setRelayInbox(inbox, hosts).then((l) => { onSaved(l); setSaved(true); }, (e) => setError(message(e)));
  };
  return (
    <section className={s.card}>
      <h2 className={s.h2}>Inbox settings</h2>
      <p className={s.note}>A label is a name you choose for a cluster, such as &ldquo;lab cluster&rdquo;: letters, digits, spaces and dashes, no host name. Only this computer knows which host it stands for. Requesters see the labels, the templates and these bounds, and nothing else.</p>
      <Checkbox label="Pause the inbox (no new requests)" checked={paused} onChange={(e) => setPaused(e.target.checked)} />
      {rows.map((l, i) => (
        <fieldset key={i} className={s.form}>
          <legend className={s.note}>Label {i + 1}</legend>
          <div className={r.grid}>
            <Input label="Label" maxLength={40} value={l.label} onChange={(e) => setLabel(i, (x) => ({ ...x, label: e.target.value }))} />
            <Select label="Host" value={l.hostId} onChange={(e) => setLabel(i, (x) => ({ ...x, hostId: e.target.value }))}>
              <option value="">Choose a host</option>
              {local.hosts.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
            </Select>
          </div>
          {TEMPLATES.map((t) => {
            const x = l.templates[t];
            return (
              <div key={t} className={s.form}>
                <Checkbox label={`Take ${t} requests`} checked={x.enabled} onChange={(e) => setTpl(i, t, (y) => ({ ...y, enabled: e.target.checked }))} />
                {x.enabled ? (
                  <>
                    <div className={r.grid}>
                      <Input label={`${t}: max CPUs`} inputMode="numeric" value={x.maxCpus} onChange={(e) => setTpl(i, t, (y) => ({ ...y, maxCpus: e.target.value }))} />
                      <Input label={`${t}: max memory (GiB)`} inputMode="numeric" value={x.maxMemGiB} onChange={(e) => setTpl(i, t, (y) => ({ ...y, maxMemGiB: e.target.value }))} />
                      <Input label={`${t}: max time (minutes)`} inputMode="numeric" value={x.maxTimeMinutes} onChange={(e) => setTpl(i, t, (y) => ({ ...y, maxTimeMinutes: e.target.value }))} />
                    </div>
                    <p className={s.note}>Settings a requester may change. Give a min and a max, or a list of allowed values. 0 above means they may not ask for that resource.</p>
                    {x.keys.map((k, n) => (
                      <div key={n} className={r.grid}>
                        <Input label={`${t} setting`} value={k.key} onChange={(e) => setTpl(i, t, (y) => ({ ...y, keys: y.keys.map((z, m) => (m === n ? { ...z, key: e.target.value } : z)) }))} />
                        <Input label="Min" inputMode="decimal" value={k.min} onChange={(e) => setTpl(i, t, (y) => ({ ...y, keys: y.keys.map((z, m) => (m === n ? { ...z, min: e.target.value } : z)) }))} />
                        <Input label="Max" inputMode="decimal" value={k.max} onChange={(e) => setTpl(i, t, (y) => ({ ...y, keys: y.keys.map((z, m) => (m === n ? { ...z, max: e.target.value } : z)) }))} />
                        <Input label="Or allowed values" placeholder="a, b" value={k.choices} onChange={(e) => setTpl(i, t, (y) => ({ ...y, keys: y.keys.map((z, m) => (m === n ? { ...z, choices: e.target.value } : z)) }))} />
                        <div className={s.row}><Button onClick={() => setTpl(i, t, (y) => ({ ...y, keys: y.keys.filter((_, m) => m !== n) }))}>Remove setting</Button></div>
                      </div>
                    ))}
                    <div className={s.row}><Button onClick={() => setTpl(i, t, (y) => ({ ...y, keys: [...y.keys, { key: "", min: "", max: "", choices: "" }] }))}>Add a setting</Button></div>
                  </>
                ) : null}
              </div>
            );
          })}
          <div className={s.row}><Button variant="danger" onClick={() => setRows(rows.filter((_, j) => j !== i))}>Remove label</Button></div>
        </fieldset>
      ))}
      <div className={s.row}>
        <Button onClick={() => setRows([...rows, emptyLabel()])}>Add a label</Button>
        <Button variant="primary" onClick={save}>Save inbox</Button>
      </div>
      {error ? <p className={s.error} role="alert">{error}</p> : null}
      {saved ? <p className={s.note} role="status">Saved. It is published at the next check.</p> : null}
    </section>
  );
}
