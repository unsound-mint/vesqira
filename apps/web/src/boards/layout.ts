// The board's grid layout engine (ADR 0060): pure functions, no React, no DOM. The grid has COLS columns of equal width and rows
// of one fixed height, so a panel's place is four integers: column x, row y, and how many columns (w) and rows (h) it spans.
// Layouts are vertically compact: nothing floats above empty rows, and no two panels overlap. The server stores the places
// and checks bounds and overlap; everything about compaction, collisions, keyboard steps and automatic arrangement is here.
// The module imports nothing so the owner's board, the print page and the reviewer's page all lay panels out the same way.

export const COLS = 12;
/** One row's height and the gap between cells, in CSS pixels (the grid sets both as custom properties from here). */
export const ROW_PX = 48;
export const GAP_PX = 16;
export const MIN_W = 2;
export const MIN_H = 2;
export const MAX_H = 40;

export type Rect = { x: number; y: number; w: number; h: number };
export type PanelKind = "figure" | "job";

/** A panel that nobody placed: the full width, as the board always showed it, a little shorter for a run that has no picture yet. */
export const defaultSize = (kind: PanelKind): { w: number; h: number } => ({ w: COLS, h: kind === "job" ? 5 : 8 });

const same = (a: Rect, b: Rect) => a.x === b.x && a.y === b.y && a.w === b.w && a.h === b.h;
export const sameLayout = (a: readonly Rect[], b: readonly Rect[]) => a.length === b.length && a.every((r, i) => same(r, b[i] as Rect));
const overlap = (a: Rect, b: Rect) => a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
const clamp = (n: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, n));
const inGrid = (r: Rect) => Number.isInteger(r.x) && Number.isInteger(r.y) && Number.isInteger(r.w) && Number.isInteger(r.h) && r.x >= 0 && r.w >= 1 && r.x + r.w <= COLS && r.y >= 0 && r.h >= 1;

/** Keeps a wanted place inside the grid: at least MIN_W by MIN_H, at most the grid's width and MAX_H. */
export function fit(r: Rect): Rect {
  const w = clamp(Math.round(r.w), MIN_W, COLS);
  const h = clamp(Math.round(r.h), MIN_H, MAX_H);
  return { x: clamp(Math.round(r.x), 0, COLS - w), y: Math.max(0, Math.round(r.y)), w, h };
}

/** Indexes in reading order: top to bottom, then left to right; the index breaks a tie, so the order is total. */
export function readingOrder(rects: readonly Rect[]): number[] {
  return rects.map((_, i) => i).sort((a, b) => (rects[a] as Rect).y - (rects[b] as Rect).y || (rects[a] as Rect).x - (rects[b] as Rect).x || a - b);
}

/**
 * Settles a layout after panel `moved` was put at `target` (or just compacts it when moved is null). The moved panel keeps its
 * place; every other panel that it overlaps is pushed down below it, or, when the panel was dragged down past a neighbour, takes
 * over the space it left; then everything falls up as far as it can. The result has no overlap and no empty row above a panel.
 */
export function settle(rects: readonly Rect[], moved: number | null, target?: Rect): Rect[] {
  const out = rects.map((r) => ({ ...r }));
  const was = moved === null ? null : (rects[moved] as Rect);
  let m: Rect | null = null;
  if (moved !== null && target) {
    m = fit(target);
    out[moved] = m;
  }
  for (let pass = 0; pass < 6; pass++) {
    const before = out.map((r) => ({ ...r }));
    const order = out.map((_, i) => i).filter((i) => i !== moved).sort((a, b) => (out[a] as Rect).y - (out[b] as Rect).y || (out[a] as Rect).x - (out[b] as Rect).x || a - b);
    const placed: Rect[] = m ? [m] : [];
    for (const i of order) {
      const r = out[i] as Rect;
      const hit = () => placed.filter((p) => overlap(r, p));
      if (hit().length && m && was && m.y > was.y && r.y >= was.y) {
        // dragged down past this panel: it takes the top of the space the moved one left, if that is free
        const there = { ...r, y: was.y };
        if (!placed.some((p) => overlap(there, p))) r.y = was.y;
      }
      for (let c = hit(); c.length; c = hit()) r.y = Math.max(...c.map((p) => p.y + p.h));
      while (r.y > 0 && !placed.some((p) => overlap({ ...r, y: r.y - 1 }, p))) r.y--;
      placed.push(r);
    }
    if (m) {
      const rest = out.filter((_, i) => i !== moved);
      while (m.y > 0 && !rest.some((p) => overlap({ ...(m as Rect), y: (m as Rect).y - 1 }, p))) m.y--;
    }
    if (sameLayout(before, out)) break;
  }
  return out;
}

