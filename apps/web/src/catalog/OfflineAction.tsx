import { Suspense, lazy, useCallback, useEffect, useState } from "react";
import { forgetOffline, getOffline } from "../api/offline";
import type { OfflineDataset } from "../api/offline";
import { Button, Pill } from "../ui";
import { bytes } from "./format";
import s from "./OfflineSheet.module.css";

// The sheet brings React Aria with it, so it loads only when someone asks to prepare.
const OfflineSheet = lazy(() => import("./OfflineSheet"));

const when = (iso: string) => new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });

/**
 * "Prepare for offline…" for a dataset on a host, with what is kept now: when it was taken, what it covers, whether it is stale,
 * and Forget. A file on this computer needs none, and the section says so (ADR 0059).
 */
export function DatasetOffline({ id, name, hostId }: { id: number; name: string; hostId: number }) {
  const [copy, setCopy] = useState<OfflineDataset | null | undefined>(undefined);
  const [open, setOpen] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const reload = useCallback(() => {
    getOffline().then((o) => { setCopy(o.datasets.find((d) => d.id === id) ?? null); setErr(null); }).catch((e: unknown) => setErr(e instanceof Error ? e.message : "Could not read the offline store."));
  }, [id]);
  useEffect(() => { setCopy(undefined); reload(); }, [reload]);

  if (hostId === 0) return <p className={s.coverage}>This file is on this computer, so it needs no offline copy.</p>;
  return (
    <div className={s.panel}>
      {copy === undefined && !err ? <p className={s.coverage} role="status">Looking for an offline copy…</p> : null}
      {copy ? (
        <>
          <div className={s.row}>
            <Pill tone={copy.stale ? "warn" : "ok"}>{copy.stale ? "Stale: the file changed" : "Copy is current"}</Pill>
            <span className={s.coverage}>{bytes(copy.bytes)} · taken {when(copy.takenAt)}</span>
          </div>
          <p className={s.coverage}>{copy.stale ? "The file on the host is no longer the one this was made from, so it is kept but never served. Prepare it again. " : ""}Kept: {copy.coverage || "nothing usable"}.</p>
        </>
      ) : copy === null ? <p className={s.coverage}>Nothing is kept offline for this dataset.</p> : null}
      <div className={s.row}>
        <Button type="button" onClick={() => setOpen(true)}>Prepare for offline…</Button>
        {copy ? (
          <Button type="button" disabled={busy} onClick={() => { setBusy(true); forgetOffline(id).then(reload).catch((e: unknown) => setErr(e instanceof Error ? e.message : "Could not forget it.")).finally(() => setBusy(false)); }}>Forget offline copy</Button>
        ) : null}
      </div>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {open ? (
        <Suspense fallback={null}>
          <OfflineSheet title={name} datasetId={id} onClose={() => setOpen(false)} onChanged={reload} />
        </Suspense>
      ) : null}
    </div>
  );
}

/** "Prepare for offline…" on a board: its pictures, and the datasets they were drawn from. */
export function BoardOffline({ id, name }: { id: number; name: string }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button type="button" onClick={() => setOpen(true)}>Prepare for offline…</Button>
      {open ? (
        <Suspense fallback={null}>
          <OfflineSheet title={name} boardId={id} onClose={() => setOpen(false)} onChanged={() => undefined} />
        </Suspense>
      ) : null}
    </>
  );
}
