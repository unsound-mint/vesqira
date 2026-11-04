import { useEffect, useRef, useState } from "react";
import { Button } from "../ui";
import { RunLine } from "./RunLine";
import { figurePreviewUrl } from "../api/boards";
import type { Board } from "../api/boards";
import s from "./PresentView.module.css";

/**
 * Shows a board one figure at a time, in the board's order, for a meeting. Arrow keys, Page keys, Home and End move;
 * P shows or hides where the figure came from; Escape leaves. The author's own notes are shown only when asked,
 * because the room may be looking at the screen.
 */
export function PresentView({ board, onClose }: { board: Board; onClose: () => void }) {
  const [i, setI] = useState(0);
  const [prov, setProv] = useState(false);
  const [notes, setNotes] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const n = board.items.length;
  const it = board.items[i];

  useEffect(() => {
    root.current?.focus();
  }, []);

  function key(e: React.KeyboardEvent) {
    const go = (d: number) => { e.preventDefault(); setI((cur) => Math.min(n - 1, Math.max(0, cur + d))); };
    if (e.key === "ArrowRight" || e.key === "PageDown" || e.key === " ") go(1);
    else if (e.key === "ArrowLeft" || e.key === "PageUp") go(-1);
    else if (e.key === "Home") { e.preventDefault(); setI(0); }
    else if (e.key === "End") { e.preventDefault(); setI(n - 1); }
    else if (e.key === "Escape") onClose();
    else if (e.key === "p" || e.key === "P") setProv((v) => !v);
    else if (e.key === "n" || e.key === "N") setNotes((v) => !v);
  }
  if (!it) return null;
  const f = it.figure;
  return (
    <div ref={root} className={s.stage} role="dialog" aria-modal="true" aria-label={`Presenting ${board.title}`} tabIndex={-1} onKeyDown={key}>
      <header className={s.bar}>
        <span className={s.count} aria-live="polite">{i + 1} / {n}</span>
        <span className={s.title}>{f.title}</span>
        <Button onClick={() => setProv((v) => !v)} aria-pressed={prov}>Where this came from</Button>
        <Button onClick={() => setNotes((v) => !v)} aria-pressed={notes}>My notes</Button>
        <Button onClick={onClose}>Leave</Button>
      </header>
      <div className={s.picture}>
        <img className={s.img} src={figurePreviewUrl(f.id)} alt={`${f.title}. ${f.provenance.source.name}`} />
      </div>
      {f.caption ? <p className={s.caption}>{f.caption}</p> : null}
      {notes && it.note ? <p className={s.note}><strong>Note:</strong> {it.note}</p> : null}
      {prov ? (
        <div className={s.prov}>
          <p className={s.line}>File <span className="mono">{f.provenance.source.name}</span> ({f.provenance.source.identity}).</p>
          {f.provenance.interpretation ? <p className={s.line}>Interpretation declared by {f.provenance.interpretation.declaredBy}: {f.provenance.interpretation.kind}, unit {f.provenance.interpretation.unit}.</p> : null}
          {f.provenance.run ? <RunLine run={f.provenance.run} /> : null}
          {f.provenance.statements.map((t) => <p key={t} className={s.line}>{t}</p>)}
        </div>
      ) : null}
      <footer className={s.hint}>← → move · P where this came from · N my notes · Esc leave</footer>
    </div>
  );
}
