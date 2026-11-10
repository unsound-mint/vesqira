import { useEffect, useRef, useState } from "react";
import { FilePlus, FolderInput, MoreHorizontal, Trash2, Undo2 } from "lucide-react";
import type { ReportSummary, ReportView } from "../api/reports";
import { Icon } from "../ui";
import { subtree } from "./tree";
import s from "./ReportsView.module.css";

type Step = null | "menu" | "move" | "withdraw" | "delete";

/**
 * The less frequent things done to a page, kept out of the bar: a page inside it, moving it, taking it back from
 * readers, deleting it. Withdrawing and deleting say exactly what they remove before they do it.
 */
export function PageMenu({ view, list, onAddChild, onMove, onWithdraw, onDelete }: {
  view: ReportView;
  list: ReportSummary[];
  onAddChild: () => void;
  onMove: (parent: number) => Promise<void>;
  onWithdraw: () => Promise<void>;
  onDelete: () => Promise<void>;
}) {
  const [step, setStep] = useState<Step>(null);
  const [target, setTarget] = useState(String(view.parent));
  const [busy, setBusy] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!step) return;
    const close = (e: MouseEvent) => { if (box.current && !box.current.contains(e.target as Node)) setStep(null); };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [step]);
  useEffect(() => { setStep(null); setTarget(String(view.parent)); }, [view.id, view.parent]);
  const published = view.publications.length > 0;
  const comments = view.comments.length;
  const blocked = subtree(list, view.id);
  const places = list.filter((r) => !blocked.has(r.id)).sort((a, b) => a.title.localeCompare(b.title, undefined, { numeric: true }));
  async function run(f: () => Promise<void>) {
    setBusy(true);
    try {
      await f();
      setStep(null);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className={s.pageMenu} ref={box} onKeyDown={(e) => { if (e.key === "Escape") setStep(null); }}>
      <button type="button" className={s.iconBtn} aria-label="More for this page" title="More" aria-expanded={!!step} onClick={() => setStep(step ? null : "menu")}><Icon icon={MoreHorizontal} /></button>
      {step === "menu" ? (
        <div className={s.menu} role="menu" aria-label="Page">
          <ul>
            <li><button type="button" role="menuitem" autoFocus onClick={() => { setStep(null); onAddChild(); }}><Icon icon={FilePlus} /><span>New page inside</span></button></li>
            <li><button type="button" role="menuitem" onClick={() => setStep("move")}><Icon icon={FolderInput} /><span>Move to…</span></button></li>
            {published ? (
              <li><button type="button" role="menuitem" onClick={() => setStep("withdraw")}><Icon icon={Undo2} /><span>Withdraw from readers…</span></button></li>
            ) : (
              <li><button type="button" role="menuitem" onClick={() => setStep("delete")}><Icon icon={Trash2} /><span>Delete…</span></button></li>
            )}
          </ul>
        </div>
      ) : step === "move" ? (
        <div className={s.menu} role="dialog" aria-label="Move this page">
          <label className={s.field}>
            <span>Put “{view.title}” inside</span>
            <select autoFocus value={target} onChange={(e) => setTarget(e.target.value)}>
              <option value="0">Nothing (top level)</option>
              {places.map((r) => <option key={r.id} value={r.id}>{r.title}</option>)}
            </select>
          </label>
          <div className={s.row}>
            <button type="button" className={s.primarySm} disabled={busy || target === String(view.parent)} onClick={() => void run(() => onMove(Number(target)))}>Move</button>
            <button type="button" className={s.ghost} onClick={() => setStep(null)}>Cancel</button>
          </div>
        </div>
      ) : step === "withdraw" ? (
        <div className={s.menu} role="alertdialog" aria-label="Withdraw from readers">
          <p className={s.confirmText}>
            Readers lose “{view.title}”: every published revision and its pictures are deleted from the publish server
            {comments ? `, with the ${comments} comment${comments === 1 ? "" : "s"} on it` : ""}. Your draft stays, and you can publish it again later as revision 1.
          </p>
          <div className={s.row}>
            <button type="button" className={s.danger} autoFocus disabled={busy} onClick={() => void run(onWithdraw)}>Withdraw</button>
            <button type="button" className={s.ghost} onClick={() => setStep(null)}>Cancel</button>
          </div>
        </div>
      ) : step === "delete" ? (
        <div className={s.menu} role="alertdialog" aria-label="Delete this page">
          <p className={s.confirmText}>Delete “{view.title}” and its history? Pages inside it move up a level. This cannot be undone.</p>
          <div className={s.row}>
            <button type="button" className={s.danger} autoFocus disabled={busy} onClick={() => void run(onDelete)}>Delete</button>
            <button type="button" className={s.ghost} onClick={() => setStep(null)}>Cancel</button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
