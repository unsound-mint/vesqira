import { useEffect, useMemo, useState } from "react";
import type { RegionShape } from "../api/planes";
import { histogram, regionHistogram } from "./render";

/** What the worker is asked to count: the whole plane, or the pixels of `region` inside it. */
export type HistogramJob = {
  data: Float32Array;
  min: number;
  max: number;
  bins: number;
  region: { shape: RegionShape; width: number; height: number } | null;
};
/** `pixels` and `nonfinite` are the region's own counts (null for the whole plane, whose counts come with its stats). */
export type HistogramResult = { counts: number[]; pixels: number | null; nonfinite: number | null };

let worker: Worker | null = null;
let nextId = 0;
const waiting = new Map<number, (r: HistogramResult) => void>();

function counter(): Worker | null {
  if (typeof Worker === "undefined") return null;
  if (!worker) {
    worker = new Worker(new URL("./histogram.worker.ts", import.meta.url), { type: "module" });
    worker.onmessage = (e: MessageEvent<{ id: number; result: HistogramResult }>) => {
      waiting.get(e.data.id)?.(e.data.result);
      waiting.delete(e.data.id);
    };
    worker.onerror = () => { worker?.terminate(); worker = null; waiting.clear(); };
  }
  return worker;
}

/** Wait this long after the last change before counting, so scrubbing through channels counts only where it stops. */
const SETTLE_MS = 60;

function count(j: HistogramJob): HistogramResult {
  return j.region ? regionHistogram(j.data, j.region.width, j.region.height, j.region.shape, j.min, j.max, j.bins) : { counts: histogram(j.data, j.min, j.max, j.bins), pixels: null, nonfinite: null };
}

/**
 * Counts of the finite values in `bins` steps from `min` to `max` (of the whole plane, or of `region` inside it), or null
 * while they are being counted. The result always belongs to the plane and region in hand: a count for another channel or
 * region is never returned. Without Worker support (tests, old browsers) it counts on the calling thread.
 */
export function useHistogram(data: Float32Array, min: number | null, max: number | null, bins: number, region: RegionShape | null = null, width = 0, height = 0): HistogramResult | null {
  const direct = typeof Worker === "undefined";
  const sync = useMemo(() => (direct && min !== null && max !== null ? count({ data, min, max, bins, region: region && { shape: region, width, height } }) : null), [direct, data, min, max, bins, region, width, height]);
  const [async, setAsync] = useState<{ data: Float32Array; region: RegionShape | null; result: HistogramResult } | null>(null);
  useEffect(() => {
    if (direct || min === null || max === null) return;
    const w = counter();
    if (!w) return;
    const id = ++nextId;
    const timer = setTimeout(() => {
      waiting.set(id, (result) => setAsync({ data, region, result }));
      w.postMessage({ id, data, min, max, bins, region: region && { shape: region, width, height } });
    }, SETTLE_MS);
    return () => { clearTimeout(timer); waiting.delete(id); };
  }, [direct, data, min, max, bins, region, width, height]);
  if (min === null || max === null) return { counts: [], pixels: null, nonfinite: null };
  if (direct) return sync;
  return async && async.data === data && async.region === region ? async.result : null;
}
