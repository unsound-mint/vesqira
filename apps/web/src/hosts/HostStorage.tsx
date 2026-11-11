import { useCallback, useEffect, useRef, useState } from "react";
import { Button } from "../ui";
import { bytes, EM } from "../catalog/format";
import { getHostStorage, listRunFolders, measureFolder, refreshHostStorage } from "../api/hosts";
import type { Host, HostStorage as StorageAnswer, QuotaEntry, RunFolder } from "../api/hosts";
import s from "./HostStorage.module.css";

/** How often an open section asks again. The server reuses a look for the same time, so most asks cost the host nothing. */
export const REASK_MS = 10 * 60 * 1000;

const msg = (e: unknown, fallback: string) => (e instanceof Error ? e.message : fallback);

function pct(used: number, of: number): number {
  return of > 0 ? Math.round((used / of) * 100) : 0;
}

function Entry({ e }: { e: QuotaEntry }) {
  const hard = e.hardBytes;
  const p = hard === null ? null : pct(e.usedBytes, hard);
  return (
    <li className={s.entry}>
      <span>
        {e.scope ? <span className="mono">{e.scope}: </span> : null}
        <strong>{bytes(e.usedBytes)}</strong> used
        {hard !== null ? <> of {bytes(hard)} hard limit ({p} %)</> : ", no hard limit reported"}
        {e.softBytes !== null ? `, soft limit ${bytes(e.softBytes)}` : ""}
        {p !== null && p >= 90 ? <strong> — close to the limit; writing may stall when it is reached</strong> : null}
      </span>
      {hard !== null ? <meter className={s.meter} min={0} max={hard} value={Math.min(e.usedBytes, hard)} aria-label={`${e.scope || "quota"}: ${p} % of the hard limit`} /> : null}
      <span className={s.raw}>as printed: <span className="mono">{e.raw}</span></span>
    </li>
  );
}

/**
 * Quota and disk usage of a host, and how big its run folders are. Nothing is asked of the host until the section is opened;
 * then the answer is reused for ten minutes (Refresh asks again). Read-only: there is no way to delete anything here.
 */
