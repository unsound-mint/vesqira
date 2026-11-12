import { useCallback, useEffect, useState } from "react";
import { listSubsets } from "../api/subsets";
import type { SubsetView } from "../api/subsets";

/** Every virtual subset (ADR 0063), for the explorer's marks and the dataset's list. `refresh` after one is made, edited, re-pinned or deleted. */
export function useSubsets() {
  const [subsets, setSubsets] = useState<SubsetView[]>([]);
  const refresh = useCallback(async () => {
    try {
      const all = await listSubsets();
      setSubsets(Array.isArray(all) ? all : []);
    } catch {
      /* the explorer still lists the files; the marks come back with the next refresh */
    }
  }, []);
  useEffect(() => { void refresh(); }, [refresh]);
  return { subsets, refresh };
}
