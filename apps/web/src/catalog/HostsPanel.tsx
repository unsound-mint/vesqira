import { useState } from "react";
import type { FormEvent } from "react";
import { File, Folder } from "lucide-react";
import { Button, Input } from "../ui";
import { checkHost, createHost, deleteHost, listRemote, registerRemote } from "../api/hosts";
import type { Host, HostCheck, RemoteEntry } from "../api/hosts";
import { bytes } from "./format";
import s from "./HostsPanel.module.css";

const join = (dir: string, name: string) => `${dir.replace(/\/$/, "")}/${name}`;
const parent = (dir: string) => dir.replace(/\/[^/]+\/?$/, "") || "/";
const msg = (e: unknown, fallback: string) => (e instanceof Error ? e.message : fallback);

/** Other machines (an HPC login node, another laptop). Reached with your own ssh and agent; the VPN is yours to bring up. */
export function HostsPanel({ hosts, onChange, onRegistered }: { hosts: Host[]; onChange: () => Promise<void>; onRegistered: () => Promise<void> }) {
  const [form, setForm] = useState({ name: "", dest: "", python: "python3", roots: "" });
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function add(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await createHost({ name: form.name.trim(), dest: form.dest.trim(), python: form.python.trim() || "python3", roots: form.roots.split("\n").map((r) => r.trim()).filter(Boolean) });
      setForm({ name: "", dest: "", python: "python3", roots: "" });
      setErr(null);
      await onChange();
    } catch (x) {
      setErr(msg(x, "Could not add the host."));
    }
    setBusy(false);
  }

  return (
    <div className={s.wrap}>
      {hosts.map((h) => <HostRow key={h.id} host={h} onChange={onChange} onRegistered={onRegistered} />)}
      {hosts.length === 0 ? <p className={s.note}>No hosts yet. Files stay on the host; only planes travel.</p> : null}
      <form className={s.form} onSubmit={add} aria-label="Add a host">
        <Input label="Host name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Cluster login" />
        <Input label="SSH destination" value={form.dest} onChange={(e) => setForm({ ...form, dest: e.target.value })} placeholder="alias from your ssh config" spellCheck={false} />
        <Input label="Python on the host" value={form.python} onChange={(e) => setForm({ ...form, python: e.target.value })} spellCheck={false} />
        <div className={s.field}>
          <label htmlFor="host-roots" className={s.label}>Source folders on the host (one per line)</label>
          <textarea id="host-roots" className={s.area} rows={3} value={form.roots} onChange={(e) => setForm({ ...form, roots: e.target.value })} placeholder="/shared/home/you/cubes" spellCheck={false} />
        </div>
        <Button type="submit" disabled={busy || !form.name.trim() || !form.dest.trim() || !form.roots.trim()}>Add host</Button>
        {err ? <p role="alert" className={s.err}>{err}</p> : null}
      </form>
      <p className={s.note}>Vesqira runs your own <span className="mono">ssh</span> without prompts: bring the VPN up first (for example gpclient), make sure <span className="mono">ssh &lt;destination&gt;</span> works, and add astropy to the Python you name. Keys and passwords are never read by Vesqira.</p>
    </div>
  );
}

function HostRow({ host, onChange, onRegistered }: { host: Host; onChange: () => Promise<void>; onRegistered: () => Promise<void> }) {
  const [check, setCheck] = useState<HostCheck | null>(null);
  const [state, setState] = useState<string | null>(null);
  const [dir, setDir] = useState<string | null>(null);
  const [entries, setEntries] = useState<RemoteEntry[]>([]);
  const [busy, setBusy] = useState(false);

  async function run<T>(f: () => Promise<T>): Promise<T | null> {
    setBusy(true);
    try {
      const r = await f();
      setState(null);
      return r;
    } catch (x) {
      setState(msg(x, "That did not work."));
      return null;
    } finally {
      setBusy(false);
    }
  }
  async function open(path?: string) {
    const r = await run(() => listRemote(host.id, path));
    if (r) {
      setDir(r.path);
      setEntries(r.entries);
    }
  }
  async function register(path: string) {
    if (await run(() => registerRemote(host.id, path))) await onRegistered();
  }

  return (
    <section className={s.host} aria-label={`Host ${host.name}`}>
      <div className={s.head}>
        <strong className={s.name}>{host.name}</strong>
        <span className="mono">{host.dest}</span>
      </div>
      <div className={s.row}>
        <Button type="button" disabled={busy} onClick={async () => setCheck(await run(() => checkHost(host.id)))}>Check connection</Button>
        <Button type="button" disabled={busy} onClick={() => open()}>Browse</Button>
        <Button type="button" disabled={busy} onClick={async () => { if (await run(() => deleteHost(host.id))) await onChange(); }}>Remove</Button>
      </div>
      {state ? <p role="alert" className={s.err}>{state}</p> : null}
      {check ? (
        <p className={s.note} role="status">
          Connected. Python {check.python}, astropy {check.astropy}. {check.enforces_roots ? "The host enforces its source folders itself." : "The host is NOT enforcing source folders."}
        </p>
      ) : null}
      {dir !== null ? (
        <div className={s.browse}>
          <p className={s.note}><span className="mono">{dir}</span></p>
          <ul className={s.list} aria-label={`Files on ${host.name}`}>
            {host.roots.every((r) => dir !== r) ? <li><button type="button" className={s.item} onClick={() => open(parent(dir))}><Folder size={14} aria-hidden="true" /> ..</button></li> : null}
            {entries.map((e) => (
              <li key={e.name}>
                {e.kind === "dir" ? (
                  <button type="button" className={s.item} onClick={() => open(join(dir, e.name))}><Folder size={14} aria-hidden="true" /> {e.name}</button>
                ) : /\.fits?$/i.test(e.name) ? (
                  <button type="button" className={s.item} onClick={() => register(join(dir, e.name))} title="Register this file (it stays on the host)"><File size={14} aria-hidden="true" /> {e.name} <span className={s.size}>{bytes(e.size ?? 0)}</span></button>
                ) : (
                  <span className={s.other}><File size={14} aria-hidden="true" /> {e.name}</span>
                )}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </section>
  );
}
