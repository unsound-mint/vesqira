import { useCallback, useEffect, useState } from "react";
import type { FormEvent } from "react";
import { ChevronRight, FolderOpen } from "lucide-react";
import { Button, Icon, Input } from "../ui";
import { fileTree, openProject, roots } from "../api/files";
import type { TreeEntry } from "../api/files";
import { bytes } from "../catalog/format";
import { hasNativeDialogs, pickFolder } from "./desktop";
import { iconFor, isFits } from "./fileIcon";
import s from "./FolderTree.module.css";

const LAST = "vesqira.project";
const remember = (p: string | null) => { try { if (p) localStorage.setItem(LAST, p); else localStorage.removeItem(LAST); } catch { /* storage may be blocked */ } };
const recall = () => { try { return localStorage.getItem(LAST); } catch { return null; } };
const join = (dir: string, name: string) => `${dir.replace(/\/$/, "")}/${name}`;
const msg = (e: unknown, fallback: string) => (e instanceof Error ? e.message : fallback);

/** The folder you opened, shown the way an editor shows it. Opening a FITS file registers it; other files are listed, not read. */
export function FolderTree({ onOpenFits }: { onOpenFits: (path: string) => Promise<string | null> }) {
  const [project, setProject] = useState<string | null>(null);
  const [typed, setTyped] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const open = useCallback(async (path: string) => {
    setBusy(true);
    try {
      const p = await openProject(path);
      remember(p.path);
      setProject(p.path);
      setErr(null);
    } catch (e) {
      setErr(msg(e, "Could not open that folder."));
    }
    setBusy(false);
  }, []);

  // Reopen the last project if it is still readable (the server remembers opened folders between runs).
  useEffect(() => {
    const last = recall();
    if (!last) return;
    let live = true;
    roots().then((r) => { if (live && r.includes(last)) setProject(last); }).catch(() => undefined);
    return () => { live = false; };
  }, []);

  async function choose() {
    try {
      const p = await pickFolder();
      if (p) await open(p);
    } catch (e) {
      setErr(typeof e === "string" ? `The folder chooser did not open: ${e}` : msg(e, "The folder chooser did not open."));
    }
  }
  function submit(e: FormEvent) {
    e.preventDefault();
    if (typed.trim()) void open(typed.trim());
  }

  return (
    <div className={s.wrap}>
      {project ? (
        <>
          <div className={s.head}>
            <strong className={s.title} title={project}>{project.split("/").filter(Boolean).pop()}</strong>
            <Button type="button" onClick={hasNativeDialogs() ? choose : () => { remember(null); setProject(null); }}>{hasNativeDialogs() ? "Open another…" : "Close"}</Button>
          </div>
          <ul className={s.tree} role="tree" aria-label={`Files in ${project}`}>
            <Children dir={project} level={0} onOpenFits={onOpenFits} />
          </ul>
        </>
      ) : hasNativeDialogs() ? (
        <div className={s.empty}>
          <p>Open a folder to see its files here.</p>
          <Button type="button" variant="primary" disabled={busy} onClick={choose}><Icon icon={FolderOpen} /> Open Folder…</Button>
        </div>
      ) : (
        <form className={s.empty} onSubmit={submit} aria-label="Open a project folder">
          <Input label="Open a folder" value={typed} onChange={(e) => setTyped(e.target.value)} placeholder="/home/you/projects/vgt" spellCheck={false} />
          <Button type="submit" variant="primary" disabled={busy || !typed.trim()}>Open Folder</Button>
          <p className={s.note}>The desktop app offers a folder chooser instead.</p>
        </form>
      )}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
    </div>
  );
}

function Children({ dir, level, onOpenFits }: { dir: string; level: number; onOpenFits: (path: string) => Promise<string | null> }) {
  const [entries, setEntries] = useState<TreeEntry[] | null>(null);
  const [truncated, setTruncated] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    fileTree(dir).then((t) => { if (live) { setEntries(t.entries); setTruncated(t.truncated); } }).catch((e) => live && setErr(msg(e, "Could not list this folder.")));
    return () => { live = false; };
  }, [dir]);

  if (err) return <li role="none" className={s.err} style={{ paddingLeft: `calc(var(--sp-3) + ${level} * var(--sp-4))` }}>{err}</li>;
  if (!entries) return <li role="none" className={s.note} style={{ paddingLeft: `calc(var(--sp-3) + ${level} * var(--sp-4))` }}>Reading…</li>;
  return (
    <>
      {entries.map((e) => <Node key={e.name} dir={dir} entry={e} level={level} onOpenFits={onOpenFits} />)}
      {entries.length === 0 ? <li role="none" className={s.note} style={{ paddingLeft: `calc(var(--sp-3) + ${level} * var(--sp-4))` }}>Empty</li> : null}
      {truncated ? <li role="none" className={s.note} style={{ paddingLeft: `calc(var(--sp-3) + ${level} * var(--sp-4))` }}>Only the first entries are shown.</li> : null}
    </>
  );
}

function Node({ dir, entry, level, onOpenFits }: { dir: string; entry: TreeEntry; level: number; onOpenFits: (path: string) => Promise<string | null> }) {
  const [open, setOpen] = useState(false);
  const [note, setNote] = useState<string | null>(null);
  const path = join(dir, entry.name);
  const pad = { paddingLeft: `calc(var(--sp-2) + ${level} * var(--sp-4))` };

  if (entry.kind === "dir") {
    return (
      <li role="none">
        <button type="button" role="treeitem" aria-expanded={open} className={s.row} style={pad} onClick={() => setOpen(!open)}>
          <span className={open ? s.chevOpen : s.chev}><Icon icon={ChevronRight} /></span>
          <img className={s.icon} src={iconFor(entry.name, "dir", open)} alt="" />
          <span className={s.name}>{entry.name}</span>
        </button>
        {open ? <ul role="group" className={s.group}><Children dir={path} level={level + 1} onOpenFits={onOpenFits} /></ul> : null}
      </li>
    );
  }
  const fits = entry.kind === "file" && isFits(entry.name);
  return (
    <li role="none">
      <button
        type="button"
        role="treeitem"
        aria-selected={false}
        className={fits ? s.row : s.rowPlain}
        style={{ ...pad, paddingLeft: `calc(var(--sp-2) + ${level} * var(--sp-4) + var(--sp-4))` }}
        title={fits ? "Open in the viewer (registers the file; it stays where it is)" : entry.kind === "link" ? "A symbolic link; not followed" : "Listed only: Vesqira has no viewer for this file"}
        disabled={!fits}
        onClick={async () => setNote(await onOpenFits(path))}
      >
        <img className={s.icon} src={iconFor(entry.name, entry.kind)} alt="" />
        <span className={s.name}>{entry.name}</span>
        {entry.size != null && entry.kind === "file" ? <span className={s.size}>{bytes(entry.size)}</span> : null}
      </button>
      {note ? <p role="alert" className={s.err} style={pad}>{note}</p> : null}
    </li>
  );
}
