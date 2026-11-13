import { useCallback, useEffect, useMemo, useState } from "react";
import { getDataset } from "../api/catalog";
import type { Dataset, DatasetSummary } from "../api/catalog";

/**
 * The full dataset (header, locations, file-header reading) for one id, which the list does not carry. It is read again whenever the
 * list's row for that id is replaced, which is what every change (a declaration, a correction, a new location, a re-read file) does
 * after it refreshes the list. `detail` is null while the first read is in flight, when the id is not in the list, or when the read
 * failed (`error` says why); a read for another id is never shown. `put` adopts a dataset a change just returned, so the panel shows
 * it without waiting for the read.
 */
export function useDatasetDetail(id: number | null, summaries: readonly DatasetSummary[]) {
  const row = useMemo(() => (id === null ? undefined : summaries.find((d) => d.id === id)), [id, summaries]);
  const [state, setState] = useState<{ d: Dataset } | { id: number; error: string } | null>(null);
  useEffect(() => {
    if (id === null || !row) return;
    const ac = new AbortController();
    getDataset(id, ac.signal)
      .then((d) => setState({ d }))
      .catch((e: unknown) => { if (!ac.signal.aborted) setState({ id, error: e instanceof Error ? e.message : "Could not read the dataset." }); });
    return () => ac.abort();
  }, [id, row]);
  const detail = row && state && "d" in state && state.d.id === id ? state.d : null;
  const error = row && state && "error" in state && state.id === id ? state.error : null;
  const put = useCallback((d: Dataset) => setState({ d }), []);
  return { detail, error, put };
}
