import { useState } from "react";
import type { FormEvent } from "react";
import { FolderOpen } from "lucide-react";
import type { ProjectListItem } from "../api/projects";
import { hasNativeDialogs, pickFolder } from "../files/desktop";
import { Button, PathField, Select } from "../ui";
import { location, num } from "./format";
import s from "./Projects.module.css";

type Host = { id: number; name: string };

/** Screen 1: recent projects, and a folder on this computer or on a host to open. Opening reads inside that folder only. */
export function OpenProject({ recent, hosts, currentId, busy, error, onOpen }: { recent: ProjectListItem[]; hosts: Host[]; currentId: number | null; busy: boolean; error: string | null; onOpen: (path: string, hostId: number) => void }) {
  const [local, setLocal] = useState("");
  const [remote, setRemote] = useState("");
  const [hostId, setHostId] = useState<number | null>(null);
  const hostName = (id: number) => hosts.find((h) => h.id === id)?.name ?? "host";

  const submitLocal = (e: FormEvent) => {
    e.preventDefault();
    if (local.trim()) onOpen(local.trim(), 0);
  };
  const submitRemote = (e: FormEvent) => {
    e.preventDefault();
    if (hostId !== null && remote.trim()) onOpen(remote.trim(), hostId);
  };

  return (
    <div className={s.root}>
      <div className={s.page}>
        <h2 className={s.pageTitle}>Open a project</h2>
        <div className={s.two}>
          <section className={s.card} aria-labelledby="recent-h">
            <h3 id="recent-h" className={s.cardTitle}>Recent</h3>
            {recent.length === 0 ? (
              <p className={s.note}>No project opened yet. Open the folder you work in: Vesqira finds runs, data, configs and job scripts by itself.</p>
            ) : (
              <ul className={s.recent} aria-label="Recent projects">
                {recent.map((p) => (
                  <li key={p.id}>
                    <button type="button" className={s.recentBtn} aria-current={p.id === currentId ? "true" : undefined} disabled={busy} onClick={() => onOpen(p.path, p.hostId)} title={p.hostId ? `${hostName(p.hostId)}: ${p.path}` : p.path}>
                      <span className={s.recentName}>{p.name}</span>
                      <span className={s.recentWhere}>{location(p, hostName)}</span>
                      <span className={s.recentCounts}>
                        {p.lastScanAt ? `${num(p.runs)} run${p.runs === 1 ? "" : "s"} · ${num(p.data)} data file${p.data === 1 ? "" : "s"}` : "not scanned yet"}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section className={s.card} aria-labelledby="open-h">
            <h3 id="open-h" className={s.cardTitle}>Open</h3>
            <form className={s.form} onSubmit={submitLocal}>
              <PathField label="Folder on this computer" kind="dir" placeholder="~/Projects/my-project" value={local} onChange={setLocal} hosts={hosts} pick={hasNativeDialogs() ? pickFolder : undefined} />
              <div><Button type="submit" variant="primary" icon={FolderOpen} disabled={busy || !local.trim()}>{busy ? "Opening…" : "Open folder"}</Button></div>
            </form>
            <form className={s.form} onSubmit={submitRemote}>
              <Select label="Host" value={hostId ?? ""} onChange={(e) => { setHostId(e.target.value ? Number(e.target.value) : null); setRemote(""); }}>
                <option value="">{hosts.length === 0 ? "No hosts added yet" : "Choose a host"}</option>
                {hosts.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
              </Select>
              <PathField label="Folder on the host" kind="dir" hostId={hostId} hosts={hosts} placeholder="~/path/on/the/host" value={remote} onChange={setRemote} disabled={hostId === null} />
              <div><Button type="submit" icon={FolderOpen} disabled={busy || hostId === null || !remote.trim()}>Open on host</Button></div>
            </form>
            {error ? <p role="alert" className={s.err}>{error}</p> : null}
            <p className={s.note}>Opening a folder lets Vesqira read inside it (and only inside it). Nothing is written until you confirm a layout.</p>
          </section>
        </div>
      </div>
    </div>
  );
}
