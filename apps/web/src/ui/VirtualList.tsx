import { useCallback, useLayoutEffect, useState } from "react";
import type { ReactNode } from "react";
import { useRoving } from "./roving";

/** Lists up to this many rows are drawn whole; above it only the rows near the viewport exist in the page. */
export const VIRTUAL_THRESHOLD = 200;
const ROW_PX = 28; // --row in design/tokens.css; every row of these lists is one row high
const OVERSCAN = 8;
const FALLBACK_VIEWPORT_PX = 800; // a scroller that reports no height (a test document)

/** The rows [first, last) to draw for a viewport that starts `hidden` px below the top of the list and is `height` px tall. */
export function windowRange(hidden: number, height: number, rowHeight: number, count: number, overscan = OVERSCAN): [number, number] {
  const first = Math.min(count, Math.max(0, Math.floor(hidden / rowHeight) - overscan));
  const last = Math.min(count, Math.ceil((hidden + height) / rowHeight) + overscan);
  return [first, Math.max(first, last)];
}

function scrollerOf(el: HTMLElement): HTMLElement | null {
  for (let p = el.parentElement; p; p = p.parentElement) {
    const o = getComputedStyle(p).overflowY;
    if (o === "auto" || o === "scroll" || o === "overlay") return p;
  }
  return null; // the page itself
}

type RowProps = { tabIndex: number; "data-roving": string };

/**
 * A list of one-row-high rows that may hold thousands: past VIRTUAL_THRESHOLD only the rows in (and just around) the
 * viewport of the nearest scrolling ancestor are drawn, each placed by its index in a box as tall as the whole list, so the
 * scroll bar and the layout above and below stay as they were. Every row is a listitem that carries its place and the
 * list's size (aria-posinset, aria-setsize), so a screen reader hears "3 of 5000" although few rows exist. Keyboard: the
 * list is one tab stop (useRoving); arrows, Home and End move by index and scroll the target into existence. The row
 * holding the tab stop is always drawn, so Tab can re-enter the list after it scrolled away.
 */
export function VirtualList({ label, ids, selectedId, renderRow, threshold = VIRTUAL_THRESHOLD }: {
  label: string;
  ids: readonly string[];
  selectedId?: string | null;
  renderRow: (id: string, rowProps: RowProps, index: number) => ReactNode;
  threshold?: number;
}) {
  const count = ids.length;
  const windowed = count > threshold;
  const [range, setRange] = useState<[number, number]>([0, Math.min(count, 60)]);
  // the roving container is also the box measured and scrolled; reveal is filled in below, once measure exists
  const revealRef = { current: (_: number) => {} };
  const roving = useRoving(ids, selectedId, (i) => revealRef.current(i));
  const box = roving.listProps.ref;

  const geometry = useCallback(() => {
    const el = box.current;
    if (!el) return null;
    const sc = scrollerOf(el);
    const top = sc ? sc.getBoundingClientRect().top : 0;
    const height = (sc ? sc.clientHeight : window.innerHeight) || FALLBACK_VIEWPORT_PX;
    return { sc, height, hidden: top - el.getBoundingClientRect().top };
  }, [box]);

  const measure = useCallback(() => {
    const g = geometry();
    if (!g) return;
    const next = windowRange(g.hidden, g.height, ROW_PX, count);
    setRange((r) => (r[0] === next[0] && r[1] === next[1] ? r : next));
  }, [geometry, count]);

  revealRef.current = (i: number) => {
    const g = geometry();
    if (!g) return;
    // the row goes to the middle of the viewport; the focus() that follows keeps it fully visible
    const delta = -g.hidden + i * ROW_PX - (g.height - ROW_PX) / 2;
    if (g.sc) g.sc.scrollTop += delta;
    else window.scrollBy(0, delta);
    measure();
  };

  // after every render: the list may have moved (a note above it appeared); one rect read is cheap
  useLayoutEffect(() => { if (windowed) measure(); });
  useLayoutEffect(() => {
    const el = box.current;
    if (!windowed || !el) return;
    const sc = scrollerOf(el);
    const target: HTMLElement | Window = sc ?? window;
    target.addEventListener("scroll", measure, { passive: true });
    window.addEventListener("resize", measure);
    const ro = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    if (ro && sc) ro.observe(sc);
    return () => {
      target.removeEventListener("scroll", measure);
      window.removeEventListener("resize", measure);
      ro?.disconnect();
    };
  }, [windowed, measure, box]);

  const drawn: number[] = [];
  const [first, last] = windowed ? range : [0, count];
  const stopAt = roving.stop === null ? -1 : ids.indexOf(roving.stop);
  const pinned = windowed && stopAt >= 0 && (stopAt < first || stopAt >= last) ? stopAt : -1;
  if (pinned >= 0 && pinned < first) drawn.push(pinned);
  for (let i = first; i < Math.min(last, count); i++) drawn.push(i);
  if (pinned >= last) drawn.push(pinned);

  return (
    <div role="list" aria-label={label} data-rows={count} data-drawn={drawn.length} {...roving.listProps} style={windowed ? { position: "relative", height: count * ROW_PX } : undefined}>
      {drawn.map((i) => {
        const id = ids[i]!;
        return (
          <div role="listitem" key={id} aria-posinset={i + 1} aria-setsize={count} style={windowed ? { position: "absolute", top: i * ROW_PX, left: 0, right: 0, height: ROW_PX } : undefined}>
            {renderRow(id, roving.itemProps(id), i)}
          </div>
        );
      })}
    </div>
  );
}
