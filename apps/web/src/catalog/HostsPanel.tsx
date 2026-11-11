import { useCallback, useEffect, useState } from "react";
import type { FormEvent } from "react";
import { File, Folder, X } from "lucide-react";
import { Button, Input, PathField, Select } from "../ui";
import { browseHostDirs, checkHost, createHost, deleteHost, listRemote, registerRemote, sshConfigHosts } from "../api/hosts";
import type { Host, HostCheck, RemoteEntry, SshAlias } from "../api/hosts";
import { bytes } from "./format";
import { HostStorage } from "../hosts/HostStorage";
import { Transfers } from "../hosts/Transfers";
import s from "./HostsPanel.module.css";

const join = (dir: string, name: string) => `${dir.replace(/\/$/, "")}/${name}`;
const parent = (dir: string) => dir.replace(/\/[^/]+\/?$/, "") || "/";
const OTHER = "\u0000other";
const msg = (e: unknown, fallback: string) => (e instanceof Error ? e.message : fallback);

/** Other machines (an HPC login node, another laptop). Reached with your own ssh and agent; the VPN is yours to bring up. */
export function HostsPanel({ hosts, onChange, onRegistered }: { hosts: Host[]; onChange: () => Promise<void>; onRegistered: () => Promise<void> }) {
  const [form, setForm] = useState({ name: "", dest: "" });
  const [roots, setRoots] = useState<string[]>([]);
  const [choosing, setChoosing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [aliases, setAliases] = useState<SshAlias[]>([]);
  const [other, setOther] = useState(false);

  // Names that already work with `ssh <name>`, offered while typing the destination. Failing to read them offers none.
  useEffect(() => {
    let live = true;
    sshConfigHosts().then((a) => live && setAliases(a)).catch(() => undefined);
    return () => { live = false; };
  }, []);

  function setDest(dest: string) {
    const known = aliases.find((a) => a.alias === dest);
    setForm((f) => ({ ...f, dest, name: known && !f.name.trim() ? dest : f.name }));
  }

  async function add(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await createHost({ name: form.name.trim(), dest: form.dest.trim(), roots });
      setForm({ name: "", dest: "" });
      setRoots([]);
      setChoosing(false);
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
      {hosts.length > 0 ? <Transfers hosts={hosts} onRegistered={onRegistered} /> : null}
      <form className={s.form} onSubmit={add} aria-label="Add a host">
        <Input label="Host name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Cluster login" />
        {aliases.length > 0 && !other ? (
          // A real dropdown of what `ssh <name>` already resolves; "Another destination" falls back to typing.
          <Select label="SSH destination" value={form.dest} onChange={(e) => (e.target.value === OTHER ? (setOther(true), setDest("")) : setDest(e.target.value))}>
            <option value="" disabled>Choose from your ssh config</option>
            {aliases.map((a) => <option key={a.alias} value={a.alias}>{[a.alias, [a.user, a.hostName].filter(Boolean).join("@")].filter(Boolean).join(" — ")}</option>)}
            <option value={OTHER}>Another destination…</option>
          </Select>
        ) : (
          <Input label="SSH destination" value={form.dest} onChange={(e) => setDest(e.target.value)} placeholder="user@host or an ssh alias" spellCheck={false} autoComplete="off" />
        )}
        <div className={s.field}>
          <span className={s.label}>Source folders on the host</span>
          {roots.map((r) => (
            <div key={r} className={s.chosen}>
              <Folder size={14} aria-hidden="true" /> <span className="mono">{r}</span>
              <button type="button" className={s.item} aria-label={`Remove ${r}`} onClick={() => setRoots(roots.filter((x) => x !== r))}><X size={14} aria-hidden="true" /></button>
            </div>
          ))}
          {choosing ? (
            <FolderChooser dest={form.dest.trim()} onChoose={(p) => { setRoots((r) => (r.includes(p) ? r : [...r, p])); setChoosing(false); }} onCancel={() => setChoosing(false)} />
          ) : (
            <Button type="button" disabled={!form.dest.trim() || roots.length >= 8} onClick={() => setChoosing(true)}>{roots.length ? "Add another folder…" : "Choose folder…"}</Button>
          )}
        </div>
        <Button type="submit" disabled={busy || !form.name.trim() || !form.dest.trim() || roots.length === 0}>Add host</Button>
        {err ? <p role="alert" className={s.err}>{err}</p> : null}
      </form>
      <p className={s.note}>Vesqira runs your own <span className="mono">ssh</span> without prompts: bring the VPN up first (for example gpclient), make sure <span className="mono">ssh &lt;destination&gt;</span> works, and install astropy for the host's python3. Keys and passwords are never read by Vesqira.</p>
    </div>
  );
}

function HostRow({ host, onChange, onRegistered }: { host: Host; onChange: () => Promise<void>; onRegistered: () => Promise<void> }) {
  const [check, setCheck] = useState<HostCheck | null>(null);
  const [state, setState] = useState<string | null>(null);
  const [dir, setDir] = useState<string | null>(null);
  const [entries, setEntries] = useState<RemoteEntry[]>([]);
  const [busy, setBusy] = useState(false);
  const [regPath, setRegPath] = useState("");

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
      setEntries(r.entries ?? []);
    }
  }
  async function register(path: string) {
    if (await run(() => registerRemote(host.id, path))) await onRegistered();
  }
  async function registerTyped(e: FormEvent) {
    e.preventDefault();
    if (regPath.trim() && (await run(() => registerRemote(host.id, regPath.trim())))) {
      setRegPath("");
      await onRegistered();
    }
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
      <form className={s.regForm} onSubmit={registerTyped}>
        <PathField label={`FITS file path on ${host.name}`} kind="file" ext={[".fits", ".fit", ".fits.gz"]} hostId={host.id} value={regPath} onChange={setRegPath} placeholder="/shared/home/you/cubes/cube.fits" />
        <Button type="submit" disabled={busy || !regPath.trim()}>Register</Button>
      </form>
      {state ? <p role="alert" className={s.err}>{state}</p> : null}
      <HostStorage host={host} />
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

/** Browses the host's folders (starting at its home) with your own ssh, so a source folder is picked and never typed. */
function FolderChooser({ dest, onChoose, onCancel }: { dest: string; onChoose: (path: string) => void; onCancel: () => void }) {
  const [dir, setDir] = useState<string | null>(null);
  const [entries, setEntries] = useState<{ name: string }[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(true);

  const go = useCallback(async (path?: string) => {
    setBusy(true);
    try {
      const r = await browseHostDirs(dest, path);
      setDir(r.path);
      setEntries(r.entries ?? []);
      setErr(null);
    } catch (x) {
      setErr(msg(x, "Could not list that folder."));
    }
    setBusy(false);
  }, [dest]);
  useEffect(() => { void go(); }, [go]);

  return (
    <div className={s.browse}>
      <p className={s.note}>{busy ? "Connecting…" : <span className="mono">{dir}</span>}</p>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {dir ? (
        <ul className={s.list} aria-label={`Folders on ${dest}`}>
          {dir !== "/" ? <li><button type="button" className={s.item} disabled={busy} onClick={() => go(parent(dir))}><Folder size={14} aria-hidden="true" /> ..</button></li> : null}
          {entries.map((e) => <li key={e.name}><button type="button" className={s.item} disabled={busy} onClick={() => go(join(dir, e.name))}><Folder size={14} aria-hidden="true" /> {e.name}</button></li>)}
        </ul>
      ) : null}
      <div className={s.row}>
        <Button type="button" variant="primary" disabled={busy || !dir || dir === "/"} onClick={() => dir && onChoose(dir)}>Use this folder</Button>
        <Button type="button" onClick={onCancel}>Cancel</Button>
      </div>
    </div>
  );
}
