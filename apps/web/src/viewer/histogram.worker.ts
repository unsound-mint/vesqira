import { histogram } from "./render";

/** Counts the finite values of one plane off the main thread (ADR 0034). The plane arrives as a copy. */
self.onmessage = (e: MessageEvent<{ id: number; data: Float32Array; min: number; max: number; bins: number }>) => {
  const { id, data, min, max, bins } = e.data;
  (self as unknown as Worker).postMessage({ id, counts: histogram(data, min, max, bins) });
};
