import { useEffect, useState } from "react";
import { Button } from "../ui";
import { RunLine } from "./RunLine";
import { figurePreviewUrl, listBoards } from "../api/boards";
import type { Board } from "../api/boards";
import s from "./PrintBoard.module.css";

/**
 * A board laid out to be printed or saved as a PDF from the browser: each figure with its caption, the author's note
 * and where it came from. Nothing here is more than the board already shows; there is no path and no data.
 */
export function PrintBoard({ boardId }: { boardId: number }) {
  const [board, setBoard] = useState<Board | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    // paper is light: use the light theme whatever the screen uses
    const root = document.documentElement;
    const before = root.dataset.theme;
    root.dataset.theme = "light";
    return () => {
      if (before === undefined) delete root.dataset.theme;
      else root.dataset.theme = before;
    };
  }, []);
  useEffect(() => {
    listBoards()
      .then((bs) => {
        const b = bs.find((x) => x.id === boardId);
        if (b) setBoard(b);
        else setErr("That board does not exist.");
      })
      .catch((e: Error) => setErr(e.message));
  }, [boardId]);
  if (err) return <main className={s.page}><p role="alert">{err}</p></main>;
  if (!board) return <main className={s.page}><p>Loading…</p></main>;
  return (
    <main className={s.page}>
      <header className={s.head}>
        <h1 className={s.h1}>{board.title}</h1>
        <p className={s.meta}>Printed {new Date().toLocaleDateString()}. Pictures are as drawn in the author's browser, not re-renderings of the data.</p>
        <div className={s.noPrint}><Button variant="primary" onClick={() => window.print()}>Print or save as PDF</Button></div>
      </header>
      {board.items.map(({ figure: f, note }, i) => (
        <section key={f.id} className={s.fig} aria-label={f.title}>
          <h2 className={s.h2}>{i + 1}. {f.title}</h2>
          <img className={s.img} src={figurePreviewUrl(f.id)} alt={`${f.title}. ${f.provenance.source.name}`} />
          {f.caption ? <p className={s.cap}>{f.caption}</p> : null}
          {note ? <p className={s.cap}>Note: {note}</p> : null}
          <div className={s.prov}>
            <p>File <span className="mono">{f.provenance.source.name}</span> ({f.provenance.source.identity}).</p>
            {f.provenance.interpretation ? <p>Interpretation declared by {f.provenance.interpretation.declaredBy}: {f.provenance.interpretation.kind}, unit {f.provenance.interpretation.unit}.</p> : null}
            {f.provenance.run ? <RunLine run={f.provenance.run} /> : null}
            {f.provenance.statements.map((t) => <p key={t}>{t}</p>)}
          </div>
        </section>
      ))}
    </main>
  );
}
