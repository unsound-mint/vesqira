import { fit } from "../viewer/geometry";
import type { ScenePoint, SceneView } from "./scene";

export type Size = { w: number; h: number };
/** How far past its edge a zoomed picture may be dragged, in screen pixels. */
const SLACK = 40;

/**
 * Where a scene's view puts the picture in a pane. The picture is the image fitted to the pane times zoom, so the window of
 * width/zoom by height/zoom image pixels around the centre is shown whole and as large as the pane allows, whatever the
 * pane's size. offset is the picture's shift from centred, in screen pixels (y down). It is clamped so that some of the
 * picture stays in view; that is the only place the pane size changes what is shown.
 */
export function placement(view: SceneView, pane: Size, image: Size): { width: number; height: number; scale: number; offset: { x: number; y: number } } {
  const f = fit(pane.w, pane.h, image.w, image.h);
  const scale = f.scale * view.zoom;
  const width = image.w * scale;
  const height = image.h * scale;
  if (scale <= 0) return { width: 0, height: 0, scale: 0, offset: { x: 0, y: 0 } };
  const c = view.centre ?? { x: image.w / 2, y: image.h / 2 };
  return { width, height, scale, offset: clampOffset({ x: (image.w / 2 - c.x) * scale, y: (c.y - image.h / 2) * scale }, width, height, pane) };
}

function clampOffset(o: { x: number; y: number }, width: number, height: number, pane: Size) {
  const mx = Math.max(0, (width - pane.w) / 2) + SLACK;
  const my = Math.max(0, (height - pane.h) / 2) + SLACK;
  return { x: Math.min(mx, Math.max(-mx, o.x)), y: Math.min(my, Math.max(-my, o.y)) };
}

/** The image point at the pane's centre for a picture shifted by offset (the inverse of placement). */
export function centreFor(offset: { x: number; y: number }, scale: number, image: Size): ScenePoint {
  const x = image.w / 2 - offset.x / scale;
  const y = image.h / 2 + offset.y / scale;
  return { x: Math.min(image.w, Math.max(0, x)), y: Math.min(image.h, Math.max(0, y)) };
}

/** The centre after dragging the picture from `start` (its offset when the drag began) by dx, dy screen pixels. */
export function dragCentre(start: { x: number; y: number }, dx: number, dy: number, view: SceneView, pane: Size, image: Size): ScenePoint | null {
  const p = placement(view, pane, image);
  if (p.scale <= 0) return null;
  return centreFor(clampOffset({ x: start.x + dx, y: start.y + dy }, p.width, p.height, pane), p.scale, image);
}

/** The part of the image the pane shows, in image coordinates (x from the left, y from the bottom), unclipped. */
export function visibleWindow(view: SceneView, pane: Size, image: Size): { x0: number; x1: number; y0: number; y1: number } {
  const p = placement(view, pane, image);
  const c = centreFor(p.offset, p.scale, image);
  return { x0: c.x - pane.w / 2 / p.scale, x1: c.x + pane.w / 2 / p.scale, y0: c.y - pane.h / 2 / p.scale, y1: c.y + pane.h / 2 / p.scale };
}
