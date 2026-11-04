import { useRef, useState } from "react";
import type { KeyboardEvent, PointerEvent } from "react";
import type { Anchor } from "../api/boards";
import s from "./AnchorImage.module.css";

type Props = {
  src: string;
  alt: string;
  width: number;
  height: number;
  marks: { n: number; anchor: Anchor }[]; // places already commented on
  draft: Anchor | null; // the place being marked now
  marking: boolean;
  onDraft: (a: Anchor | null) => void;
};

const clamp = (v: number) => Math.min(1, Math.max(0, v));

/**
 * The published picture with the places comments refer to. While marking, a click marks a point and a drag marks a
 * box; with the keyboard the arrow keys move a point and Enter keeps it. A mark is a place on the picture as drawn,
 * not a sky position: the reviewer never has the data.
 */
export function AnchorImage({ src, alt, width, height, marks, draft, marking, onDraft }: Props) {
  const box = useRef<HTMLDivElement>(null);
  const start = useRef<{ x: number; y: number } | null>(null);
  const [live, setLive] = useState<Anchor | null>(null);

  const at = (e: PointerEvent) => {
    const r = box.current!.getBoundingClientRect();
    return { x: clamp((e.clientX - r.left) / r.width), y: clamp((e.clientY - r.top) / r.height) };
  };
  function down(e: PointerEvent) {
    if (!marking) return;
    e.currentTarget.setPointerCapture?.(e.pointerId);
    start.current = at(e);
    setLive({ kind: "point", ...start.current });
  }
  function move(e: PointerEvent) {
    if (!start.current) return;
    const p = at(e);
    const s0 = start.current;
    const wide = Math.abs(p.x - s0.x) > 0.01 && Math.abs(p.y - s0.y) > 0.01;
    setLive(wide ? { kind: "box", x: Math.min(s0.x, p.x), y: Math.min(s0.y, p.y), x1: Math.max(s0.x, p.x), y1: Math.max(s0.y, p.y) } : { kind: "point", ...s0 });
  }
  function up() {
    if (!start.current) return;
    start.current = null;
    onDraft(live);
    setLive(null);
  }
  function key(e: KeyboardEvent) {
    if (!marking) return;
    const step = e.shiftKey ? 0.1 : 0.02;
    const cur = draft?.kind === "point" ? draft : { kind: "point" as const, x: 0.5, y: 0.5 };
    const moves: Record<string, [number, number]> = { ArrowLeft: [-step, 0], ArrowRight: [step, 0], ArrowUp: [0, -step], ArrowDown: [0, step] };
    const m = moves[e.key];
    if (m) {
      e.preventDefault();
      onDraft({ kind: "point", x: clamp(cur.x + m[0]), y: clamp(cur.y + m[1]) });
    } else if (e.key === "Enter" && !draft) {
      e.preventDefault();
      onDraft(cur);
    } else if (e.key === "Escape") {
      onDraft(null);
    }
  }
  const shown = live ?? draft;
  return (
    <div className={s.wrap} ref={box}>
      <img className={s.img} src={src} alt={alt} width={width} height={height} />
      {marks.map((m) => <Mark key={m.n} anchor={m.anchor} label={String(m.n)} />)}
      {shown ? <Mark anchor={shown} draft /> : null}
      {marking ? (
        <div
          className={s.layer}
          tabIndex={0}
          role="application"
          aria-label="Mark a place on the picture: click or drag, or use the arrow keys and Enter"
          onPointerDown={down}
          onPointerMove={move}
          onPointerUp={up}
          onKeyDown={key}
        />
      ) : null}
    </div>
  );
}

function Mark({ anchor, label, draft }: { anchor: Anchor; label?: string; draft?: boolean }) {
  const cls = `${s.mark} ${draft ? s.draft : ""}`;
  if (anchor.kind === "box") {
    return <span className={`${cls} ${s.box}`} style={{ left: `${anchor.x * 100}%`, top: `${anchor.y * 100}%`, width: `${(anchor.x1 - anchor.x) * 100}%`, height: `${(anchor.y1 - anchor.y) * 100}%` }}>{label}</span>;
  }
  return <span className={`${cls} ${s.point}`} style={{ left: `${anchor.x * 100}%`, top: `${anchor.y * 100}%` }}>{label}</span>;
}