export const compact = (rects: readonly Rect[]) => settle(rects, null);

/**
 * The places to show for a board, from what is stored: null where a panel has no place. A board that was never arranged is
 * stacked full width in its order; a panel that has no place on an arranged board goes below the others in its order; a stored
 * place that is outside the grid or overlaps an earlier one is treated as missing. Only stored data is read (order and kind),
 * so every reader of the same board draws the same layout, whatever the screen or the pictures. The result is compacted, and
 * is unchanged for a layout this module saved.
 */
export function resolve(stored: readonly (Rect | null)[], kinds: readonly PanelKind[]): Rect[] {
  const kept: (Rect | null)[] = [];
  for (const r of stored) kept.push(r && inGrid(r) && !kept.some((k) => k && overlap(r, k)) ? { ...r } : null);
  let bottom = Math.max(0, ...kept.map((r) => (r ? r.y + r.h : 0)));
  const rects = kept.map((r, i) => {
    if (r) return r;
    const d = defaultSize(kinds[i] ?? "figure");
    const placed = { x: 0, y: bottom, w: d.w, h: d.h };
    bottom += d.h;
    return placed;
  });
  return compact(rects);
}

type Placeable = { kind?: string | undefined; layout?: Rect | undefined };

/**
 * The panels of a board (the owner's, or a reviewer's frozen copy) with the place each has, in reading order. The one answer to
 * "in what order, and where" for the presentation, the printed page and the reviewer: a board nobody arranged comes back in its
 * stored order, each panel full width, which is how it was always shown. A frozen panel's kind is absent for a figure.
 */
export function placed<T extends Placeable>(items: readonly T[]): { item: T; rect: Rect }[] {
  const rects = resolve(items.map((it) => it.layout ?? null), items.map((it) => (it.kind === "job" ? "job" : "figure")));
  return readingOrder(rects).map((i) => ({ item: items[i] as T, rect: rects[i] as Rect }));
}

export const hasStoredLayout = (stored: readonly (Rect | null)[]) => stored.some((r) => r !== null);

export type Step = { rects: Rect[]; changed: boolean; reason?: "edge" | "limit" };

/**
 * One keyboard move of panel i. Left and right move one column. Up and down move one row when the layout can hold the panel
 * there; in a compact layout an empty row above a panel is closed again, so when a single row would change nothing the move goes
 * on until the panel has passed the neighbour in the way, and the caller says where it ended up.
 */
export function moveStep(rects: readonly Rect[], i: number, dx: number, dy: number): Step {
  const r = rects[i] as Rect;
  if (dx !== 0) {
    const x = clamp(r.x + dx, 0, COLS - r.w);
    if (x === r.x) return { rects: rects.map((q) => ({ ...q })), changed: false, reason: "edge" };
    const next = settle(rects, i, { ...r, x });
    return { rects: next, changed: !sameLayout(next, rects) };
  }
  const dir = dy < 0 ? -1 : 1;
  for (let d = 1; d <= COLS * 8; d++) {
    const y = r.y + dir * d;
    if (y < 0) break;
    const next = settle(rects, i, { ...r, y });
    if (!sameLayout(next, rects)) return { rects: next, changed: true };
  }
  return { rects: rects.map((q) => ({ ...q })), changed: false, reason: "edge" };
}

/** One keyboard resize of panel i by whole columns and rows, within the grid and the size limits. */
export function resizeStep(rects: readonly Rect[], i: number, dw: number, dh: number): Step {
  const r = rects[i] as Rect;
  const w = clamp(r.w + dw, MIN_W, COLS - r.x);
  const h = clamp(r.h + dh, MIN_H, MAX_H);
  if (w === r.w && h === r.h) return { rects: rects.map((q) => ({ ...q })), changed: false, reason: "limit" };
  const next = settle(rects, i, { ...r, w, h });
  return { rects: next, changed: !sameLayout(next, rects) };
}