export function HostStorage({ host }: { host: Host }) {
  const [open, setOpen] = useState(false);
  const [data, setData] = useState<StorageAnswer | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [folders, setFolders] = useState<RunFolder[]>([]);
  const [measuring, setMeasuring] = useState<string | null>(null);
  const [all, setAll] = useState(false);
  const [rowErr, setRowErr] = useState<Record<string, string>>({});
  const stop = useRef(false);

  const look = useCallback(async (force: boolean) => {
    setBusy(true);
    try {
      setData(await (force ? refreshHostStorage(host.id) : getHostStorage(host.id)));
      setErr(null);
    } catch (e) {
      setErr(msg(e, "Could not look at the host."));
    }
    setBusy(false);
  }, [host.id]);

  const loadFolders = useCallback(async () => {
    try {
      setFolders(await listRunFolders(host.id));
    } catch (e) {
      setErr(msg(e, "Could not list the run folders."));
    }
  }, [host.id]);

  useEffect(() => {
    if (!open) return;
    void look(false);
    void loadFolders();
    const t = setInterval(() => void look(false), REASK_MS);
    return () => clearInterval(t);
  }, [open, look, loadFolders]);

  async function size(path: string): Promise<boolean> {
    setMeasuring(path);
    try {
      const u = await measureFolder(host.id, path);
      setFolders((cur) => cur.map((f) => (f.path === path ? { ...f, bytes: u.bytes, partial: u.partial, measuredAt: u.measuredAt } : f)));
      setRowErr((cur) => { const { [path]: _gone, ...rest } = cur; return rest; });
      return true;
    } catch (e) {
      setRowErr((cur) => ({ ...cur, [path]: msg(e, "Could not measure it.") }));
      return false;
    } finally {
      setMeasuring(null);
    }
  }

  /** One folder at a time, so the login node is never asked for two `du` at once. A failure does not stop the rest. */
  async function sizeAll() {
    stop.current = false;
    setAll(true);
    for (const f of folders.filter((x) => x.bytes === null)) {
      if (stop.current) break;
      await size(f.path);
    }
    setAll(false);
  }

  const report = data?.report ?? null;
  const sorted = [...folders].sort((a, b) => (b.bytes ?? -1) - (a.bytes ?? -1));
  const unmeasured = folders.filter((f) => f.bytes === null).length;

  return (
    <section className={s.root} aria-label={`Storage on ${host.name}`}>
      <div className={s.head}>
        <Button type="button" aria-expanded={open} onClick={() => setOpen((o) => !o)}>{open ? "Hide disk and quota" : "Disk and quota"}</Button>
        {open ? <Button type="button" disabled={busy} onClick={() => void look(true)}>Refresh</Button> : null}
      </div>
      {open ? (
        <div className={s.body}>
          {busy && !report ? <p className={s.note} role="status">Asking {host.name}…</p> : null}
          {err ? <p role="alert" className={s.err}>{err}</p> : null}
          {data?.error ? <p role="alert" className={s.err}>Could not look at {host.name}: {data.error}.{report ? " Showing the last look." : ""}</p> : null}
          {report ? (
            <>
              <p className={s.note}>
                Looked at {new Date(report.checkedAt).toLocaleTimeString()}{data?.cached ? " (kept from an earlier look; Refresh asks again)" : ""}. Sizes are powers of 1024.
              </p>
              <h3 className={s.h}>Quota</h3>
              {report.quota ? (
                <>
                  <p className={s.note}>Answered by <span className="mono">{report.quota.tool}</span> (<span className="mono">{report.quota.command}</span>).</p>
                  <ul className={s.list}>{report.quota.entries.map((e, i) => <Entry key={`${e.scope}-${i}`} e={e} />)}</ul>
                </>
              ) : (
                <p className={s.note}>{report.quotaNote}</p>
              )}
              <h3 className={s.h}>Disk of the source folders</h3>
              <ul className={s.list}>
                {report.filesystems.map((f) => (
                  <li key={f.root} className={s.entry}>
                    <span className="mono">{f.root}</span>
                    {f.error ? (
                      <span className={s.err}>{f.error}</span>
                    ) : (
                      <span>
                        {f.mount} ({f.filesystem}): {bytes(f.usedBytes)} used of {bytes(f.totalBytes)}, <strong>{bytes(f.availBytes)} free</strong> ({pct(f.usedBytes, f.totalBytes)} % used). This is the file system, not your allowance.
                      </span>
                    )}
                    {!f.error ? <meter className={s.meter} min={0} max={f.totalBytes} value={Math.min(f.usedBytes, f.totalBytes)} aria-label={`${f.root}: ${pct(f.usedBytes, f.totalBytes)} % of the file system used`} /> : null}
                  </li>
                ))}
              </ul>
            </>
          ) : null}
          <h3 className={s.h}>Sizes of my runs</h3>
          <p className={s.note}>Output folders of your jobs and run folders found in opened projects on this host. A size is measured with <span className="mono">du -sk</span> when you ask, one folder at a time; large folders can take a minute. Nothing is deleted here.</p>
          {folders.length === 0 ? <p className={s.note}>No run folders known on this host yet.</p> : (
            <>
              <div className={s.head}>
                <Button type="button" disabled={all || unmeasured === 0 || measuring !== null} onClick={() => void sizeAll()}>Measure all ({unmeasured} not yet measured)</Button>
                {all ? <Button type="button" onClick={() => { stop.current = true; }}>Stop after this one</Button> : null}
              </div>
              <ul className={s.list} aria-label={`Run folders on ${host.name}, largest first`}>
                {sorted.map((f) => (
                  <li key={f.path} className={s.entry}>
                    <span><strong>{f.name}</strong> <span className={s.raw}>{f.kind === "job" ? "job output" : "run folder"}</span></span>
                    <span className="mono">{f.path}</span>
                    <span>
                      {f.bytes === null ? `not measured (${EM})` : <><strong>{f.partial ? "at least " : ""}{bytes(f.bytes)}</strong>{f.partial ? " (some entries could not be read)" : ""}{f.measuredAt ? `, measured ${new Date(f.measuredAt).toLocaleTimeString()}` : ""}</>}
                    </span>
                    {rowErr[f.path] ? <span role="alert" className={s.err}>{rowErr[f.path]}</span> : null}
                    <Button type="button" disabled={measuring !== null} aria-label={`${f.bytes === null ? "Measure" : "Measure again"} ${f.name}`} onClick={() => void size(f.path)}>
                      {measuring === f.path ? "Measuring…" : f.bytes === null ? "Measure" : "Measure again"}
                    </Button>
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
      ) : null}
    </section>
  );
}
