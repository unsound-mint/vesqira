import { useEffect, useId, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { File, Folder, FolderOpen } from "lucide-react";
import { chooseFile, completePath } from "../api/fs";
import type { Chosen, PathEntry, PathKind } from "../api/fs";
import { Button } from "./Button";
import { Icon } from "./Icon";
import c from "./Controls.module.css";
import s from "./PathField.module.css";

const DEBOUNCE_MS = 120;

/** The desktop has no file chooser: asked once, remembered for the session so later fields do not show a dead button. */
let noChooser = false;

const askable = (v: string) => v === "~" || v.startsWith("/") || v.startsWith("~/");

/** Longest common prefix, compared exactly. */
function commonPrefix(names: string[]): string {
  let p = names[0] ?? "";
  for (const n of names) while (!n.startsWith(p)) p = p.slice(0, -1);
  return p;
}

type Host = { id: number; name: string };

/**
 * A path input that completes like zsh and can open the desktop's file chooser (ADR 0031).
 * Completion asks the local server, or the host's worker when `hostId` is set; it returns names only.
 * Tab completes the longest common prefix, Enter or a click takes the highlighted name, Escape closes the list.
 */
export function PathField({ value, onChange, label, kind = "any", ext, hostId: hostProp, hosts, onHost, placeholder, required, disabled }: {
  value: string;
  onChange: (v: string) => void;
  label: string;
  kind?: PathKind;
  ext?: string[];
  /** Complete on this host instead of on this computer. `null` means a host is needed and none is chosen yet: no completion, no chooser. */
  hostId?: number | null;
  /** Names for hosts, used to say where a chosen folder is. */
  hosts?: Host[];
  /** Called when the file chooser returns a folder of another registered host and the person agrees to switch. */
  onHost?: (hostId: number) => void;
  placeholder?: string;
  required?: boolean;
  disabled?: boolean;
}) {
  const id = useId();
  const needsHost = hostProp === null;
  const hostId = hostProp ?? undefined;
  const listId = `${id}-list`;
  const [entries, setEntries] = useState<PathEntry[]>([]);
  const [truncated, setTruncated] = useState(false);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const [browse, setBrowse] = useState(!noChooser);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<{ text: string; hostId?: number; remotePath?: string } | null>(null);
  const seq = useRef(0);
  const listed = useRef({ for: "", base: "" }); // what the shown names were fetched for
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const abort = useRef<AbortController | undefined>(undefined);
  const extKey = (ext ?? []).join(",");

  useEffect(() => () => { clearTimeout(timer.current); abort.current?.abort(); }, []);

  function cancelPending() {
    seq.current++; // an answer to anything asked before now is stale
    clearTimeout(timer.current);
    abort.current?.abort();
  }

  async function fetchNow(v: string) {
    const mine = ++seq.current;
    abort.current?.abort();
    if (!askable(v) || needsHost) {
      setEntries([]);
      setOpen(false);
      return;
    }
    abort.current = new AbortController();
    try {
      const r = await completePath(v, { kind, ext: extKey ? extKey.split(",") : undefined, hostId }, abort.current.signal);
      if (mine !== seq.current) return; // a newer request was made while this one travelled
      listed.current = { for: v, base: r.base };
      setEntries(r.entries);
      setTruncated(r.truncated);
      setActive(-1);
      // a list whose only name is exactly what is typed offers nothing, and would cover the controls below
      setOpen(r.entries.length > 0 && !(r.entries.length === 1 && !r.entries[0]?.dir && r.entries[0]?.path === v));
    } catch {
      if (mine !== seq.current) return;
      setEntries([]);
      setOpen(false);
    }
  }

  function typed(v: string) {
    onChange(v);
    setNotice(null);
    clearTimeout(timer.current);
    seq.current++;
    abort.current?.abort();
    if (!askable(v) || needsHost) {
      setEntries([]);
      setOpen(false);
      return;
    }
    timer.current = setTimeout(() => void fetchNow(v), DEBOUNCE_MS);
  }

  const lastPart = value.slice(value.lastIndexOf("/") + 1);

  /** A name is taken as an absolute path ("~/" becomes the real home folder), so what is sent later is unambiguous. */
  function accept(e: PathEntry) {
    const v = e.path + (e.dir ? "/" : "");
    onChange(v);
    setNotice(null);
    if (e.dir) {
      clearTimeout(timer.current);
      void fetchNow(v); // a folder shows what is inside it
    } else {
      cancelPending();
      setOpen(false);
      setEntries([]);
    }
  }

  function onKey(ev: KeyboardEvent<HTMLInputElement>) {
    if (ev.key === "ArrowDown" || ev.key === "ArrowUp") {
      if (entries.length === 0) {
        if (askable(value) && ev.key === "ArrowDown") { ev.preventDefault(); void fetchNow(value); }
        return;
      }
      ev.preventDefault();
      setOpen(true);
      const d = ev.key === "ArrowDown" ? 1 : -1;
      setActive((a) => (a < 0 ? (d === 1 ? 0 : entries.length - 1) : (a + d + entries.length) % entries.length));
    } else if (ev.key === "Enter") {
      const e = open ? entries[active] : undefined;
      if (e) {
        ev.preventDefault(); // taking a name is not submitting the form
        accept(e);
      }
    } else if (ev.key === "Escape") {
      if (open) {
        ev.preventDefault();
        ev.stopPropagation();
        setOpen(false);
      }
    } else if (ev.key === "Tab" && !ev.shiftKey && open && entries.length > 0) {
      const picked = entries[active];
      if (picked) {
        ev.preventDefault();
        accept(picked);
        return;
      }
      if (truncated || listed.current.for !== value) return; // part of the folder, or names for older text: do not guess
      const names = entries.map((e) => e.name);
      const common = commonPrefix(names);
      // Only when it adds text to what was typed (the match may have been case-insensitive: then it is not a completion)
      const only = entries.length === 1 ? entries[0] : undefined;
      const exactFolder = !!only && only.dir && only.name === lastPart; // "data" + Tab -> "data/", as zsh does
      if (exactFolder || (common.length > lastPart.length && common.toLowerCase().startsWith(lastPart.toLowerCase()))) {
        ev.preventDefault();
        if (only) {
          accept(only);
        } else {
          const v = (listed.current.base.endsWith("/") ? listed.current.base : listed.current.base + "/") + common;
          onChange(v);
          clearTimeout(timer.current);
          void fetchNow(v);
        }
      }
      // nothing to complete: the focus moves on as usual
    }
  }

  async function choose() {
    setBusy(true);
    setNotice(null);
    try {
      const r = await chooseFile({ mode: kind === "dir" ? "folder" : "file", title: label, ext });
      if (r.status === "unavailable") {
        noChooser = true;
        setBrowse(false);
      } else if (r.status === "chosen") {
        take(r.chosen);
      }
    } catch (x) {
      setNotice({ text: x instanceof Error ? x.message : "The file chooser did not work." });
    } finally {
      setBusy(false);
    }
  }

  function take(ch: Chosen) {
    const name = (i: number) => hosts?.find((h) => h.id === i)?.name ?? "another host";
    cancelPending();
    setOpen(false);
    if (ch.hostId !== null && ch.remotePath !== null) {
      if (ch.hostId === hostId) {
        onChange(ch.remotePath);
      } else if (onHost) {
        setNotice({ text: `That folder is on ${name(ch.hostId)}.`, hostId: ch.hostId, remotePath: ch.remotePath });
      } else {
        setNotice({ text: `That folder is on ${name(ch.hostId)}, not where this field looks.` });
      }
    } else if (ch.gvfs) {
      setNotice({ text: `That folder is on ${ch.gvfs.user ? `${ch.gvfs.user}@` : ""}${ch.gvfs.host}, which is not added as a host yet. Add it under Hosts first.` });
    } else if (hostId !== undefined) {
      setNotice({ text: `That folder is on this computer, not on ${hosts?.find((h) => h.id === hostId)?.name ?? "the host"}.` });
    } else {
      onChange(ch.path);
    }
  }

  const showList = open && entries.length > 0;
  return (
    <div className={`${c.field} ${s.wrap}`}>
      <label htmlFor={id} id={`${id}-label`} className={c.label}>{label}</label>
      <div className={s.row}>
        <input
          id={id}
          className={`${c.input} ${s.input}`}
          role="combobox"
          aria-expanded={showList}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={showList && active >= 0 ? `${id}-o${active}` : undefined}
          autoComplete="off"
          spellCheck={false}
          value={value}
          placeholder={placeholder}
          required={required}
          disabled={disabled}
          onChange={(e) => typed(e.target.value)}
          onKeyDown={onKey}
          onBlur={() => setOpen(false)}
        />
        {browse ? (
          <Button type="button" icon={FolderOpen} disabled={disabled || busy || needsHost} title={needsHost ? "Choose a host first" : undefined} onClick={() => void choose()} aria-describedby={`${id}-label`}>
            Browse…
          </Button>
        ) : null}
      </div>
      <ul id={listId} role="listbox" aria-label="Matching names" className={s.list} hidden={!showList}>
        {entries.map((e, i) => (
          <li
            key={e.path}
            id={`${id}-o${i}`}
            role="option"
            aria-selected={i === active}
            className={s.option}
            onMouseDown={(ev) => ev.preventDefault()} // keep the focus in the input
            onClick={() => accept(e)}
          >
            <Icon icon={e.dir ? Folder : File} />
            <span className={s.name}>{e.name}{e.dir ? "/" : ""}</span>
          </li>
        ))}
        {truncated ? <li role="presentation" className={s.more}>More names than shown: keep typing.</li> : null}
      </ul>
      {notice ? (
        <p className={s.notice} role="status">
          {notice.text}
          {notice.hostId !== undefined && onHost ? (
            <Button type="button" onClick={() => { onHost(notice.hostId as number); onChange(notice.remotePath ?? ""); setNotice(null); }}>
              Use {hosts?.find((h) => h.id === notice.hostId)?.name ?? "that host"}
            </Button>
          ) : null}
        </p>
      ) : null}
    </div>
  );
}