/** The wanted place of a panel dragged by a pointer: whole cells from where the drag started, kept inside the grid. */
export function dragTarget(start: Rect, dxPx: number, dyPx: number, colStride: number, rowStride: number): Rect {
  return fit({ ...start, x: start.x + Math.round(dxPx / colStride), y: start.y + Math.round(dyPx / rowStride) });
}

/** The wanted place of a panel resized from its corner by a pointer. */
export function resizeTarget(start: Rect, dxPx: number, dyPx: number, colStride: number, rowStride: number): Rect {
  return fit({ ...start, w: start.w + Math.round(dxPx / colStride), h: start.h + Math.round(dyPx / rowStride) });
}

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** What a screen reader hears after a change: where the panel is and how big it is, 1-based like the labels. */
export function sentence(title: string, r: Rect, verb: "moved" | "resized" | "is"): string {
  const where = `column ${r.x + 1}, row ${r.y + 1}; ${plural(r.w, "column")} wide, ${plural(r.h, "row")} tall`;
  return verb === "is" ? `“${title}” is at ${where}.` : `“${title}” ${verb} to ${where}.`;
}

export type Arrangeable = { kind: PanelKind; channel?: number | undefined; method?: string | undefined };
export type Arranged = { rects: Rect[]; by: "channel-method" | "reading-order"; sentence: string };

/**
 * "Arrange automatically" (Q12): channels as columns, methods as rows. Only what each panel records is used: the channel its
 * figure shows and the kind of run it was drawn from. items are in reading order and so are the rectangles returned. When the
 * records do not say enough (a run that has no picture yet, a figure without a channel or a run, two panels for the same
 * channel and method, a grid with holes, more channels than the grid has room for) nothing is guessed: the panels are stacked in reading order and
 * the sentence says what was missing.
 */
export function arrange(items: readonly Arrangeable[]): Arranged {
  const stack = (why: string): Arranged => {
    let y = 0;
    const rects = items.map((it) => {
      const d = defaultSize(it.kind);
      const r = { x: 0, y, w: d.w, h: d.h };
      y += d.h;
      return r;
    });
    return { rects, by: "reading-order", sentence: `Not enough is recorded to arrange by channel and method: ${why}. The panels were placed in reading order instead.` };
  };
  if (items.length === 0) return stack("the board has no panels");
  const runs = items.filter((it) => it.kind === "job").length;
  if (runs > 0) return stack(`${plural(runs, "panel")} follow${runs === 1 ? "s" : ""} a run that has no picture yet, so no channel or method is recorded for ${runs === 1 ? "it" : "them"}`);
  const noChannel = items.filter((it) => it.channel === undefined).length;
  if (noChannel > 0) return stack(`${plural(noChannel, "figure")} ${noChannel === 1 ? "does" : "do"} not record a channel`);
  const noMethod = items.filter((it) => !it.method).length;
  if (noMethod > 0) return stack(`${plural(noMethod, "figure")} ${noMethod === 1 ? "was" : "were"} not drawn from a recorded run, so no method is known`);
  const channels = [...new Set(items.map((it) => it.channel as number))].sort((a, b) => a - b);
  const methods = [...new Set(items.map((it) => it.method as string))];
  if (channels.length > COLS / MIN_W) return stack(`${channels.length} different channels do not fit in ${COLS} columns`);
  const cell = new Set<string>();
  for (const it of items) {
    const k = `${it.channel}|${it.method}`;
    if (cell.has(k)) return stack(`two figures show channel ${(it.channel as number) + 1} of the same method (${it.method})`);
    cell.add(k);
  }
  // a layout is compact, so a row with a missing cell would let the panel below rise into it and no longer sit in its method's row
  if (items.length !== channels.length * methods.length) return stack(`not every method has a figure for every channel (${items.length} figures for ${plural(channels.length, "channel")} and ${plural(methods.length, "method")})`);
  const w = Math.floor(COLS / channels.length);
  const h = defaultSize("figure").h;
  const rects = items.map((it) => ({ x: channels.indexOf(it.channel as number) * w, y: methods.indexOf(it.method as string) * h, w, h }));
  return {
    rects,
    by: "channel-method",
    sentence: `Arranged with ${plural(channels.length, "channel")} as columns and ${plural(methods.length, "method")} (run kind) as rows, from what each figure records.`,
  };
}
