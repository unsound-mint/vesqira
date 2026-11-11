import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import { listMasks } from "../api/masks";
import type { Mask } from "../api/masks";

/* The masks of a dataset, and which of them the viewer is showing. What is shown is a view setting of the moment (like the pinned
   pixel), not part of the dataset or of a saved figure. */

const listeners = new Set<() => void>();
let changed = 0;
let shown: Record<number, number[]> = {};
const notify = () => listeners.forEach((l) => l());
const subscribe = (l: () => void) => { listeners.add(l); return () => { listeners.delete(l); }; };

/** Tell every view of a dataset's masks that they changed (one was made or forgotten). */
export function masksChanged() { changed++; notify(); }

export function setMaskShown(datasetId: number, maskId: number, on: boolean) {
  const cur = new Set(shown[datasetId] ?? []);
  if (on) cur.add(maskId); else cur.delete(maskId);
  shown = { ...shown, [datasetId]: [...cur].sort((a, b) => a - b) };
  notify();
}

const NONE: number[] = [];
export function useShownMasks(datasetId: number | null): number[] {
  return useSyncExternalStore(subscribe, () => (datasetId === null ? NONE : (shown[datasetId] ?? NONE)));
}

/** The dataset's masks; reloads when masksChanged() is called. `null` while loading, [] when it has none. */
export function useMasks(datasetId: number | null, enabled = true): { masks: Mask[] | null; error: string | null; reload: () => void } {
  const [masks, setMasks] = useState<Mask[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const version = useSyncExternalStore(subscribe, () => changed);
  const [tick, setTick] = useState(0);
  useEffect(() => {
    if (datasetId === null || !enabled) { setMasks(null); return; }
    const ac = new AbortController();
    listMasks(datasetId, ac.signal).then((m) => { setMasks(m); setError(null); }).catch((e: unknown) => {
      if (!ac.signal.aborted) { setMasks([]); setError(e instanceof Error ? e.message : "Could not read the masks."); }
    });
    return () => ac.abort();
  }, [datasetId, enabled, version, tick]);
  return { masks, error, reload: useCallback(() => setTick((t) => t + 1), []) };
}
