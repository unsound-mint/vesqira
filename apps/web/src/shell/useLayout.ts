import { useCallback, useEffect, useState } from "react";

export type Layout = { side: number; insp: number; timeline: number; sideOpen: boolean; inspOpen: boolean; timelineOpen: boolean };

export const LIMITS = {
  side: { min: 200, max: 480 },
  insp: { min: 280, max: 560 },
  timeline: { min: 88, max: 320 },
} as const;

const BASE: Layout = { side: 280, insp: 360, timeline: 164, sideOpen: true, inspOpen: true, timelineOpen: true };
// On narrow screens panels start closed: they open over the canvas on demand.
const narrow = () => typeof matchMedia === "function" && matchMedia("(width <= 800px)").matches;
const defaults = (): Layout => (narrow() ? { ...BASE, sideOpen: false, inspOpen: false } : BASE);
const KEY = "vesqira.layout.v1";

const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));

/** Reads stored sizes defensively: storage may be blocked, empty or hand-edited. */
export function parseLayout(raw: string | null): Layout {
  if (!raw) return defaults();
  try {
    const o = JSON.parse(raw) as Partial<Record<keyof Layout, unknown>>;
    const num = (k: "side" | "insp" | "timeline") => {
      const v = o[k];
      return typeof v === "number" && Number.isFinite(v) ? clamp(v, LIMITS[k].min, LIMITS[k].max) : defaults()[k];
    };
    const bool = (k: "sideOpen" | "inspOpen" | "timelineOpen") => (typeof o[k] === "boolean" ? (o[k] as boolean) : defaults()[k]);
    return { side: num("side"), insp: num("insp"), timeline: num("timeline"), sideOpen: bool("sideOpen"), inspOpen: bool("inspOpen"), timelineOpen: bool("timelineOpen") };
  } catch {
    return defaults();
  }
}

function read(): Layout {
  try {
    return parseLayout(localStorage.getItem(KEY));
  } catch {
    return defaults();
  }
}

export function useLayout() {
  const [layout, setLayout] = useState<Layout>(read);
  useEffect(() => {
    try {
      localStorage.setItem(KEY, JSON.stringify(layout));
    } catch {
      /* per-browser convenience only; the layout still works without storage */
    }
  }, [layout]);
  const set = useCallback(<K extends keyof Layout>(k: K, v: Layout[K]) => setLayout((l) => ({ ...l, [k]: v })), []);
  const toggle = useCallback(
    (k: "sideOpen" | "inspOpen" | "timelineOpen") =>
      setLayout((l) => {
        const next = { ...l, [k]: !l[k] };
        // On narrow screens the side panels overlay the same cell, so opening one closes the other.
        if (narrow() && next[k]) {
          if (k === "sideOpen") next.inspOpen = false;
          if (k === "inspOpen") next.sideOpen = false;
        }
        return next;
      }),
    [],
  );
  return { layout, set, toggle };
}
