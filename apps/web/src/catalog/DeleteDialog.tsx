import { useEffect, useState } from "react";
import { deleteEntry, getImpact, ImpactChanged } from "../api/lifecycle";
import type { EntryKind, Impact, ImpactEffect } from "../api/lifecycle";
import { Button } from "../ui";
import { Modal } from "../ui/Modal";
import { bytes } from "./format";
import s from "./EntryActions.module.css";

/** Said in words, never by colour alone. */
const EFFECT: Record<ImpactEffect, string> = { stays: "Stays", unlinked: "Unlinked", removed: "Removed", blocks: "Blocks deleting" };

const NOUN: Record<EntryKind, string> = { datasets: "dataset", runs: "run", figures: "figure", boards: "board" };

/**
 * The confirmation for "Delete…": what depends on the entry, in plain words, before anything happens. The delete sends
 * the digest of exactly this preview; if the dependents changed meanwhile the server refuses and the new preview is
 * shown to be confirmed again. A file Vesqira wrote itself asks a separate question about its bytes.
 */
export default function DeleteDialog({ kind, id, name, onClose, onDeleted }: { kind: EntryKind; id: number; name: string; onClose: () => void; onDeleted: () => void }) {
  const [impact, setImpact] = useState<Impact | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [owned, setOwned] = useState<"" | "keep" | "remove">("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let live = true;
    getImpact(kind, id).then((i) => live && setImpact(i)).catch((e: unknown) => live && setErr(e instanceof Error ? e.message : "Could not work out what depends on this."));
    return () => {
      live = false;
    };
  }, [kind, id]);

  const needsChoice = impact?.ownedBytes != null && owned === "";
  async function confirm() {
    if (!impact) return;
    setBusy(true);
    setErr(null);
    try {
      await deleteEntry(kind, id, { impactDigest: impact.digest, ...(impact.ownedBytes ? { ownedBytes: owned === "remove" ? "remove" : "keep" } : {}) });
      onDeleted();
    } catch (e) {
      if (e instanceof ImpactChanged) {
        setImpact(e.impact);
        setNotice("What depends on this changed since you looked. The list below is the new one; confirm again if it is still what you want.");
      } else {
        setErr(e instanceof Error ? e.message : "Could not delete.");
      }
    }
    setBusy(false);
  }

  return (
    <Modal label={`Delete ${NOUN[kind]} ${name}`} onClose={onClose} className={s.dialog}>
      <h2 className={s.title}>Delete “{name}” from the catalog?</h2>
      {!impact && !err ? <p className={s.note} role="status">Working out what depends on it…</p> : null}
      {impact ? (
        <div className={s.body}>
          {notice ? <p role="alert" className={s.warn}>{notice}</p> : null}
          {impact.groups.length === 0 ? <p className={s.note}>Nothing else depends on this {NOUN[kind]}.</p> : (
            <ul className={s.groups} aria-label="What depends on it">
              {impact.groups.map((g) => (
                <li key={g.key} className={s.group}>
                  <span className={`${s.effect} ${s[g.effect]}`}>{EFFECT[g.effect]}</span>
                  <span>{g.text}</span>
                  {g.items.length > 0 && g.items[0]!.label !== "panel" ? (
                    <span className={s.items}>{g.items.map((i) => i.label).join(", ")}{g.count > g.items.length ? `, and ${g.count - g.items.length} more` : ""}</span>
                  ) : null}
                </li>
              ))}
            </ul>
          )}
          {impact.fileNote ? <p className={s.fileNote}>{impact.fileNote}</p> : null}
          {impact.ownedBytes ? (
            <fieldset className={s.owned}>
              <legend>This is a {impact.ownedBytes.kind} that Vesqira wrote ({bytes(impact.ownedBytes.sizeBytes)}). What happens to its bytes?</legend>
              <label><input type="radio" name="owned" checked={owned === "keep"} onChange={() => setOwned("keep")} /> Keep the file in Vesqira’s data folder</label>
              <label><input type="radio" name="owned" checked={owned === "remove"} onChange={() => setOwned("remove")} /> Also remove the file from Vesqira’s data folder (cannot be undone)</label>
            </fieldset>
          ) : null}
          {impact.blocked ? <p role="alert" className={s.warn}>This cannot be deleted yet. You can archive it instead: it is then hidden from lists and everything that uses it keeps working.</p> : null}
        </div>
      ) : null}
      {err ? <p role="alert" className={s.err}>{err}</p> : null}
      <div className={s.buttons}>
        <Button type="button" onClick={onClose}>Cancel</Button>
        <Button type="button" variant="primary" disabled={!impact || impact.blocked || needsChoice || busy} onClick={() => void confirm()}>{busy ? "Deleting…" : "Delete from catalog"}</Button>
      </div>
    </Modal>
  );
}
