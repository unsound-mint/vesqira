import { useEffect, useState } from "react";
import { getRegionHistogram } from "../api/planes";
import type { RegionHistogramResult, RegionShape } from "../api/planes";
import { ApiError } from "../api/http";
import type { MaskUse } from "../api/masks";

export type MaskedHistogram = { result: RegionHistogramResult | null; error: string | null };

/**
 * The histogram of a region of one channel with masks applied. The browser has no mask bytes, so the worker counts it (the unmasked
 * histogram is still counted here, from the plane in hand). Null when no mask is named. `result` is null while counting; a result for
 * another channel, region or choice of masks is never returned.
 */
export function useMaskedHistogram(datasetId: number | null, channel: number, region: RegionShape | null, min: number | null, max: number | null, bins: number, uses: MaskUse[]): MaskedHistogram | null {
  const key = region && datasetId !== null && min !== null && max !== null && uses.length > 0 ? JSON.stringify([datasetId, channel, region, min, max, bins, uses]) : null;
  const [got, setGot] = useState<{ key: string; result: RegionHistogramResult | null; error: string | null } | null>(null);
  useEffect(() => {
    if (key === null) return;
    const ac = new AbortController();
    const t = setTimeout(() => {
      getRegionHistogram(datasetId!, channel, region!, min!, max!, bins, uses, ac.signal)
        .then((result) => setGot({ key, result, error: null }))
        .catch((e: unknown) => {
          if (e instanceof DOMException && e.name === "AbortError") return;
          setGot({ key, result: null, error: e instanceof ApiError ? e.message : "Could not count the region with these masks." });
        });
    }, 120);
    return () => { clearTimeout(t); ac.abort(); };
  }, [key]); // eslint-disable-line react-hooks/exhaustive-deps
  if (key === null) return null;
  return got && got.key === key ? { result: got.result, error: got.error } : { result: null, error: null };
}
