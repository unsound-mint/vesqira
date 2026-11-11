import { useRef, useState } from "react";
import type { FocusEvent, KeyboardEvent } from "react";

/** The index to move to for a key in a vertical list of `count` items from `from`, or null when the key is not one the list handles. */
export function rovingTarget(key: string, from: number, count: number): number | null {
  if (count === 0) return null;
  switch (key) {
    case "ArrowDown": return Math.min(count - 1, from + 1);
    case "ArrowUp": return Math.max(0, from - 1);
    case "Home": return 0;
    case "End": return count - 1;
    default: return null;
  }
}

/**
 * One tab stop for a long list (a roving tabindex): Tab enters the list once, at the item last used (else the selected one,
 * else the first), the arrow keys, Home and End move between rows, and Enter or Space opens the row as the button it is.
 * Put `listProps` on the container and `itemProps(id)` on each row's button; rows are found by `data-roving`.
 */
export function useRoving(ids: readonly string[], selectedId?: string | null) {
  const box = useRef<HTMLDivElement>(null);
  const [last, setLast] = useState<string | null>(null);
  const stop = last !== null && ids.includes(last) ? last : selectedId != null && ids.includes(selectedId) ? selectedId : (ids[0] ?? null);
  return {
    listProps: {
      ref: box,
      onKeyDown(e: KeyboardEvent<HTMLElement>) {
        if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
        const items = Array.from(box.current?.querySelectorAll<HTMLElement>("[data-roving]") ?? []);
        const from = items.findIndex((el) => el === document.activeElement);
        if (from < 0) return;
        const to = rovingTarget(e.key, from, items.length);
        if (to === null) return;
        e.preventDefault();
        items[to]!.focus();
      },
      onFocus(e: FocusEvent<HTMLElement>) {
        const id = (e.target as HTMLElement).dataset?.roving;
        if (id !== undefined) setLast(id);
      },
    },
    itemProps: (id: string) => ({ "data-roving": id, tabIndex: id === stop ? 0 : -1 }),
  };
}
