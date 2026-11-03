import { useRef } from "react";
import type { KeyboardEvent, PointerEvent } from "react";
import s from "./ResizeHandle.module.css";

type Props = {
  /** Axis the handle moves along: "vertical" separators resize widths. */
  orientation: "vertical" | "horizontal";
  value: number;
  min: number;
  max: number;
  onChange: (v: number) => void;
  label: string;
  /** Drag direction sign: -1 when growing the pane means moving the pointer toward the origin (right-hand panes, bottom panes). */
  direction?: 1 | -1;
};

const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));

/** WAI-ARIA window splitter: pointer drag plus Arrow (8px), Shift+Arrow (32px), Home and End. */
export function ResizeHandle({ orientation, value, min, max, onChange, label, direction = 1 }: Props) {
  const start = useRef<{ p: number; v: number } | null>(null);
  const horiz = orientation === "vertical"; // a vertical bar is dragged horizontally

  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    e.currentTarget.setPointerCapture(e.pointerId);
    start.current = { p: horiz ? e.clientX : e.clientY, v: value };
  };
  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    if (!start.current) return;
    const delta = ((horiz ? e.clientX : e.clientY) - start.current.p) * direction;
    onChange(clamp(start.current.v + delta, min, max));
  };
  const onPointerUp = () => {
    start.current = null;
  };
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const step = e.shiftKey ? 32 : 8;
    const grow = horiz ? "ArrowRight" : "ArrowDown";
    const shrink = horiz ? "ArrowLeft" : "ArrowUp";
    let next: number | null = null;
    if (e.key === grow) next = value + step * direction;
    else if (e.key === shrink) next = value - step * direction;
    else if (e.key === "Home") next = min;
    else if (e.key === "End") next = max;
    if (next === null) return;
    e.preventDefault();
    onChange(clamp(next, min, max));
  };

  return (
    <div
      role="separator"
      tabIndex={0}
      aria-label={label}
      aria-orientation={orientation}
      aria-valuenow={Math.round(value)}
      aria-valuemin={min}
      aria-valuemax={max}
      className={[s.handle, horiz ? s.v : s.h].join(" ")}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      onKeyDown={onKeyDown}
    />
  );
}
