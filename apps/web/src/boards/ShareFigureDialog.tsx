import { useState } from "react";
import { AccessBadge, Button, Input, Select } from "../ui";
import { Modal, ModalActions } from "../ui/Modal";
import { accessRefusal } from "../api/access";
import type { AccessRefusal } from "../api/access";
import { revokeSnapshot, shareFigure } from "../api/boards";
import type { Figure } from "../api/boards";
import { readBase, reviewLink, saveBase } from "./reviewBase";
import s from "./ShareFigureDialog.module.css";

type Done = { link: string; snapshotId: number; copied: boolean; revoked: boolean };

/**
 * "Share this view" (ADR 0071): one dialog that freezes one figure and makes a review link for it, with no board. The defaults
 * are enough, so opening it and pressing Enter is the whole job; the link is copied as it is made. `getFigure` gives the figure:
 * the viewer saves what is on screen first, a saved figure just returns itself. Labelled data asks once more, in words, and a
 * label the workspace blocks says so and offers nothing.
 */
export default function ShareFigureDialog({ title, getFigure, onClose }: { title: string; getFigure: () => Promise<Figure>; onClose: () => void }) {
  const [label, setLabel] = useState("");
  const [hours, setHours] = useState("168");
  const [base, setBase] = useState(readBase);
  const [figure, setFigure] = useState<Figure | null>(null); // kept after the first attempt, so a confirmation shares the same picture
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [refusal, setRefusal] = useState<(AccessRefusal & { status: number }) | null>(null);
  const [done, setDone] = useState<Done | null>(null);

  async function go(confirmAccess = false) {
    setBusy(true);
    setErr(null);
    try {
      const f = figure ?? (await getFigure());
      setFigure(f);
      const r = await shareFigure(f.id, label.trim(), Number(hours), confirmAccess ? { confirmAccess: true } : {});
      saveBase(base);
      const link = reviewLink(base, r.reviewPath);
      let copied = false;
      try {
        await navigator.clipboard.writeText(link);
        copied = true;
      } catch {
        /* no clipboard permission: the link is shown and the Copy button asks again */
      }
      setRefusal(null);
      setDone({ link, snapshotId: r.snapshot.id, copied, revoked: false });
    } catch (e) {
      const ref = accessRefusal(e);
      if (ref) setRefusal(ref);
      else setErr(e instanceof Error ? e.message : "Could not share the figure.");
    } finally {
      setBusy(false);
    }
  }

  async function revoke() {
    if (!done) return;
    try {
      await revokeSnapshot(done.snapshotId);
      setDone({ ...done, revoked: true });
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not revoke the link.");
    }
  }

  async function copy() {
    if (!done) return;
    try {
      await navigator.clipboard.writeText(done.link);
      setDone({ ...done, copied: true });
    } catch {
      setErr("The browser did not allow copying. Select the link and copy it.");
    }
  }

  return (
    <Modal label="Share this view" onClose={onClose} size="sm">
      <h2 className={s.title}>Share this view</h2>
      {done ? (
        <div className={s.body}>
          <p className={s.note}>
            {done.revoked ? "This link is revoked and no longer opens." : `Link made${done.copied ? " and copied to the clipboard" : ""}. Anyone with it can see this frozen picture and its provenance, and can comment. They never get the data, file paths or any credential. It is shown once; revoke it to end access.`}
          </p>
          <input className={s.linkInput} readOnly value={done.link} aria-label="Review link" onFocus={(e) => e.currentTarget.select()} />
          {err ? <p role="alert" className={s.err}>{err}</p> : null}
          <ModalActions>
            {done.revoked ? null : <Button onClick={() => void revoke()}>Revoke link</Button>}
            {done.revoked ? null : <Button onClick={() => void copy()}>Copy link</Button>}
            <Button variant="primary" onClick={onClose}>Done</Button>
          </ModalActions>
        </div>
      ) : (
        <form className={s.body} onSubmit={(e) => { e.preventDefault(); if (!refusal) void go(); }}>
          <p className={s.note}>Freezes “{title}” as it is now and makes a read-only review link. The reviewer sees the picture and where it came from, not the data.</p>
          <Input label="Label (optional)" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="e.g. For the group call" maxLength={200} disabled={busy || refusal !== null} />
          <Select label="Link valid for" value={hours} onChange={(e) => setHours(e.target.value)} disabled={busy || refusal !== null}>
            <option value="24">1 day</option>
            <option value="168">7 days</option>
            <option value="336">14 days</option>
            <option value="720">30 days</option>
          </Select>
          <details className={s.more}>
            <summary>Link address</summary>
            <Input label="Review address" value={base} onChange={(e) => setBase(e.target.value)} />
          </details>
          {err ? <p role="alert" className={s.err}>{err}</p> : null}
          {refusal ? (
            <div role="alert" aria-label="Labelled data" className={s.refusal}>
              <p className={s.note}><AccessBadge label={refusal.label} /> {refusal.error}</p>
            </div>
          ) : null}
          <ModalActions>
            <Button type="button" onClick={onClose}>Cancel</Button>
            {refusal?.status === 403 ? null : refusal ? (
              <Button type="button" variant="primary" disabled={busy} onClick={() => void go(true)}>I confirm: share with this data</Button>
            ) : (
              <Button type="submit" variant="primary" disabled={busy}>{busy ? "Working…" : "Create link"}</Button>
            )}
          </ModalActions>
        </form>
      )}
    </Modal>
  );
}
