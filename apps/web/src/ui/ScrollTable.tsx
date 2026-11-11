import { useCallback, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import s from "./ScrollTable.module.css";

/** Whether a scroller has more to show past its right edge, and whether it has been scrolled away from its left edge. */
export function scrollEdges(el: { scrollLeft: number; clientWidth: number; scrollWidth: number }): { left: boolean; right: boolean } {
  const max = el.scrollWidth - el.clientWidth;
  // a pixel of slack: scrollLeft is fractional on zoomed or scaled screens
  return { left: el.scrollLeft > 1, right: max > 1 && el.scrollLeft < max - 1 };
}

/**
 * A table that may be wider than its column. It scrolls sideways (and from the keyboard: the region takes focus, the
 * arrow keys scroll it), and a shade on the edge that still has more says so, so a clipped column is never mistaken for
 * the last one. `label` names the region for assistive technology; `className` is for the scrolling box (height limits).
 */
export function ScrollTable({ label, className, children }: { label: string; className?: string; children: ReactNode }) {
  const box = useRef<HTMLDivElement>(null);
  const [edges, setEdges] = useState({ left: false, right: false });
  const measure = useCallback(() => {
    const el = box.current;
    if (!el) return;
    const next = scrollEdges(el);
    setEdges((e) => (e.left === next.left && e.right === next.right ? e : next));
  }, []);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    if (el.firstElementChild) ro.observe(el.firstElementChild);
    return () => ro.disconnect();
  }, [measure, children]);
  return (
    <div className={s.wrap} data-more-right={edges.right} data-more-left={edges.left}>
      <div ref={box} className={`${s.box} ${className ?? ""}`} role="region" aria-label={label} tabIndex={0} onScroll={measure}>
        {children}
      </div>
      {edges.left ? <span className={`${s.shade} ${s.shadeLeft}`} aria-hidden="true" /> : null}
      {edges.right ? <span className={`${s.shade} ${s.shadeRight}`} aria-hidden="true" /> : null}
    </div>
  );
}
