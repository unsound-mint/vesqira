import { Suspense, lazy, useState } from "react";
import { archiveEntry, restoreEntry } from "../api/lifecycle";
import type { EntryKind } from "../api/lifecycle";
import { Button } from "../ui";
import s from "./EntryActions.module.css";

// The dialog brings React Aria with it, so it loads only when someone asks to delete.
const DeleteDialog = lazy(() => import("./DeleteDialog"));

export type EntryChange = "archived" | "restored" | "deleted";

/**
 * Archive (instant, undone with Restore) and Delete… (opens the impact preview) for one catalog entry. Archiving only
 * hides the entry from lists; what refers to it keeps working. Deleting forgets the entry in the catalog and never
 * removes the file it points to (ADR 0039).
 */
export function EntryActions({ kind, id, name, archived, onChanged }: { kind: EntryKind; id: number; name: string; archived: boolean; onChanged: (change: EntryChange) => void | Promise<void> }) {
  const [confirming, setConfirming] = useState(false);
  const [note, setNote] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function change(c: "archived" | "restored") {
    setBusy(true);
    try {
      await (c === "archived" ? archiveEntry(kind, id) : restoreEntry(kind, id));
      setErr(null);
      setNote(c === "archived" ? `Archived “${name}”. It is hidden from the lists; what uses it keeps working.` : null);
      await onChanged(c);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "That did not work.");
    }
    setBusy(false);
  }

  return (
    <div className={s.wrap}>
      {archived ? (
        <p className={s.note} role="status">{note ?? "Archived: hidden from the lists, still used by whatever refers to it."}</p>
      ) : null}
      <div className={s.row}>
        {archived ? (
          <Button type="button" disabled={busy} onClick={() => void change("restored")}>{note ? "Undo" : "Restore"}</Button>
        ) : (
          <Button type="button" disabled={busy} onClick={() => void change("archived")}>Archive</Button>
        )}
        <Button type="button" disabled={busy} onClick={() => setConfirming(true)}>Delete…</Button>
      </div>
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      {confirming ? (
        <Suspense fallback={null}>
          <DeleteDialog kind={kind} id={id} name={name} onClose={() => setConfirming(false)} onDeleted={() => { setConfirming(false); void onChanged("deleted"); }} />
        </Suspense>
      ) : null}
    </div>
  );
}
