import { useEffect, useRef, useState } from "react";
import { Button } from "../ui";
import { tierLine } from "../api/tier";
import { Modal } from "../ui/Modal";
import { RunLine } from "./RunLine";
import { figurePreviewUrl } from "../api/boards";
import type { Board } from "../api/boards";
import { placed } from "./layout";
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
  const items = placed(board.items).map((p) => p.item); // the board's reading order, as arranged on the grid
  const n = items.length;
  const it = items[i];

  useEffect(() => {
    root.current?.focus();
  }, []);

  function key(e: React.KeyboardEvent) {
    const go = (d: number) => { e.preventDefault(); setI((cur) => Math.min(n - 1, Math.max(0, cur + d))); };
    if (e.key === "ArrowRight" || e.key === "PageDown" || e.key === " ") go(1);
    else if (e.key === "ArrowLeft" || e.key === "PageUp") go(-1);
    else if (e.key === "Home") { e.preventDefault(); setI(0); }
    else if (e.key === "End") { e.preventDefault(); setI(n - 1); }
    // Escape is handled by the modal
    else if (e.key === "p" || e.key === "P") setProv((v) => !v);
    else if (e.key === "n" || e.key === "N") setNotes((v) => !v);
  }
  if (!it) return null;
  const f = it.kind === "job" ? null : it.figure;
  return (
    <Modal label={`Presenting ${board.title}`} onClose={onClose} fullscreen>
      <div ref={root} className={s.stage} tabIndex={-1} onKeyDown={key}>
        {it.kind === "job" ? (
          // a run that has not been pinned yet: say so, never draw a stand-in picture
          <>
            <header className={s.bar}>
              <span className={s.count} aria-live="polite">{i + 1} / {n}</span>
              <span className={s.title}>Run {it.job.name}</span>
              <Button onClick={() => setNotes((v) => !v)} aria-pressed={notes}>My notes</Button>
              <Button onClick={onClose}>Leave</Button>
            </header>
            <div className={s.picture}>
              <p className={s.caption}>Run {it.job.name} is {it.job.state}{it.job.progress ? ` (${it.job.progress.done} of ${it.job.progress.total} chunks)` : ""}. Its result is not on the board yet.</p>
            </div>
            {notes && it.note ? <p className={s.note}><strong>Note:</strong> {it.note}</p> : null}
            <footer className={s.hint}>← → move · N my notes · Esc leave</footer>
          </>
        ) : f ? (
          <>
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
                <p className={s.line}>{f.provenance.runPlot ? "Source" : "File"} <span className="mono">{f.provenance.source.name}</span> ({f.provenance.source.identity}).</p>
                {f.provenance.interpretation ? <p className={s.line}>Interpretation declared by {f.provenance.interpretation.declaredBy}: {f.provenance.interpretation.kind}, unit {f.provenance.interpretation.unit}.</p> : null}
                {f.provenance.run ? <RunLine run={f.provenance.run} /> : null}
                {(f.provenance.tiers ?? []).map((t) => <p key={t.what} className={s.line} data-tier={t.tier}>{tierLine(t)}</p>)}
                {f.provenance.statements.map((t) => <p key={t} className={s.line}>{t}</p>)}
              </div>
            ) : null}
            <footer className={s.hint}>← → move · P where this came from · N my notes · Esc leave</footer>
          </>
        ) : null}
      </div>
    </Modal>
  );
}
