import { histogram, regionHistogram } from "./render";
import type { HistogramJob, HistogramResult } from "./useHistogram";

/** Counts the finite values of one plane, or of a region of it, off the main thread (ADR 0034). The plane arrives as a copy. */
self.onmessage = (e: MessageEvent<HistogramJob & { id: number }>) => {
  const { id, data, min, max, bins, region } = e.data;
  const result: HistogramResult = region
    ? regionHistogram(data, region.width, region.height, region.shape, min, max, bins)
    : { counts: histogram(data, min, max, bins), pixels: null, nonfinite: null };
  (self as unknown as Worker).postMessage({ id, result });
};
