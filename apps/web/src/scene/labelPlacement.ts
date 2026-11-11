import { LABEL_PX } from "./overlays";
import type { Mark } from "./overlays";

export type TextMark = Extract<Mark, { type: "text" }>;
export type Box = { x: number; y: number; w: number; h: number };
/** A label with its position in screen pixels inside the frame, ready to draw. */
export type PlacedLabel = TextMark & { sx: number; sy: number };

/** Gap kept between a label and the picture's edge, and between a label and the viewer's own controls. */
export const LABEL_INSET = 6;
const GLYPH = 0.62; // the data font is monospaced: a glyph is about this many em wide

const textWidth = (t: string) => Math.ceil(t.length * LABEL_PX * GLYPH);

function boxOf(m: TextMark, x: number, y: number): Box {
  const w = textWidth(m.text), h = LABEL_PX + 2;
  return { x: m.anchor === "start" ? x : m.anchor === "end" ? x - w : x - w / 2, y: m.baseline === "top" ? y : m.baseline === "bottom" ? y - h : y - h / 2, w, h };
}

const hit = (a: Box, b: Box, gap: number) => a.x < b.x + b.w + gap && b.x < a.x + a.w + gap && a.y < b.y + b.h + gap && b.y < a.y + a.h + gap;

/**
 * Where each overlay label is drawn: inside an inset of the frame, clear of the viewer's own controls (`chrome`, in frame
 * pixels) and of the labels already placed. A label that collides slides along the edge it sits on, to the nearest free
 * spot; one with no free spot is dropped (its line is still drawn, and the overlay row still says what it is). Labels are
 * in screen pixels so text keeps its size at every zoom.
 */
export function placeLabels(labels: TextMark[], cell: number, frame: { w: number; h: number }, chrome: Box[]): PlacedLabel[] {
  const out: PlacedLabel[] = [];
  const taken: Box[] = [];
  const inside = (b: Box) => b.x >= LABEL_INSET && b.y >= LABEL_INSET && b.x + b.w <= frame.w - LABEL_INSET && b.y + b.h <= frame.h - LABEL_INSET;
  for (const m of labels) {
    let x = m.x * cell, y = m.y * cell;
    const b0 = boxOf(m, x, y);
    // keep the whole text inside the inset: nudge the anchor, not the line it labels
    if (b0.x < LABEL_INSET) x += LABEL_INSET - b0.x;
    else if (b0.x + b0.w > frame.w - LABEL_INSET) x += frame.w - LABEL_INSET - (b0.x + b0.w);
    if (b0.y < LABEL_INSET) y += LABEL_INSET - b0.y;
    else if (b0.y + b0.h > frame.h - LABEL_INSET) y += frame.h - LABEL_INSET - (b0.y + b0.h);
    const alongX = m.baseline !== "middle"; // on the top or bottom edge: slide sideways; on the left or right edge: slide up and down
    const step = alongX ? Math.max(8, b0.w / 2) : Math.max(8, b0.h);
    const span = alongX ? frame.w : frame.h;
    let placed: Box | null = null;
    let at = { x, y };
    for (let k = 0; k * step <= span && !placed; k++) {
      for (const sign of k === 0 ? [0] : [1, -1]) {
        const cx = alongX ? x + sign * k * step : x;
        const cy = alongX ? y : y + sign * k * step;
        const b = boxOf(m, cx, cy);
        if (!inside(b) || chrome.some((c) => hit(b, c, 2)) || taken.some((t) => hit(b, t, 2))) continue;
        placed = b;
        at = { x: cx, y: cy };
        break;
      }
    }
    if (!placed) continue;
    taken.push(placed);
    out.push({ ...m, sx: at.x, sy: at.y });
  }
  return out;
}
