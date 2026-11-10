import { useEffect, useRef, useState } from "react";
import { Copy, X } from "lucide-react";
import * as api from "../api/reports";
import type { PublishState, Reader } from "../api/reports";
import { Button, Icon, Input } from "../ui";
import { relTime } from "./time";
import s from "./ReportsView.module.css";

/**
 * Who can read, and where reports are published. A reader link is made for one named person, shown once, and can be
 * revoked at once. Anyone holding a link can read the space and comment under that name, so it is attribution, not a
 * login; the panel says so.
 */
export function SharePanel({ state, onState, onClose }: { state: PublishState | null; onState: (s: PublishState) => void; onClose: () => void }) {
  const heading = useRef<HTMLHeadingElement>(null);
  const [readers, setReaders] = useState<Reader[] | null>(null);
  const [name, setName] = useState("");
  const [made, setMade] = useState<{ name: string; link: string } | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [setup, setSetup] = useState(!state?.url || !state.keySet);
  const [url, setUrl] = useState(state?.url ?? "");
  const [key, setKey] = useState("");
  const [owner, setOwner] = useState(state?.ownerName ?? "");
  const [agent, setAgent] = useState(state?.agentName ?? "");
  const [space, setSpace] = useState(state?.spaceTitle ?? "");
  const [copied, setCopied] = useState(false);
  useEffect(() => heading.current?.focus(), []);
  useEffect(() => {
    if (state?.url && state.keySet) api.listReaders().then(setReaders).catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)));
  }, [state?.url, state?.keySet]);

  async function act(f: () => Promise<void>) {
    setErr(null);
    try {
      await f();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "That did not work.");
    }
  }

  return (
    <div className={s.panelInner}>
      <div className={s.panelHead}>
        <h2 ref={heading} tabIndex={-1}>Share</h2>
        <button type="button" className={s.iconBtn} onClick={onClose} aria-label="Close sharing"><Icon icon={X} /></button>
      </div>
      <div className={s.panelScroll}>
        {err ? <p className={s.error} role="alert">{err}</p> : null}
        {setup ? (
          <form className={s.form} onSubmit={(e) => { e.preventDefault(); void act(async () => { const st = await api.savePublishSettings({ url, ...(key ? { key } : {}), ownerName: owner, agentName: agent, spaceTitle: space }); onState(st); setKey(""); setSetup(false); }); }}>
            <p className={s.muted}>Reports are published to your own publish server, so your supervisor can read them when this computer is off. It holds only what you publish. Setup: docs/setup/publish-server.md.</p>
            <Input label="Publish server address" placeholder="https://reports.example.org" value={url} onChange={(e) => setUrl(e.target.value)} required />
            <Input label={state?.keySet ? "Publisher key (leave empty to keep the current one)" : "Publisher key"} type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} required={!state?.keySet} />
            <Input label="Your name, as readers see it" value={owner} onChange={(e) => setOwner(e.target.value)} />
            <Input label="Your agent's name" placeholder="VGT agent" value={agent} onChange={(e) => setAgent(e.target.value)} />
            <Input label="Title of the space readers see" placeholder="LMC reports" value={space} onChange={(e) => setSpace(e.target.value)} />
            <div className={s.row}><Button variant="primary" type="submit">Save</Button>{state?.url ? <button type="button" className={s.ghost} onClick={() => setSetup(false)}>Cancel</button> : null}</div>
          </form>
        ) : (
          <>
            <section className={s.shareSec}>
              <h3>Readers</h3>
              {readers === null ? <p className={s.muted}>Loading…</p> : readers.filter((r) => !r.revoked).length === 0 ? <p className={s.muted}>Nobody has a link yet.</p> : null}
              <ul className={s.readers}>
                {readers?.filter((r) => !r.revoked).map((r) => (
                  <li key={r.id}>
                    <span><strong>{r.name}</strong><span className={s.muted}> · since {new Date(r.createdAt).toLocaleDateString()}</span></span>
                    <button type="button" className={s.ghost} onClick={() => { if (confirm(`Revoke ${r.name}'s link? It stops working at once.`)) void act(async () => { await api.revokeReader(r.id); setReaders(await api.listReaders()); }); }}>Revoke</button>
                  </li>
                ))}
              </ul>
              {made ? (
                <div className={s.made}>
                  <p>Link for <strong>{made.name}</strong>. It is shown only now; send it to them yourself.</p>
                  <div className={s.row}>
                    <input readOnly value={made.link} onFocus={(e) => e.target.select()} aria-label="Reader link" className={s.linkBox} />
                    <button type="button" className={s.iconBtn} aria-label="Copy link" onClick={() => void navigator.clipboard?.writeText(made.link).then(() => setCopied(true))}><Icon icon={Copy} /></button>
                  </div>
                  {copied ? <p className={s.muted}>Copied.</p> : null}
                </div>
              ) : null}
              <form className={s.row} onSubmit={(e) => { e.preventDefault(); if (!name.trim()) return; void act(async () => { const r = await api.createReader(name.trim()); setMade({ name: r.reader.name, link: r.link }); setCopied(false); setName(""); setReaders(await api.listReaders()); }); }}>
                <input className={s.linkBox} placeholder="Name, e.g. Supervisor" value={name} onChange={(e) => setName(e.target.value)} aria-label="Reader name" />
                <Button type="submit" disabled={!name.trim()}>Make link</Button>
              </form>
              <p className={s.muted}>Anyone holding a link can read the shared reports and comment under that name. Revoke a link that was forwarded.</p>
            </section>
            <section className={s.shareSec}>
              <h3>Publish server</h3>
              <p className={s.muted}>{state?.url}<br />{state?.lastError ? `Last sync failed: ${state.lastError}` : state?.lastSync ? `Comments synced ${relTime(state.lastSync)}` : "Not synced yet"}</p>
              <button type="button" className={s.ghost} onClick={() => setSetup(true)}>Change settings</button>
            </section>
          </>
        )}
      </div>
    </div>
  );
}
