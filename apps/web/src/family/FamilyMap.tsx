import { useEffect, useMemo, useRef } from "react";
import type { KeyboardEvent, MouseEvent } from "react";
import type { Plane } from "../api/planes";
import { renderPlane } from "../viewer/render";
import { nodataColours } from "../viewer/nodata";
import { clampCursor, displaySize } from "./familyScale";
import s from "./Family.module.css";

export type Cursor = { x: number; y: number };

/**
 * One map of a family on a scale it shares with its siblings. Pixels with no value are hatched (never a colour of the scale), and the pixel
 * under the pointer, or reached with the arrow keys, is the same pixel in every panel: its values are read in the table, so no number depends on a colour.
 */
export function FamilyMap({ title, plane, lut, range, axial, cursor, onCursor }: {
  title: string;
  plane: Plane;
  lut: Uint8ClampedArray;
  range: [number, number] | null;
  axial?: { toDegrees: number };
  cursor: Cursor | null;
  onCursor: (c: Cursor) => void;
}) {
  const ref = useRef<HTMLCanvasElement>(null);
  const w = plane.head.width;
  const h = plane.head.height;
  const size = useMemo(() => displaySize(w, h), [w, h]);
  useEffect(() => {
    const cv = ref.current;
    const g = cv?.getContext("2d");
    if (!cv || !g) return;
    const img = renderPlane(plane.data, w, h, { lut, range, stretch: "linear", outW: size.outW, outH: size.outH, nodata: nodataColours(), axial });
    g.putImageData(new ImageData(img.pixels, img.width, img.height), 0, 0);
  }, [plane, w, h, lut, range, axial, size]);

  const at = (e: MouseEvent<HTMLCanvasElement>): Cursor | null => {
    const r = e.currentTarget.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) return null;
    return clampCursor({ x: Math.floor(((e.clientX - r.left) / r.width) * w), y: h - 1 - Math.floor(((e.clientY - r.top) / r.height) * h) }, w, h);
  };
  const key = (e: KeyboardEvent<HTMLCanvasElement>) => {
    const d: Record<string, [number, number]> = { ArrowRight: [1, 0], ArrowLeft: [-1, 0], ArrowUp: [0, 1], ArrowDown: [0, -1] };
    const st = d[e.key];
    if (!st) return;
    e.preventDefault();
    const c = cursor ?? { x: Math.floor(w / 2), y: Math.floor(h / 2) };
    onCursor(clampCursor({ x: c.x + st[0] * (e.shiftKey ? 10 : 1), y: c.y + st[1] * (e.shiftKey ? 10 : 1) }, w, h));
  };
  return (
    <figure className={s.map}>
      <figcaption className={s.cap}>{title}</figcaption>
      <div className={s.stack}>
        <canvas
          ref={ref}
          width={size.outW}
          height={size.outH}
          className={s.canvas}
          style={{ aspectRatio: `${w} / ${h}` }}
          tabIndex={0}
          role="application"
          aria-label={`${title}. ${w} by ${h} pixels. Arrow keys move the cursor shared by every panel; hold Shift to move ten pixels.${cursor ? ` Cursor at pixel ${cursor.x + 1}, ${cursor.y + 1}.` : ""}`}
          onPointerMove={(e) => { const c = at(e); if (c) onCursor(c); }}
          onClick={(e) => { const c = at(e); if (c) onCursor(c); }}
          onKeyDown={key}
        />
        {cursor && cursor.x < w && cursor.y < h ? (
          <span className={s.marker} aria-hidden="true" style={{ left: `${((cursor.x + 0.5) / w) * 100}%`, top: `${((h - 1 - cursor.y + 0.5) / h) * 100}%` }} />
        ) : null}
      </div>
    </figure>
  );
}
