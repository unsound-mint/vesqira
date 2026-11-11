import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import type { CSSProperties, KeyboardEvent, PointerEvent as ReactPointerEvent, ReactNode } from "react";
import { GripVertical, LayoutGrid, RotateCcw } from "lucide-react";
import { Button } from "../ui";
import { setLayout } from "../undo/boardCommands";
import type { Board, BoardItem } from "../api/boards";
import { arrange, COLS, dragTarget, GAP_PX, hasStoredLayout, moveStep, readingOrder, resizeStep, resizeTarget, resolve, ROW_PX, sameLayout, sentence, settle } from "./layout";
import type { Rect } from "./layout";
import s from "./BoardGrid.module.css";

const titleOf = (it: BoardItem) => (it.kind === "job" ? `Run ${it.job.name}` : it.figure.title);

/**
 * The researcher's board as a grid (ADR 0060): panels placed by column and row on 12 columns of fixed-height rows, snapped while
 * they are dragged or resized, never overlapping, compact. The layout is saved through the board's own undoable command; a board
 * nobody arranged is shown from its order and nothing is written until the first change. Every move and resize also works from
 * the keyboard, on the panel's handle. The panels' content is the caller's (`renderCard`), so the board screen keeps its own.
 */
export function BoardGrid({ board, onBoard, onChanged, onError, renderCard }: {
  board: Board;
  onBoard: (b: Board) => void;
  onChanged: () => void;
  onError: (message: string) => void;
  renderCard: (it: BoardItem, index: number) => ReactNode;
}) {
  const items = board.items;
  const none = items.length === 0;
  const stored = items.map((it) => it.layout ?? null);
  const resolved = resolve(stored, items.map((it) => it.kind));
  const arranged = hasStoredLayout(stored);

  // Saves are queued one after another, each replacing what the one before stored, and the layout the researcher is looking at
  // is kept here until the board that comes back has it: two quick key presses never act on a stale layout.
  const [pending, setPending] = useState<Rect[] | null>(null);
  const savedRef = useRef(stored);
  const chain = useRef<Promise<void>>(Promise.resolve());
  const inflight = useRef(0);
  const live = useRef(board);
  live.current = board;
  useEffect(() => {
    if (inflight.current === 0) {
      savedRef.current = board.items.map((it) => it.layout ?? null);
      setPending(null);
    }
  }, [board]);

  const base = pending ?? resolved;
  const [drag, setDrag] = useState<{ index: number; rects: Rect[]; mode: "move" | "resize" } | null>(null);
  const shown = drag?.rects ?? base;
  const [said, setSaid] = useState("");
  const [note, setNote] = useState("");
  const hint = useId();
  const gridRef = useRef<HTMLOListElement>(null);

  // Panels are in reading order in the page, so a change that reorders them moves their elements and the browser drops focus:
  // put it back on the handle the person was using.
  const refocus = useRef<number | null>(null);
  useEffect(() => {
    const forget = () => { refocus.current = null; };
    document.addEventListener("pointerdown", forget, true);
    return () => document.removeEventListener("pointerdown", forget, true);
  }, []);
  useLayoutEffect(() => {
    if (refocus.current === null || (document.activeElement && document.activeElement !== document.body)) return;
    gridRef.current?.querySelector<HTMLElement>(`[data-panel="${refocus.current}"] > button`)?.focus({ preventScroll: true });
  });

  /** `clear` stores no places at all (the board is shown from its order again); next is then what is shown meanwhile. */
  function commit(next: Rect[], name: string, clear = false) {
    setPending(next);
    inflight.current++;
    chain.current = chain.current.then(async () => {
      try {
        const b = await setLayout(live.current.id, live.current.items, savedRef.current, clear ? next.map(() => null) : next, name);
        savedRef.current = b.items.map((it) => it.layout ?? null);
        onBoard(b);
      } catch (e) {
        onError(e instanceof Error ? e.message : "The layout could not be saved.");
        onChanged(); // show what the board is now
      } finally {
        inflight.current--;
        if (inflight.current === 0) setPending(null);
      }
    });
  }

  function onKey(e: KeyboardEvent<HTMLButtonElement>, index: number) {
    if (e.target !== e.currentTarget || e.altKey || e.ctrlKey || e.metaKey) return;
    const dir = ({ ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] } as Record<string, [number, number]>)[e.key];
    if (e.key === "Escape" && drag) { e.preventDefault(); setDrag(null); return; }
    if (!dir) return;
    e.preventDefault();
    refocus.current = index;
    const it = items[index] as BoardItem;
    const resizing = e.shiftKey;
    const step = resizing ? resizeStep(base, index, dir[0], dir[1]) : moveStep(base, index, dir[0], dir[1]);
    if (!step.changed) {
      setSaid(`${resizing ? "Cannot resize further that way." : "Cannot move further that way."} ${sentence(titleOf(it), base[index] as Rect, "is")}`);
      return;
    }
    commit(step.rects, resizing ? "resize panel" : "move panel");
    setSaid(sentence(titleOf(it), step.rects[index] as Rect, resizing ? "resized" : "moved"));
  }

  // Pointer drags: whole cells from where the drag started, the layout settling live around the panel; one save when it ends.
  const session = useRef<{ index: number; x: number; y: number; from: Rect[]; mode: "move" | "resize"; col: number; row: number } | null>(null);
  function begin(e: ReactPointerEvent<HTMLElement>, index: number, mode: "move" | "resize") {
    if (e.button !== 0 || !gridRef.current) return;
    e.preventDefault();
    e.currentTarget.setPointerCapture?.(e.pointerId);
    const width = gridRef.current.getBoundingClientRect().width;
    session.current = { index, x: e.clientX, y: e.clientY, from: base, mode, col: (width + GAP_PX) / COLS, row: ROW_PX + GAP_PX };
    setDrag({ index, rects: base, mode });
  }
  function move(e: ReactPointerEvent<HTMLElement>) {
    const d = session.current;
    if (!d) return;
    const start = d.from[d.index] as Rect;
    const pick = d.mode === "move" ? dragTarget : resizeTarget;
    const rects = settle(d.from, d.index, pick(start, e.clientX - d.x, e.clientY - d.y, d.col, d.row));
    setDrag((cur) => (cur && sameLayout(cur.rects, rects) ? cur : { index: d.index, rects, mode: d.mode }));
  }
  function end(e: ReactPointerEvent<HTMLElement>, cancelled: boolean) {
    const d = session.current;
    if (!d) return;
    session.current = null;
    e.currentTarget.releasePointerCapture?.(e.pointerId);
    const final = drag?.rects;
    setDrag(null);
    if (cancelled || !final || sameLayout(final, d.from)) return;
    commit(final, d.mode === "move" ? "move panel" : "resize panel");
    setSaid(sentence(titleOf(items[d.index] as BoardItem), final[d.index] as Rect, d.mode === "move" ? "moved" : "resized"));
  }

  function arrangeAutomatically() {
    const order = readingOrder(base);
    const result = arrange(order.map((i) => {
      const it = items[i] as BoardItem;
      return it.kind === "job" ? { kind: "job" as const } : { kind: "figure" as const, channel: it.figure.provenance.view.channel, method: it.figure.provenance.run?.kind };
    }));
    const next = base.map((r) => ({ ...r }));
    order.forEach((i, k) => { next[i] = result.rects[k] as Rect; });
    setNote(result.sentence);
    if (!sameLayout(next, base)) commit(settle(next, null), "arrange panels automatically");
  }
  const reset = () => { commit(resolve(items.map(() => null), items.map((it) => it.kind)), "reset layout", true); setNote("The panels are back in their original order, one per row. The layout is no longer saved with the board."); };

  if (none) return null;
  const ordered = readingOrder(shown);
  return (
    <div className={s.wrap}>
      <div className={s.tools}>
        <Button icon={LayoutGrid} onClick={arrangeAutomatically} disabled={items.length < 2}>Arrange automatically</Button>
        {arranged ? <Button icon={RotateCcw} onClick={reset}>Back to reading order</Button> : null}
        <p id={hint} className={s.hint}>Drag a panel by its handle, or focus the handle and use the arrow keys to move it by one cell and Shift with the arrow keys to resize it. Ctrl+Z undoes a change.</p>
      </div>
      {note ? <p className={s.arranged} role="status">{note}</p> : null}
      <p className={s.sr} role="status" aria-live="polite" aria-atomic="true">{said}</p>
      <div className={s.stage}>
      {drag ? <div className={s.lines} aria-hidden="true">{Array.from({ length: COLS }, (_, c) => <i key={c} />)}</div> : null}
      <ol ref={gridRef} className={s.grid} data-active={drag?.mode} aria-label="Figures on the board" style={{ "--row": `${ROW_PX}px`, "--gap": `${GAP_PX}px` } as CSSProperties}>
        {ordered.map((index) => {
          const it = items[index] as BoardItem;
          const r = shown[index] as Rect;
          const active = drag?.index === index;
          return (
            <li key={it.position} className={`${s.cell} ${active ? s.active : ""}`} data-panel={index} style={{ "--col": `${r.x + 1} / span ${r.w}`, "--rows": `${r.y + 1} / span ${r.h}` } as CSSProperties}>
              <button
                type="button"
                className={s.handle}
                aria-label={`Move or resize “${titleOf(it)}”`}
                aria-describedby={hint}
                onKeyDown={(e) => onKey(e, index)}
                onBlur={(e) => { if (e.relatedTarget) refocus.current = null; }}
                onPointerDown={(e) => begin(e, index, "move")}
                onPointerMove={move}
                onPointerUp={(e) => end(e, false)}
                onPointerCancel={(e) => end(e, true)}
              >
                <GripVertical size={16} aria-hidden="true" />
                <span className={s.where}>column {r.x + 1}, row {r.y + 1} · {r.w} × {r.h}</span>
              </button>
              {renderCard(it, index)}
              <span
                className={s.corner}
                aria-hidden="true"
                onPointerDown={(e) => begin(e, index, "resize")}
                onPointerMove={move}
                onPointerUp={(e) => end(e, false)}
                onPointerCancel={(e) => end(e, true)}
              />
            </li>
          );
        })}
      </ol>
      </div>
    </div>
  );
}
