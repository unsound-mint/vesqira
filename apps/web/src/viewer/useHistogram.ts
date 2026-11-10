import { useEffect, useMemo, useState } from "react";
import { histogram } from "./render";

let worker: Worker | null = null;
let nextId = 0;
const waiting = new Map<number, (counts: number[]) => void>();

function counter(): Worker | null {
  if (typeof Worker === "undefined") return null;
  if (!worker) {
    worker = new Worker(new URL("./histogram.worker.ts", import.meta.url), { type: "module" });
    worker.onmessage = (e: MessageEvent<{ id: number; counts: number[] }>) => {
      waiting.get(e.data.id)?.(e.data.counts);
      waiting.delete(e.data.id);
    };
    worker.onerror = () => { worker?.terminate(); worker = null; waiting.clear(); };
  }
  return worker;
}

/** Wait this long after the last change before counting, so scrubbing through channels counts only where it stops. */
const SETTLE_MS = 60;

/**
 * Counts of the finite values in `bins` steps from `min` to `max`, or null while they are being counted. The result
 * always belongs to the plane in hand: a count for another channel is never returned. Without Worker support
 * (tests, old browsers) it counts on the calling thread.
 */
export function useHistogram(data: Float32Array, min: number | null, max: number | null, bins: number): number[] | null {
  const direct = typeof Worker === "undefined";
  const sync = useMemo(() => (direct && min !== null && max !== null ? histogram(data, min, max, bins) : null), [direct, data, min, max, bins]);
  const [async, setAsync] = useState<{ data: Float32Array; counts: number[] } | null>(null);
  useEffect(() => {
    if (direct || min === null || max === null) return;
    const w = counter();
    if (!w) return;
    const id = ++nextId;
    const timer = setTimeout(() => {
      waiting.set(id, (counts) => setAsync({ data, counts }));
      w.postMessage({ id, data, min, max, bins });
    }, SETTLE_MS);
    return () => { clearTimeout(timer); waiting.delete(id); };
  }, [direct, data, min, max, bins]);
  if (min === null || max === null) return [];
  if (direct) return sync;
  return async && async.data === data ? async.counts : null;
}
